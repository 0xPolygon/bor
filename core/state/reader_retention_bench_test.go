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

package state

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

func benchAddr(i int) common.Address {
	var a common.Address
	binary.BigEndian.PutUint64(a[12:], uint64(i))
	return a
}

// BenchmarkReaderCacheAccountHit shows the lock-free hit path is unaffected by
// retention: the flag branch is taken only after the sync.Map hit check, so a
// warm read costs the same whether retention is on or off. This rebuts the
// concern that retention inflates the (dominant) hit path.
func BenchmarkReaderCacheAccountHit(b *testing.B) {
	for _, on := range []bool{false, true} {
		b.Run(fmt.Sprintf("retention=%v", on), func(b *testing.B) {
			prev := readerRetentionEnabled
			readerRetentionEnabled = on
			defer func() { readerRetentionEnabled = prev }()

			rc := newReaderWithCache(emptyBackingReader{})
			rc.accounts.Store(addrB, &accountCacheEntry{acct: &types.StateAccount{Balance: uint256.NewInt(200)}})
			rs := newReaderWithCacheStats(rc, roleProcess)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := rs.Account(addrB); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkReaderCacheAccountMissInsert isolates the miss-insert cost. Off uses
// the original develop path (sync.Map LoadOrStore); on adds the generation guard
// (advanceMu RLock + gen load + count). The backing read is a constant no-op, so
// the delta is purely the retention insert overhead — this is the tax paid on
// the ~1.75% of reads that miss.
func BenchmarkReaderCacheAccountMissInsert(b *testing.B) {
	for _, on := range []bool{false, true} {
		b.Run(fmt.Sprintf("retention=%v", on), func(b *testing.B) {
			prev := readerRetentionEnabled
			readerRetentionEnabled = on
			defer func() { readerRetentionEnabled = prev }()

			rc := newReaderWithCache(emptyBackingReader{})
			rs := newReaderWithCacheStats(rc, roleProcess)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Distinct addresses so every call is a first-time miss+insert.
				if _, err := rs.Account(benchAddr(i)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkAdvanceEvict measures the per-commit advance cost as a function of the
// write-set size on the live (raw-key, no-deletion) path: O(write-set) sync.Map
// deletes, no full Range. Cache pre-populated well above the write set.
func BenchmarkAdvanceEvict(b *testing.B) {
	prev := readerRetentionEnabled
	readerRetentionEnabled = true
	defer func() { readerRetentionEnabled = prev }()

	for _, writeSet := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("writeset=%d", writeSet), func(b *testing.B) {
			// Build a stateUpdate whose accountsOrigin is the write set.
			upd := &stateUpdate{
				root:           common.Hash{},
				rawStorageKey:  true,
				accountsOrigin: make(map[common.Address][]byte, writeSet),
			}
			for i := 0; i < writeSet; i++ {
				upd.accountsOrigin[benchAddr(i)] = []byte{0x01}
			}
			backing := emptyBackingReader{}
			b.ReportAllocs()
			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				b.StopTimer()
				rc := newReaderWithCache(backing)
				// Populate the write-set keys so the deletes actually do work.
				for i := 0; i < writeSet; i++ {
					rc.accounts.Store(benchAddr(i), &accountCacheEntry{})
					rc.accountCount.Add(1)
				}
				b.StartTimer()
				rc.advance(backing, upd)
			}
		})
	}
}

// emptyBackingReader is a constant no-op Reader: accounts/slots resolve to
// nil/empty with zero I/O, so benchmarks isolate cache overhead from backing
// latency.
type emptyBackingReader struct{}

func (emptyBackingReader) Account(common.Address) (*types.StateAccount, error) { return nil, nil }
func (emptyBackingReader) Storage(common.Address, common.Hash) (common.Hash, error) {
	return common.Hash{}, nil
}
func (emptyBackingReader) Code(common.Address, common.Hash) ([]byte, error)  { return nil, nil }
func (emptyBackingReader) CodeSize(common.Address, common.Hash) (int, error) { return 0, nil }
