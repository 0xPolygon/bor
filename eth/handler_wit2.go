package eth

import (
	"errors"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rlp"
)

var errInvalidSignatureLength = errors.New("invalid wit2 announce signature length")

// Metrics for WIT2 signed-announce path. Emitted only when metrics are enabled.
var (
	wit2RelayInMeter                    = metrics.NewRegisteredMeter("eth/wit2/announce/relay_in", nil)
	wit2RelayOutMeter                   = metrics.NewRegisteredMeter("eth/wit2/announce/relay_out", nil)
	wit2InvalidSigMeter                 = metrics.NewRegisteredMeter("eth/wit2/announce/invalid_sig", nil)
	wit2NotValidatorMeter               = metrics.NewRegisteredMeter("eth/wit2/announce/not_validator", nil)
	wit2DuplicateMeter                  = metrics.NewRegisteredMeter("eth/wit2/announce/duplicate", nil)
	wit2BroadcastByteMismatchMeter      = metrics.NewRegisteredMeter("eth/wit2/serve/broadcast_byte_mismatch", nil)
	wit2BroadcastUnverifiedSkippedMeter = metrics.NewRegisteredMeter("eth/wit2/serve/broadcast_unverified_skipped", nil)
	wit2DeferredPerPeerDropMeter        = metrics.NewRegisteredMeter("eth/wit2/announce/deferred_per_peer_drop", nil)
	wit2HeaderUnknownMeter              = metrics.NewRegisteredMeter("eth/wit2/announce/header_unknown", nil)
	wit2ConflictingWitnessHashMeter     = metrics.NewRegisteredMeter("eth/wit2/announce/conflicting_witness_hash", nil)
	wit2RateLimitDropMeter              = metrics.NewRegisteredMeter("eth/wit2/announce/rate_limit_drop", nil)
	wit2StrikeDisconnectMeter           = metrics.NewRegisteredMeter("eth/wit2/announce/strike_disconnect", nil)
	wit2WaiterPushMeter                 = metrics.NewRegisteredMeter("eth/wit2/serve/waiter_push", nil)
	wit2WaiterPushOversizeMeter         = metrics.NewRegisteredMeter("eth/wit2/serve/waiter_push_oversize", nil)
	wit2BroadcastUnknownHeaderDropMeter = metrics.NewRegisteredMeter("eth/wit2/serve/broadcast_unknown_header_drop", nil)
	wit2BroadcastDeferredImportMeter    = metrics.NewRegisteredMeter("eth/wit2/serve/broadcast_deferred_import_only", nil)
)

// Per-peer rate-limit + strike tracker for wit2 announces. We size the bucket
// at burst=256 with a sustained rate of 64 announces/sec — higher than any
// honest gossip mesh would produce on Polygon's block cadence, low enough to
// neutralise an attacker spamming valid-but-redundant signed packets.
const (
	wit2AnnounceBurstCap        = 256
	wit2AnnounceRefillPerSecond = 64
	// wit2MisbehaviorStrikeLimit is the number of structurally-invalid (bad
	// signature, wrong producer, oversized packet) announces a peer may
	// produce within strikeDecayWindow before being disconnected.
	wit2MisbehaviorStrikeLimit = 5
	wit2MisbehaviorWindow      = time.Minute
)

// peerWit2State tracks a peer's wit2-announce burst budget and recent strikes.
// Lifecycle is tied to the eth handler's peer registration; entries are
// cleaned up when the peer disconnects.
type peerWit2State struct {
	tokens        float64
	lastRefill    time.Time
	strikeCount   int
	firstStrikeAt time.Time
}

type peerWit2Tracker struct {
	mu    sync.Mutex
	state map[string]*peerWit2State
}

func newPeerWit2Tracker() *peerWit2Tracker {
	return &peerWit2Tracker{state: make(map[string]*peerWit2State)}
}

func (t *peerWit2Tracker) forget(peerID string) {
	t.mu.Lock()
	delete(t.state, peerID)
	t.mu.Unlock()
}

// allow returns true if the peer has enough budget to consume `count`
// announcements right now. False means the packet should be dropped and a
// rate-limit metric recorded; the caller decides whether to disconnect.
func (t *peerWit2Tracker) allow(peerID string, count int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.state[peerID]
	now := time.Now()
	if !ok {
		st = &peerWit2State{tokens: wit2AnnounceBurstCap, lastRefill: now}
		t.state[peerID] = st
	}
	elapsed := now.Sub(st.lastRefill).Seconds()
	if elapsed > 0 {
		st.tokens += elapsed * wit2AnnounceRefillPerSecond
		if st.tokens > wit2AnnounceBurstCap {
			st.tokens = wit2AnnounceBurstCap
		}
		st.lastRefill = now
	}
	if st.tokens < float64(count) {
		return false
	}
	st.tokens -= float64(count)
	return true
}

