package fetcher

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

// blockAnnounceForTest constructs a minimal blockAnnounce wired to a fetch
// function that fails closed. Used to seed manager.pending so that the
// processWitnessResponse path can take its happy/sad branches without
// going through the full announce → request flow.
func blockAnnounceForTest(origin string, hash common.Hash, number uint64) *blockAnnounce {
	return &blockAnnounce{
		origin:       origin,
		hash:         hash,
		number:       number,
		time:         time.Now(),
		fetchWitness: func(common.Hash, chan *eth.Response) (*eth.Request, error) { return nil, errors.New("noop") },
	}
}

// primePendingWitness seeds manager.pending with a request state for the
// block under the given origin, exactly as the announce → request flow would.
func primePendingWitness(tw *testWitnessManager, origin string, block *types.Block) {
	tw.manager.mu.Lock()
	tw.manager.pending[block.Hash()] = &witnessRequestState{
		op:       &blockOrHeaderInject{origin: origin, block: block},
		announce: blockAnnounceForTest(origin, block.Hash(), block.NumberU64()),
	}
	tw.manager.mu.Unlock()
}

// witnessResponse wraps witnesses in a synthetic eth.Response, as the request
// dispatcher would deliver them. Call with no arguments for an empty response
// (peer does not hold the body).
func witnessResponse(witnesses ...*stateless.Witness) *eth.Response {
	return &eth.Response{
		Time: time.Millisecond,
		Done: make(chan error, 1),
		Res:  witnesses,
	}
}

