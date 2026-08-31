package sequencer

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/0xPolygon/sequence-store-proto/commitment"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
)

type partialReuseEngine struct {
	*ethash.Ethash
}

func (e *partialReuseEngine) VerifyHeader(consensus.ChainHeaderReader, *types.Header) error {
	return nil
}

func (e *partialReuseEngine) CalcDifficulty(consensus.ChainHeaderReader, uint64, *types.Header) *big.Int {
	return big.NewInt(1)
}

func buildPartialReuseBlock(t *testing.T, h *execHarness, txs types.Transactions) (*types.Block, types.Receipts) {
	t.Helper()
	parent := h.chain.CurrentBlock()
	statedb, err := h.chain.StateAt(parent.Root)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	env := newBlockEnv(h.chain, statedb, openOn(parent, h.config, commitment.Head{}).GetBlockOpen(), nil)
	for _, tx := range txs {
		if _, _, err := env.applyTransaction(tx); err != nil {
			t.Fatalf("execute canonical transaction: %v", err)
		}
	}
	body := &types.Body{Transactions: append(types.Transactions(nil), txs...)}
	block, receipts, _, err := h.chain.Engine().FinalizeAndAssemble(h.chain, types.CopyHeader(env.header), env.statedb.Copy(), body, cloneReceipts(env.receipts))
	if err != nil {
		t.Fatalf("finalize canonical block: %v", err)
	}
	return block, receipts
}

func partialReuseHarness(t *testing.T) *execHarness {
	t.Helper()
	return startExecHarnessEngine(t, finalizationConfig(), vm.Config{}, &partialReuseEngine{Ethash: ethash.NewFullFaker()})
}

func publishPrefix(t *testing.T, h *execHarness, txs types.Transactions) *session {
	t.Helper()
	s := h.session()
	cur := handleOK(t, s, openOn(h.chain.CurrentBlock(), h.config, commitment.Head{0x51}))
	for _, tx := range txs {
		raw, err := tx.MarshalBinary()
		if err != nil {
			t.Fatalf("marshal prefix transaction: %v", err)
		}
		cur = handleOK(t, s, recordEntry(raw, cur))
	}
	return s
}

func TestCanonicalImportResumesPreconfPrefix(t *testing.T) {
	h := partialReuseHarness(t)
	txs := make(types.Transactions, 100)
	for index := range txs {
		txs[index] = h.transfer(t, uint64(index))
	}
	block, _ := buildPartialReuseBlock(t, h, txs)
	s := publishPrefix(t, h, txs[:80])
	h.chain.SetPreconfProvider(s.consumer)

	s.applyMu.Lock()
	insertDone := make(chan error, 1)
	go func() {
		_, err := h.chain.InsertChain(types.Blocks{block}, false)
		insertDone <- err
	}()
	deadline := time.Now().Add(time.Second)
	for !s.env.interrupt.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	interrupted := s.env.interrupt.Load()
	if !interrupted {
		s.applyMu.Unlock()
		t.Fatal("canonical import did not interrupt the active preconfirmation execution")
	}
	var insertErr error
	select {
	case insertErr = <-insertDone:
	case <-time.After(time.Second):
		s.applyMu.Unlock()
		t.Fatal("canonical import waited for the preconfirmation execution")
	}
	s.applyMu.Unlock()
	s.applyMu.Lock()
	s.clearEnv()
	s.parked = nil
	s.applyMu.Unlock()
	if insertErr != nil {
		t.Fatalf("insert partially executed block: %v", insertErr)
	}
	if h.chain.CurrentBlock().Hash() != block.Hash() {
		t.Fatalf("canonical head = %s, want %s", h.chain.CurrentBlock().Hash(), block.Hash())
	}
	stateDB, err := h.chain.StateAt(block.Root())
	if err != nil {
		t.Fatalf("canonical state: %v", err)
	}
	if nonce := stateDB.GetNonce(h.addr); nonce != 100 {
		t.Fatalf("sender nonce = %d, want 100", nonce)
	}
	if pending, _, _ := s.consumer.Pending(); pending != nil {
		t.Fatalf("committed prefix remained pending: %v", pending)
	}
	if records := rawdb.ReadInvalidPreconfs(h.chain.DB(), 1); len(records) != 0 {
		t.Fatalf("valid prefix recorded as invalid: %+v", records)
	}
}