// strike records a misbehavior for the peer. Returns true when the peer has
// exceeded the threshold within the decay window and must be disconnected.
func (t *peerWit2Tracker) strike(peerID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	st, ok := t.state[peerID]
	now := time.Now()
	if !ok {
		st = &peerWit2State{tokens: wit2AnnounceBurstCap, lastRefill: now}
		t.state[peerID] = st
	}
	if st.firstStrikeAt.IsZero() || now.Sub(st.firstStrikeAt) > wit2MisbehaviorWindow {
		st.firstStrikeAt = now
		st.strikeCount = 0
	}
	st.strikeCount++
	return st.strikeCount >= wit2MisbehaviorStrikeLimit
}

// wit2 announce-cache lifecycle constants.
const (
	// wit2AnnounceTTL bounds how long we remember a signed announcement so we
	// can re-emit it on body delivery and skip duplicate relays. Must outlast
	// typical fetch+import latency so producers/relayers still have the
	// signature when stateless peers come asking for the body.
	wit2AnnounceTTL = 30 * time.Second

	// wit2RelayWindow is the per-(blockHash, peer) duplicate-suppression window.
	// Even without this, knownWitnesses dedup blocks repeats; the window adds
	// belt-and-suspenders coverage during the brief gap between receive and
	// known-cache update under concurrent gossip storms.
	wit2RelayWindow = 200 * time.Millisecond

	// witnessBodyCacheCapacity bounds the number of pre-import witness bodies
	// held in memory. Each entry is ~50MB on Polygon, so the cap keeps total
	// memory under ~500MB worst case. Older entries are evicted as new ones
	// arrive; a 10-block window comfortably covers typical block-fetch and
	// import latency.
	witnessBodyCacheCapacity = 10
)

// pendingWitnessBody holds RLP-encoded witness bytes received from the network
// before the corresponding block has been imported (and thus before the bytes
// have been written to chain storage). Lets serving peers answer GetWitness
// requests during the import gap, which is what makes early relay actually
// useful — a peer that received the body can serve it the moment its TCP
// receive completes, rather than waiting ~500ms for full block validation.
type pendingWitnessBody struct {
	bytes       []byte
	witnessHash common.Hash
	receivedAt  time.Time
}

// pendingWitnessBodyCache holds bytes by block hash with a short TTL. Entries
// are dropped after the body has been written to chain storage, or after the
// TTL expires (whichever first). The cache is a simple map; the witness body
// is large (~50MB) so the cap is set conservatively.
type pendingWitnessBodyCache struct {
	mu       sync.RWMutex
	entries  map[common.Hash]*pendingWitnessBody
	capacity int
}

func newPendingWitnessBodyCache(capacity int) *pendingWitnessBodyCache {
	return &pendingWitnessBodyCache{
		entries:  make(map[common.Hash]*pendingWitnessBody),
		capacity: capacity,
	}
}

func (c *pendingWitnessBodyCache) put(blockHash common.Hash, bytes []byte, witnessHash common.Hash) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gcLocked()
	if len(c.entries) >= c.capacity {
		// Evict the oldest entry. Linear scan is fine at the configured cap.
		var oldestHash common.Hash
		var oldest time.Time
		for h, e := range c.entries {
			if oldest.IsZero() || e.receivedAt.Before(oldest) {
				oldest = e.receivedAt
				oldestHash = h
			}
		}
		delete(c.entries, oldestHash)
	}
	c.entries[blockHash] = &pendingWitnessBody{
		bytes:       bytes,
		witnessHash: witnessHash,
		receivedAt:  time.Now(),
	}
}

func (c *pendingWitnessBodyCache) get(blockHash common.Hash) ([]byte, common.Hash, bool) {
	c.mu.RLock()
	e, ok := c.entries[blockHash]
	if !ok {
		c.mu.RUnlock()
		return nil, common.Hash{}, false
	}
	if time.Since(e.receivedAt) > wit2AnnounceTTL {
		// Expired: drop the large byte slice now rather than waiting for the
		// next put() to gc. Without this, a node that stops receiving witness
		// bodies retains up to capacity (10) ~50MB blobs indefinitely past the
		// TTL, since gcLocked() only fires on put().
		c.mu.RUnlock()
		c.mu.Lock()
		// Re-check under the write lock: a concurrent put() may have replaced
		// the entry with a fresh one we should not delete.
		if cur, ok2 := c.entries[blockHash]; ok2 && cur == e {
			delete(c.entries, blockHash)
		}
		c.mu.Unlock()
		return nil, common.Hash{}, false
	}
	c.mu.RUnlock()
	return e.bytes, e.witnessHash, true
}

func (c *pendingWitnessBodyCache) drop(blockHash common.Hash) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, blockHash)
}

func (c *pendingWitnessBodyCache) gcLocked() {
	cutoff := time.Now().Add(-wit2AnnounceTTL)
	for h, e := range c.entries {
		if e.receivedAt.Before(cutoff) {
			delete(c.entries, h)
		}
	}
}

