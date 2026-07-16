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
	"os"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// withRetention enables the retention flag for the duration of a test.
func withRetention(t *testing.T) {
	t.Helper()
	prev := readerRetentionEnabled
	readerRetentionEnabled = true
	t.Cleanup(func() { readerRetentionEnabled = prev })
}

var (
	addrA = common.BytesToAddress([]byte{0xaa})
	addrB = common.BytesToAddress([]byte{0xbb})
	addrC = common.BytesToAddress([]byte{0xcc}) // contract with storage
	slot1 = common.BytesToHash([]byte{0x01})
	slot2 = common.BytesToHash([]byte{0x02})
)

// buildBaseState commits an initial state (A=100, B=200, C is a contract with
// slot1=0x11, slot2=0x22) and returns the resulting root.
func buildBaseState(t *testing.T, db *CachingDB) common.Hash {
	t.Helper()
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
	root, err := base.Commit(0, true, true)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestReaderRetentionAcrossCommit is the headline correctness test: the shared
// read cache of a committed block is served to the next block at the committed
// root, every written key is evicted (re-resolved correctly from the new
// backing), every untouched key is retained and served, and every served value
// is byte-equivalent to a fresh reader at the new root.
func TestReaderRetentionAcrossCommit(t *testing.T) {
	withRetention(t)
	db := NewDatabaseForTesting()
	root1 := buildBaseState(t, db)

	// Block 1: read through a shared cached reader, mutate a subset, commit.
	_, proc, err := db.ReadersWithCacheStats(root1, true)
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
		t.Fatalf("C.slot1: got %v, want 0x11", got)
	}
	if got := st1.GetState(addrC, slot2); got != common.BytesToHash([]byte{0x22}) {
		t.Fatalf("C.slot2: got %v, want 0x22", got)
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
	_, proc2, err := db.ReadersWithCacheStats(root2, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := retentionServeMeter.Snapshot().Count(); got != servedBefore+1 {
		t.Fatalf("retained cache not served: serve meter %d, want %d", got, servedBefore+1)
	}

	// Fresh reader at root2 to compare byte-equivalence.
	fresh, err := db.Reader(root2)
	if err != nil {
		t.Fatal(err)
	}

	// Written keys re-resolve to their new values; untouched keys keep theirs.
	// All must equal a fresh reader at root2.
	st2, err := NewWithReader(root2, db, proc2)
	if err != nil {
		t.Fatal(err)
	}
	statsBefore := proc2.GetStats()
	// A was written -> evicted -> must be a MISS re-resolved to 101.
	if got := st2.GetBalance(addrA); got.Uint64() != 101 {
		t.Fatalf("A after commit: got %v, want 101", got)
	}
	// B untouched -> retained -> HIT, value 200.
	if got := st2.GetBalance(addrB); got.Uint64() != 200 {
		t.Fatalf("B after commit: got %v, want 200", got)
	}
	// C.slot1 written -> evicted -> MISS re-resolved to 0x33.
	if got := st2.GetState(addrC, slot1); got != common.BytesToHash([]byte{0x33}) {
		t.Fatalf("C.slot1 after commit: got %v, want 0x33", got)
	}
	// C.slot2 untouched -> retained -> HIT, value 0x22.
	if got := st2.GetState(addrC, slot2); got != common.BytesToHash([]byte{0x22}) {
		t.Fatalf("C.slot2 after commit: got %v, want 0x22", got)
	}
	statsAfter := proc2.GetStats()

	// Eviction/retention proof: A must have been a miss, B a hit.
	if statsAfter.AccountMiss-statsBefore.AccountMiss < 1 {
		t.Fatalf("written account A should be evicted (miss); account miss delta %d", statsAfter.AccountMiss-statsBefore.AccountMiss)
	}
	if statsAfter.AccountHit-statsBefore.AccountHit < 1 {
		t.Fatalf("untouched account B should be retained (hit); account hit delta %d", statsAfter.AccountHit-statsBefore.AccountHit)
	}
	if statsAfter.StorageMiss-statsBefore.StorageMiss < 1 {
		t.Fatalf("written slot C.slot1 should be evicted (miss); storage miss delta %d", statsAfter.StorageMiss-statsBefore.StorageMiss)
	}
	if statsAfter.StorageHit-statsBefore.StorageHit < 1 {
		t.Fatalf("untouched slot C.slot2 should be retained (hit); storage hit delta %d", statsAfter.StorageHit-statsBefore.StorageHit)
	}

	// Byte-equivalence against a fresh reader at root2.
	for _, addr := range []common.Address{addrA, addrB, addrC} {
		want, err := fresh.Account(addr)
		if err != nil {
			t.Fatal(err)
		}
		got, err := proc2.Account(addr)
		if err != nil {
			t.Fatal(err)
		}
		if (want == nil) != (got == nil) {
			t.Fatalf("account %x nil mismatch: served=%v fresh=%v", addr, got, want)
		}
		if want != nil && (want.Nonce != got.Nonce || want.Balance.Cmp(got.Balance) != 0 || want.Root != got.Root || string(want.CodeHash) != string(got.CodeHash)) {
			t.Fatalf("account %x served != fresh: %+v vs %+v", addr, got, want)
		}
	}
	for _, slot := range []common.Hash{slot1, slot2} {
		want, err := fresh.Storage(addrC, slot)
		if err != nil {
			t.Fatal(err)
		}
		got, err := proc2.Storage(addrC, slot)
		if err != nil {
			t.Fatal(err)
		}
		if want != got {
			t.Fatalf("C.slot %x served %x != fresh %x", slot, got, want)
		}
	}
}

// TestReaderRetentionStaleRootFallback proves a request for any root other than
// the one the cache was advanced to gets a fresh cache (reorg / sidechain).
func TestReaderRetentionStaleRootFallback(t *testing.T) {
	withRetention(t)
	db := NewDatabaseForTesting()
	root1 := buildBaseState(t, db)

	_, proc, err := db.ReadersWithCacheStats(root1, true)
	if err != nil {
		t.Fatal(err)
	}
	st1, err := NewWithReader(root1, db, proc)
	if err != nil {
		t.Fatal(err)
	}
	st1.AddBalance(addrA, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	root2, err := st1.Commit(1, true, true)
	if err != nil {
		t.Fatal(err)
	}

	// Asking for root1 (not the advanced root2) must NOT be served the retained cache.
	servedBefore := retentionServeMeter.Snapshot().Count()
	if _, _, err := db.ReadersWithCacheStats(root1, true); err != nil {
		t.Fatal(err)
	}
	if got := retentionServeMeter.Snapshot().Count(); got != servedBefore {
		t.Fatalf("retained cache served at wrong root: serve meter moved %d -> %d", servedBefore, got)
	}
	_ = root2
}

// warmCache builds a readerWithCache over root and warms A, B, C.slot1, C.slot2
// into it (retention on, so misses insert). Returns the cache.
func warmCache(t *testing.T, db *CachingDB, root common.Hash) *readerWithCache {
	t.Helper()
	reader, err := db.Reader(root)
	if err != nil {
		t.Fatal(err)
	}
	rc := newReaderWithCache(reader)
	rs := newReaderWithCacheStats(rc, roleProcess)
	if _, err := rs.Account(addrA); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Account(addrB); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Storage(addrC, slot1); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Storage(addrC, slot2); err != nil {
		t.Fatal(err)
	}
	return rc
}

// TestReaderRetentionDeletionDropsStorage proves that when advance sees an
// account deletion (nil account payload under the account hash), it drops all of
// that account's cached storage slots — the implicit storage wipe on deletion.
// Post-Cancun the live commit path cannot delete an account that has committed
// storage (noStorageWiping rejects it), so this exercises advance() directly with
// the stateUpdate a same-tx create-destruct (EIP-6780) would produce.
func TestReaderRetentionDeletionDropsStorage(t *testing.T) {
	withRetention(t)
	db := NewDatabaseForTesting()
	root1 := buildBaseState(t, db)
	rc := warmCache(t, db, root1)

	// Sanity: C's slots are cached before the advance.
	if _, ok := rc.storageCache.Load(storageKey{addr: addrC, slot: slot1}); !ok {
		t.Fatal("precondition: C.slot1 should be cached")
	}

	// Advance with a stateUpdate that deletes C: accountsOrigin has C, and the
	// account payload under keccak(C) is nil (deletion marker).
	reader, err := db.Reader(root1)
	if err != nil {
		t.Fatal(err)
	}
	upd := &stateUpdate{
		root:           root1,
		rawStorageKey:  true,
		accountsOrigin: map[common.Address][]byte{addrC: {0x01}}, // non-nil origin
		accounts:       map[common.Hash][]byte{crypto.Keccak256Hash(addrC.Bytes()): nil},
	}
	rc.advance(reader, upd)

	if _, ok := rc.storageCache.Load(storageKey{addr: addrC, slot: slot1}); ok {
		t.Fatal("deleted account slot1 not dropped from cache")
	}
	if _, ok := rc.storageCache.Load(storageKey{addr: addrC, slot: slot2}); ok {
		t.Fatal("deleted account slot2 not dropped from cache")
	}
	if _, ok := rc.accounts.Load(addrC); ok {
		t.Fatal("deleted account entry not evicted from cache")
	}
}

// TestReaderRetentionHashedKeyDropsStorage proves that when the block commits
// with hashed storage keys (pre-Cancun / noStorageWiping=false), advance cannot
// match individual raw-keyed slots and drops the whole account's cached slots.
func TestReaderRetentionHashedKeyDropsStorage(t *testing.T) {
	withRetention(t)
	db := NewDatabaseForTesting()
	root1 := buildBaseState(t, db)
	rc := warmCache(t, db, root1)

	reader, err := db.Reader(root1)
	if err != nil {
		t.Fatal(err)
	}
	// rawStorageKey=false: inner keys are slot hashes, unmatchable against the
	// raw-keyed cache, so C's whole slot set must be dropped.
	upd := &stateUpdate{
		root:          root1,
		rawStorageKey: false,
		storagesOrigin: map[common.Address]map[common.Hash][]byte{
			addrC: {crypto.Keccak256Hash(slot1.Bytes()): {0x99}},
		},
	}
	rc.advance(reader, upd)

	if _, ok := rc.storageCache.Load(storageKey{addr: addrC, slot: slot1}); ok {
		t.Fatal("hashed-key mode: C.slot1 not dropped")
	}
	if _, ok := rc.storageCache.Load(storageKey{addr: addrC, slot: slot2}); ok {
		t.Fatal("hashed-key mode: C.slot2 not dropped")
	}
}

// TestReaderRetentionGenerationGuardAccount proves that an in-flight insert that
// began against the pre-advance backing is a no-op once the cache has advanced,
// so a key the block wrote cannot resurface with its stale pre-block value.
func TestReaderRetentionGenerationGuardAccount(t *testing.T) {
	withRetention(t)
	db := NewDatabaseForTesting()
	root1 := buildBaseState(t, db)

	// Advance the cache A(100)->A(200) so A is in the write set and evicted.
	_, proc, err := db.ReadersWithCacheStats(root1, true)
	if err != nil {
		t.Fatal(err)
	}
	st1, err := NewWithReader(root1, db, proc)
	if err != nil {
		t.Fatal(err)
	}
	st1.AddBalance(addrA, uint256.NewInt(100), tracing.BalanceChangeUnspecified) // A -> 200
	root2, _, err := st1.CommitWithUpdate(1, true, true)
	if err != nil {
		t.Fatal(err)
	}

	// The retained cache is now advanced to root2 with A evicted. Simulate a
	// straggler that fetched A's OLD value under the OLD generation, then tries
	// to store it AFTER the advance.
	db.retainMu.Lock()
	rc := db.retainedCache
	db.retainMu.Unlock()
	if rc == nil {
		t.Fatal("expected a retained cache after commit")
	}
	staleGen := rc.gen.Load() - 1 // a generation from before the last advance
	staleEntry := &accountCacheEntry{acct: &types.StateAccount{Balance: uint256.NewInt(100)}, origin: rolePrefetch}
	_, stored := rc.storeAccountGuarded(addrA, staleEntry, staleGen)
	if stored {
		t.Fatal("stale in-flight insert across advance must be a no-op")
	}
	if _, ok := rc.accounts.Load(addrA); ok {
		t.Fatal("evicted written key must not be present after a guarded stale store")
	}

	// A fresh read must resolve A to its new value (200), not the stale 100.
	_, proc2, err := db.ReadersWithCacheStats(root2, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustAccount(t, proc2, addrA).Balance.Uint64(); got != 200 {
		t.Fatalf("A resolved to stale value: got %d, want 200", got)
	}
}

// TestReaderRetentionCap proves the retained cache resets wholesale once it
// exceeds the (test-lowered) size caps, keeping memory bounded.
func TestReaderRetentionCap(t *testing.T) {
	withRetention(t)
	db := NewDatabaseForTesting()
	root1 := buildBaseState(t, db)
	reader, err := db.Reader(root1)
	if err != nil {
		t.Fatal(err)
	}
	rc := newReaderWithCache(reader)

	// Inflate the account count past the cap, then advance with an empty write
	// set: the advance must reset the cache wholesale.
	rc.accountCount.Store(retentionMaxAccounts + 1)
	rc.accounts.Store(addrB, &accountCacheEntry{acct: &types.StateAccount{Balance: uint256.NewInt(1)}})

	resetBefore := retentionResetMeter.Snapshot().Count()
	rc.advance(reader, &stateUpdate{root: root1, rawStorageKey: true})
	if got := retentionResetMeter.Snapshot().Count(); got != resetBefore+1 {
		t.Fatalf("cap exceed did not trigger a wholesale reset: reset meter %d -> %d", resetBefore, got)
	}
	if _, ok := rc.accounts.Load(addrB); ok {
		t.Fatal("cache not cleared after wholesale reset")
	}
	if got := rc.accountCount.Load(); got != 0 {
		t.Fatalf("account count not reset: got %d", got)
	}
}

// TestReaderRetentionConcurrentAdvanceInsert stresses the advance-vs-insert race
// under -race: many goroutines resolve keys through the shared cache while the
// cache is advanced repeatedly. It must never serve a stale value for a written
// key and must be race-clean.
func TestReaderRetentionConcurrentAdvanceInsert(t *testing.T) {
	withRetention(t)
	db := NewDatabaseForTesting()
	root1 := buildBaseState(t, db)
	reader, err := db.Reader(root1)
	if err != nil {
		t.Fatal(err)
	}
	// Production concurrent-enables the retained backing at retain time; mirror
	// that so this test exercises the advance-vs-insert discipline rather than
	// the pre-existing non-concurrency of a plain trie reader.
	enableConcurrentOnReader(reader)
	rc := newReaderWithCache(reader)

	var wg sync.WaitGroup
	// Readers: hammer the miss/hit path concurrently.
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rs := newReaderWithCacheStats(rc, rolePrefetch)
			for i := 0; i < 2000; i++ {
				_, _ = rs.Account(addrB)
				_, _ = rs.Storage(addrC, slot2)
			}
		}()
	}
	// Advancer: repeatedly advance to the same backing/root with an empty write
	// set (exercises the gen bump + backing swap + lock discipline).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			rc.advance(reader, &stateUpdate{root: root1, rawStorageKey: true})
		}
	}()
	wg.Wait()

	// Untouched keys must still read correctly.
	rs := newReaderWithCacheStats(rc, roleProcess)
	if got := mustAccount(t, rs, addrB).Balance.Uint64(); got != 200 {
		t.Fatalf("B corrupted under concurrency: got %d, want 200", got)
	}
}

