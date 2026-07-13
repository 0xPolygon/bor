// Copyright 2024 The go-ethereum Authors
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
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// clockStubReader satisfies Reader with synthetic values so cache misses
// always resolve, letting the tests drive readerWithCache directly.
type clockStubReader struct{}

func (clockStubReader) Code(common.Address, common.Hash) ([]byte, error) { return nil, nil }
func (clockStubReader) CodeSize(common.Address, common.Hash) (int, error) {
	return 0, nil
}
func (clockStubReader) Account(common.Address) (*types.StateAccount, error) {
	return &types.StateAccount{Nonce: 1}, nil
}
func (clockStubReader) Storage(common.Address, common.Hash) (common.Hash, error) {
	return common.Hash{0xaa}, nil
}

// clockTestAddr returns an address landing in bucket 0 so per-bucket caps are
// exercised deterministically.
func clockTestAddr(i int) common.Address {
	var addr common.Address
	addr[0] = 0x00 // bucket 0
	binary.BigEndian.PutUint32(addr[16:], uint32(i))
	return addr
}

func clockTestSlot(i int) common.Hash {
	var slot common.Hash
	binary.BigEndian.PutUint32(slot[28:], uint32(i))
	return slot
}

func withClockCaps(t *testing.T, acct, slots int) {
	t.Helper()
	prevA, prevS := clockAcctBucketCap, clockSlotBucketCap
	clockAcctBucketCap, clockSlotBucketCap = acct, slots
	t.Cleanup(func() { clockAcctBucketCap, clockSlotBucketCap = prevA, prevS })
}

// TestClockEviction_AccountsCapAndSecondChance verifies the account cache
// stays at cap and that hit (ref'd) entries survive the sweep over cold ones.
func TestClockEviction_AccountsCapAndSecondChance(t *testing.T) {
	withClockCaps(t, 8, 0)
	r := newReaderWithCache(newReader(clockStubReader{}, clockStubReader{}))

	// Fill to cap: 8 inserts, no eviction.
	for i := 0; i < 8; i++ {
		if _, _, _, inserted, err := r.account(clockTestAddr(i), roleProcess); err != nil || !inserted {
			t.Fatalf("insert %d: inserted=%v err=%v", i, inserted, err)
		}
	}
	bucket := &r.accountBuckets[0]
	if got := len(bucket.accounts); got != 8 {
		t.Fatalf("expected 8 cached accounts, got %d", got)
	}

	// Hit addresses 0-3 so their ref bits protect them.
	for i := 0; i < 4; i++ {
		if _, incache, _, _, err := r.account(clockTestAddr(i), roleProcess); err != nil || !incache {
			t.Fatalf("hit %d: incache=%v err=%v", i, incache, err)
		}
	}

	// Insert 4 more: evictions must target the un-hit 4-7 range, sparing 0-3.
	for i := 8; i < 12; i++ {
		if _, _, _, _, err := r.account(clockTestAddr(i), roleProcess); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if got := len(bucket.accounts); got != 8 {
		t.Fatalf("expected cap of 8 after overflow, got %d", got)
	}
	for i := 0; i < 4; i++ {
		if _, ok := bucket.accounts[clockTestAddr(i)]; !ok {
			t.Errorf("hit entry %d was evicted; ref bit did not protect it", i)
		}
	}
	for i := 4; i < 8; i++ {
		if _, ok := bucket.accounts[clockTestAddr(i)]; ok {
			t.Errorf("cold entry %d survived while hit entries should be preferred", i)
		}
	}
	if got := r.accountCount.Load(); got != 8 {
		t.Errorf("accountCount=%d, want 8", got)
	}
}

// TestClockEviction_StorageCapAndStaleRing verifies the storage cache stays at
// cap, per-bucket slot counting survives invalidation, and stale ring slots
// (keys removed by dropStorage) are skipped without breaking the sweep.
func TestClockEviction_StorageCapAndStaleRing(t *testing.T) {
	withClockCaps(t, 0, 8)
	r := newReaderWithCache(newReader(clockStubReader{}, clockStubReader{}))

	addr := clockTestAddr(1)
	for i := 0; i < 8; i++ {
		if _, _, _, inserted, err := r.storage(addr, clockTestSlot(i), roleProcess); err != nil || !inserted {
			t.Fatalf("insert %d: inserted=%v err=%v", i, inserted, err)
		}
	}
	bucket := &r.storageBuckets[0]
	if bucket.count != 8 {
		t.Fatalf("bucket.count=%d, want 8", bucket.count)
	}

	// Invalidate the whole account: ring now holds 8 stale keys.
	if dropped := r.dropStorage(addr); dropped != 8 {
		t.Fatalf("dropStorage=%d, want 8", dropped)
	}
	if bucket.count != 0 {
		t.Fatalf("bucket.count=%d after drop, want 0", bucket.count)
	}

	// Refill past cap with a different account; the sweep must skip the stale
	// ring prefix and still enforce the cap.
	addr2 := clockTestAddr(2)
	for i := 0; i < 12; i++ {
		if _, _, _, _, err := r.storage(addr2, clockTestSlot(i), roleProcess); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if bucket.count != 8 {
		t.Fatalf("bucket.count=%d after refill, want cap 8", bucket.count)
	}
	if got := r.storageCount.Load(); got != 8 {
		t.Errorf("storageCount=%d, want 8", got)
	}
	// The most recent inserts must be present (FIFO evicts oldest first).
	for i := 8; i < 12; i++ {
		if slots := bucket.storages[addr2]; slots == nil || slots[clockTestSlot(i)] == nil {
			t.Errorf("recent slot %d missing after sweep", i)
		}
	}
}

// TestClockEviction_DisabledKeepsUnboundedBehavior guards the default path:
// with the knob off nothing is evicted and no ring memory accrues.
func TestClockEviction_DisabledKeepsUnboundedBehavior(t *testing.T) {
	withClockCaps(t, 0, 0)
	r := newReaderWithCache(newReader(clockStubReader{}, clockStubReader{}))

	for i := 0; i < 64; i++ {
		if _, _, _, _, err := r.account(clockTestAddr(i), roleProcess); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		if _, _, _, _, err := r.storage(clockTestAddr(1), clockTestSlot(i), roleProcess); err != nil {
			t.Fatalf("storage insert %d: %v", i, err)
		}
	}
	if got := len(r.accountBuckets[0].accounts); got != 64 {
		t.Errorf("accounts=%d, want 64 (no eviction when disabled)", got)
	}
	if r.storageBuckets[0].count != 64 {
		t.Errorf("storage count=%d, want 64", r.storageBuckets[0].count)
	}
	if len(r.accountBuckets[0].ring) != 0 || len(r.storageBuckets[0].ring) != 0 {
		t.Error("rings must stay empty when the CLOCK knob is off")
	}
}