const (
	// witnessWaiterHashCap bounds how many block hashes we track waiters for.
	// Entries are tiny (a peer pointer + timestamp); the cap is a backstop
	// against a peer asking for many distinct not-yet-available hashes.
	witnessWaiterHashCap = 256

	// witnessWaiterPerHashCap bounds waiters recorded per hash so a burst of
	// distinct peers asking for the same not-yet-available witness can't grow a
	// single bucket without bound.
	witnessWaiterPerHashCap = 64

	// witnessWaiterTTL drops stale waiter entries (peer gave up, disconnected,
	// or obtained the body elsewhere). Aligned with the body cache TTL.
	witnessWaiterTTL = 30 * time.Second
)

// witnessWaiter records a peer that asked us for a witness body we did not yet
// have. We only record a waiter when a BP-signed announcement is on file for
// the hash, so the witness is known to exist and the registry is bounded by
// real, signed blocks rather than arbitrary peer-chosen hashes.
type witnessWaiter struct {
	peer *wit.Peer
	at   time.Time
}

// witnessWaiterRegistry tracks peers awaiting a witness body so we can push it
// to them the moment we obtain it. This restores the WIT1-style hand-off the
// WIT2 fast announce removed: WIT1 only ever announces a witness it already
// holds (and the announce marks the sender a body-holder), so a stateless
// consumer's first pull lands; WIT2 relays the signed announce ahead of the
// body, leaving the consumer to poll an announce-only relayer with repeated
// empty GetWitness until it catches up. Pushing on arrival closes that gap
// without flooding — at most one body per peer that actually asked, exactly the
// bandwidth a successful pull would have cost.
type witnessWaiterRegistry struct {
	mu      sync.Mutex
	waiters map[common.Hash]map[string]*witnessWaiter
}

func newWitnessWaiterRegistry() *witnessWaiterRegistry {
	return &witnessWaiterRegistry{waiters: make(map[common.Hash]map[string]*witnessWaiter)}
}

// record notes that peer is waiting for the body of hash. No-op for a nil peer.
func (r *witnessWaiterRegistry) record(hash common.Hash, peer *wit.Peer) {
	if peer == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gcLocked()

	per, ok := r.waiters[hash]
	if !ok {
		if len(r.waiters) >= witnessWaiterHashCap {
			// Registry full of distinct hashes; skip recording rather than
			// evict. The peer simply keeps polling (with backoff) and lands the
			// body on a later GetWitness — correctness is unaffected.
			return
		}
		per = make(map[string]*witnessWaiter)
		r.waiters[hash] = per
	}
	if _, exists := per[peer.ID()]; !exists && len(per) >= witnessWaiterPerHashCap {
		return
	}
	per[peer.ID()] = &witnessWaiter{peer: peer, at: time.Now()}
}

// has reports whether any non-expired waiter is recorded for hash. Used to skip
// the witness decode on the push path when nobody is waiting.
func (r *witnessWaiterRegistry) has(hash common.Hash) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	per, ok := r.waiters[hash]
	if !ok {
		return false
	}
	cutoff := time.Now().Add(-witnessWaiterTTL)
	for _, w := range per {
		if !w.at.Before(cutoff) {
			return true
		}
	}
	return false
}

// take returns and clears the live (non-expired) waiters for hash.
func (r *witnessWaiterRegistry) take(hash common.Hash) []*wit.Peer {
	r.mu.Lock()
	defer r.mu.Unlock()
	per, ok := r.waiters[hash]
	if !ok {
		return nil
	}
	delete(r.waiters, hash)
	cutoff := time.Now().Add(-witnessWaiterTTL)
	out := make([]*wit.Peer, 0, len(per))
	for _, w := range per {
		if w.at.Before(cutoff) {
			continue
		}
		out = append(out, w.peer)
	}
	return out
}

// gcLocked drops expired waiter entries and empty buckets. Caller holds r.mu.
func (r *witnessWaiterRegistry) gcLocked() {
	cutoff := time.Now().Add(-witnessWaiterTTL)
	for h, per := range r.waiters {
		for id, w := range per {
			if w.at.Before(cutoff) {
				delete(per, id)
			}
		}
		if len(per) == 0 {
			delete(r.waiters, h)
		}
	}
}

// witnessPushMaxSize caps the encoded size of a witness we full-push to
// waiting peers via NewWitness. The wit protocol rejects inbound messages
// larger than 16MB (wit.maxMessageSize), so pushing a bigger body would make
// every waiter drop us as a protocol violator — the paged GetWitness path
// exists precisely for those witnesses. The margin covers the NewWitnessPacket
// RLP envelope around the witness bytes. Oversized witnesses simply stay on
// the pull path: by the time any push could fire we hold servable bytes, so
// the waiter's next (backed-off) poll gets real pages instead of empty.
const witnessPushMaxSize = MaximumResponseSize - 64*1024