func TestCanonicalImportFallsBackFromInvalidPrefixState(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0), h.transfer(t, 1)}
	block, _ := buildPartialReuseBlock(t, h, txs)
	s := publishPrefix(t, h, txs[:1])
	store := s.consumer.pendingStore()
	key := pendingKey{number: block.NumberU64(), parent: block.ParentHash()}
	store.mu.Lock()
	store.entries[key].RPCView.State.(*pendingStateReader).state.SetNonce(h.addr, 0, tracing.NonceChangeUnspecified)
	store.mu.Unlock()
	h.chain.SetPreconfProvider(s.consumer)

	if _, err := h.chain.InsertChain(types.Blocks{block}, false); err != nil {
		t.Fatalf("normal fallback import: %v", err)
	}
	if h.chain.CurrentBlock().Hash() != block.Hash() {
		t.Fatalf("canonical head = %s, want %s", h.chain.CurrentBlock().Hash(), block.Hash())
	}
	if records := rawdb.ReadInvalidPreconfs(h.chain.DB(), 1); len(records) != 0 {
		t.Fatalf("local prefix fallback recorded an invalid preconfirmation: %+v", records)
	}
}

func TestPendingStoreClaimsOnlyMatchingCanonicalPrefix(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0), h.transfer(t, 1)}
	block, receipts := buildPartialReuseBlock(t, h, txs)
	s := publishPrefix(t, h, txs[:1])
	store := s.consumer.pendingStore()
	pendingBlock, pendingReceipts, pendingState := store.Pending()
	generation := s.env.generation

	for _, test := range []struct {
		name   string
		mutate func(*types.Header)
	}{
		{name: "timestamp", mutate: func(header *types.Header) { header.Time++ }},
		{name: "gas limit", mutate: func(header *types.Header) { header.GasLimit++ }},
		{name: "base fee", mutate: func(header *types.Header) { header.BaseFee = new(big.Int).Add(header.BaseFee, big.NewInt(1)) }},
		{name: "difficulty", mutate: func(header *types.Header) { header.Difficulty = big.NewInt(2) }},
		{name: "beacon root", mutate: func(header *types.Header) { root := common.Hash{1}; header.ParentBeaconRoot = &root }},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := block.Header()
			test.mutate(header)
			candidate := block.WithSeal(header)
			if _, ok := store.claimPreconfPrefix(candidate); ok {
				t.Fatal("claimed prefix with incompatible execution context")
			}
			if _, invalid := store.CheckPreconfInvalidation(candidate, receipts); !invalid {
				t.Fatal("incompatible execution context was not marked invalid")
			}
		})
	}

	different := block.WithBody(types.Body{Transactions: types.Transactions{txs[1], txs[0]}})
	if _, ok := store.claimPreconfPrefix(different); ok {
		t.Fatal("claimed a different transaction prefix")
	}
	h.chain.Config().Bor.Sprint = map[string]uint64{"0": 4}
	if _, ok := s.consumer.ClaimPreconfPrefix(block); ok {
		t.Fatal("claimed a prefix without reusable sprint finalization inputs")
	}
	h.chain.Config().Bor.Sprint = map[string]uint64{"0": 16}
	stateSyncBlock := block.WithBody(types.Body{Transactions: append(append(types.Transactions(nil), txs...), types.NewTx(&types.StateSyncTx{}))})
	if _, ok := s.consumer.ClaimPreconfPrefix(stateSyncBlock); ok {
		t.Fatal("claimed a canonical body containing a state-sync transaction")
	}
	prefix, ok := store.claimPreconfPrefix(block)
	if !ok || len(prefix.Transactions) != 1 || prefix.Result.GasUsed != receipts[0].CumulativeGasUsed {
		t.Fatalf("prefix = %+v, ok = %v", prefix, ok)
	}
	prefixState, err := prefix.State.NewStateDB()
	if err != nil {
		t.Fatalf("prefix state: %v", err)
	}
	if _, invalid := store.CheckPreconfInvalidation(block, receipts); invalid {
		t.Fatal("canonical extension of claimed prefix was marked invalid")
	}
	pendingState.SetNonce(h.addr, 9, tracing.NonceChangeUnspecified)
	if store.publish(pendingBlock, pendingReceipts, pendingState, nil, generation) {
		t.Fatal("claimed prefix accepted a concurrent worker publication")
	}
	store.CompletePreconf(block, false)
	if store.publish(pendingBlock, pendingReceipts, pendingState, nil, generation) {
		t.Fatal("aborted prefix claim accepted its superseded worker")
	}
	if nonce := prefixState.GetNonce(h.addr); nonce != 1 {
		t.Fatalf("claimed snapshot changed with live publication: nonce=%d", nonce)
	}
	generation = store.begin(block.NumberU64(), block.ParentHash(), true)
	if !store.publish(pendingBlock, pendingReceipts, pendingState, nil, generation) {
		t.Fatal("fresh worker publication failed after aborted claim")
	}
	store.mu.Lock()
	store.entries[pendingKey{number: block.NumberU64(), parent: block.ParentHash()}].canonicalBase = false
	store.mu.Unlock()
	if _, ok := store.claimPreconfPrefix(block); ok {
		t.Fatal("claimed prefix built on a speculative parent")
	}
}

