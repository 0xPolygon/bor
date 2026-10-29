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
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestRebroadcastRetriesOnNextTick(t *testing.T) {
	for _, mode := range []string{"refused", "accepted", "acknowledgement only", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			for _, delay := range []time.Duration{5 * time.Millisecond, 35*time.Second + 5*time.Millisecond} {
				t.Run(delay.String(), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) { testRebroadcastTick(t, mode, delay) })
				})
			}
		})
	}
}

func testRebroadcastTick(t *testing.T, mode string, delay time.Duration) {
	t.Helper()
	interval := 30 * time.Second
	pool, _, from, tx := setupRebroadcastTest(t, interval, 10*time.Minute, 100)
	defer pool.Close()
	ch := make(chan core.StuckTxsEvent)
	subscribe := pool.SubscribeRebroadcastTransactionsWithAcknowledgement
	if mode == "legacy" {
		subscribe = pool.SubscribeRebroadcastTransactions
	}
	sub := subscribe(ch)
	defer sub.Unsubscribe()
	pool.mu.Lock()
	setTxAge(pool, from, time.Minute)
	pool.mu.Unlock()
	for range 3 {
		// Model selection/feed/handler work, including a missed ticker delivery.
		time.Sleep(interval + delay)
		batch := <-ch
		require.Len(t, batch.Txs, 1)
		require.Same(t, tx, batch.Txs[0])
		if mode != "legacy" && mode != "acknowledgement only" {
			pool.RebroadcastAttempt(batch.Txs)([]common.Hash{tx.Hash()})
		}
		if mode == "accepted" || mode == "acknowledgement only" {
			pool.RebroadcastAcknowledgement(batch.Txs)([]common.Hash{tx.Hash()})
		}
		synctest.Wait()
		time.Sleep(interval - delay%interval - time.Nanosecond)
		synctest.Wait()
		select {
		case <-ch:
			t.Fatal("delayed reports must not allow an immediate retry of a missed tick")
		default:
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		select {
		case batch = <-ch:
			require.Len(t, batch.Txs, 1, "retry must happen on the next tick")
		default:
			t.Fatal("retry skipped the next scheduled tick after a delayed queue report")
		}
		synctest.Wait()
	}
}

func TestRebroadcastAccountingBeforeFirstTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pool, _, _, tx := setupRebroadcastTest(t, 30*time.Second, time.Minute, 100)
		defer pool.Close()
		txs := []*types.Transaction{tx}
		attempt, acknowledge := pool.RebroadcastAttempt(txs), pool.RebroadcastAcknowledgement(txs)
		time.Sleep(5 * time.Millisecond)
		now := time.Now()
		attempt([]common.Hash{tx.Hash()})
		acknowledge([]common.Hash{tx.Hash()})
		pool.mu.RLock()
		defer pool.mu.RUnlock()
		require.True(t, pool.rebroadcastTick.IsZero())
		require.Equal(t, now, pool.lastRebroadcastAttempt[tx.Hash()])
		require.Equal(t, now, pool.lastRebroadcast[tx.Hash()])
	})
}