// pushWitnessToWaiters delivers the full witness body to peers that previously
// asked us for it and got an empty answer (we did not hold the body yet). The
// moment we obtain the bytes the waiting consumer receives them and imports,
// instead of continuing to poll us with empty GetWitness. encodedSize is the
// canonical RLP size of the witness, used to keep the push under the wit
// protocol message cap.
func (h *handler) pushWitnessToWaiters(hash common.Hash, witness *stateless.Witness, encodedSize int) {
	if h.witnessWaiters == nil || witness == nil {
		return
	}
	if encodedSize > witnessPushMaxSize {
		// Too large for a single NewWitness message — leave the waiters on
		// the paged pull path (entries expire by TTL; the bytes are already
		// servable, so their next poll succeeds).
		wit2WaiterPushOversizeMeter.Mark(1)
		log.Debug("wit2: witness too large for full push; serving via paged pull only",
			"hash", hash, "size", encodedSize, "cap", witnessPushMaxSize)
		return
	}
	for _, p := range h.witnessWaiters.take(hash) {
		if p.KnownWitnessContainsHash(hash) {
			continue // already delivered / known to hold it
		}
		p.AsyncSendNewWitness(witness)
		wit2WaiterPushMeter.Mark(1)
	}
}

// flushWitnessWaitersForImported pushes a just-imported block's witness to any
// peer that asked us for it before we held it. This covers the dominant case
// the fetch/broadcast push hooks miss: a node (especially a full / producing
// node) that obtains the witness by generating it during native block import,
// rather than by pulling it or receiving a gossip broadcast. Called from the
// chain-head loop on every new head; cheap no-op when no peer is waiting.
func (h *handler) flushWitnessWaitersForImported(blockHash common.Hash) {
	if h.witnessWaiters == nil || !h.witnessWaiters.has(blockHash) {
		return
	}
	body := h.chain.GetWitness(blockHash)
	if len(body) == 0 {
		return
	}
	h.pushWitnessBytesToWaiters(blockHash, body)
}

// pushWitnessBytesToWaiters decodes verified witness bytes (already checked
// against the BP-signed hash by the caller) and pushes them to waiting peers.
// The decode — re-encoded canonically on send — round-trips to the same bytes,
// so downstream byte-correctness checks still pass. Skipped entirely when no
// peer is waiting, so the common (no-waiter) case pays nothing.
func (h *handler) pushWitnessBytesToWaiters(hash common.Hash, witnessBytes []byte) {
	if h.witnessWaiters == nil || len(witnessBytes) == 0 || !h.witnessWaiters.has(hash) {
		return
	}
	if len(witnessBytes) > witnessPushMaxSize {
		// Skip the decode entirely — the push would be over the wit message
		// cap anyway; waiters fall back to the paged pull path.
		wit2WaiterPushOversizeMeter.Mark(1)
		log.Debug("wit2: witness too large for full push; serving via paged pull only",
			"hash", hash, "size", len(witnessBytes), "cap", witnessPushMaxSize)
		return
	}
	var witness stateless.Witness
	if err := rlp.DecodeBytes(witnessBytes, &witness); err != nil {
		log.Warn("wit2: failed to decode witness bytes for waiter push", "hash", hash, "err", err)
		return
	}
	h.pushWitnessToWaiters(hash, &witness, len(witnessBytes))
}

// deferredAnnounceCapacity bounds how many header-unknown signed announcements
// we hold while waiting for the corresponding block to arrive. Each entry is
// ~200 bytes; the cap is sized for a worst-case stall window where the local
// chain falls a few hundred blocks behind a busy mesh and announcements
// arrive ahead of headers en masse.
const deferredAnnounceCapacity = 256

// deferredAnnouncePerPeerDivisor caps how large a share of the deferred queue a
// single peer may occupy: perPeerCap = capacity / divisor. Without a per-peer
// cap, one peer operating within the announce rate limit (64/s) can fill all
// the slots with its own entries — each a distinct, attacker-chosen blockHash
// at a plausible near-tip number (the cache is keyed by hash, so a fixed
// blockNumber is no obstacle) — and evict honest header-racing announces,
// silently downgrading those blocks to unsigned WIT1 byte-verification. The cap
// reserves the bulk of the queue for the honest mesh. Honest peers race only
// the current tip, so a handful of in-flight deferrals is the norm and this cap
// is never approached in practice.
const deferredAnnouncePerPeerDivisor = 8

// deferredAnnounceEntry holds a signed announcement whose producer-binding
// could not be checked yet because the corresponding block header wasn't
// local. The drain path re-runs verification once the chain catches up.
type deferredAnnounceEntry struct {
	announcement wit.SignedWitnessAnnouncement
	peerID       string
	receivedAt   time.Time
}

