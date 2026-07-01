package eth

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
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

// deferredAnnounceMaxCandidatesPerBlock bounds how many distinct candidate
// announcements we keep for a single block hash while its header is unknown.
// At deferral time the header is absent by definition, so the honest producer's
// signer cannot be distinguished from a forger: an attacker that knows the
// (gossiped) block hash can submit a structurally-valid signature over a forged
// WitnessHash. Keeping only one candidate lets that forgery evict the honest
// commitment (whether by last- or first-write-wins) — on import the forged
// entry fails producer-binding and is dropped, leaving no signed hash on file
// and silently downgrading the block to WIT1 byte-handling. Holding several
// candidates and letting the post-import drain promote the one whose signer is
// the real producer closes that vector; an attacker now has to crowd out every
// honest candidate from distinct peer connections to win. One candidate per
// (blockHash, peerID) keeps a single peer to a single slot per block.
const deferredAnnounceMaxCandidatesPerBlock = 8

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
//
// Each block hash maps to a bounded set of candidate announcements (at most one
// per peer, at most deferredAnnounceMaxCandidatesPerBlock total) so a forged
// header-racing announce cannot evict the honest commitment before the producer
// binding can be checked at import. The drain promotes the candidate whose
// signer is the actual block producer.
type deferredAnnounceCache struct {
	mu          sync.RWMutex
	entries     map[common.Hash][]*deferredAnnounceEntry
	perPeer     map[string]int // live entry count per originating peer
	total       int            // total live candidate entries across all hashes
	capacity    int
	perPeerCap  int
	maxPerBlock int
}

