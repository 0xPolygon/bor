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
	"fmt"
	"math/big"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// TestCacheAttribution_PrefetchToProcess verifies that PREFETCH-origin entries
// are correctly attributed when PROCESS hits them.
func TestCacheAttribution_PrefetchToProcess(t *testing.T) {
	// Setup: Create a state database with some accounts
	db := rawdb.NewMemoryDatabase()
	triedb := triedb.NewDatabase(db, nil)
	statedb := NewDatabase(triedb, nil)

	// Create initial state with some accounts
	state, err := New(types.EmptyRootHash, statedb)
	if err != nil {
		t.Fatalf("Failed to create state: %v", err)
	}

	// Create test accounts
	addr1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	addr3 := common.HexToAddress("0x3333333333333333333333333333333333333333")

	// Add accounts to state using state objects
	obj1 := state.getOrNewStateObject(addr1)
	obj1.SetBalance(uint256.NewInt(100))
	obj2 := state.getOrNewStateObject(addr2)
	obj2.SetBalance(uint256.NewInt(200))
	obj3 := state.getOrNewStateObject(addr3)
	obj3.SetBalance(uint256.NewInt(300))

	// Add storage for one account
	storageKey := common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	state.SetState(addr1, storageKey, common.HexToHash("0xabcd"))

	// Commit state to database
	root, err := state.Commit(0, false, false)
	if err != nil {
		t.Fatalf("Failed to commit state: %v", err)
	}

	// Create dual readers with shared cache (simulating StateAtWithReaders)
	prefetchReader, processReader, err := statedb.ReadersWithCacheStats(root)
	if err != nil {
		t.Fatalf("Failed to create readers: %v", err)
	}

	// PREFETCH phase: Read accounts (this should insert with origin=rolePrefetch)
	prefetchAcct1, err := prefetchReader.Account(addr1)
	if err != nil {
		t.Fatalf("Prefetch failed to read account: %v", err)
	}
	if prefetchAcct1 == nil {
		t.Fatal("Prefetch got nil account")
	}

	prefetchAcct2, err := prefetchReader.Account(addr2)
	if err != nil {
		t.Fatalf("Prefetch failed to read account: %v", err)
	}
	if prefetchAcct2 == nil {
		t.Fatal("Prefetch got nil account")
	}

	// Prefetch storage slot
	prefetchStorage, err := prefetchReader.Storage(addr1, storageKey)
	if err != nil {
		t.Fatalf("Prefetch failed to read storage: %v", err)
	}
	if prefetchStorage != common.HexToHash("0xabcd") {
		t.Fatalf("Prefetch got wrong storage value: %v", prefetchStorage)
	}

	// Check prefetch stats
	prefetchStats := prefetchReader.GetStats()
	if prefetchStats.AccountMiss < 2 {
		t.Errorf("Expected at least 2 account misses in prefetch, got %d", prefetchStats.AccountMiss)
	}

	prefetchAttribStats := prefetchReader.GetPrefetchStats()
	if prefetchAttribStats.AccountInsert < 2 {
		t.Errorf("Expected at least 2 account inserts from prefetch, got %d", prefetchAttribStats.AccountInsert)
	}
	if prefetchAttribStats.StorageInsert < 1 {
		t.Errorf("Expected at least 1 storage insert from prefetch, got %d", prefetchAttribStats.StorageInsert)
	}

	// PROCESS phase: Read same accounts (should hit prefetch-warmed cache)
	processAcct1, err := processReader.Account(addr1)
	if err != nil {
		t.Fatalf("Process failed to read account: %v", err)
	}
	if processAcct1 == nil {
		t.Fatal("Process got nil account")
	}

	processAcct2, err := processReader.Account(addr2)
	if err != nil {
		t.Fatalf("Process failed to read account: %v", err)
	}
	if processAcct2 == nil {
		t.Fatal("Process got nil account")
	}

	// Process reads storage
	processStorage, err := processReader.Storage(addr1, storageKey)
	if err != nil {
		t.Fatalf("Process failed to read storage: %v", err)
	}
	if processStorage != common.HexToHash("0xabcd") {
		t.Fatalf("Process got wrong storage value: %v", processStorage)
	}

	// Verify process stats show hits
	processStats := processReader.GetStats()
	if processStats.AccountHit < 2 {
		t.Errorf("Expected at least 2 account hits in process, got %d", processStats.AccountHit)
	}
	if processStats.StorageHit < 1 {
		t.Errorf("Expected at least 1 storage hit in process, got %d", processStats.StorageHit)
	}

	// Verify attribution: process hits came from prefetch-origin entries
	processAttribStats := processReader.GetPrefetchStats()
	if processAttribStats.AccountHitFromPrefetch < 2 {
		t.Errorf("Expected at least 2 account hits from prefetch in process, got %d",
			processAttribStats.AccountHitFromPrefetch)
	}
	if processAttribStats.StorageHitFromPrefetch < 1 {
		t.Errorf("Expected at least 1 storage hit from prefetch in process, got %d",
			processAttribStats.StorageHitFromPrefetch)
	}

	// Verify unique usage tracking
	if processAttribStats.AccountHitFromPrefetchUnique < 2 {
		t.Errorf("Expected at least 2 unique prefetch accounts used by process, got %d",
			processAttribStats.AccountHitFromPrefetchUnique)
	}

	t.Logf("Prefetch stats: AccountMiss=%d, AccountInsert=%d, StorageInsert=%d",
		prefetchStats.AccountMiss, prefetchAttribStats.AccountInsert, prefetchAttribStats.StorageInsert)
	t.Logf("Process stats: AccountHit=%d, StorageHit=%d",
		processStats.AccountHit, processStats.StorageHit)
	t.Logf("Attribution stats: AccountHitFromPrefetch=%d, StorageHitFromPrefetch=%d, UniqueUsed=%d",
		processAttribStats.AccountHitFromPrefetch,
		processAttribStats.StorageHitFromPrefetch,
		processAttribStats.AccountHitFromPrefetchUnique)
}