// encodedCommitHash returns the WIT2 commit hash over the witness's canonical
// RLP encoding — the value a BP would sign for this witness.
func encodedCommitHash(t *testing.T, witness *stateless.Witness) common.Hash {
	t.Helper()

	var buf bytes.Buffer
	if err := witness.EncodeRLP(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return stateless.WitnessCommitHash(buf.Bytes())
}

// encodedSize returns the canonical RLP-encoded byte length of the witness —
// the value a BP would sign as WitnessSize for this witness.
func encodedSize(t *testing.T, witness *stateless.Witness) uint64 {
	t.Helper()

	var buf bytes.Buffer
	if err := witness.EncodeRLP(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return uint64(buf.Len())
}

// requireNoDroppedPeers fails the test when any peer was drop-disconnected.
func requireNoDroppedPeers(t *testing.T, tw *testWitnessManager, context string) {
	t.Helper()

	tw.mu.Lock()
	defer tw.mu.Unlock()
	if len(tw.droppedPeers) != 0 {
		t.Fatalf("%s; drops=%v", context, tw.droppedPeers)
	}
}

// TestProcessWitnessResponseDoesNotDropOnByteMismatch encodes the
// non-determinism-tolerant policy: a served witness whose bytes hash
// differently from the BP-signed WitnessHash is NOT a fault — witnesses vary
// between honest nodes (BlockSTM speculative reads), so a differing hash within
// the signed size band is a valid witness. The manager must neither drop NOR
// strike the server for a within-band hash divergence; import-time state-root
// execution arbitrates content-correctness, which is the responsibility of the
// producer that signed the announcement, not the serving peer. Only an oversized
// witness (beyond the size band) is rejected — covered separately.
func TestProcessWitnessResponseDoesNotDropOnByteMismatch(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(101)
	hash := block.Hash()

	// The honest server returns the canonical witness for this block — its
	// keccak commitment is `canonical`.
	canonical := createTestWitnessForBlock(block)

	// Simulate a malicious / faulty BP that signed a bogus, unrelated hash.
	// processWitnessResponse will see canonical bytes whose hash does not
	// match what parentSignedWitnessHash reports.
	rogueSignedHash := common.HexToHash("0xdeadbeef")
	signedSize := encodedSize(t, canonical)
	tw.manager.parentSignedWitnessHash = func(h common.Hash) (common.Hash, uint64, bool) {
		if h == hash {
			return rogueSignedHash, signedSize, true
		}
		return common.Hash{}, 0, false
	}

	struck := 0
	tw.manager.parentStrikeWitnessServer = func(string) { struck++ }

	primePendingWitness(tw, "honest", block)

	tw.manager.processWitnessResponse("honest-server", hash, witnessResponse(canonical), time.Now())

	requireNoDroppedPeers(t, tw, "byte-server must not be dropped on signed-hash divergence (witnesses are non-deterministic)")
	if struck != 0 {
		t.Fatalf("a witness within the signed size band must not strike the server despite a differing hash; got %d strikes", struck)
	}
}

// TestProcessWitnessResponseAcceptsMatchingHash is the contrapositive: a
// peer that returns bytes whose keccak256 matches the BP-signed hash must
// not be dropped. State-root mismatches on subsequent execution are handled
// elsewhere and do not reflect on the server.
func TestProcessWitnessResponseAcceptsMatchingHash(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(101)
	witness := createTestWitnessForBlock(block)
	matchingHash := encodedCommitHash(t, witness)

	tw.manager.parentSignedWitnessHash = func(h common.Hash) (common.Hash, uint64, bool) {
		return matchingHash, encodedSize(t, witness), true
	}

	primePendingWitness(tw, "honest", block)

	tw.manager.processWitnessResponse("honest", block.Hash(), witnessResponse(witness), time.Now())

	requireNoDroppedPeers(t, tw, "honest peer must not be dropped on hash match")
}

// TestProcessWitnessResponseCachesForServingAfterByteCheck is the regression
// for the missing pre-import-serving cache populate. The fetcher must hand
// canonical-encoded bytes back to the eth handler after a verified fetch so
// downstream peers can ask THIS node for the body before chain-write
// finishes. Without this callback firing, multi-hop fast propagation has no
// body source past hop-1 — the entire WIT2 latency win evaporates.
func TestProcessWitnessResponseCachesForServingAfterByteCheck(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(202)
	hash := block.Hash()
	witness := createTestWitnessForBlock(block)
	want := encodedCommitHash(t, witness)

	var (
		gotBlock common.Hash
		gotBytes []byte
		gotHash  common.Hash
	)
	tw.manager.parentCacheWitnessForServing = func(blockHash common.Hash, witnessBytes []byte, witnessHash common.Hash) {
		gotBlock = blockHash
		gotBytes = append([]byte{}, witnessBytes...)
		gotHash = witnessHash
	}
	tw.manager.parentSignedWitnessHash = func(h common.Hash) (common.Hash, uint64, bool) {
		if h == hash {
			return want, encodedSize(t, witness), true
		}
		return common.Hash{}, 0, false
	}

	primePendingWitness(tw, "honest", block)

	tw.manager.processWitnessResponse("honest", hash, witnessResponse(witness), time.Now())

	if gotBlock != hash {
		t.Fatalf("cache callback not invoked or wrong blockHash: got %s want %s", gotBlock.Hex(), hash.Hex())
	}
	if gotHash != want {
		t.Fatalf("cache callback received wrong witnessHash: got %s want %s", gotHash.Hex(), want.Hex())
	}
	if len(gotBytes) == 0 {
		t.Fatal("cache callback received empty bytes; pre-import serving cache will not be populated")
	}
}

// TestProcessWitnessResponseSkipsCheckWhenNoSignature confirms the WIT1
// fallback path: when the receiver has no BP-signed announcement on file
// for a block, byte-correctness verification is skipped (there's nothing to
// verify against), and behavior matches the pre-WIT2 code path.
func TestProcessWitnessResponseSkipsCheckWhenNoSignature(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(101)
	witness := createTestWitnessForBlock(block)

	// No lookup configured → skip path.
	tw.manager.parentSignedWitnessHash = func(common.Hash) (common.Hash, uint64, bool) {
		return common.Hash{}, 0, false
	}

	primePendingWitness(tw, "wit1-peer", block)

	tw.manager.processWitnessResponse("wit1-peer", block.Hash(), witnessResponse(witness), time.Now())

	requireNoDroppedPeers(t, tw, "WIT1 fallback must not drop any peer")
}

// TestVerifyAgainstSignedHashSkipsEncodeWhenNoSignedHash is the regression
// for the blame-asymmetry bug: caching unverified bytes for serving means a
// downstream peer would ask us for the body, get bytes that don't match THEIR
// BP-signed hash (because we never had one to compare against), and drop us.
// The fix gates serving-cache population on having a BP-signed hash on file —
// verifyAgainstSignedHash returns body=nil on the WIT1 path, and the caller
// short-circuits the cache call (no-op when body is empty).
func TestVerifyAgainstSignedHashSkipsEncodeWhenNoSignedHash(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(303)
	hash := block.Hash()
	witness := createTestWitnessForBlock(block)

	cacheCalls := 0
	tw.manager.parentCacheWitnessForServing = func(common.Hash, []byte, common.Hash) {
		cacheCalls++
	}
	// No signed hash on file for any block → verification must return
	// body=nil so the caller skips the cache.
	tw.manager.parentSignedWitnessHash = func(common.Hash) (common.Hash, uint64, bool) {
		return common.Hash{}, 0, false
	}

	body, _, _, ok := tw.manager.verifyAgainstSignedHash("peer1", hash, witness)
	if !ok {
		t.Fatalf("verifyAgainstSignedHash returned ok=false on WIT1 path")
	}
	if body != nil {
		t.Fatalf("WIT1 path returned non-nil body; downstream peers will see uncovered bytes (len=%d)", len(body))
	}
	tw.manager.cacheVerifiedWitnessForServing(hash, body, common.Hash{})
	if cacheCalls != 0 {
		t.Fatalf("cache populated without BP-signed hash on file; downstream peers will drop us as liars (calls=%d)", cacheCalls)
	}
}

// TestEmptyResponseBacksOffToAvoidHammering pins the consumer-side mitigation
// for the WIT2 stateless regression. In an all-WIT2 fleet a stateless node
// always fetches the body from an announce-only relayer (no peer is ever
// marked as a body-holder), and the relayer answers "empty" until it has
// pulled+imported the block itself. The pre-fix code reset announce.time to
// time.Now() on every empty response, so the next tick re-fired ~gatherSlack
// later — a tight poll loop that hammered the single relayer hundreds of times
// (the ~15x "Empty response received" count seen on devnet) without ever
// shortening the wait.
//
// The fix keeps the first couple of retries fast (so the body is picked up the
// instant the relayer obtains it — the common case) and then backs off
// exponentially, capping the empty-poll rate without discarding the pending
// request (whose witness provably exists — a BP signed it).
func TestEmptyResponseBacksOffToAvoidHammering(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(606)
	hash := block.Hash()

	primePendingWitness(tw, "relay-only", block)

	// Drive several consecutive empty responses, as an announce-only relayer
	// that does not yet hold the body would produce.
	var lastDelay time.Duration
	for i := 0; i < 8; i++ {
		tw.manager.processWitnessResponse("relay-only", hash, witnessResponse(), time.Now())
		tw.manager.mu.Lock()
		st := tw.manager.pending[hash]
		if st == nil {
			tw.manager.mu.Unlock()
			t.Fatalf("pending entry dropped on empty response at attempt %d; a provably-existing witness must not be discarded", i)
		}
		lastDelay = time.Until(st.announce.time)
		tw.manager.mu.Unlock()
	}

	// After repeated empties the next retry must be deferred (backoff), not
	// scheduled immediately. Pre-fix this is ~0 (tight hammering loop).
	if lastDelay < 200*time.Millisecond {
		t.Fatalf("expected empty-response backoff to defer the next retry after repeated empties; got delay=%v (no backoff → relayer is hammered)", lastDelay)
	}
}

// TestProcessWitnessResponseEmptyDoesNotDropAnnounceOnlyPeer locks the
// fast-path safety property: a peer that only saw the signed announce (and
// has not yet imported the body) responds with empty bytes when asked. That
// is NOT lying — they simply do not have it yet. Dropping them here would
// shrink the pool of candidate body sources and re-introduce the regression
// where WIT2 multi-hop propagation has nowhere to fetch from at hop>=2.
//
// Byte-mismatch (handled by TestProcessWitnessResponseDropsOnHashMismatch)
// is the only condition that should drop a serving peer.
func TestProcessWitnessResponseEmptyDoesNotDropAnnounceOnlyPeer(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(404)
	hash := block.Hash()

	primePendingWitness(tw, "announce-only", block)

	tw.manager.processWitnessResponse("announce-only", hash, witnessResponse(), time.Now())

	requireNoDroppedPeers(t, tw, "empty response must NOT drop the responder")
}

// TestSignedHashQuarantineAfterDistinctMismatches is the regression for
// Vikram's finding that a bad/stale BP-signed witness hash blocks stateless
// import until the announcement TTL expires: every honest server's canonical
// bytes mismatch the bogus hash, so the fetch keeps getting rejected. After
// distinct servers mismatch the same signed hash, the manager must quarantine
// it and fall back to WIT1 (import-time execution arbitrates the bytes) so the
// block can import promptly. A single bad server must NOT trigger the quarantine
// — that would let one liar force a WIT1 downgrade.
func TestSignedHashQuarantineAfterDistinctMismatches(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(303)
	hash := block.Hash()
	witness := createTestWitnessForBlock(block)

	// Signed size is tiny, so the canonical witness is oversized against the
	// band for every server that serves it — the size-oracle analog of the
	// bad/stale producer case. Distinct oversizing servers quarantine the hash.
	tw.manager.parentSignedWitnessHash = func(h common.Hash) (common.Hash, uint64, bool) {
		if h == hash {
			return common.HexToHash("0xbadbadbad"), 1, true
		}
		return common.Hash{}, 0, false
	}
	primePendingWitness(tw, "peerA", block)

	// First distinct server mismatches: rejected, but not yet quarantined.
	if _, _, _, ok := tw.manager.verifyAgainstSignedHash("peerA", hash, witness); ok {
		t.Fatal("mismatch must return ok=false")
	}
	if tw.manager.isSignedHashQuarantined(hash) {
		t.Fatal("a single distinct mismatch must not quarantine the signed hash")
	}

	// Second DISTINCT server mismatches: the signed hash is now the suspect.
	if _, _, _, ok := tw.manager.verifyAgainstSignedHash("peerB", hash, witness); ok {
		t.Fatal("mismatch must return ok=false")
	}
	if !tw.manager.isSignedHashQuarantined(hash) {
		t.Fatal("distinct-source mismatches must quarantine the signed hash")
	}

	// A subsequent fetch falls back to WIT1: body=nil, ok=true, so the witness
	// is accepted for import (execution validates) instead of stalling for 30s.
	body, _, _, ok := tw.manager.verifyAgainstSignedHash("peerC", hash, witness)
	if !ok {
		t.Fatal("quarantined signed hash must fall back to WIT1 (accept, execution validates)")
	}
	if body != nil {
		t.Fatal("WIT1 fallback must not return bytes for the pre-import serving cache")
	}
}

// TestMarkWitnessUnavailableClearsQuarantine is the regression for the F8
// quarantine-map leak (review finding C-2): clearSignedHashMismatch is wired to
// the three pending-removal exits (soft-fail removePending, safeEnqueue, forget)
// but NOT to the retry-exhaustion exit (markWitnessUnavailable). A block that
// gets quarantined and then exhausts its fetch retries must not leak its
// quarantine entry for the process lifetime.
func TestMarkWitnessUnavailableClearsQuarantine(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	hash := common.HexToHash("0xfeed")

	// Quarantine the signed hash via two distinct-server mismatches.
	tw.manager.recordSignedHashMismatch(hash, "s1")
	if quarantined, _ := tw.manager.recordSignedHashMismatch(hash, "s2"); !quarantined {
		t.Fatal("two distinct mismatches must quarantine the hash")
	}
	if !tw.manager.isSignedHashQuarantined(hash) {
		t.Fatal("precondition: hash must be quarantined")
	}

	// The retry-exhaustion exit must clear the quarantine state (else it leaks
	// permanently — there is no TTL or periodic GC on the quarantine maps).
	tw.manager.markWitnessUnavailable(hash)

	if tw.manager.isSignedHashQuarantined(hash) {
		t.Fatal("markWitnessUnavailable must clear quarantine state; otherwise it leaks for the process lifetime")
	}
}

// TestWit2QuarantineStateExpiresAfterLateMismatch is the regression for the
// unbounded wit2MismatchPeers/wit2Quarantined growth finding: clearSignedHashMismatch
// is wired to the 4 pending-removal exits, but a mismatch that arrives AFTER a
// hash already left m.pending through one of those exits (forget in this case)
// creates a fresh entry that none of them will ever clear again. The TTL sweep
// (cleanupWit2QuarantineState) is the only backstop for that case, so this test
// drives exactly that ordering and asserts the entry does not survive past its TTL.
func TestWit2QuarantineStateExpiresAfterLateMismatch(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	hash := common.HexToHash("0xd15c0")

	// The block resolves through another path first (forget clears any state —
	// there is none yet, so this is a no-op here, but marks the point after which
	// none of the 4 pending-removal exits will run again for this hash).
	tw.manager.forget(hash)

	// A slow/malicious peer's mismatching response arrives late, after forget()
	// already ran. verifyAgainstSignedHash still creates fresh mismatch/quarantine
	// state for the hash even though nothing will ever call clearSignedHashMismatch
	// for it again.
	tw.manager.recordSignedHashMismatch(hash, "late-peer")

	tw.manager.wit2QuarantineMu.Lock()
	_, tracked := tw.manager.wit2StateExpiry[hash]
	tw.manager.wit2QuarantineMu.Unlock()
	if !tracked {
		t.Fatal("recordSignedHashMismatch must register a TTL for the entry it creates")
	}

	// Not yet expired: the sweep must leave it alone.
	tw.manager.cleanupWit2QuarantineState()
	tw.manager.wit2QuarantineMu.Lock()
	_, stillTracked := tw.manager.wit2MismatchPeers[hash]
	tw.manager.wit2QuarantineMu.Unlock()
	if !stillTracked {
		t.Fatal("an unexpired entry must survive a cleanup sweep")
	}

	// Force expiry (as if wit2QuarantineStateTTL had elapsed) and sweep again.
	tw.manager.wit2QuarantineMu.Lock()
	tw.manager.wit2StateExpiry[hash] = time.Now().Add(-time.Second)
	tw.manager.wit2QuarantineMu.Unlock()
	tw.manager.cleanupWit2QuarantineState()

	tw.manager.wit2QuarantineMu.Lock()
	_, peersLeft := tw.manager.wit2MismatchPeers[hash]
	_, quarantineLeft := tw.manager.wit2Quarantined[hash]
	_, expiryLeft := tw.manager.wit2StateExpiry[hash]
	tw.manager.wit2QuarantineMu.Unlock()
	if peersLeft || quarantineLeft || expiryLeft {
		t.Fatal("an expired late-mismatch entry must not survive a cleanup sweep — it would otherwise leak for the process lifetime")
	}
}

// TestVerifyAgainstSignedHashStrikesNonEmptyMismatchServer is the regression for
// the quarantine-weaponization / unpenalized-bad-bytes finding (E-1/E-2): a peer
// that serves a NON-EMPTY witness whose bytes mismatch the on-file signed hash is
// misbehaving relative to an honest empty "not ready" response, and must be struck
// so a sybil cannot drive the distinct-server quarantine (or feed bad bytes that
// fail import) for free. The penalty is a STRIKE, not a drop — a faulty/malicious
// BP that signed a bogus hash makes honest servers mismatch too, so a single
// mismatch stays tolerated (see TestProcessWitnessResponseDoesNotDropOnByteMismatch).
func TestVerifyAgainstSignedHashStrikesNonEmptyMismatchServer(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(305)
	hash := block.Hash()
	witness := createTestWitnessForBlock(block)

	// Signed size is tiny → the served (canonical) witness is oversized.
	tw.manager.parentSignedWitnessHash = func(h common.Hash) (common.Hash, uint64, bool) {
		if h == hash {
			return common.HexToHash("0xdeadbeef"), 1, true
		}
		return common.Hash{}, 0, false
	}

	var struck []string
	tw.manager.parentStrikeWitnessServer = func(peer string) {
		struck = append(struck, peer)
	}

	if _, _, _, ok := tw.manager.verifyAgainstSignedHash("sybil-server", hash, witness); ok {
		t.Fatal("oversized witness must return ok=false")
	}
	if len(struck) != 1 || struck[0] != "sybil-server" {
		t.Fatalf("a server that served an oversized witness must be struck; got strikes=%v", struck)
	}

	// Must NOT also drop the peer (drop would let a bad BP hash disconnect honest
	// servers — the whole reason mismatch does not drop).
	requireNoDroppedPeers(t, tw, "byte-mismatch must strike, not drop")
}

// TestVerifyAgainstSignedHashDoesNotStrikeOnWit1Path guards the converse: with no
// signed hash on file (WIT1 fallback) there is nothing to verify against, so the
// server is never struck.
func TestVerifyAgainstSignedHashDoesNotStrikeOnWit1Path(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(306)
	hash := block.Hash()
	witness := createTestWitnessForBlock(block)

	tw.manager.parentSignedWitnessHash = func(common.Hash) (common.Hash, uint64, bool) {
		return common.Hash{}, 0, false
	}
	struck := 0
	tw.manager.parentStrikeWitnessServer = func(string) { struck++ }

	tw.manager.verifyAgainstSignedHash("wit1-peer", hash, witness)
	if struck != 0 {
		t.Fatalf("WIT1 path (no signed hash on file) must not strike; got %d", struck)
	}
}

// TestSignedHashSingleServerDoesNotQuarantine guards the asymmetry: many
// mismatches from the SAME single server must never quarantine a signed hash —
// otherwise one malicious byte-server could force every block it touches down
// to WIT1, defeating the byte-authenticated fast path.
func TestSignedHashSingleServerDoesNotQuarantine(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(304)
	hash := block.Hash()
	witness := createTestWitnessForBlock(block)

	tw.manager.parentSignedWitnessHash = func(common.Hash) (common.Hash, uint64, bool) {
		return common.HexToHash("0xdeadbeef"), 1, true
	}
	primePendingWitness(tw, "lonely", block)

	for range 4 {
		tw.manager.verifyAgainstSignedHash("lonely", hash, witness)
	}
	if tw.manager.isSignedHashQuarantined(hash) {
		t.Fatal("repeated mismatches from a single server must not quarantine the signed hash")
	}
}

// TestVerifyAgainstSignedHashStrikesSolePeerOncePerBlock is the regression for
// the honest-sole-peer self-DoS: when only one peer is announce-known for a
// block whose BP-signed hash is bad/stale, the retry loop re-hits that same
// peer every ~gatherSlack. Striking on every mismatch would jail an honest
// sole witness source in ~1s, inverting the documented "single mismatch stays
// tolerated" guarantee. A peer must be struck at most once per (peer, block);
// cross-block garbage and a second distinct peer are penalized/quarantined
// elsewhere.
func TestVerifyAgainstSignedHashStrikesSolePeerOncePerBlock(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(307)
	hash := block.Hash()
	witness := createTestWitnessForBlock(block)

	tw.manager.parentSignedWitnessHash = func(common.Hash) (common.Hash, uint64, bool) {
		return common.HexToHash("0xdeadbeef"), 1, true
	}
	struck := 0
	tw.manager.parentStrikeWitnessServer = func(string) { struck++ }
	primePendingWitness(tw, "lonely", block)

	for range 6 {
		tw.manager.verifyAgainstSignedHash("lonely", hash, witness)
	}
	if struck != 1 {
		t.Fatalf("an honest sole peer must be struck at most once per block, not once per retry; got %d strikes", struck)
	}
	if tw.manager.isSignedHashQuarantined(hash) {
		t.Fatal("a single server must never quarantine the signed hash")
	}
}

// TestRecordSignedHashMismatchReturns pins the (quarantined, firstForPeer) return
// contract, which drives the caller's "log the downgrade once" and "strike a peer
// at most once per block" decisions. The side-effect (isSignedHashQuarantined) is
// covered elsewhere; here we assert the return values themselves.
func TestRecordSignedHashMismatchReturns(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	hash := common.HexToHash("0xbeef")

	// An empty peer is ignored entirely and never counts toward the threshold.
	if q, f := tw.manager.recordSignedHashMismatch(hash, ""); q || f {
		t.Fatalf("empty peer must return (false,false), got (%v,%v)", q, f)
	}
	// First mismatch from s1: below threshold, and first for that peer.
	if q, f := tw.manager.recordSignedHashMismatch(hash, "s1"); q || !f {
		t.Fatalf("first mismatch for s1 must return (false,true), got (%v,%v)", q, f)
	}
	// Same peer again: still below threshold and no longer first — the caller must
	// not double-strike the same peer across retries.
	if q, f := tw.manager.recordSignedHashMismatch(hash, "s1"); q || f {
		t.Fatalf("repeat mismatch for s1 must return (false,false), got (%v,%v)", q, f)
	}
	if tw.manager.isSignedHashQuarantined(hash) {
		t.Fatal("one distinct server must not quarantine")
	}
	// Second DISTINCT server reaches the threshold: newly quarantined, first for s2.
	if q, f := tw.manager.recordSignedHashMismatch(hash, "s2"); !q || !f {
		t.Fatalf("second distinct mismatch must return (true,true), got (%v,%v)", q, f)
	}
	if !tw.manager.isSignedHashQuarantined(hash) {
		t.Fatal("distinct-server threshold must quarantine the signed hash")
	}
	// Already quarantined: further mismatches are no-ops returning (false,false).
	if q, f := tw.manager.recordSignedHashMismatch(hash, "s3"); q || f {
		t.Fatalf("post-quarantine mismatch must return (false,false), got (%v,%v)", q, f)
	}
}

// TestEmptyResponseBackoff pins the empty-response re-poll schedule: an immediate
// fast-retry window, then exponential doubling from the base, clamped at the max.
func TestEmptyResponseBackoff(t *testing.T) {
	for n := 0; n <= emptyResponseFastRetries; n++ {
		if d := emptyResponseBackoff(n); d != 0 {
			t.Fatalf("n=%d: fast-retry window must return 0, got %v", n, d)
		}
	}
	if d := emptyResponseBackoff(emptyResponseFastRetries + 1); d != emptyResponseBaseBackoff {
		t.Fatalf("first backoff must equal base %v, got %v", emptyResponseBaseBackoff, d)
	}
	if d := emptyResponseBackoff(emptyResponseFastRetries + 2); d != 2*emptyResponseBaseBackoff {
		t.Fatalf("second backoff must double the base to %v, got %v", 2*emptyResponseBaseBackoff, d)
	}
	if d := emptyResponseBackoff(1000); d != emptyResponseMaxBackoff {
		t.Fatalf("large n must clamp to max %v, got %v", emptyResponseMaxBackoff, d)
	}
}

// TestVerifyAgainstSignedHashAcceptsDivergentHashWithinBand is the core
// non-determinism-tolerance property: a witness whose bytes hash DIFFERENTLY
// from the BP-signed WitnessHash but whose size is within the band around the
// signed WitnessSize must be ACCEPTED FOR IMPORT (ok=true), not rejected and not
// struck. It must NOT be returned for the serving cache (body=nil): only a
// byte-identical (hash-matching) witness is re-served/relayed, so the serving
// and relay fast-paths carry only the BP's own bytes. Import-time state-root
// execution is the content-correctness arbiter; a differing hash is expected
// because honest nodes produce different-but-valid witnesses.
func TestVerifyAgainstSignedHashAcceptsDivergentHashWithinBand(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(308)
	hash := block.Hash()
	witness := createTestWitnessForBlock(block)

	// BP signed a different hash (non-deterministic divergence) but a size the
	// received witness is within band of.
	tw.manager.parentSignedWitnessHash = func(h common.Hash) (common.Hash, uint64, bool) {
		if h == hash {
			return common.HexToHash("0xd1ffe7e17"), encodedSize(t, witness), true
		}
		return common.Hash{}, 0, false
	}
	struck := 0
	tw.manager.parentStrikeWitnessServer = func(string) { struck++ }

	body, _, diverged, ok := tw.manager.verifyAgainstSignedHash("honest-diverging", hash, witness)
	if !ok {
		t.Fatal("a witness within the signed size band must be accepted for import despite a differing hash")
	}
	if !diverged {
		t.Fatal("a within-band witness with a differing hash must be flagged diverged so an import failure is charged to the server")
	}
	if body != nil {
		t.Fatal("a non-identical within-band witness must import but NOT be cached for serving (body=nil), so the fast-path carries only the BP's bytes")
	}
	if struck != 0 {
		t.Fatalf("within-band divergence must not strike the server; got %d strikes", struck)
	}
	if tw.manager.isSignedHashQuarantined(hash) {
		t.Fatal("within-band divergence must not quarantine the signed size")
	}
}

// TestVerifyAgainstSignedHashServesOnExactMatch is the contrapositive of the
// above: a witness byte-identical to the BP's (hash match) within the band is
// accepted AND returned as body for the pre-import serving cache, so the BP's
// own bytes propagate on the fast-path.
func TestVerifyAgainstSignedHashServesOnExactMatch(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(309)
	hash := block.Hash()
	witness := createTestWitnessForBlock(block)
	match := encodedCommitHash(t, witness)

	tw.manager.parentSignedWitnessHash = func(h common.Hash) (common.Hash, uint64, bool) {
		if h == hash {
			return match, encodedSize(t, witness), true
		}
		return common.Hash{}, 0, false
	}

	body, gotHash, diverged, ok := tw.manager.verifyAgainstSignedHash("honest-matching", hash, witness)
	if !ok {
		t.Fatal("a byte-identical within-band witness must be accepted")
	}
	if diverged {
		t.Fatal("a byte-identical witness must not be flagged diverged: an import failure of the BP's own bytes is the BP's fault, not the server's")
	}
	if body == nil {
		t.Fatal("a byte-identical witness must return canonical bytes for the serving cache")
	}
	if gotHash != match {
		t.Fatalf("served witness hash must be the signed hash; got %s want %s", gotHash.Hex(), match.Hex())
	}
}

// TestAcceptableWitnessSizeCeiling pins the two-tier ceiling: the dynamic
// working bound is wit2SizeBandMultiplier*signedSize, clamped by the retained
// gas-derived absolute cap so an implausibly large signed size cannot lift the
// band.
func TestAcceptableWitnessSizeCeiling(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	abs := tw.manager.calculatePageThreshold() * maxPageSizeMB * bytesPerMiB

	// A small signed size → the band (3*S) dominates, well under the absolute.
	if got := tw.manager.acceptableWitnessSizeCeiling(1000); got != wit2SizeBandMultiplier*1000 {
		t.Fatalf("band = %d*signedSize expected %d, got %d", wit2SizeBandMultiplier, wit2SizeBandMultiplier*1000, got)
	}
	// A signed size whose 3x band would exceed the absolute must clamp to it.
	if got := tw.manager.acceptableWitnessSizeCeiling(abs); got != abs {
		t.Fatalf("band exceeding the absolute must clamp to %d, got %d", abs, got)
	}
	// An implausibly large signed size must still clamp to the absolute.
	if got := tw.manager.acceptableWitnessSizeCeiling(abs * 10); got != abs {
		t.Fatalf("implausibly large signed size must clamp to absolute %d, got %d", abs, got)
	}
}

// TestWitnessSizeExceedsCeiling pins the accept/reject boundary of the size
// oracle: a witness exactly at the ceiling is accepted (not oversized), and one
// byte over is rejected.
func TestWitnessSizeExceedsCeiling(t *testing.T) {
	if witnessSizeExceedsCeiling(100, 100) {
		t.Fatal("a witness exactly at the ceiling must be accepted (not oversized)")
	}
	if !witnessSizeExceedsCeiling(101, 100) {
		t.Fatal("a witness one byte over the ceiling must be rejected as oversized")
	}
	if witnessSizeExceedsCeiling(99, 100) {
		t.Fatal("a witness below the ceiling must be accepted")
	}
}

// TestVerifyAgainstSignedHashWithoutLookupIsWit1 pins the WIT1-only wiring: with
// no signed-hash lookup configured at all, every witness is accepted for import
// (ok), nothing is offered for pre-import serving (body nil), and nothing is
// flagged diverged (there is no commitment to diverge from).
func TestVerifyAgainstSignedHashWithoutLookupIsWit1(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()
	tw.manager.parentSignedWitnessHash = nil

	block := createTestBlock(310)
	body, gotHash, diverged, ok := tw.manager.verifyAgainstSignedHash("peer", block.Hash(), createTestWitnessForBlock(block))
	if !ok || body != nil || gotHash != (common.Hash{}) || diverged {
		t.Fatalf("WIT1-only path must return (nil, zero, false, true); got body=%v hash=%s diverged=%v ok=%v", body != nil, gotHash.Hex(), diverged, ok)
	}
}
