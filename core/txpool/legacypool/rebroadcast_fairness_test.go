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
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

func TestRebroadcastRetriesAcrossNewBatches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		partial bool
	}{{"no acceptance", false}, {"partial acceptance", true}} {
		t.Run(tc.name, func(t *testing.T) {
			testRebroadcastRetriesAcrossNewBatches(t, tc.partial)
		})
	}
}

func testRebroadcastRetriesAcrossNewBatches(t *testing.T, partial bool) {
	const interval = time.Hour
	pool, key, from, head := setupRebroadcastTest(t, interval, 10*interval, 2)
	defer pool.Close()
	testAddBalance(pool, from, new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil))
	ch := make(chan core.StuckTxsEvent, 1)
	sub := pool.SubscribeRebroadcastTransactionsWithAcknowledgement(ch)
	defer sub.Unsubscribe()
	addRebroadcastArrivals(t, pool, key, 1, 1, interval)
	pool.mu.Lock()
	setTxAge(pool, from, interval+interval/2)
	initial := pool.identifyStuckTransactions()
	pool.mu.Unlock()
	if len(initial) != 2 || initial[0] != head {
		t.Fatal("initial batch did not include the lowest nonce first")
	}
	publishRebroadcastFairnessBatch(t, pool, ch, initial, partial)
	retries := 0
	for round := 1; round <= 5; round++ {
		advanceRebroadcastTime(pool, from, interval+interval/2)
		addRebroadcastArrivals(t, pool, key, uint64(2*round), 2, interval+interval/10)
		batch := selectRebroadcastRetryBatch(t, pool, head)
		for _, tx := range batch {
			if tx == head {
				retries++
			}
		}
		publishRebroadcastFairnessBatch(t, pool, ch, batch, partial)
	}
	if retries == 0 {
		t.Error("new arrivals prevented every retry of the eligible lowest nonce")
	}
	advanceRebroadcastTime(pool, from, interval+interval/2)
	assertRebroadcastRetryExpiry(t, pool, from, head)
}

func addRebroadcastArrivals(t *testing.T, pool *LegacyPool, key *ecdsa.PrivateKey, first uint64, count int, age time.Duration) {
	t.Helper()
	for i := 0; i < count; i++ {
		tx := pricedTransaction(first+uint64(i), 100000, big.NewInt(params.BorDefaultTxPoolPriceLimit), key)
		if err := pool.addRemoteSync(tx); err != nil {
			t.Fatal(err)
		}
		pool.mu.Lock()
		tx.SetTime(time.Now().Add(-age))
		pool.mu.Unlock()
	}
}

func advanceRebroadcastTime(pool *LegacyPool, from common.Address, elapsed time.Duration) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	for _, tx := range pool.pending[from].Flatten() {
		tx.SetTime(tx.Time().Add(-elapsed))
	}
	for hash, attempted := range pool.lastRebroadcastAttempt {
		pool.lastRebroadcastAttempt[hash] = attempted.Add(-elapsed)
	}
	for hash, accepted := range pool.lastRebroadcast {
		pool.lastRebroadcast[hash] = accepted.Add(-elapsed)
	}
}

func selectRebroadcastRetryBatch(t *testing.T, pool *LegacyPool, head *types.Transaction) []*types.Transaction {
	t.Helper()
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	last := pool.rebroadcastLastActivity(head.Hash())
	if !pool.rebroadcastEligible(head, last, time.Now(), pool.strandedBaseFee(), pool.gasTip.Load().ToBig()) {
		t.Fatal("lowest nonce became ineligible before its retry window ended")
	}
	batch := pool.identifyStuckTransactions()
	if len(batch) != pool.config.RebroadcastBatchSize {
		t.Fatalf("selected %d transactions, want a full batch", len(batch))
	}
	return batch
}

func publishRebroadcastFairnessBatch(t *testing.T, pool *LegacyPool, ch <-chan core.StuckTxsEvent, batch []*types.Transaction, partial bool) {
	t.Helper()
	pool.publishRebroadcastTransactions(batch)
	delivered := receiveRebroadcastEvent(t, ch)
	offered := make([]common.Hash, len(delivered.Txs))
	for i, tx := range delivered.Txs {
		offered[i] = tx.Hash()
	}
	pool.RebroadcastAttempt(delivered.Txs)(offered)
	var accepted []common.Hash
	if partial {
		for _, tx := range delivered.Txs {
			if tx.Nonce() != 0 {
				accepted = append(accepted, tx.Hash())
				break
			}
		}
	}
	pool.RebroadcastAcknowledgement(delivered.Txs)(accepted)
}

func assertRebroadcastRetryExpiry(t *testing.T, pool *LegacyPool, from common.Address, head *types.Transaction) {
	t.Helper()
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if pool.rebroadcastEligible(head, pool.rebroadcastLastActivity(head.Hash()), time.Now(), pool.strandedBaseFee(), pool.gasTip.Load().ToBig()) {
		t.Error("retried transaction remained eligible beyond maximum age")
	}
	if pool.all.Get(head.Hash()) != head || !pool.pending[from].Contains(head.Nonce()) {
		t.Error("rebroadcast expiry removed the transaction from pending")
	}
	if !pool.lastRebroadcast[head.Hash()].IsZero() {
		t.Error("unaccepted lowest nonce was recorded as queue-accepted")
	}
	for _, tx := range pool.identifyStuckTransactions() {
		if tx == head {
			t.Error("expired transaction was selected again")
		}
	}
}