func TestPendingStorePrefixClaimRacesPublication(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0), h.transfer(t, 1)}
	block, _ := buildPartialReuseBlock(t, h, txs)
	s := publishPrefix(t, h, txs[:1])
	store := s.consumer.pendingStore()
	pendingBlock, receipts, statedb := store.Pending()
	generation := s.env.generation

	for attempt := 0; attempt < 50; attempt++ {
		var (
			claimed bool
			wg      sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, claimed = store.claimPreconfPrefix(block)
		}()
		go func() {
			defer wg.Done()
			store.publish(pendingBlock, receipts, statedb, nil, generation)
		}()
		wg.Wait()
		if !claimed {
			t.Fatalf("attempt %d did not claim a valid prefix", attempt)
		}
		store.CompletePreconf(block, false)
		generation = store.begin(block.NumberU64(), block.ParentHash(), true)
		if !store.publish(pendingBlock, receipts, statedb, nil, generation) {
			t.Fatalf("attempt %d failed to republish a fresh worker", attempt)
		}
	}
}

func TestCanonicalReanchorClearsSpeculativeHashes(t *testing.T) {
	h := partialReuseHarness(t)
	s := h.session()
	parent := h.chain.CurrentBlock()
	s.sealed = map[uint64]common.Hash{parent.Number.Uint64(): common.HexToHash("0xdead")}
	handleOK(t, s, openOn(parent, h.config, commitment.Head{0x61}))
	if got := s.env.evm.Context.GetHash(parent.Number.Uint64()); got != parent.Hash() {
		t.Fatalf("canonical BLOCKHASH = %s, want %s", got, parent.Hash())
	}
	if s.sealed != nil {
		t.Fatalf("canonical re-anchor retained speculative hashes: %v", s.sealed)
	}
}

func TestReconcileAcceptsCanonicalExtensionOfPrefix(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0), h.transfer(t, 1)}
	block, receipts := buildPartialReuseBlock(t, h, txs)
	s := publishPrefix(t, h, txs[:1])
	logs := s.consumer.pendingStore().reconcileThrough(block.NumberU64(), func(uint64) *types.Block {
		return block
	}, func(common.Hash) types.Receipts {
		return receipts
	})
	if len(logs) != 0 {
		t.Fatalf("canonical prefix emitted removed logs: %v", logs)
	}
	if records := rawdb.ReadInvalidPreconfs(h.chain.DB(), 1); len(records) != 0 {
		t.Fatalf("canonical prefix recorded invalidation: %+v", records)
	}
}

func TestCommittedPrefixRemovesLogsAfterReceiptMismatch(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0), h.transfer(t, 1)}
	block, receipts := buildPartialReuseBlock(t, h, txs)
	s := publishPrefix(t, h, txs[:1])
	store := s.consumer.pendingStore()
	key := pendingKey{number: block.NumberU64(), parent: block.ParentHash()}
	store.mu.Lock()
	store.entries[key].RPCView.Logs = []*types.Log{{BlockNumber: block.NumberU64()}}
	store.mu.Unlock()

	mismatch := cloneReceipts(receipts)
	mismatch[0].Status ^= 1
	if _, invalid := store.CheckPreconfInvalidation(block, mismatch); !invalid {
		t.Fatal("receipt mismatch was accepted")
	}
	logs, invalidations, _, _ := store.completePreconf(block, mismatch, true)
	store.writeInvalidations(invalidations)
	if len(logs) != 1 || !logs[0].Removed {
		t.Fatalf("removed logs = %+v", logs)
	}
	if records := rawdb.ReadInvalidPreconfs(h.chain.DB(), 1); len(records) != 1 || records[0].Reason != "canonical_mismatch" {
		t.Fatalf("invalidation records = %+v", records)
	}
}

func TestSessionDoesNotResurrectCompletedPrefix(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0), h.transfer(t, 1)}
	block, _ := buildPartialReuseBlock(t, h, txs)
	s := publishPrefix(t, h, txs[:1])
	store := s.consumer.pendingStore()
	if _, ok := s.consumer.ClaimPreconfPrefix(block); !ok {
		t.Fatal("prefix snapshot failed")
	}
	if s.env != nil {
		t.Fatal("idle claimed environment was not cleared")
	}
	s.consumer.CompletePreconf(block, nil, true)

	raw, err := txs[1].MarshalBinary()
	if err != nil {
		t.Fatalf("marshal suffix transaction: %v", err)
	}
	s.applyRecord(recordEntry(raw, s.head).GetRecord())
	if s.env != nil {
		t.Fatal("rejected late publication left the worker active")
	}
	if _, _, ok := s.consumer.index.Lookup(txs[1].Hash()); ok {
		t.Fatal("late suffix receipt was added after canonical completion")
	}
	if pending, _, _ := store.Pending(); pending != nil {
		t.Fatalf("late suffix resurrected pending view: %v", pending)
	}
	s.applySeal(sealEntry(encodeHeader(t, block.Header()), s.head).GetBlockSeal())
	if s.env != nil || s.parked != nil || s.tip == block.Hash() {
		t.Fatal("late seal retained a losing speculative lineage")
	}
}

