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
		c.evictOldestLocked()
	}

	c.entries[ann.BlockHash] = &deferredAnnounceEntry{
		announcement: ann,
		peerID:       peerID,
		receivedAt:   time.Now(),
	}
	c.perPeer[peerID]++
}

// evictOldestLocked drops the oldest entry across all peers to make room for
// a new one (linear scan is cheap at the configured size). Caller must hold
// the write lock.
func (c *deferredAnnounceCache) evictOldestLocked() {
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

// peek returns the announcement for blockHash and the peer that relayed it,
// without consuming the entry, if a fresh one exists. Used by the broadcast
// path to bind a pushed body to a pending (deferred, not yet
// producer-verified) announcement, and by the fetch path to find a pull
// target when no marked peer exists. The entry must stay in place so the
// post-import drain still runs the real producer verification, promotion,
// and relay.
func (c *deferredAnnounceCache) peek(blockHash common.Hash) (wit.SignedWitnessAnnouncement, string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[blockHash]
	if !ok || time.Since(e.receivedAt) > wit2AnnounceTTL {
		return wit.SignedWitnessAnnouncement{}, "", false
	}
	return e.announcement, e.peerID, true
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
