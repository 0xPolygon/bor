// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package legacypool

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

func receiveRebroadcastEvent(t *testing.T, ch <-chan core.StuckTxsEvent) core.StuckTxsEvent {
	t.Helper()
	select {
	case batch := <-ch:
		return batch
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a rebroadcast candidate")
		return core.StuckTxsEvent{}
	}
}

func TestRebroadcastSelectionDoesNotRecordSend(t *testing.T) {
	pool, _, from, tx := setupRebroadcastTest(t, 20*time.Millisecond, 60*time.Millisecond, 100)
	defer pool.Close()
	ch := make(chan core.StuckTxsEvent, 10)
	sub := pool.SubscribeRebroadcastTransactions(ch)
	defer sub.Unsubscribe()
	pool.mu.Lock()
	setTxAge(pool, from, 40*time.Millisecond)
	pool.mu.Unlock()
	batch := receiveRebroadcastEvent(t, ch)
	if len(batch.Txs) != 1 || batch.Txs[0].Hash() != tx.Hash() {
		t.Fatal("unexpected rebroadcast candidate")
	}
	pool.mu.Lock()
	_, tracked := pool.lastRebroadcast[tx.Hash()]
	setTxAge(pool, from, time.Second)
	pool.mu.Unlock()
	if tracked {
		t.Fatal("selecting a batch must not record a rebroadcast")
	}
	for i := 0; i < 3; i++ {
		batch = receiveRebroadcastEvent(t, ch)
		if len(batch.Txs) != 1 || batch.Txs[0].Hash() != tx.Hash() {
			t.Fatal("unsent transaction must remain eligible past max age")
		}
	}
	pool.RebroadcastAcknowledgement(batch.Txs)([]common.Hash{tx.Hash()})
	pool.mu.RLock()
	_, tracked = pool.lastRebroadcast[tx.Hash()]
	eligible := pool.identifyStuckTransactions()
	pool.mu.RUnlock()
	if !tracked || len(eligible) != 0 {
		t.Fatal("the recovered transaction must be accounted for after gossip")
	}
}

func TestRebroadcastAcknowledgementTracksOnlySentHashes(t *testing.T) {
	pool, key, from, first := setupRebroadcastTest(t, time.Hour, 2*time.Hour, 100)
	defer pool.Close()
	second := pricedTransaction(1, 100000, big.NewInt(params.BorDefaultTxPoolPriceLimit), key)
	if err := pool.addRemoteSync(second); err != nil {
		t.Fatal(err)
	}
	pool.mu.Lock()
	setTxAge(pool, from, 3*time.Hour)
	pool.mu.Unlock()
	acknowledge := pool.RebroadcastAcknowledgement([]*types.Transaction{first, second})
	acknowledge([]common.Hash{first.Hash(), {255}})
	pool.mu.RLock()
	eligible := pool.identifyStuckTransactions()
	tracked := len(pool.lastRebroadcast)
	pool.mu.RUnlock()
	if tracked != 1 || len(eligible) != 1 || eligible[0].Hash() != second.Hash() {
		t.Fatal("only the acknowledged batch member may lose eligibility")
	}
	acknowledge([]common.Hash{second.Hash()})
	pool.mu.RLock()
	tracked = len(pool.lastRebroadcast)
	pool.mu.RUnlock()
	if tracked != 2 {
		t.Fatal("a later acknowledgment must account for the rest of the batch")
	}
}

func TestRebroadcastAcknowledgementIsIdempotent(t *testing.T) {
	pool, _, _, tx := setupRebroadcastTest(t, time.Hour, 2*time.Hour, 100)
	defer pool.Close()
	acknowledge := pool.RebroadcastAcknowledgement([]*types.Transaction{tx})
	acknowledge([]common.Hash{tx.Hash()})
	pool.mu.RLock()
	first := pool.lastRebroadcast[tx.Hash()]
	pool.mu.RUnlock()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			acknowledge([]common.Hash{tx.Hash(), tx.Hash()})
		}()
	}
	wg.Wait()
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if len(pool.lastRebroadcast) != 1 || pool.lastRebroadcast[tx.Hash()] != first {
		t.Fatal("repeated peer acknowledgments must not advance the same batch twice")
	}
}

func TestRebroadcastAcknowledgementMetrics(t *testing.T) {
	pool, _, _, tx := setupRebroadcastTest(t, time.Hour, 2*time.Hour, 100)
	defer pool.Close()
	before := rebroadcastTxMeter.Snapshot().Count()
	rebroadcastTrackingGauge.Update(0)
	acknowledge := pool.RebroadcastAcknowledgement([]*types.Transaction{tx})
	if rebroadcastTxMeter.Snapshot().Count() != before {
		t.Fatal("identification must not count as rebroadcast")
	}
	acknowledge([]common.Hash{tx.Hash(), tx.Hash()})
	acknowledge([]common.Hash{tx.Hash()})
	if rebroadcastTxMeter.Snapshot().Count() != before+1 {
		t.Fatal("one batch member must be counted exactly once")
	}
	if rebroadcastTrackingGauge.Snapshot().Value() != 1 {
		t.Fatal("tracking gauge must reflect the acknowledged transaction")
	}
}

func TestRebroadcastAcknowledgementDoesNotRestoreRemovedTracking(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "replaced"}[replaced], func(t *testing.T) {
			pool, key, _, tx := setupRebroadcastTest(t, time.Hour, 2*time.Hour, 100)
			defer pool.Close()
			acknowledge := pool.RebroadcastAcknowledgement([]*types.Transaction{tx})
			if replaced {
				replacement := pricedTransaction(0, 100000, big.NewInt(2*params.BorDefaultTxPoolPriceLimit), key)
				if err := pool.addRemoteSync(replacement); err != nil {
					t.Fatal(err)
				}
			} else {
				pool.mu.Lock()
				pool.removeTx(tx.Hash(), true, true)
				pool.mu.Unlock()
			}
			acknowledge([]common.Hash{tx.Hash()})
			pool.mu.RLock()
			defer pool.mu.RUnlock()
			if len(pool.lastRebroadcast) != 0 {
				t.Fatal("a stale acknowledgment must not restore a removed transaction's tracking")
			}
		})
	}
}