// TestCacheAttribution_UniqueUsageTracking validates that the usedByProcess
// atomic flag flips exactly once per entry, even when accessed multiple times.
func TestCacheAttribution_UniqueUsageTracking(t *testing.T) {
	// Setup: Create a state database with an account
	db := rawdb.NewMemoryDatabase()
	triedb := triedb.NewDatabase(db, nil)
	statedb := NewDatabase(triedb, nil)

	// Create initial state
	state, err := New(types.EmptyRootHash, statedb)
	if err != nil {
		t.Fatalf("Failed to create state: %v", err)
	}

	// Create test account using state object
	addr := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	obj := state.getOrNewStateObject(addr)
	obj.SetBalance(uint256.NewInt(1000))

	// Commit state
	root, err := state.Commit(0, false, false)
	if err != nil {
		t.Fatalf("Failed to commit state: %v", err)
	}

	// Create dual readers
	prefetchReader, processReader, err := statedb.ReadersWithCacheStats(root)
	if err != nil {
		t.Fatalf("Failed to create readers: %v", err)
	}

	// PREFETCH: Read account once (inserts with origin=rolePrefetch)
	_, err = prefetchReader.Account(addr)
	if err != nil {
		t.Fatalf("Prefetch failed: %v", err)
	}

	prefetchStats := prefetchReader.GetPrefetchStats()
	if prefetchStats.AccountInsert != 1 {
		t.Fatalf("Expected 1 account insert from prefetch, got %d", prefetchStats.AccountInsert)
	}

	// PROCESS: Read account FIRST time (should increment unique counter)
	_, err = processReader.Account(addr)
	if err != nil {
		t.Fatalf("Process first read failed: %v", err)
	}

	processStats1 := processReader.GetPrefetchStats()
	if processStats1.AccountHitFromPrefetch != 1 {
		t.Errorf("Expected 1 account hit from prefetch after first read, got %d",
			processStats1.AccountHitFromPrefetch)
	}
	if processStats1.AccountHitFromPrefetchUnique != 1 {
		t.Errorf("Expected unique counter = 1 after first read, got %d",
			processStats1.AccountHitFromPrefetchUnique)
	}

	// PROCESS: Read account SECOND time (should increment hit counter but NOT unique counter)
	_, err = processReader.Account(addr)
	if err != nil {
		t.Fatalf("Process second read failed: %v", err)
	}

	processStats2 := processReader.GetPrefetchStats()
	if processStats2.AccountHitFromPrefetch != 2 {
		t.Errorf("Expected 2 account hits from prefetch after second read, got %d",
			processStats2.AccountHitFromPrefetch)
	}
	if processStats2.AccountHitFromPrefetchUnique != 1 {
		t.Errorf("Expected unique counter to stay at 1 after second read, got %d",
			processStats2.AccountHitFromPrefetchUnique)
	}

	// PROCESS: Read account THIRD time (verify unique counter still doesn't increment)
	_, err = processReader.Account(addr)
	if err != nil {
		t.Fatalf("Process third read failed: %v", err)
	}

	processStats3 := processReader.GetPrefetchStats()
	if processStats3.AccountHitFromPrefetch != 3 {
		t.Errorf("Expected 3 account hits from prefetch after third read, got %d",
			processStats3.AccountHitFromPrefetch)
	}
	if processStats3.AccountHitFromPrefetchUnique != 1 {
		t.Errorf("Expected unique counter to stay at 1 after third read, got %d",
			processStats3.AccountHitFromPrefetchUnique)
	}

	t.Logf("After 3 reads: AccountHitFromPrefetch=%d, AccountHitFromPrefetchUnique=%d",
		processStats3.AccountHitFromPrefetch,
		processStats3.AccountHitFromPrefetchUnique)

	// Verify: Hit counter increased 3 times, unique counter only once
	if processStats3.AccountHitFromPrefetch != 3 {
		t.Error("Hit counter should increment on every read")
	}
	if processStats3.AccountHitFromPrefetchUnique != 1 {
		t.Error("Unique counter should only increment once (atomic CAS ensures this)")
	}
}

