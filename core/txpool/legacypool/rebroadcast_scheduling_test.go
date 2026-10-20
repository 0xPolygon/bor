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
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

func TestRebroadcastAttemptWithoutQueueAcceptance(t *testing.T) {
	for _, age := range []time.Duration{2 * time.Hour, 48 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			pool, _, from, tx := setupRebroadcastTest(t, time.Hour, 24*time.Hour, 200)
			defer pool.Close()
			ch := make(chan core.StuckTxsEvent, 1)
			sub := pool.SubscribeRebroadcastTransactionsWithAcknowledgement(ch)
			defer sub.Unsubscribe()
			pool.mu.Lock()
			setTxAge(pool, from, age)
			batch := pool.identifyStuckTransactions()
			pool.mu.Unlock()
			if len(batch) != 1 {
				t.Fatal("first eligible attempt missing")
			}
			pool.publishRebroadcastTransactions(batch)
			<-ch
			pool.RebroadcastAcknowledgement(batch)(nil)
			pool.mu.Lock()
			defer pool.mu.Unlock()
			if len(pool.lastRebroadcast) != 0 || pool.lastRebroadcastAttempt[tx.Hash()].IsZero() {
				t.Fatal("attempt and queue acceptance must be recorded separately")
			}
			if len(pool.identifyStuckTransactions()) != 0 {
				t.Fatal("failed queue acceptance bypassed retry interval")
			}
			pool.lastRebroadcastAttempt[tx.Hash()] = time.Now().Add(-2 * time.Hour)
			want := 1
			if age > pool.config.RebroadcastMaxAge {
				want = 0
			}
			if got := len(pool.identifyStuckTransactions()); got != want {
				t.Fatalf("after cooldown: got %d candidates, want %d", got, want)
			}
			if pool.all.Get(tx.Hash()) == nil {
				t.Fatal("rebroadcast expiry removed transaction from pool")
			}
		})
	}
}

func TestRebroadcastBatchProgressWithoutQueueAcceptance(t *testing.T) {
	pool, key, from, _ := setupRebroadcastTest(t, time.Hour, 24*time.Hour, 200)
	defer pool.Close()
	testAddBalance(pool, from, new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil))
	for i := 1; i < 201; i++ {
		tx := pricedTransaction(uint64(i), 100000, big.NewInt(params.BorDefaultTxPoolPriceLimit), key)
		if err := pool.addRemoteSync(tx); err != nil {
			t.Fatal(err)
		}
	}
	pool.mu.Lock()
	setTxAge(pool, from, 2*time.Hour)
	first := pool.identifyStuckTransactions()
	pool.mu.Unlock()
	if len(first) != 200 {
		t.Fatalf("first batch: got %d", len(first))
	}
	pool.recordRebroadcastAttempts(first)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for hash := range pool.lastRebroadcastAttempt {
		pool.lastRebroadcastAttempt[hash] = time.Now().Add(-2 * time.Hour)
	}
	second := pool.identifyStuckTransactions()
	seen := make(map[common.Hash]bool)
	for _, batch := range [][]*types.Transaction{first, second} {
		for _, tx := range batch {
			seen[tx.Hash()] = true
		}
	}
	if len(second) != 200 || len(seen) != 201 {
		t.Fatalf("second batch=%d; covered=%d/201", len(second), len(seen))
	}
	if len(pool.lastRebroadcast) != 0 || pool.pending[from].Len() != 201 {
		t.Fatal("selection changed queue accounting or pool contents")
	}
}

func TestRebroadcastAttemptsCleanedUp(t *testing.T) {
	for _, mode := range []string{"removed", "replaced", "stranded"} {
		t.Run(mode, func(t *testing.T) {
			pool, key, from, tx := setupRebroadcastTest(t, time.Hour, 24*time.Hour, 200)
			defer pool.Close()
			pool.recordRebroadcastAttempts([]*types.Transaction{tx})
			if mode == "replaced" {
				replacement := pricedTransaction(0, 100000, big.NewInt(2*params.BorDefaultTxPoolPriceLimit), key)
				if err := pool.addRemoteSync(replacement); err != nil {
					t.Fatal(err)
				}
			} else {
				pool.mu.Lock()
				if mode == "stranded" {
					pool.dropPendingAccount(from, pool.pending[from])
				} else {
					pool.removeTx(tx.Hash(), true, false)
				}
				pool.mu.Unlock()
			}
			pool.recordRebroadcastAttempts([]*types.Transaction{tx})
			pool.mu.RLock()
			defer pool.mu.RUnlock()
			if len(pool.lastRebroadcastAttempt) != 0 {
				t.Fatal("removed transaction retained attempt history")
			}
		})
	}
}

func TestRebroadcastQueueRecovery(t *testing.T) {
	pool, _, from, tx := setupRebroadcastTest(t, time.Hour, 24*time.Hour, 200)
	defer pool.Close()
	pool.mu.Lock()
	setTxAge(pool, from, 2*time.Hour)
	pool.mu.Unlock()
	pool.recordRebroadcastAttempts([]*types.Transaction{tx})
	pool.mu.Lock()
	pool.lastRebroadcastAttempt[tx.Hash()] = time.Now().Add(-2 * time.Hour)
	batch := pool.identifyStuckTransactions()
	pool.mu.Unlock()
	if len(batch) != 1 {
		t.Fatal("retry missing after cooldown")
	}
	pool.RebroadcastAcknowledgement(batch)([]common.Hash{tx.Hash()})
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if pool.lastRebroadcast[tx.Hash()].IsZero() || len(pool.identifyStuckTransactions()) != 0 {
		t.Fatal("queue recovery did not update acceptance and suppress immediate retry")
	}
}

func BenchmarkRebroadcastSelection(b *testing.B) {
	for _, count := range []int{200, 16384} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			pool, _ := setupPoolWithConfig(params.TestChainConfig, func(pool *LegacyPool) {
				pool.config.Rebroadcast = false
			})
			defer pool.Close()
			pool.mu.Lock()
			pending := newList(true)
			for i := 0; i < count; i++ {
				tx := types.NewTransaction(uint64(i), common.Address{}, new(big.Int), 21000, big.NewInt(params.BorDefaultTxPoolPriceLimit), nil)
				tx.SetTime(time.Now().Add(-time.Minute))
				if added, _ := pending.Add(tx, 10); !added {
					b.Fatal("benchmark transaction was not added")
				}
				pool.all.Add(tx)
			}
			pool.pending[common.Address{}] = pending
			pool.mu.Unlock()
			pool.mu.RLock()
			defer pool.mu.RUnlock()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if len(pool.identifyStuckTransactions()) != 200 {
					b.Fatal("unexpected benchmark batch size")
				}
			}
		})
	}
}
