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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestRebroadcastEligibilityBoundaries(t *testing.T) {
	pool := &LegacyPool{config: Config{RebroadcastInterval: time.Minute, RebroadcastMaxAge: time.Hour}}
	now := time.Now()
	for _, tc := range []struct {
		name                         string
		age, sinceAttempt            time.Duration
		maxAge                       time.Duration
		feeCap, tip, baseFee, minTip int64
		want                         bool
	}{
		{"young", time.Minute - time.Nanosecond, 0, time.Hour, 10, 2, 10, 2, false},
		{"minimum age", time.Minute, 0, time.Hour, 10, 2, 10, 2, true},
		{"recent attempt", time.Minute, time.Minute - time.Nanosecond, time.Hour, 10, 2, 10, 2, false},
		{"retry boundary", time.Minute, time.Minute, time.Hour, 10, 2, 10, 2, true},
		{"maximum age", time.Hour, time.Minute, time.Hour, 10, 2, 10, 2, true},
		{"expired", time.Hour + time.Nanosecond, time.Minute, time.Hour, 10, 2, 10, 2, false},
		{"expiry disabled", 2 * time.Hour, time.Minute, 0, 10, 2, 10, 2, true},
		{"below base fee", time.Minute, 0, time.Hour, 9, 2, 10, 2, false},
		{"below tip", time.Minute, 0, time.Hour, 10, 1, 10, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool.config.RebroadcastMaxAge = tc.maxAge
			tx := types.NewTx(&types.DynamicFeeTx{GasFeeCap: big.NewInt(tc.feeCap), GasTipCap: big.NewInt(tc.tip)})
			tx.SetTime(now.Add(-tc.age))
			var last time.Time
			if tc.sinceAttempt > 0 {
				last = now.Add(-tc.sinceAttempt)
			}
			pool.lastRebroadcast = map[common.Hash]time.Time{tx.Hash(): last}
			if got := pool.rebroadcastEligible(tx, last, now, big.NewInt(tc.baseFee), big.NewInt(tc.minTip)); got != tc.want {
				t.Fatalf("eligible=%t, want %t", got, tc.want)
			}
		})
	}
}

func TestRebroadcastSelectionWithoutCapacity(t *testing.T) {
	pool, _, from, _ := setupRebroadcastTest(t, time.Hour, 24*time.Hour, 200)
	defer pool.Close()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	setTxAge(pool, from, 2*time.Hour)
	pool.config.RebroadcastBatchSize = 0
	if len(pool.identifyStuckTransactions()) != 0 {
		t.Fatal("zero capacity returned candidates")
	}
	pool.config.RebroadcastBatchSize = 200
	pool.currentHead.Store(nil)
	if len(pool.identifyStuckTransactions()) != 0 {
		t.Fatal("missing head returned candidates")
	}
}

func TestRebroadcastSelectionFollowsBaseFee(t *testing.T) {
	pool, _, from, tx := setupRebroadcastTest(t, time.Hour, 24*time.Hour, 200)
	defer pool.Close()
	pool.mu.Lock()
	defer pool.mu.Unlock()
	setTxAge(pool, from, 2*time.Hour)
	for _, tc := range []struct {
		name    string
		baseFee *big.Int
		want    int
	}{
		{"above fee cap", new(big.Int).Mul(tx.GasFeeCap(), big.NewInt(2)), 0},
		{"fee recovered", new(big.Int).Div(tx.GasFeeCap(), big.NewInt(2)), 1},
		{"missing fee", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := types.CopyHeader(pool.currentHead.Load())
			head.BaseFee = tc.baseFee
			pool.currentHead.Store(head)
			if got := len(pool.identifyStuckTransactions()); got != tc.want {
				t.Fatalf("selected %d transactions, want %d", got, tc.want)
			}
			if pool.all.Get(tx.Hash()) == nil {
				t.Fatal("fee filtering removed transaction from pool")
			}
		})
	}
}

func TestRebroadcastCandidateOrdering(t *testing.T) {
	now := time.Now()
	makeCandidate := func(nonce uint64, admitted, attempted time.Time) rebroadcastCandidate {
		tx := types.NewTransaction(nonce, common.Address{}, new(big.Int), 21000, big.NewInt(1), nil)
		tx.SetTime(admitted)
		return rebroadcastCandidate{tx: tx, last: attempted}
	}
	for _, tc := range []struct {
		name         string
		older, newer rebroadcastCandidate
	}{
		{"attempt", makeCandidate(2, now, now.Add(-time.Minute)), makeCandidate(1, now, now)},
		{"admission", makeCandidate(2, now.Add(-time.Minute), now), makeCandidate(1, now, now)},
		{"nonce", makeCandidate(1, now, now), makeCandidate(2, now, now)},
		{"retry before later admission", makeCandidate(1, now.Add(-2*time.Minute), now.Add(-time.Minute)), makeCandidate(2, now, time.Time{})},
		{"first admission before recent retry", makeCandidate(2, now.Add(-2*time.Minute), time.Time{}), makeCandidate(1, now.Add(-3*time.Minute), now.Add(-time.Minute))},
		{"first attempts", makeCandidate(2, now.Add(-time.Minute), time.Time{}), makeCandidate(1, now, time.Time{})},
		{"equal priority", makeCandidate(2, now.Add(-time.Minute), now), makeCandidate(1, now, time.Time{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !tc.older.before(tc.newer) || tc.newer.before(tc.older) || tc.older.before(tc.older) {
				t.Fatal("candidate order is not strict")
			}
			batch := rebroadcastCandidates{}
			batch.consider(tc.newer, 1)
			batch.consider(tc.older, 1)
			if len(batch) != 1 || batch[0].tx != tc.older.tx {
				t.Fatal("bounded batch discarded preferred candidate")
			}
		})
	}
}

func TestRebroadcastSelectionKeepsOldestAttempts(t *testing.T) {
	now := time.Now()
	candidates := rebroadcastCandidates{}
	for i := 5; i >= 1; i-- {
		tx := types.NewTransaction(uint64(i), common.Address{}, new(big.Int), 21000, big.NewInt(1), nil)
		candidates.consider(rebroadcastCandidate{tx: tx, last: now.Add(time.Duration(i) * time.Minute)}, 2)
	}
	if len(candidates) != 2 {
		t.Fatal("selection exceeded batch limit")
	}
	seen := map[uint64]bool{}
	for _, candidate := range candidates {
		seen[candidate.tx.Nonce()] = true
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("retained nonces=%v; want 1 and 2", seen)
	}
}