func TestAbortedPrefixClearsReceiptIndex(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0), h.transfer(t, 1)}
	block, _ := buildPartialReuseBlock(t, h, txs)
	s := publishPrefix(t, h, txs[:1])
	if _, ok := s.consumer.ClaimPreconfPrefix(block); !ok {
		t.Fatal("prefix claim failed")
	}
	s.consumer.CompletePreconf(block, nil, false)
	if _, _, ok := s.consumer.index.Lookup(txs[0].Hash()); ok {
		t.Fatal("aborted prefix receipt remained indexed")
	}
	if pending, _, _ := s.consumer.Pending(); pending != nil {
		t.Fatalf("aborted prefix remained pending: %v", pending)
	}
	if records := rawdb.ReadInvalidPreconfs(h.chain.DB(), 1); len(records) != 0 {
		t.Fatalf("aborted prefix invalidations = %+v", records)
	}
}

func TestRejectedPrefixWaitsForCanonicalComparison(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0), h.transfer(t, 1)}
	block, receipts := buildPartialReuseBlock(t, h, txs)
	s := publishPrefix(t, h, txs[:1])
	pendingBlock, pendingReceipts, pendingState := s.consumer.Pending()
	generation := s.env.generation
	if _, ok := s.consumer.ClaimPreconfPrefix(block); !ok {
		t.Fatal("prefix claim failed")
	}
	s.consumer.RejectClaimedPreconf(block)
	if _, _, ok := s.consumer.index.Lookup(txs[0].Hash()); !ok {
		t.Fatal("rejected prefix was removed before canonical comparison")
	}
	if pending, _, _ := s.consumer.Pending(); pending == nil {
		t.Fatal("rejected prefix view was removed before canonical comparison")
	}
	if s.consumer.pendingStore().publish(pendingBlock, pendingReceipts, pendingState, nil, generation) {
		t.Fatal("interrupted worker republished after prefix rejection")
	}
	if records := rawdb.ReadInvalidPreconfs(h.chain.DB(), 1); len(records) != 0 {
		t.Fatalf("prefix rejection wrote invalidation before canonical comparison: %+v", records)
	}
	if _, invalid := s.consumer.pendingStore().CheckPreconfInvalidation(block, receipts); invalid {
		t.Fatal("matching canonical prefix was marked invalid")
	}
	s.consumer.CompletePreconf(block, receipts, true)
	if _, _, ok := s.consumer.index.Lookup(txs[0].Hash()); ok {
		t.Fatal("committed prefix receipt remained indexed")
	}
	if pending, _, _ := s.consumer.Pending(); pending != nil {
		t.Fatalf("committed prefix remained pending: %v", pending)
	}
	if records := rawdb.ReadInvalidPreconfs(h.chain.DB(), 1); len(records) != 0 {
		t.Fatalf("matching canonical prefix invalidations = %+v", records)
	}
}

func TestPrefixClaimSerializesWorkerPublication(t *testing.T) {
	h := partialReuseHarness(t)
	txs := types.Transactions{h.transfer(t, 0), h.transfer(t, 1)}
	block, _ := buildPartialReuseBlock(t, h, txs)

	for attempt := 0; attempt < 20; attempt++ {
		s := publishPrefix(t, h, txs[:1])
		raw, err := txs[1].MarshalBinary()
		if err != nil {
			t.Fatalf("marshal suffix transaction: %v", err)
		}
		start := make(chan struct{})
		claimed := make(chan bool, 1)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, ok := s.consumer.ClaimPreconfPrefix(block)
			claimed <- ok
		}()
		go func() {
			defer wg.Done()
			<-start
			s.applyMu.Lock()
			s.applyRecord(recordEntry(raw, s.head).GetRecord())
			s.applyMu.Unlock()
		}()
		close(start)
		wg.Wait()
		if !<-claimed {
			t.Fatalf("attempt %d failed to claim a matching prefix", attempt)
		}
		if s.env != nil {
			t.Fatalf("attempt %d retained the claimed worker", attempt)
		}
		s.consumer.CompletePreconf(block, nil, true)
		if pending, _, _ := s.consumer.Pending(); pending != nil {
			t.Fatalf("attempt %d retained pending block %v", attempt, pending)
		}
		if _, _, ok := s.consumer.index.Lookup(txs[1].Hash()); ok {
			t.Fatalf("attempt %d retained suffix receipt", attempt)
		}
	}
}
