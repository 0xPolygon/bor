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

// TestProcessWitnessResponseDropsOnHashMismatch is the load-bearing safety
// guarantee for WIT2 pre-import serving: a peer that returns bytes whose
// keccak256 doesn't match the BP-signed witnessHash must be dropped, even
// if every other check passes.
//
// Without this, a malicious server could pollute downstream relayers with
// bytes the BP never committed to, and the relayers would face state-root
// failures during execution that they cannot attribute to the right party.
func TestProcessWitnessResponseDropsOnHashMismatch(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(101)
	hash := block.Hash()

	// Prepare a "correct" witness that the BP signed over.
	correct := createTestWitnessForBlock(block)
	var buf bytes.Buffer
	if err := correct.EncodeRLP(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	signedWitnessHash := stateless.WitnessCommitHash(buf.Bytes())

	// The peer will return a different witness — same block number, but
	// the trie differs, producing different bytes and a different hash.
	differentHeader := types.CopyHeader(block.Header())
	differentHeader.GasUsed = 999_999_999
	differentBlock := types.NewBlockWithHeader(differentHeader)
	rogueWitness := createTestWitnessForBlock(differentBlock)

	// Inject the signed-witness lookup so processWitnessResponse uses it.
	tw.manager.parentSignedWitnessHash = func(h common.Hash) (common.Hash, bool) {
		if h == hash {
			return signedWitnessHash, true
		}
		return common.Hash{}, false
	}

	// Seed pending state so the failure handler back-off path is exercised.
	tw.manager.mu.Lock()
	tw.manager.pending[hash] = &witnessRequestState{
		op:       &blockOrHeaderInject{origin: "rogue", block: block},
		announce: blockAnnounceForTest("rogue", hash, block.NumberU64()),
	}
	tw.manager.mu.Unlock()

	// Fabricate the response container expected by processWitnessResponse.
	res := &eth.Response{
		Time: time.Millisecond,
		Done: make(chan error, 1),
		Res:  []*stateless.Witness{rogueWitness},
	}

	tw.manager.processWitnessResponse("rogue", hash, res, time.Now())

	tw.mu.Lock()
	defer tw.mu.Unlock()
	if len(tw.droppedPeers) != 1 || tw.droppedPeers[0] != "rogue" {
		t.Fatalf("expected the lying peer to be dropped, got drops=%v", tw.droppedPeers)
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