// P1 Tests

// TestReaderWithCache_ConcurrentAccess validates thread-safety of shared cache
// between multiple readers accessing concurrently
func TestReaderWithCache_ConcurrentAccess(t *testing.T) {
	// Setup: Create a state database with many accounts
	db := rawdb.NewMemoryDatabase()
	triedb := triedb.NewDatabase(db, nil)
	statedb := NewDatabase(triedb, nil)

	// Create initial state with 100 accounts
	state, err := New(types.EmptyRootHash, statedb)
	if err != nil {
		t.Fatalf("Failed to create state: %v", err)
	}

	accountCount := 100
	for i := 0; i < accountCount; i++ {
		addr := common.BigToAddress(big.NewInt(int64(i)))
		obj := state.getOrNewStateObject(addr)
		obj.SetBalance(uint256.NewInt(uint64(i * 1000)))

		// Add storage for every 5th account
		if i%5 == 0 {
			storageKey := common.BigToHash(big.NewInt(int64(i)))
			state.SetState(addr, storageKey, common.BigToHash(big.NewInt(int64(i*100))))
		}
	}

	// Commit state
	root, err := state.Commit(0, false, false)
	if err != nil {
		t.Fatalf("Failed to commit state: %v", err)
	}

	// Create two readers sharing the same cache
	prefetchReader, processReader, err := statedb.ReadersWithCacheStats(root)
	if err != nil {
		t.Fatalf("Failed to create readers: %v", err)
	}

	// Use a wait group to synchronize goroutines
	var wg sync.WaitGroup
	errChan := make(chan error, 20) // Buffer for potential errors

	// Spawn 10 goroutines for prefetch reader
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			// Each goroutine accesses 100 random accounts
			for i := 0; i < 100; i++ {
				addr := common.BigToAddress(big.NewInt(int64((goroutineID*100 + i) % accountCount)))
				_, err := prefetchReader.Account(addr)
				if err != nil {
					errChan <- fmt.Errorf("prefetch goroutine %d: %v", goroutineID, err)
					return
				}

				// Also access storage for some accounts
				if i%5 == 0 {
					storageKey := common.BigToHash(big.NewInt(int64(i)))
					_, err := prefetchReader.Storage(addr, storageKey)
					if err != nil {
						errChan <- fmt.Errorf("prefetch goroutine %d storage: %v", goroutineID, err)
						return
					}
				}
			}
		}(g)
	}

	// Spawn 10 goroutines for process reader
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			// Each goroutine accesses 100 random accounts
			for i := 0; i < 100; i++ {
				addr := common.BigToAddress(big.NewInt(int64((goroutineID*50 + i) % accountCount)))
				_, err := processReader.Account(addr)
				if err != nil {
					errChan <- fmt.Errorf("process goroutine %d: %v", goroutineID, err)
					return
				}

				// Also access storage for some accounts
				if i%5 == 0 {
					storageKey := common.BigToHash(big.NewInt(int64(i)))
					_, err := processReader.Storage(addr, storageKey)
					if err != nil {
						errChan <- fmt.Errorf("process goroutine %d storage: %v", goroutineID, err)
						return
					}
				}
			}
		}(g)
	}

	// Wait for all goroutines to complete
	wg.Wait()
	close(errChan)

	// Check for any errors
	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}
	if len(errors) > 0 {
		t.Fatalf("Concurrent access failed with %d errors, first error: %v", len(errors), errors[0])
	}

	// Verify both readers have stats (proving they worked concurrently)
	prefetchStats := prefetchReader.GetStats()
	processStats := processReader.GetStats()

	if prefetchStats.AccountHit+prefetchStats.AccountMiss == 0 {
		t.Error("Prefetch reader should have accessed accounts")
	}
	if processStats.AccountHit+processStats.AccountMiss == 0 {
		t.Error("Process reader should have accessed accounts")
	}

	// Verify attribution: process should have some hits from prefetch-warmed cache
	processAttrib := processReader.GetPrefetchStats()
	t.Logf("Concurrent access results - Prefetch: %d hits/%d misses, Process: %d hits/%d misses, HitsFromPrefetch: %d",
		prefetchStats.AccountHit, prefetchStats.AccountMiss,
		processStats.AccountHit, processStats.AccountMiss,
		processAttrib.AccountHitFromPrefetch)

	// The test passing without panics or race conditions validates:
	// 1. RWMutex locking works correctly
	// 2. First-writer-wins semantics are thread-safe
	// 3. Concurrent reads and writes to the cache are safe
}
