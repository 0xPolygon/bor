package fetcher

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
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

// TestProcessWitnessResponseDoesNotDropOnByteMismatch encodes the post-
// adversarial-review safety policy: when the served witness bytes do not
// match the BP-signed witnessHash on file, the manager must back off and
// retry, but it MUST NOT drop the byte-server. The accepted announcement
// only proves *some* BP signed *some* hash — not that the hash matches the
// canonical witness. A faulty or malicious scheduled producer that signs a
// bogus hash would otherwise weaponise this code path to disconnect every
// honest peer serving the real witness.
//
// The mismatched bytes are still rejected (not cached for serving), and the
// pending state stays alive with a fresh back-off so another peer (or another
// announcement) gets a chance. Blame-pinning belongs at execution time, where
// import-side validation can attribute fault to signer vs. server vs. caller.
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
	tw.manager.parentSignedWitnessHash = func(h common.Hash) (common.Hash, bool) {
		if h == hash {
			return rogueSignedHash, true
		}
		return common.Hash{}, false
	}

	tw.manager.mu.Lock()
	tw.manager.pending[hash] = &witnessRequestState{
		op:       &blockOrHeaderInject{origin: "honest", block: block},
		announce: blockAnnounceForTest("honest", hash, block.NumberU64()),
	}
	tw.manager.mu.Unlock()

	res := &eth.Response{
		Time: time.Millisecond,
		Done: make(chan error, 1),
		Res:  []*stateless.Witness{canonical},
	}

	tw.manager.processWitnessResponse("honest-server", hash, res, time.Now())

	tw.mu.Lock()
	defer tw.mu.Unlock()
	if len(tw.droppedPeers) != 0 {
		t.Fatalf("byte-server must not be dropped on signed-hash mismatch (BP may have signed bogus); drops=%v", tw.droppedPeers)
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
	hash := block.Hash()
	witness := createTestWitnessForBlock(block)
	var buf bytes.Buffer
	if err := witness.EncodeRLP(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	matchingHash := stateless.WitnessCommitHash(buf.Bytes())

	tw.manager.parentSignedWitnessHash = func(h common.Hash) (common.Hash, bool) {
		return matchingHash, true
	}

	tw.manager.mu.Lock()
	tw.manager.pending[hash] = &witnessRequestState{
		op:       &blockOrHeaderInject{origin: "honest", block: block},
		announce: blockAnnounceForTest("honest", hash, block.NumberU64()),
	}
	tw.manager.mu.Unlock()

	res := &eth.Response{
		Time: time.Millisecond,
		Done: make(chan error, 1),
		Res:  []*stateless.Witness{witness},
	}

	tw.manager.processWitnessResponse("honest", hash, res, time.Now())

	tw.mu.Lock()
	defer tw.mu.Unlock()
	if len(tw.droppedPeers) != 0 {
		t.Fatalf("honest peer must not be dropped on hash match; drops=%v", tw.droppedPeers)
	}
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
	var buf bytes.Buffer
	if err := witness.EncodeRLP(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	want := stateless.WitnessCommitHash(buf.Bytes())

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
	tw.manager.parentSignedWitnessHash = func(h common.Hash) (common.Hash, bool) {
		if h == hash {
			return want, true
		}
		return common.Hash{}, false
	}

	tw.manager.mu.Lock()
	tw.manager.pending[hash] = &witnessRequestState{
		op:       &blockOrHeaderInject{origin: "honest", block: block},
		announce: blockAnnounceForTest("honest", hash, block.NumberU64()),
	}
	tw.manager.mu.Unlock()

	res := &eth.Response{
		Time: time.Millisecond,
		Done: make(chan error, 1),
		Res:  []*stateless.Witness{witness},
	}

	tw.manager.processWitnessResponse("honest", hash, res, time.Now())

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
	hash := block.Hash()
	witness := createTestWitnessForBlock(block)

	// No lookup configured → skip path.
	tw.manager.parentSignedWitnessHash = func(common.Hash) (common.Hash, bool) {
		return common.Hash{}, false
	}

	tw.manager.mu.Lock()
	tw.manager.pending[hash] = &witnessRequestState{
		op:       &blockOrHeaderInject{origin: "wit1-peer", block: block},
		announce: blockAnnounceForTest("wit1-peer", hash, block.NumberU64()),
	}
	tw.manager.mu.Unlock()

	res := &eth.Response{
		Time: time.Millisecond,
		Done: make(chan error, 1),
		Res:  []*stateless.Witness{witness},
	}

	tw.manager.processWitnessResponse("wit1-peer", hash, res, time.Now())

	tw.mu.Lock()
	defer tw.mu.Unlock()
	if len(tw.droppedPeers) != 0 {
		t.Fatalf("WIT1 fallback must not drop any peer; drops=%v", tw.droppedPeers)
	}
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
	tw.manager.parentSignedWitnessHash = func(common.Hash) (common.Hash, bool) {
		return common.Hash{}, false
	}

	body, _, ok := tw.manager.verifyAgainstSignedHash("peer1", hash, witness)
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

	tw.manager.mu.Lock()
	tw.manager.pending[hash] = &witnessRequestState{
		op:       &blockOrHeaderInject{origin: "relay-only", block: block},
		announce: blockAnnounceForTest("relay-only", hash, block.NumberU64()),
	}
	tw.manager.mu.Unlock()

	emptyRes := func() *eth.Response {
		return &eth.Response{Time: time.Millisecond, Done: make(chan error, 1), Res: []*stateless.Witness{}}
	}

	// Drive several consecutive empty responses, as an announce-only relayer
	// that does not yet hold the body would produce.
	var lastDelay time.Duration
	for i := 0; i < 8; i++ {
		tw.manager.processWitnessResponse("relay-only", hash, emptyRes(), time.Now())
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

	tw.manager.mu.Lock()
	tw.manager.pending[hash] = &witnessRequestState{
		op:       &blockOrHeaderInject{origin: "announce-only", block: block},
		announce: blockAnnounceForTest("announce-only", hash, block.NumberU64()),
	}
	tw.manager.mu.Unlock()

	res := &eth.Response{
		Time: time.Millisecond,
		Done: make(chan error, 1),
		Res:  []*stateless.Witness{}, // empty/unavailable
	}

	tw.manager.processWitnessResponse("announce-only", hash, res, time.Now())

	tw.mu.Lock()
	defer tw.mu.Unlock()
	if len(tw.droppedPeers) != 0 {
		t.Fatalf("empty response must NOT drop the responder; drops=%v", tw.droppedPeers)
	}
}