// deferredAnnounceCache holds signed announcements deferred on header-unknown
// rejection so the chain-head loop can re-evaluate them when the matching
// block arrives. Without it, an announce that races ahead of its block — the
// expected outcome of independent block + announce gossip streams — is lost
// for good and subsequent witness fetches silently fall back to unsigned
// (WIT1) verification, leaking the WIT2 trust property for that block.
type deferredAnnounceCache struct {
	mu         sync.RWMutex
	entries    map[common.Hash]*deferredAnnounceEntry
	perPeer    map[string]int // live entry count per originating peer
	capacity   int
	perPeerCap int
}

func newDeferredAnnounceCache(capacity int) *deferredAnnounceCache {
	perPeerCap := capacity / deferredAnnouncePerPeerDivisor
	if perPeerCap < 1 {
		perPeerCap = 1
	}
	return &deferredAnnounceCache{
		entries:    make(map[common.Hash]*deferredAnnounceEntry),
		perPeer:    make(map[string]int),
		capacity:   capacity,
		perPeerCap: perPeerCap,
	}
}

// decPeerLocked drops one live-entry credit for peerID, removing the map key
// when it reaches zero. Caller must hold the write lock.
func (c *deferredAnnounceCache) decPeerLocked(peerID string) {
	c.perPeer[peerID]--
	if c.perPeer[peerID] <= 0 {
		delete(c.perPeer, peerID)
	}
}

// put stores the announcement keyed by block hash. A second put for the same
// hash refreshes receivedAt and overwrites the announcement — the more recent
// gossip wins, which is desirable when the original sender disconnected and a
// different peer now carries the announce forward; per-peer credit moves with
// it. For a new hash, the per-peer cap is enforced first (a peer at its share
// is dropped, recording a metric, so it cannot evict honest entries), then the
// global cap (evict the oldest entry across all peers; linear scan is cheap at
// the configured size).
func (c *deferredAnnounceCache) put(ann wit.SignedWitnessAnnouncement, peerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gcLocked()

	if existing, exists := c.entries[ann.BlockHash]; exists {
		// Overwrite for the same hash: net-zero slot change. Move per-peer
		// credit if a different peer now carries this announce forward.
		if existing.peerID != peerID {
			c.decPeerLocked(existing.peerID)
			c.perPeer[peerID]++
		}
		c.entries[ann.BlockHash] = &deferredAnnounceEntry{
			announcement: ann,
			peerID:       peerID,
			receivedAt:   time.Now(),
		}
		return
	}

	// New hash for this peer: enforce its share of the queue so no single peer
	// can monopolise the cache and evict honest header-racing announces.
	if c.perPeer[peerID] >= c.perPeerCap {
		wit2DeferredPerPeerDropMeter.Mark(1)
		return
	}

	if len(c.entries) >= c.capacity {
		var oldestHash common.Hash
		var oldest time.Time
		for h, e := range c.entries {
			if oldest.IsZero() || e.receivedAt.Before(oldest) {
				oldest = e.receivedAt
				oldestHash = h
			}
		}
		if victim, ok := c.entries[oldestHash]; ok {
			c.decPeerLocked(victim.peerID)
			delete(c.entries, oldestHash)
		}
	}

	c.entries[ann.BlockHash] = &deferredAnnounceEntry{
		announcement: ann,
		peerID:       peerID,
		receivedAt:   time.Now(),
	}
	c.perPeer[peerID]++
}

// take removes and returns the entry for blockHash if present and fresh.
// Returns ok=false on miss or expiry; expired entries are deleted in place.
func (c *deferredAnnounceCache) take(blockHash common.Hash) (*deferredAnnounceEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[blockHash]
	if !ok {
		return nil, false
	}
	delete(c.entries, blockHash)
	c.decPeerLocked(e.peerID)
	if time.Since(e.receivedAt) > wit2AnnounceTTL {
		return nil, false
	}
	return e, true
}

// peek returns the announcement for blockHash without consuming it, if a
// fresh entry exists. Used by the broadcast path to bind a pushed body to a
// pending (deferred, not yet producer-verified) announcement; the entry must
// stay in place so the post-import drain still runs the real producer
// verification, promotion, and relay.
func (c *deferredAnnounceCache) peek(blockHash common.Hash) (wit.SignedWitnessAnnouncement, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[blockHash]
	if !ok || time.Since(e.receivedAt) > wit2AnnounceTTL {
		return wit.SignedWitnessAnnouncement{}, false
	}
	return e.announcement, true
}

// has reports whether a fresh entry exists for blockHash. Test-facing only;
// production code uses take to ensure the entry is consumed.
func (c *deferredAnnounceCache) has(blockHash common.Hash) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[blockHash]
	if !ok {
		return false
	}
	return time.Since(e.receivedAt) <= wit2AnnounceTTL
}

// gcLocked drops entries past the TTL. Caller must hold the write lock.
func (c *deferredAnnounceCache) gcLocked() {
	cutoff := time.Now().Add(-wit2AnnounceTTL)
	for h, e := range c.entries {
		if e.receivedAt.Before(cutoff) {
			c.decPeerLocked(e.peerID)
			delete(c.entries, h)
		}
	}
}