// TestReaderRetentionNotServedWhenDisallowed proves a consumer that passes
// allowRetention=false (the miner build path, and any witness-producing block)
// is always given a FRESH cache, never the shared retained object — even when a
// retained cache exists at exactly that root. This is the fix for the aliasing
// hazard: the retained cache is a mutable object advanced in place by the serial
// importer, so a concurrent long-lived consumer (the builder) must not share it,
// or an import advancing it would read new-root state into the old-root build.
func TestReaderRetentionNotServedWhenDisallowed(t *testing.T) {
	withRetention(t)
	db := NewDatabaseForTesting()
	root1 := buildBaseState(t, db)

	_, proc, err := db.ReadersWithCacheStats(root1, true)
	if err != nil {
		t.Fatal(err)
	}
	st1, err := NewWithReader(root1, db, proc)
	if err != nil {
		t.Fatal(err)
	}
	st1.AddBalance(addrA, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	root2, err := st1.Commit(1, true, true)
	if err != nil {
		t.Fatal(err)
	}

	// A retained cache now exists at root2. A miner-style request for root2 with
	// allowRetention=false must NOT be served it.
	servedBefore := retentionServeMeter.Snapshot().Count()
	if _, _, err := db.ReadersWithCacheStats(root2, false); err != nil {
		t.Fatal(err)
	}
	if got := retentionServeMeter.Snapshot().Count(); got != servedBefore {
		t.Fatalf("retained cache served to a disallowed (allowRetention=false) consumer: serve meter %d -> %d", servedBefore, got)
	}

	// And an allowed request for the same root IS served it (sanity: the cache
	// really was retainable), proving the gate — not a missing cache — blocked it.
	if _, _, _, err := db.ReadersWithCacheStatsTriple(root2, true); err != nil {
		t.Fatal(err)
	}
	if got := retentionServeMeter.Snapshot().Count(); got != servedBefore+1 {
		t.Fatalf("retained cache not served to an allowed consumer: serve meter %d -> %d", servedBefore, got)
	}
}

// TestReaderRetentionDisabledByDefault proves the flag is off by default and no
// cache is served across commits when disabled.
func TestReaderRetentionDisabledByDefault(t *testing.T) {
	// The package default (env unset) must be OFF.
	if os.Getenv("BOR_READER_RETENTION") == "" && readerRetentionEnabled {
		t.Fatal("BOR_READER_RETENTION must default to OFF")
	}
	// Force the flag OFF locally so the "no serve when disabled" behavior is
	// validated even when the suite is run with BOR_READER_RETENTION=1 set.
	prev := readerRetentionEnabled
	readerRetentionEnabled = false
	defer func() { readerRetentionEnabled = prev }()

	db := NewDatabaseForTesting()
	root1 := buildBaseState(t, db)
	_, proc, err := db.ReadersWithCacheStats(root1, true)
	if err != nil {
		t.Fatal(err)
	}
	st1, err := NewWithReader(root1, db, proc)
	if err != nil {
		t.Fatal(err)
	}
	st1.AddBalance(addrA, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	root2, err := st1.Commit(1, true, true)
	if err != nil {
		t.Fatal(err)
	}
	servedBefore := retentionServeMeter.Snapshot().Count()
	if _, _, err := db.ReadersWithCacheStats(root2, true); err != nil {
		t.Fatal(err)
	}
	if got := retentionServeMeter.Snapshot().Count(); got != servedBefore {
		t.Fatalf("retention served while disabled: serve meter moved %d -> %d", servedBefore, got)
	}
}

func mustAccount(t *testing.T, r ReaderWithStats, addr common.Address) *types.StateAccount {
	t.Helper()
	acct, err := r.Account(addr)
	if err != nil {
		t.Fatal(err)
	}
	if acct == nil {
		t.Fatalf("account %x missing", addr)
	}
	return acct
}
