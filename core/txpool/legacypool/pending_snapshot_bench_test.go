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

// Investigation benchmarks: cost of a miner-style txpool snapshot via Pending(),
// across pool shapes. Pending() holds pool.mu (write lock) for its entire body,
// so the measured wall-clock per call is also the writer-blocking time an Add()
// would observe while a snapshot is in progress. Used to evaluate the
// feasibility of taking additional snapshots mid-block (every 50-250ms inside a
// ~1.5s block-building window).

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// buildSnapshotPool constructs a pool with `accounts` senders, each holding
// `txsPerAccount` consecutive-nonce pending transactions, inserted directly via
// promoteTx (bypassing validation) so pool-size caps don't truncate the shape.
func buildSnapshotPool(b *testing.B, accounts, txsPerAccount int) *LegacyPool {
	b.Helper()

	pool, _ := setupPool()
	b.Cleanup(func() { pool.Close() })

	for i := 0; i < accounts; i++ {
		key, err := ecdsa.GenerateKey(crypto.S256(), deterministicReader(i))
		if err != nil {
			b.Fatalf("keygen: %v", err)
		}
		addr := crypto.PubkeyToAddress(key.PublicKey)
		testAddBalance(pool, addr, big.NewInt(1_000_000_000_000_000))
		for n := 0; n < txsPerAccount; n++ {
			tx := pricedTransaction(uint64(n), 100_000, big.NewInt(2), key)
			pool.promoteTx(addr, tx.Hash(), tx)
		}
	}
	return pool
}

// deterministicReader yields a deterministic byte stream per index so the
// benchmark pool shape is reproducible across runs.
func deterministicReader(seed int) *detReader { return &detReader{state: uint64(seed)*2654435761 + 1} }

type detReader struct{ state uint64 }

func (r *detReader) Read(p []byte) (int, error) {
	for i := range p {
		r.state = r.state*6364136223846793005 + 1442695040888963407
		p[i] = byte(r.state >> 33)
	}
	return len(p), nil
}

func minerFilter() txpool.PendingFilter {
	return txpool.PendingFilter{
		MinTip:  uint256.NewInt(1),
		BaseFee: uint256.NewInt(1),
	}
}

func BenchmarkPendingSnapshot(b *testing.B) {
	shapes := []struct{ accounts, txsPerAccount int }{
		{100, 1},
		{100, 16},
		{1_000, 1},
		{1_000, 4},
		{1_000, 16},
		{5_000, 1},
		{5_000, 4},
		{10_000, 1},
		{10_000, 4},
	}
	for _, s := range shapes {
		name := fmt.Sprintf("accounts=%d/txsPerAcct=%d/total=%d", s.accounts, s.txsPerAccount, s.accounts*s.txsPerAccount)
		b.Run(name, func(b *testing.B) {
			pool := buildSnapshotPool(b, s.accounts, s.txsPerAccount)
			filter := minerFilter()

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				p := pool.Pending(filter, nil)
				if len(p) != s.accounts {
					b.Fatalf("snapshot returned %d accounts, want %d", len(p), s.accounts)
				}
			}
		})
	}
}

// BenchmarkPendingSnapshotRepeat measures the steady-state cost of the exact
// mid-block pattern under evaluation: a snapshot every iteration against an
// unchanged pool, where the per-account flatten cache is warm (the dominant
// real-world case: most accounts don't change between two snapshots 100ms
// apart). Contrast with BenchmarkPendingSnapshotColdCache below.
func BenchmarkPendingSnapshotRepeat(b *testing.B) {
	pool := buildSnapshotPool(b, 5_000, 4)
	filter := minerFilter()
	// Warm the per-account flatten caches.
	pool.Pending(filter, nil)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		pool.Pending(filter, nil)
	}
}

// BenchmarkPendingSnapshotColdCache measures the worst case where every
// account's sorted-tx cache was invalidated between snapshots (every account
// received a new tx within the interval).
func BenchmarkPendingSnapshotColdCache(b *testing.B) {
	pool := buildSnapshotPool(b, 5_000, 4)
	filter := minerFilter()

	// Collect per-account lists so we can invalidate caches each iteration.
	lists := make([]*list, 0, len(pool.pending))
	pool.mu.RLock()
	for _, l := range pool.pending {
		lists = append(lists, l)
	}
	pool.mu.RUnlock()

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		for _, l := range lists {
			l.txs.cacheMu.Lock()
			l.txs.cache = nil
			l.txs.cacheMu.Unlock()
		}
		b.StartTimer()
		pool.Pending(filter, nil)
	}
}

// BenchmarkAddWhileSnapshotLoop measures writer latency while a tight loop of
// snapshots holds/releases pool.mu — an upper bound on the Add() stalls that
// mid-block re-snapshotting could introduce.
func BenchmarkAddWhileSnapshotLoop(b *testing.B) {
	for _, contended := range []bool{false, true} {
		name := "baseline"
		if contended {
			name = "contended"
		}
		b.Run(name, func(b *testing.B) {
			pool := buildSnapshotPool(b, 5_000, 4)
			filter := minerFilter()

			key, _ := crypto.GenerateKey()
			addr := crypto.PubkeyToAddress(key.PublicKey)
			testAddBalance(pool, addr, big.NewInt(1_000_000_000_000_000))

			// Pre-sign transactions outside the timed region.
			signed := make([]*types.Transaction, b.N)
			for i := 0; i < b.N; i++ {
				signed[i] = pricedTransaction(uint64(i), 100_000, big.NewInt(2), key)
			}

			stop := make(chan struct{})
			done := make(chan struct{})
			if contended {
				go func() {
					defer close(done)
					for {
						select {
						case <-stop:
							return
						default:
							pool.Pending(filter, nil)
						}
					}
				}()
			} else {
				close(done)
			}

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := pool.addRemoteSync(signed[i]); err != nil {
					b.Fatalf("add: %v", err)
				}
			}
			b.StopTimer()
			close(stop)
			<-done
		})
	}
}