// signedWitnessCache stores BP-signed announcements by block hash. The cache
// is consulted by:
//   - the relay path on receive (skip if already seen recently),
//   - the body-broadcast path (re-emit the cached signed announce when a
//     stateless peer requests the body), and
//   - the producer path (cache the locally-signed announcement so subsequent
//     re-emissions from this node don't re-sign).
type signedWitnessCache struct {
	mu      sync.RWMutex
	entries map[common.Hash]*signedAnnounceEntry
}

type signedAnnounceEntry struct {
	announcement wit.SignedWitnessAnnouncement
	receivedAt   time.Time
}

func newSignedWitnessCache() *signedWitnessCache {
	return &signedWitnessCache{entries: make(map[common.Hash]*signedAnnounceEntry)}
}

// putIfNewer stores the announcement keyed by block hash, returning true if
// the cache did not already contain a fresh entry for this hash. Callers use
// the return value to decide whether to relay (false → suppress duplicate).
//
// If a fresh entry already exists with a *different* WitnessHash, the new
// announcement is rejected outright (returns false): the first valid signed
// commitment wins for the lifetime of the entry. This prevents an attacker
// who has obtained a second valid signature (e.g. a compromised producer
// later in the same window) from poisoning the cache mid-fetch and dropping
// honest serving peers against a different hash.
func (c *signedWitnessCache) putIfNewer(ann wit.SignedWitnessAnnouncement) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gcLocked()
	if existing, ok := c.entries[ann.BlockHash]; ok {
		if existing.announcement.WitnessHash != ann.WitnessHash {
			wit2ConflictingWitnessHashMeter.Mark(1)
			return false
		}
		// Same WitnessHash, recent: dedup.
		if time.Since(existing.receivedAt) < wit2RelayWindow {
			return false
		}
	}
	c.entries[ann.BlockHash] = &signedAnnounceEntry{
		announcement: ann,
		receivedAt:   time.Now(),
	}
	return true
}

// get returns the cached announcement for a block hash, if present and fresh.
func (c *signedWitnessCache) get(blockHash common.Hash) (wit.SignedWitnessAnnouncement, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[blockHash]
	if !ok {
		return wit.SignedWitnessAnnouncement{}, false
	}
	if time.Since(e.receivedAt) > wit2AnnounceTTL {
		return wit.SignedWitnessAnnouncement{}, false
	}
	return e.announcement, true
}

// gcLocked drops entries past the TTL. Caller must hold the write lock.
func (c *signedWitnessCache) gcLocked() {
	cutoff := time.Now().Add(-wit2AnnounceTTL)
	for h, e := range c.entries {
		if e.receivedAt.Before(cutoff) {
			delete(c.entries, h)
		}
	}
}

// verifySignedAnnouncement returns the recovered signer address if the
// signature is structurally valid; otherwise an error. Validator-set
// membership is checked separately against the consensus engine.
func verifySignedAnnouncement(ann wit.SignedWitnessAnnouncement) (common.Address, error) {
	if len(ann.Signature) != wit.SignatureLength {
		return common.Address{}, errInvalidSignatureLength
	}
	digest := wit.WitnessAnnouncementSigningHash(ann.BlockHash, ann.BlockNumber, ann.WitnessHash)
	pubkey, err := crypto.Ecrecover(digest.Bytes(), ann.Signature)
	if err != nil {
		return common.Address{}, err
	}
	var addr common.Address
	copy(addr[:], crypto.Keccak256(pubkey[1:])[12:])
	return addr, nil
}

// cosendWitnessAnnouncement co-sends a witness announcement to every peer
// that just received the full block via the propagate=true fanout, provided
// the peer doesn't already have the witness. WIT2 peers receive the signed
// variant; older peers receive the unsigned WIT1 announce. Skipped entirely
// when the local node hasn't yet stored the witness or doesn't have a
// signing key configured.
func (h *handler) cosendWitnessAnnouncement(blockHash common.Hash, blockNumber uint64, transfer []*ethPeer, staticAndTrustedPeers []*ethPeer) {
	if !h.chain.HasWitness(blockHash) {
		return
	}
	ann, hasSigned := h.signLocalWitnessAnnouncement(blockHash, blockNumber)
	if !hasSigned {
		return
	}
	witnessRecipientsByID := make(map[string]*witPeer)
	for _, wp := range h.peers.peersWithoutWitness(blockHash) {
		witnessRecipientsByID[wp.Peer.ID()] = wp
	}
	cosend := func(id string) {
		wp, ok := witnessRecipientsByID[id]
		if !ok {
			return
		}
		if wp.Peer.Version() >= wit.WIT2 {
			wp.Peer.AsyncSendSignedWitnessAnnouncement(ann)
		} else {
			wp.Peer.AsyncSendNewWitnessHash(blockHash, blockNumber)
		}
	}
	for _, peer := range transfer {
		cosend(peer.Peer.ID())
	}
	for _, peer := range staticAndTrustedPeers {
		cosend(peer.ID())
	}
}

