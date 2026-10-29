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
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

func TestRebroadcastRemovalDuringPromotion(t *testing.T) {
	pool, key, from, tx := setupRebroadcastTest(t, time.Hour, 24*time.Hour, 200)
	defer pool.Close()
	pool.recordRebroadcastAttempts([]*types.Transaction{tx})
	replacement := pricedTransaction(0, 100000, big.NewInt(2*params.BorDefaultTxPoolPriceLimit), key)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.all.Add(replacement)
	pool.priced.Put(replacement, pool.priced.reheaps.Load())
	if !pool.promoteTx(from, replacement.Hash(), replacement) {
		t.Fatal("promotion failed")
	}
	if len(pool.lastRebroadcastAttempt) != 0 {
		t.Fatal("promoted replacement left old attempt history")
	}
}

func TestRebroadcastRemovalDuringDemotion(t *testing.T) {
	for _, mode := range []string{"balance", "conditional"} {
		t.Run(mode, func(t *testing.T) {
			pool, _, from, tx := setupRebroadcastTest(t, time.Hour, 24*time.Hour, 200)
			defer pool.Close()
			pool.recordRebroadcastAttempts([]*types.Transaction{tx})
			pool.mu.Lock()
			defer pool.mu.Unlock()
			if mode == "balance" {
				pool.currentState.SetBalance(from, new(uint256.Int), tracing.BalanceChangeUnspecified)
			} else {
				tx.PutOptions(&types.OptionsPIP15{BlockNumberMax: big.NewInt(-1)})
			}
			pool.demoteUnexecutables()
			if pool.all.Get(tx.Hash()) != nil || len(pool.lastRebroadcastAttempt) != 0 {
				t.Fatal("demotion left removed transaction or attempt history")
			}
		})
	}
}

func TestRebroadcastRemovalDuringQueueTruncation(t *testing.T) {
	pool, key, _, _ := setupRebroadcastTest(t, time.Hour, 24*time.Hour, 200)
	defer pool.Close()
	queued := pricedTransaction(5, 100000, big.NewInt(params.BorDefaultTxPoolPriceLimit), key)
	if err := pool.addRemoteSync(queued); err != nil {
		t.Fatal(err)
	}
	pool.recordRebroadcastAttempts([]*types.Transaction{queued})
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.queue.config.GlobalQueue = 0
	pool.truncateQueue()
	if pool.all.Get(queued.Hash()) != nil || len(pool.lastRebroadcastAttempt) != 0 {
		t.Fatal("queue truncation left removed transaction or attempt history")
	}
}

func TestRebroadcastRemovalDuringPendingTruncation(t *testing.T) {
	pool, _ := setupPool()
	defer pool.Close()
	var txs []*types.Transaction
	for _, count := range []int{8, 4} {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		addr := crypto.PubkeyToAddress(key.PublicKey)
		testAddBalance(pool, addr, new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil))
		for nonce := 0; nonce < count; nonce++ {
			tx := pricedTransaction(uint64(nonce), 100000, big.NewInt(params.BorDefaultTxPoolPriceLimit), key)
			if err := pool.addRemoteSync(tx); err != nil {
				t.Fatal(err)
			}
			txs = append(txs, tx)
		}
	}
	pool.recordRebroadcastAttempts(txs)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.config.AccountSlots, pool.config.GlobalSlots = 1, 4
	pool.truncatePending()
	if pool.all.Count() != 4 || len(pool.lastRebroadcastAttempt) != 4 {
		t.Fatalf("remaining transactions=%d; attempt records=%d", pool.all.Count(), len(pool.lastRebroadcastAttempt))
	}
	for hash := range pool.lastRebroadcastAttempt {
		if pool.all.Get(hash) == nil {
			t.Fatal("pending truncation left removed transaction's history")
		}
	}
}
