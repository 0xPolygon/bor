package sequencer

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestPendingStoreInvalidOperationsLeaveNoState(t *testing.T) {
	fixture := newPendingRPCCoverageFixture(t, 3, common.HexToHash("0x1"))
	store := NewPendingStore(nil)
	if store.publish(nil, nil, fixture.state, nil, 0) {
		t.Fatal("nil block was published")
	}
	if store.publish(fixture.block, nil, nil, nil, 0) {
		t.Fatal("nil state was published")
	}
	if execution, ok := store.ClaimPreconf(nil); ok || execution != nil {
		t.Fatalf("nil block claim = %+v, %v", execution, ok)
	}
	if prefix, ok := store.claimPreconfPrefix(nil); ok || prefix != nil {
		t.Fatalf("nil prefix claim = %+v, %v", prefix, ok)
	}
	if reason, invalid := store.CheckPreconfInvalidation(nil, nil); invalid || reason != "" {
		t.Fatalf("nil invalidation = %q, %v", reason, invalid)
	}
	if logs := store.RejectClaimedPreconf(nil); len(logs) != 0 {
		t.Fatalf("nil rejection logs = %+v", logs)
	}
	if logs := store.CompletePreconf(nil, false); len(logs) != 0 {
		t.Fatalf("nil completion logs = %+v", logs)
	}
	if entryMatchesCanonical(new(pendingEntry), fixture.block, nil) {
		t.Fatal("entry without an RPC view matched canonical data")
	}
	if block, payload, ok := preparePending(&blockEnv{statedb: fixture.state}, nil, common.Hash{}, nil); ok || block != nil || payload.view != nil {
		t.Fatalf("pending data without a header = %v, %+v, %v", block, payload, ok)
	}

	store.mu.Lock()
	store.removeLocked(pendingKey{number: 99})
	store.mu.Unlock()
	if len(store.entries) != 0 || store.hasActive {
		t.Fatal("invalid operations mutated the pending store")
	}
}

func TestPendingStoreFailedCompletionPreservesUnclaimedEntry(t *testing.T) {
	fixture := newPendingRPCCoverageFixture(t, 3, common.HexToHash("0x1"))
	store := NewPendingStore(nil)
	if !store.publish(fixture.block, types.Receipts{fixture.receipt}, fixture.state, nil, 0) {
		t.Fatal("publish failed")
	}
	if logs := store.CompletePreconf(fixture.block, false); len(logs) != 0 {
		t.Fatalf("unclaimed completion logs = %+v", logs)
	}
	if pending := store.PendingBlock(); pending != fixture.block {
		t.Fatalf("unclaimed entry was removed: %v", pending)
	}
}

func TestConsumerRejectsUnavailablePrefixClaims(t *testing.T) {
	h := startExecHarness(t)

	if prefix, ok := (&Consumer{}).ClaimPreconfPrefix(nil); ok || prefix != nil {
		t.Fatalf("nil block prefix = %+v, %v", prefix, ok)
	}
	sprintBlock := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(16)})
	consumer := &Consumer{chain: h.chain, index: NewIndex()}
	if prefix, ok := consumer.ClaimPreconfPrefix(sprintBlock); ok || prefix != nil {
		t.Fatalf("sprint-boundary prefix = %+v, %v", prefix, ok)
	}
	stateSyncBlock := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(3)}).WithBody(types.Body{
		Transactions: types.Transactions{types.NewTx(&types.StateSyncTx{})},
	})
	if prefix, ok := consumer.ClaimPreconfPrefix(stateSyncBlock); ok || prefix != nil {
		t.Fatalf("state-sync prefix = %+v, %v", prefix, ok)
	}
	ordinary := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(3)}).WithBody(types.Body{
		Transactions: types.Transactions{h.transfer(t, 0)},
	})
	if prefix, ok := consumer.ClaimPreconfPrefix(ordinary); ok || prefix != nil {
		t.Fatalf("missing prefix = %+v, %v", prefix, ok)
	}
}

func TestConsumerReleasesPrefixWhenWorkerIsUnavailable(t *testing.T) {
	h := startExecHarness(t)
	store := NewPendingStore(nil)
	_, candidate := publishPrefixWithTail(t, store)
	consumer := &Consumer{chain: h.chain, index: NewIndex(), store: store}
	if execution, ok := consumer.ClaimPreconfPrefix(candidate); ok || execution != nil {
		t.Fatalf("prefix without worker = %+v, %v", execution, ok)
	}
	if pending := store.PendingBlock(); pending != nil {
		t.Fatalf("released prefix remained pending: %v", pending)
	}
}

func TestConsumerRejectsClaimsFromStaleHead(t *testing.T) {
	h := startExecHarness(t)
	consumer := &Consumer{chain: h.chain, index: NewIndex()}
	consumer.reconciled.Store(&types.Header{Number: big.NewInt(99)})
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(3)})
	if execution, ok := consumer.ClaimPreconf(block); ok || execution != nil {
		t.Fatalf("stale-head claim = %+v, %v", execution, ok)
	}
}