// lookupSignedWitnessHash returns the BP-signed witness hash for a block, if
// the local cache has a verified announcement. Used by the witness manager
// on fetch success to verify byte-correctness against the signed commitment.
func (h *handler) lookupSignedWitnessHash(blockHash common.Hash) (common.Hash, bool) {
	ann, ok := h.signedWitnesses.get(blockHash)
	if !ok {
		return common.Hash{}, false
	}
	return ann.WitnessHash, true
}

// cacheVerifiedWitnessForServing receives canonical-encoded witness bytes from
// the fetcher after a successful, byte-verified paged download and stores them
// in the in-flight cache so peers can fetch the body before this node finishes
// chain-write. Bytes here have already passed verifyAgainstSignedHash (when a
// signed announcement was on file), or arrived via WIT1 unsigned path; in both
// cases they're the same bytes the upstream peer agreed upon, so serving them
// to downstream peers cannot expose this node to byte-mismatch drops beyond
// the upstream's already-incurred risk.
func (h *handler) cacheVerifiedWitnessForServing(blockHash common.Hash, witnessBytes []byte, witnessHash common.Hash) {
	if h.pendingWitnessBodies == nil {
		return
	}
	h.pendingWitnessBodies.put(blockHash, witnessBytes, witnessHash)
	// We now hold servable bytes: hand them straight to any peer that asked for
	// this body before we had it, so a stateless consumer stops polling us with
	// empty GetWitness and imports immediately.
	h.pushWitnessBytesToWaiters(blockHash, witnessBytes)
}

// signLocalWitnessAnnouncement looks up the witness body for blockHash, hashes
// it, and signs the announcement digest using the engine's authorized signer.
// The result is cached so subsequent broadcasts of the same block reuse the
// signature without recomputing the keccak.
//
// Returns (announcement, true) on success. Returns (_, false) if any of:
// - no signer configured (full node not producing blocks)
// - witness bytes not yet stored in chain
// - signing failed
//
// Cost: ~150ms keccak over a 50MB witness, plus ~100μs ECDSA. Off the
// block-production critical path; runs once per produced block on the
// announce path.
func (h *handler) signLocalWitnessAnnouncement(blockHash common.Hash, blockNumber uint64) (wit.SignedWitnessAnnouncement, bool) {
	if cached, ok := h.signedWitnesses.get(blockHash); ok {
		return cached, true
	}

	borEngine, ok := h.chain.Engine().(*bor.Bor)
	if !ok {
		return wit.SignedWitnessAnnouncement{}, false
	}
	if (borEngine.CurrentSigner() == common.Address{}) {
		return wit.SignedWitnessAnnouncement{}, false
	}

	witnessHash, ok := h.canonicalWitnessHash(blockHash)
	if !ok {
		return wit.SignedWitnessAnnouncement{}, false
	}
	preimage := wit.WitnessAnnouncementSigningPreImage(blockHash, blockNumber, witnessHash)
	_, sig, err := borEngine.SignBytes(accounts.MimetypeBorWitnessAnnounce, preimage)
	if err != nil {
		log.Warn("wit2: failed to sign witness announcement", "blockHash", blockHash, "err", err)
		return wit.SignedWitnessAnnouncement{}, false
	}

	ann := wit.SignedWitnessAnnouncement{
		BlockHash:   blockHash,
		BlockNumber: blockNumber,
		WitnessHash: witnessHash,
		Signature:   sig,
	}
	h.signedWitnesses.putIfNewer(ann)
	return ann, true
}

// canonicalWitnessHash reads the witness bytes for blockHash from chain
// storage and returns the WIT2 chunked-aggregate commitment over those bytes.
// Witness.EncodeRLP is now deterministic (state nodes sorted), so every newly
// written witness blob is canonical at write time and can be hashed directly
// without a decode/re-encode round-trip — saving roughly the cost of one RLP
// pass on the announce path. Returns (_, false) when no witness is on file.
func (h *handler) canonicalWitnessHash(blockHash common.Hash) (common.Hash, bool) {
	stored := h.chain.GetWitness(blockHash)
	if len(stored) == 0 {
		return common.Hash{}, false
	}
	return stateless.WitnessCommitHash(stored), true
}

