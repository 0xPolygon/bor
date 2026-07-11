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
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// TestReaderRetentionAcrossCommit exercises the BOR_READER_RETENTION path:
// the shared read cache of a committed block is served to the next block at
// the committed root, with every written key evicted and every retained key
// still reading its correct value.
func TestReaderRetentionAcrossCommit(t *testing.T) {
	prev := readerRetentionEnabled
	readerRetentionEnabled = true
	defer func() { readerRetentionEnabled = prev }()

	var (
		db = NewDatabaseForTesting()

		addrA = common.BytesToAddress([]byte{0xaa})
		addrB = common.BytesToAddress([]byte{0xbb})
		addrC = common.BytesToAddress([]byte{0xcc}) // contract with storage
		slot1 = common.BytesToHash([]byte{0x01})
		slot2 = common.BytesToHash([]byte{0x02})
	)

	// Block 0: set up the base state.
	base, err := New(types.EmptyRootHash, db)
	if err != nil {
		t.Fatal(err)
	}
	base.AddBalance(addrA, uint256.NewInt(100), tracing.BalanceChangeUnspecified)
	base.AddBalance(addrB, uint256.NewInt(200), tracing.BalanceChangeUnspecified)
	base.SetNonce(addrC, 1, tracing.NonceChangeUnspecified)
	base.SetCode(addrC, []byte{0xfe}, tracing.CodeChangeUnspecified)
	base.SetState(addrC, slot1, common.BytesToHash([]byte{0x11}))
	base.SetState(addrC, slot2, common.BytesToHash([]byte{0x22}))
	root1, err := base.Commit(0, true, true)
	if err != nil {
		t.Fatal(err)
	}

	// Block 1: read through a shared cached reader, mutate a subset, commit.
	_, proc, err := db.ReadersWithCacheStats(root1)
	if err != nil {
		t.Fatal(err)
	}
	st1, err := NewWithReader(root1, db, proc)
	if err != nil {
		t.Fatal(err)
	}
	// Warm the shared cache with all keys.
	if got := st1.GetBalance(addrA); got.Uint64() != 100 {
		t.Fatalf("balance A: got %v, want 100", got)
	}
	if got := st1.GetBalance(addrB); got.Uint64() != 200 {
		t.Fatalf("balance B: got %v, want 200", got)
	}
	if got := st1.GetState(addrC, slot1); got != common.BytesToHash([]byte{0x11}) {
		t.Fatalf("C.slot1: got %v", got)
	}
	if got := st1.GetState(addrC, slot2); got != common.BytesToHash([]byte{0x22}) {
		t.Fatalf("C.slot2: got %v", got)
	}
	// Mutate A's balance and C.slot1; leave B and C.slot2 untouched.
	st1.AddBalance(addrA, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	st1.SetState(addrC, slot1, common.BytesToHash([]byte{0x33}))
	servedBefore := retentionServeMeter.Snapshot().Count()
	root2, err := st1.Commit(1, true, true)
	if err != nil {
		t.Fatal(err)
	}

	// Block 2 at root2 must be served the retained cache.
	_, proc2, err := db.ReadersWithCacheStats(root2)
	if err != nil {
		t.Fatal(err)
	}
	if got := retentionServeMeter.Snapshot().Count(); got != servedBefore+1 {
		t.Fatalf("retained cache not served: serve meter %d, want %d", got, servedBefore+1)
	}
	st2, err := NewWithReader(root2, db, proc2)
	if err != nil {
		t.Fatal(err)
	}
	// Reference statedb reading root2 without any shared cache.
	ref, err := New(root2, db)
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name      string
		got, want interface{}
	}{
		{"balance A (invalidated)", st2.GetBalance(addrA).Uint64(), ref.GetBalance(addrA).Uint64()},
		{"balance B (retained)", st2.GetBalance(addrB).Uint64(), ref.GetBalance(addrB).Uint64()},
		{"C.slot1 (invalidated)", st2.GetState(addrC, slot1), ref.GetState(addrC, slot1)},
		{"C.slot2 (retained)", st2.GetState(addrC, slot2), ref.GetState(addrC, slot2)},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
	if got := st2.GetBalance(addrA).Uint64(); got != 101 {
		t.Errorf("balance A: got %d, want 101", got)
	}
	if got := st2.GetState(addrC, slot1); got != common.BytesToHash([]byte{0x33}) {
		t.Errorf("C.slot1: got %v, want 0x33", got)
	}

	// A request at a non-matching root (reorg shape) must NOT be served the
	// retained cache.
	servedBefore = retentionServeMeter.Snapshot().Count()
	if _, _, err := db.ReadersWithCacheStats(root1); err != nil {
		t.Fatal(err)
	}
	if got := retentionServeMeter.Snapshot().Count(); got != servedBefore {
		t.Fatalf("retained cache wrongly served at stale root")
	}
}
