package txpool

import (
	"math/big"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
)

type speculativeTestSubPool struct {
	SubPool
	head  *types.Header
	state *state.StateDB
}

func (p *speculativeTestSubPool) SetSpeculativeState(head *types.Header, statedb *state.StateDB) {
	p.head = head
	p.state = statedb
}

type plainTestSubPool struct {
	SubPool
}

type rebroadcastTestSubPool struct {
	SubPool
	txs      []*types.Transaction
	callback func([]common.Hash)
}

func (p *rebroadcastTestSubPool) RebroadcastAcknowledgement(txs []*types.Transaction) func([]common.Hash) {
	p.txs = txs
	return p.callback
}

func TestRebroadcastAcknowledgementWithoutCallbacks(t *testing.T) {
	var absent *TxPool
	if callback := absent.RebroadcastAcknowledgement(nil); callback != nil {
		t.Fatal("nil pool returned an acknowledgement callback")
	}
	for _, tc := range []struct {
		name  string
		pools []SubPool
	}{
		{"empty", nil},
		{"unsupported", []SubPool{new(plainTestSubPool)}},
		{"nil callback", []SubPool{new(rebroadcastTestSubPool)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := &TxPool{subpools: tc.pools}
			callback := pool.RebroadcastAcknowledgement(nil)
			if callback == nil {
				t.Fatal("initialized pool did not return a no-op callback")
			}
			callback([]common.Hash{{1}})
		})
	}
}

func TestRebroadcastAcknowledgementForwardsToSubpools(t *testing.T) {
	txs := []*types.Transaction{
		types.NewTx(&types.LegacyTx{Nonce: 1}),
		types.NewTx(&types.LegacyTx{Nonce: 2}),
	}
	var received [2][][]common.Hash
	subpools := []*rebroadcastTestSubPool{{}, {}}
	for i, subpool := range subpools {
		subpool.callback = func(hashes []common.Hash) {
			received[i] = append(received[i], slices.Clone(hashes))
		}
	}
	pool := &TxPool{subpools: []SubPool{subpools[0], new(plainTestSubPool), new(rebroadcastTestSubPool), subpools[1]}}
	callback := pool.RebroadcastAcknowledgement(txs)
	for i, subpool := range subpools {
		if !slices.Equal(subpool.txs, txs) || len(received[i]) != 0 {
			t.Fatal("creating acknowledgement did not preserve the batch or acknowledged prematurely")
		}
	}
	if callback == nil {
		t.Fatal("missing acknowledgement callback")
	}
	for _, tx := range txs {
		callback([]common.Hash{tx.Hash()})
	}
	for i, batches := range received {
		if len(batches) != len(txs) {
			t.Fatalf("subpool %d received %d acknowledgements, want %d", i, len(batches), len(txs))
		}
		for j, hashes := range batches {
			if !slices.Equal(hashes, []common.Hash{txs[j].Hash()}) {
				t.Fatalf("subpool %d received wrong acknowledged hashes: %v", i, hashes)
			}
		}
	}
}

// TestSubscribeRebroadcastTransactionsNilPool tests that calling
// SubscribeRebroadcastTransactions on a nil TxPool returns a valid no-op
// subscription.
func TestSubscribeRebroadcastTransactionsNilPool(t *testing.T) {
	var pool *TxPool // nil pool

	ch := make(chan core.StuckTxsEvent, 1)
	sub := pool.SubscribeRebroadcastTransactions(ch)

	// Verify the subscription is valid even for nil pool
	if sub == nil {
		t.Fatal("expected non-nil subscription")
	}

	// Unsubscribe should work without issues
	sub.Unsubscribe()

	// Channel should be empty (no events should be sent)
	select {
	case event := <-ch:
		t.Fatalf("unexpected event: %v", event)
	default:
		// Expected - no events
	}
}

func TestSetSpeculativeState(t *testing.T) {
	setter := new(speculativeTestSubPool)
	plain := new(plainTestSubPool)
	pool := &TxPool{subpools: []SubPool{setter, plain}}
	header := &types.Header{Number: big.NewInt(42)}
	statedb := new(state.StateDB)

	pool.SetSpeculativeState(header, statedb)

	pool.stateLock.RLock()
	aggregatedState := pool.state
	pool.stateLock.RUnlock()
	if aggregatedState != statedb {
		t.Fatal("aggregator did not retain speculative state")
	}
	if setter.head != header || setter.state != statedb {
		t.Fatal("speculative state was not forwarded to supporting subpool")
	}
}