// isScheduledProducer binds the recovered signer of a wit2 announcement to the
// actual block producer of the announced block. When the block header is
// locally available — the common case — we recover the seal-signer of the
// header and require an exact address match. Validator-set membership is no
// longer sufficient: any current validator could otherwise sign an
// announcement for another producer's block hash with a forged WitnessHash,
// poisoning this node's cache and dropping honest serving peers.
//
// Returns (ok, headerAvailable):
//   - ok=true, headerAvailable=true: signer matches the block producer; safe
//     to cache and relay.
//   - ok=false, headerAvailable=true: confirmed bad signer; the caller MUST
//     strike the relayer.
//   - ok=false, headerAvailable=false: header not yet local. The announce
//     cannot be bound to a producer right now. The caller MUST NOT strike —
//     this is expected during the cosend window where a signed announce
//     races the block to the receiver. The handler stashes the announce in
//     the deferred queue and the chain-head loop re-evaluates it once the
//     block arrives.
//
// Header presence is checked first regardless of engine: an announce we
// cannot match to a local block is by definition unverifiable here. Only
// after the header is on file do we route into the bor-specific producer
// recovery (or short-circuit to ok=true on non-bor test chains).
func (h *handler) isScheduledProducer(signer common.Address, blockNumber uint64, blockHash common.Hash) (bool, bool) {
	header := h.chain.GetHeaderByHash(blockHash)
	if header == nil {
		wit2HeaderUnknownMeter.Mark(1)
		return false, false
	}
	borEngine, isBor := h.chain.Engine().(*bor.Bor)
	if !isBor {
		// Non-bor chain (tests): header presence already validated above; the
		// producer check is bor-specific and intentionally skipped here.
		if header.Number.Uint64() != blockNumber {
			return false, true
		}
		return true, true
	}
	return verifyScheduledProducer(borEngine, header, signer, blockNumber, blockHash)
}

// drainDeferredAnnouncesFor re-evaluates any deferred announcement whose
// blockHash now matches a header that has just been imported. On verification
// success the announce is cached in signedWitnesses, the original sender is
// credited as announce-known, and the announce is relayed to peers that have
// not seen it. On confirmed mis-binding (signer ≠ producer) the deferred
// entry is dropped — relayers cannot be re-struck post-hoc since we lost the
// peer reference between deferral and drain.
//
// Called from the chain-head subscription on each new block. Also exposed for
// direct invocation in tests.
func (h *handler) drainDeferredAnnouncesFor(blockHash common.Hash) {
	if h.deferredAnnounces == nil {
		return
	}
	entry, ok := h.deferredAnnounces.take(blockHash)
	if !ok {
		return
	}
	signer, err := verifySignedAnnouncement(entry.announcement)
	if err != nil {
		// Should be unreachable: we re-verified the same bytes that already
		// passed the signature check at acceptSignedAnnouncement time.
		// Surfaced via metric in case a future refactor reorders this.
		wit2InvalidSigMeter.Mark(1)
		log.Debug("wit2: deferred announce failed signature re-check", "blockHash", blockHash, "err", err)
		return
	}
	prodOk, headerAvailable := h.isScheduledProducer(signer, entry.announcement.BlockNumber, blockHash)
	if !prodOk {
		if !headerAvailable {
			// Header still not local — re-stash with fresh receivedAt so the
			// next chain-head event can try again before the TTL expires.
			h.deferredAnnounces.put(entry.announcement, entry.peerID)
			return
		}
		wit2NotValidatorMeter.Mark(1)
		log.Debug("wit2: deferred announce signer is not the scheduled producer",
			"blockHash", blockHash, "signer", signer)
		return
	}
	if !h.signedWitnesses.putIfNewer(entry.announcement) {
		wit2DuplicateMeter.Mark(1)
		return
	}
	// Credit the original sender as announce-known so we don't re-relay back.
	if peer := h.peers.peer(entry.peerID); peer != nil && peer.witPeer != nil {
		peer.witPeer.Peer.AddKnownAnnounce(blockHash)
	}
	h.relaySignedAnnouncement(entry.peerID, entry.announcement)
}

// verifyScheduledProducer is the pure decision logic for binding a wit2
// announcement signer to the block producer of `blockHash`. Split from
// isScheduledProducer so it can be unit-tested without standing up a full
// handler. Returns the same (ok, headerAvailable) shape — see
// isScheduledProducer for the contract.
func verifyScheduledProducer(borEngine *bor.Bor, header *types.Header, signer common.Address, blockNumber uint64, blockHash common.Hash) (bool, bool) {
	if header == nil {
		wit2HeaderUnknownMeter.Mark(1)
		log.Debug("wit2: header for announced block not yet local; deferring until block arrives",
			"blockHash", blockHash, "blockNumber", blockNumber)
		return false, false
	}
	if header.Number.Uint64() != blockNumber {
		log.Debug("wit2: announce blockNumber does not match local header",
			"blockHash", blockHash, "announced", blockNumber, "local", header.Number.Uint64())
		return false, true
	}
	producer, err := borEngine.Author(header)
	if err != nil {
		log.Debug("wit2: failed to recover header sealer", "blockHash", blockHash, "err", err)
		return false, true
	}
	if producer != signer {
		log.Debug("wit2: announce signer is not the block producer",
			"blockHash", blockHash, "producer", producer, "signer", signer)
		return false, true
	}
	return true, true
}