func newDeferredAnnounceCache(capacity int) *deferredAnnounceCache {
	perPeerCap := capacity / deferredAnnouncePerPeerDivisor
	if perPeerCap < 1 {
		perPeerCap = 1
	}
	return &deferredAnnounceCache{
		entries:     make(map[common.Hash][]*deferredAnnounceEntry),
		perPeer:     make(map[string]int),
		capacity:    capacity,
		perPeerCap:  perPeerCap,
		maxPerBlock: deferredAnnounceMaxCandidatesPerBlock,
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

// removeAtLocked removes the candidate at index idx for blockHash, refunding the
// per-peer credit and the total counter. Caller must hold the write lock.
func (c *deferredAnnounceCache) removeAtLocked(blockHash common.Hash, idx int) {
	cands := c.entries[blockHash]
	c.decPeerLocked(cands[idx].peerID)
	cands = append(cands[:idx], cands[idx+1:]...)
	if len(cands) == 0 {
		delete(c.entries, blockHash)
	} else {
		c.entries[blockHash] = cands
	}
	c.total--
}

// put stores the announcement as a candidate for its block hash. A re-put from
// the same peer for the same block refreshes that peer's single candidate in
// place (net-zero slot change). A new peer adds a distinct candidate, subject to
// (in order) the per-block candidate cap, the per-peer cap, and the global cap
// (which evicts the oldest entry across all blocks). Each cap drop records a
// metric. Holding multiple candidates is deliberate — see
// deferredAnnounceMaxCandidatesPerBlock.
func (c *deferredAnnounceCache) put(ann wit.SignedWitnessAnnouncement, peerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gcLocked()

	cands := c.entries[ann.BlockHash]
	// One candidate per (blockHash, peerID): refresh this peer's slot in place.
	for i, e := range cands {
		if e.peerID == peerID {
			cands[i] = &deferredAnnounceEntry{announcement: ann, peerID: peerID, receivedAt: time.Now()}
			c.entries[ann.BlockHash] = cands
			return
		}
	}

	// New distinct peer for this block. Keep early candidates rather than
	// evicting them: the per-block cap is the structural defense that stops a
	// burst of sybil peers from crowding out the honest producer's announce.
	if len(cands) >= c.maxPerBlock {
		wit2DeferredPerBlockDropMeter.Mark(1)
		return
	}
	if c.perPeer[peerID] >= c.perPeerCap {
		wit2DeferredPerPeerDropMeter.Mark(1)
		return
	}
	if c.total >= c.capacity {
		c.evictOldestLocked()
		cands = c.entries[ann.BlockHash] // re-read: eviction may have touched this block
	}

	c.entries[ann.BlockHash] = append(cands, &deferredAnnounceEntry{
		announcement: ann,
		peerID:       peerID,
		receivedAt:   time.Now(),
	})
	c.perPeer[peerID]++
	c.total++
}

// evictOldestLocked drops the single oldest candidate across all blocks and
// peers to make room for a new one (linear scan is cheap at the configured
// size). Caller must hold the write lock.
func (c *deferredAnnounceCache) evictOldestLocked() {
	var oldestHash common.Hash
	oldestIdx := -1
	var oldest time.Time
	for h, cands := range c.entries {
		for i, e := range cands {
			if oldestIdx == -1 || e.receivedAt.Before(oldest) {
				oldest = e.receivedAt
				oldestHash = h
				oldestIdx = i
			}
		}
	}
	if oldestIdx >= 0 {
		c.removeAtLocked(oldestHash, oldestIdx)
	}
}

// take removes and returns all fresh candidates for blockHash. Returns
// ok=false when none are present or all have expired; expired entries are
// dropped (with their credits refunded) regardless.
func (c *deferredAnnounceCache) take(blockHash common.Hash) ([]*deferredAnnounceEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cands, ok := c.entries[blockHash]
	if !ok {
		return nil, false
	}
	delete(c.entries, blockHash)
	cutoff := time.Now().Add(-wit2AnnounceTTL)
	out := make([]*deferredAnnounceEntry, 0, len(cands))
	for _, e := range cands {
		c.decPeerLocked(e.peerID)
		c.total--
		if e.receivedAt.Before(cutoff) {
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// peekPeer returns the freshest candidate's relaying peer for blockHash without
// consuming any entry, used by the fetch path to find a pull target when no
// marked body-holder exists. The entries stay in place so the post-import drain
// still runs the real producer verification, promotion, and relay.
//
// Candidates are scanned in freshness order against isLive: the freshest
// candidate whose peer is still connected wins. This matters because deferred
// candidates are deliberately retained across the relayer's disconnect (a
// producer-signed commitment must outlive the peer that relayed it, so the
// post-import drain can still promote it). Without the liveness filter, a
// disconnected relayer whose entry happens to be freshest would keep being
// returned as the pull target — stranding the consumer for up to the TTL even
// though up to deferredAnnounceMaxCandidatesPerBlock-1 other live candidates
// hold the same commitment. A nil isLive treats every candidate as eligible.
func (c *deferredAnnounceCache) peekPeer(blockHash common.Hash, isLive func(peerID string) bool) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cutoff := time.Now().Add(-wit2AnnounceTTL)
	var best *deferredAnnounceEntry
	for _, e := range c.entries[blockHash] {
		if e.receivedAt.Before(cutoff) {
			continue
		}
		if isLive != nil && !isLive(e.peerID) {
			continue
		}
		if best == nil || e.receivedAt.After(best.receivedAt) {
			best = e
		}
	}
	if best == nil {
		return "", false
	}
	return best.peerID, true
}

// hasWitnessHash reports whether a fresh candidate for blockHash commits to
// witnessHash. Used by the broadcast path to bind pushed bytes to a pending
// (deferred, not yet producer-verified) commitment without consuming it.
func (c *deferredAnnounceCache) hasWitnessHash(blockHash common.Hash, witnessHash common.Hash) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cutoff := time.Now().Add(-wit2AnnounceTTL)
	for _, e := range c.entries[blockHash] {
		if e.receivedAt.Before(cutoff) {
			continue
		}
		if e.announcement.WitnessHash == witnessHash {
			return true
		}
	}
	return false
}

// has reports whether any fresh candidate exists for blockHash.
func (c *deferredAnnounceCache) has(blockHash common.Hash) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cutoff := time.Now().Add(-wit2AnnounceTTL)
	for _, e := range c.entries[blockHash] {
		if !e.receivedAt.Before(cutoff) {
			return true
		}
	}
	return false
}

// hashes returns a snapshot of the block hashes with at least one fresh
// candidate. Used by the chain-head loop to find deferred announces whose
// header has since become local (batched insertChain fires one accumulated
// ChainHeadEvent, so head-hash-only draining would miss intermediate blocks).
func (c *deferredAnnounceCache) hashes() []common.Hash {
	c.mu.RLock()
	defer c.mu.RUnlock()
	cutoff := time.Now().Add(-wit2AnnounceTTL)
	out := make([]common.Hash, 0, len(c.entries))
	for h, cands := range c.entries {
		for _, e := range cands {
			if !e.receivedAt.Before(cutoff) {
				out = append(out, h)
				break
			}
		}
	}
	return out
}

// gcLocked drops candidates past the TTL, refunding credits. Caller must hold
// the write lock.
func (c *deferredAnnounceCache) gcLocked() {
	cutoff := time.Now().Add(-wit2AnnounceTTL)
	for h, cands := range c.entries {
		kept := cands[:0]
		for _, e := range cands {
			if e.receivedAt.Before(cutoff) {
				c.decPeerLocked(e.peerID)
				c.total--
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(c.entries, h)
		} else {
			c.entries[h] = kept
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
