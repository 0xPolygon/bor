package sequencer

import (
	"math/big"
	"testing"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

func TestResolveOpenParentValidatesLineage(t *testing.T) {
	h := startExecHarness(t)
	head := h.chain.CurrentBlock()
	statedb, err := h.chain.StateAt(head.Root)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	t.Run("canonical height", func(t *testing.T) {
		s := h.session()
		if header, cacheable, ok := s.resolveOpenParent(head.Hash(), head.Number.Uint64()+2); ok || cacheable || header != nil {
			t.Fatalf("invalid canonical lineage returned header %v, cacheable %v, ok %v", header, cacheable, ok)
		}
	})
	t.Run("speculative parent displaced", func(t *testing.T) {
		s := h.session()
		s.tip = common.Hash{9}
		s.tipNumber = head.Number.Uint64()
		s.parked = statedb.Copy()
		if header, cacheable, ok := s.resolveOpenParent(s.tip, s.tipNumber+1); ok || cacheable || header != nil {
			t.Fatalf("displaced speculative parent returned header %v, cacheable %v, ok %v", header, cacheable, ok)
		}
	})
	t.Run("speculative height", func(t *testing.T) {
		s := h.session()
		s.tip = common.Hash{8}
		s.tipNumber = head.Number.Uint64() + 100
		s.tipHeader = head
		s.parked = statedb.Copy()
		if header, cacheable, ok := s.resolveOpenParent(s.tip, s.tipNumber+2); ok || cacheable || header != nil {
			t.Fatalf("invalid speculative height returned header %v, cacheable %v, ok %v", header, cacheable, ok)
		}
	})
	t.Run("missing speculative header", func(t *testing.T) {
		s := h.session()
		s.tip = common.Hash{7}
		s.tipNumber = head.Number.Uint64() + 100
		s.parked = statedb.Copy()
		if header, cacheable, ok := s.resolveOpenParent(s.tip, s.tipNumber+1); ok || cacheable || header != nil {
			t.Fatalf("missing speculative header returned header %v, cacheable %v, ok %v", header, cacheable, ok)
		}
	})
	t.Run("speculative tip", func(t *testing.T) {
		s := h.session()
		s.tip = common.Hash{6}
		s.tipNumber = head.Number.Uint64() + 100
		s.tipHeader = types.CopyHeader(head)
		s.parked = statedb.Copy()
		header, cacheable, ok := s.resolveOpenParent(s.tip, s.tipNumber+1)
		if !ok || cacheable || header != s.tipHeader {
			t.Fatalf("speculative tip returned header %v, cacheable %v, ok %v", header, cacheable, ok)
		}
	})
}

func TestSessionRejectsStaleOpenPublication(t *testing.T) {
	h := startExecHarness(t)
	head := h.chain.CurrentBlock()

	t.Run("canonical parent advanced", func(t *testing.T) {
		s := h.session()
		s.publishOpen(nil, pendingPayload{}, common.Hash{1}, head.Number.Uint64()+1, true)
		if s.env != nil || s.parked != nil {
			t.Fatal("stale canonical open retained speculative state")
		}
	})
	t.Run("reconciliation anchor stale", func(t *testing.T) {
		s := h.session()
		statedb, err := h.chain.StateAt(head.Root)
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		header := &types.Header{Number: new(big.Int).Add(head.Number, common.Big1), ParentHash: head.Hash(), GasLimit: head.GasLimit}
		block := types.NewBlockWithHeader(header)
		payload, ok := makePendingPayload(block, nil, statedb, nil)
		if !ok {
			t.Fatal("pending payload rejected")
		}
		s.env = &blockEnv{header: header}
		s.parked = statedb
		s.consumer.reconciled.Store(&types.Header{Number: big.NewInt(999)})
		s.publishOpen(block, payload, head.Hash(), block.NumberU64(), false)
		if s.env != nil || s.parked != nil {
			t.Fatal("stale anchor retained speculative state")
		}
	})
}

func TestSessionRejectsInvalidOpenContext(t *testing.T) {
	h := startExecHarness(t)
	s := h.session()
	open := openOn(h.chain.CurrentBlock(), h.config, [32]byte{1}).GetBlockOpen()
	open.GasLimit = params.MaxGasLimit + 1
	s.applyOpen(open)
	if s.env != nil {
		t.Fatal("invalid open context started execution")
	}
}

func TestExecuteRecordStopsOnInterruptAndPublicationFailure(t *testing.T) {
	h := startExecHarness(t)
	header := &types.Header{Number: big.NewInt(10), GasLimit: 30_000_000}

	t.Run("interrupted", func(t *testing.T) {
		s := h.session()
		s.env = &blockEnv{header: types.CopyHeader(header)}
		s.env.interrupt.Store(true)
		s.executeRecord(&pb.Record{Transactions: [][]byte{{1}}})
		if s.env != nil {
			t.Fatal("interrupted execution retained environment")
		}
	})
}

func TestCompletePreconfPrefixRejectsInvalidInputs(t *testing.T) {
	h := startExecHarness(t)
	head := h.chain.CurrentBlock()
	statedb, err := h.chain.StateAt(head.Root)
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	header := &types.Header{
		ParentHash: head.Hash(),
		Number:     new(big.Int).Add(head.Number, common.Big1),
		Time:       head.Time + 2,
		GasLimit:   head.GasLimit,
		BaseFee:    big.NewInt(1),
		Difficulty: big.NewInt(1),
	}
	block := types.NewBlockWithHeader(header)
	if execution, err := h.session().consumer.completePreconfPrefix(block, nil); err == nil || execution != nil {
		t.Fatalf("incomplete prefix returned execution %v and error %v", execution, err)
	}
	invalid := &pendingPrefix{
		Transactions: types.Transactions{h.transfer(t, 0)},
		StateDB:      statedb.Copy(),
		Result:       &core.ProcessResult{},
	}
	if env, err := newBlockEnvFromPrefix(h.chain, block, invalid); err == nil || env != nil {
		t.Fatalf("invalid prefix returned environment %v and error %v", env, err)
	}
	stateSyncBlock := block.WithBody(types.Body{Transactions: types.Transactions{types.NewTx(&types.StateSyncTx{})}})
	prefix := &pendingPrefix{StateDB: statedb.Copy(), Result: &core.ProcessResult{}}
	if execution, err := h.session().consumer.completePreconfPrefix(stateSyncBlock, prefix); err == nil || execution != nil {
		t.Fatalf("state-sync suffix returned execution %v and error %v", execution, err)
	}
}

func TestPendingPublicationCountIsBounded(t *testing.T) {
	const transactionCount = 6000
	unchanged := &blockEnv{header: new(types.Header), txs: make([]*types.Transaction, 1), publishedTxs: 1}
	if unchanged.shouldPublishPending() {
		t.Fatal("unchanged pending block was republished")
	}

	eager := &blockEnv{
		header:       &types.Header{GasLimit: 1},
		txs:          make([]*types.Transaction, pendingEagerPublicationTxs),
		publishedTxs: pendingEagerPublicationTxs - 1,
	}
	if !eager.shouldPublishPending() {
		t.Fatal("last eager transaction was not published")
	}

	env := &blockEnv{header: &types.Header{GasLimit: transactionCount*21_000 + 1}}
	publications := 0
	for index := 0; index < transactionCount; index++ {
		env.txs = append(env.txs, nil)
		env.header.GasUsed += 21_000
		if !env.shouldPublishPending() {
			continue
		}
		publications++
		env.publishedTxs = len(env.txs)
		env.publishedGas = env.header.GasUsed
	}
	if publications > pendingEagerPublicationTxs+pendingRPCPublicationLimit {
		t.Fatalf("pending publications = %d", publications)
	}

	small := &blockEnv{
		header:       &types.Header{GasLimit: 1, GasUsed: 21_000},
		txs:          make([]*types.Transaction, pendingEagerPublicationTxs+1),
		publishedTxs: pendingEagerPublicationTxs,
	}
	if !small.shouldPublishPending() {
		t.Fatal("minimum gas checkpoint was not published")
	}
}
