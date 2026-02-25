// Copyright 2021 The go-ethereum Authors
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
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/internal/testrand"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/triedb"
)

func filledStateDB() *StateDB {
	state, _ := New(types.EmptyRootHash, NewDatabaseForTesting())

	// Create an account and check if the retrieved balance is correct
	addr := common.HexToAddress("0xaffeaffeaffeaffeaffeaffeaffeaffeaffeaffe")
	skey := common.HexToHash("aaa")
	sval := common.HexToHash("bbb")

	state.SetBalance(addr, uint256.NewInt(42), tracing.BalanceChangeUnspecified) // Change the account trie
	state.SetCode(addr, []byte("hello"), tracing.CodeChangeUnspecified)          // Change an external metadata
	state.SetState(addr, skey, sval)                                             // Change the storage trie
	for i := 0; i < 100; i++ {
		sk := common.BigToHash(big.NewInt(int64(i)))
		state.SetState(addr, sk, sk) // Change the storage trie
	}

	return state
}

func TestUseAfterTerminate(t *testing.T) {
	db := filledStateDB()
	prefetcher := newTriePrefetcher(db.db, db.originalRoot, "", true)
	skey := common.HexToHash("aaa")

	if err := prefetcher.prefetch(common.Hash{}, db.originalRoot, common.Address{}, nil, []common.Hash{skey}, false); err != nil {
		t.Errorf("Prefetch failed before terminate: %v", err)
	}
	prefetcher.terminate(false)

	if err := prefetcher.prefetch(common.Hash{}, db.originalRoot, common.Address{}, nil, []common.Hash{skey}, false); err == nil {
		t.Errorf("Prefetch succeeded after terminate: %v", err)
	}
	if tr := prefetcher.trie(common.Hash{}, db.originalRoot); tr == nil {
		t.Errorf("Prefetcher returned nil trie after terminate")
	}
}

func TestVerklePrefetcher(t *testing.T) {
	disk := rawdb.NewMemoryDatabase()
	db := triedb.NewDatabase(disk, triedb.VerkleDefaults)
	sdb := NewDatabase(db, nil)

	state, err := New(types.EmptyRootHash, sdb)
	if err != nil {
		t.Fatalf("failed to initialize state: %v", err)
	}
	// Create an account and check if the retrieved balance is correct
	addr := testrand.Address()
	skey := testrand.Hash()
	sval := testrand.Hash()

	state.SetBalance(addr, uint256.NewInt(42), tracing.BalanceChangeUnspecified) // Change the account trie
	state.SetCode(addr, []byte("hello"), tracing.CodeChangeUnspecified)          // Change an external metadata
	state.SetState(addr, skey, sval)                                             // Change the storage trie
	root, _ := state.Commit(0, true, false)

	state, _ = New(root, sdb)
	sRoot := state.GetStorageRoot(addr)
	fetcher := newTriePrefetcher(sdb, root, "", false)

	// Read account
	fetcher.prefetch(common.Hash{}, root, common.Address{}, []common.Address{addr}, nil, false)

	// Read storage slot
	fetcher.prefetch(crypto.Keccak256Hash(addr.Bytes()), sRoot, addr, nil, []common.Hash{skey}, false)

	fetcher.terminate(false)
	accountTrie := fetcher.trie(common.Hash{}, root)
	storageTrie := fetcher.trie(crypto.Keccak256Hash(addr.Bytes()), sRoot)

	rootA := accountTrie.Hash()
	rootB := storageTrie.Hash()
	if rootA != rootB {
		t.Fatal("Two different tries are retrieved")
	}
}

// newTerminatedSubfetcher creates a subfetcher that is already terminated,
// suitable for testing used()/appendUsed() without needing a real trie.
func newTerminatedSubfetcher(db Database, state common.Hash, owner common.Hash, root common.Hash, addr common.Address) *subfetcher {
	sf := &subfetcher{
		db:            db,
		state:         state,
		owner:         owner,
		root:          root,
		addr:          addr,
		wake:          make(chan struct{}, 1),
		stop:          make(chan struct{}),
		term:          make(chan struct{}),
		seenReadAddr:  make(map[common.Address]struct{}),
		seenWriteAddr: make(map[common.Address]struct{}),
		seenReadSlot:  make(map[common.Hash]struct{}),
		seenWriteSlot: make(map[common.Hash]struct{}),
	}
	close(sf.term)
	return sf
}

// TestConcurrentUsed verifies that calling used() concurrently from multiple
// goroutines on different subfetchers produces correct results. Run with -race
// to detect data races.
func TestConcurrentUsed(t *testing.T) {
	db := filledStateDB()
	prefetcher := newTriePrefetcher(db.db, db.originalRoot, "concurrent-used", false)

	const N = 10
	const addrCount = 100
	const slotCount = 50

	type fetcherKey struct {
		owner common.Hash
		root  common.Hash
	}
	keys := make([]fetcherKey, N)
	for i := 0; i < N; i++ {
		owner := common.Hash{byte(i + 1)}
		root := common.Hash{byte(i + 1)}
		keys[i] = fetcherKey{owner: owner, root: root}

		sf := newTerminatedSubfetcher(db.db, db.originalRoot, owner, root, common.Address{byte(i)})
		prefetcher.fetchers[prefetcher.trieID(owner, root)] = sf
	}

	// Spawn N goroutines, each calling used() on its own subfetcher
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			addrs := make([]common.Address, addrCount)
			for j := range addrs {
				addrs[j] = common.Address{byte(idx), byte(j)}
			}
			slots := make([]common.Hash, slotCount)
			for j := range slots {
				slots[j] = common.Hash{byte(idx), byte(j)}
			}
			prefetcher.used(keys[idx].owner, keys[idx].root, addrs, slots)
		}(i)
	}
	wg.Wait()

	// Verify each subfetcher received exactly the expected data
	for i := 0; i < N; i++ {
		id := prefetcher.trieID(keys[i].owner, keys[i].root)
		fetcher := prefetcher.fetchers[id]
		if fetcher == nil {
			t.Fatalf("subfetcher %d not found", i)
		}
		if got := len(fetcher.usedAddr); got != addrCount {
			t.Errorf("subfetcher %d: len(usedAddr) = %d, want %d", i, got, addrCount)
		}
		if got := len(fetcher.usedSlot); got != slotCount {
			t.Errorf("subfetcher %d: len(usedSlot) = %d, want %d", i, got, slotCount)
		}
		for j := 0; j < addrCount && j < len(fetcher.usedAddr); j++ {
			expected := common.Address{byte(i), byte(j)}
			if fetcher.usedAddr[j] != expected {
				t.Errorf("subfetcher %d: usedAddr[%d] = %x, want %x", i, j, fetcher.usedAddr[j], expected)
				break
			}
		}
		for j := 0; j < slotCount && j < len(fetcher.usedSlot); j++ {
			expected := common.Hash{byte(i), byte(j)}
			if fetcher.usedSlot[j] != expected {
				t.Errorf("subfetcher %d: usedSlot[%d] = %x, want %x", i, j, fetcher.usedSlot[j], expected)
				break
			}
		}
	}
}

// TestConcurrentUsedParallelism verifies that concurrent used() calls on
// different subfetchers actually run in parallel rather than serializing behind
// a global lock. The test compares wall-clock time of parallel vs sequential
// execution and asserts at least a 2x speedup.
func TestConcurrentUsedParallelism(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping parallelism test in short mode")
	}

	const N = 50          // number of subfetchers / goroutines
	const M = 5000        // iterations per goroutine
	const batchSize = 100 // addresses per used() call

	type fetcherKey struct {
		owner common.Hash
		root  common.Hash
	}

	newPrefetcherWithSubfetchers := func() (*triePrefetcher, []fetcherKey) {
		db := filledStateDB()
		p := newTriePrefetcher(db.db, db.originalRoot, "parallelism", false)
		keys := make([]fetcherKey, N)
		for i := 0; i < N; i++ {
			// Encode index into two bytes to support N > 255
			owner := common.Hash{byte(i/255 + 1), byte(i%255 + 1)}
			root := common.Hash{byte(i/255 + 1), byte(i%255 + 1)}
			keys[i] = fetcherKey{owner: owner, root: root}

			sf := newTerminatedSubfetcher(db.db, db.originalRoot, owner, root, common.Address{byte(i)})
			p.fetchers[p.trieID(owner, root)] = sf
		}
		return p, keys
	}

	// Pre-create address batches (outside the timed section)
	batches := make([][]common.Address, N)
	for i := 0; i < N; i++ {
		batches[i] = make([]common.Address, batchSize)
		for j := range batches[i] {
			batches[i][j] = common.Address{byte(i), byte(j)}
		}
	}

	// Sequential baseline: single goroutine makes all N*M calls
	p1, keys1 := newPrefetcherWithSubfetchers()
	seqStart := time.Now()
	for iter := 0; iter < M; iter++ {
		for i := 0; i < N; i++ {
			p1.used(keys1[i].owner, keys1[i].root, batches[i], nil)
		}
	}
	sequential := time.Since(seqStart)

	// Parallel: N goroutines each make M calls on their own subfetcher
	p2, keys2 := newPrefetcherWithSubfetchers()
	var wg sync.WaitGroup
	parStart := time.Now()
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for iter := 0; iter < M; iter++ {
				p2.used(keys2[idx].owner, keys2[idx].root, batches[idx], nil)
			}
		}(i)
	}
	wg.Wait()
	parallel := time.Since(parStart)

	speedup := float64(sequential) / float64(parallel)
	t.Logf("sequential=%v parallel=%v speedup=%.1fx", sequential, parallel, speedup)
	if speedup < 2.0 {
		t.Errorf("expected at least 2x speedup, got %.1fx (sequential=%v, parallel=%v)", speedup, sequential, parallel)
	}
}

// TestUsedStateCorrectAfterReport verifies that report() correctly reads
// usedAddr/usedSlot after concurrent used() calls. It exercises both the
// account-trie and storage-trie branches in report(). Run with -race to
// detect data races on the usedLock.
func TestUsedStateCorrectAfterReport(t *testing.T) {
	metrics.Enable()

	db := filledStateDB()
	prefetcher := newTriePrefetcher(db.db, db.originalRoot, "report", false)

	// Account-trie subfetcher: root matches p.root
	acctOwner, acctRoot := common.Hash{}, db.originalRoot
	acctSF := newTerminatedSubfetcher(db.db, db.originalRoot, acctOwner, acctRoot, common.Address{})
	prefetcher.fetchers[prefetcher.trieID(acctOwner, acctRoot)] = acctSF

	// Storage-trie subfetchers: root != p.root
	const storageN = 5
	type fetcherKey struct {
		owner common.Hash
		root  common.Hash
	}
	storageKeys := make([]fetcherKey, storageN)
	for i := 0; i < storageN; i++ {
		owner := common.Hash{byte(i + 1)}
		root := common.Hash{byte(i + 1)}
		storageKeys[i] = fetcherKey{owner: owner, root: root}

		sf := newTerminatedSubfetcher(db.db, db.originalRoot, owner, root, common.Address{byte(i)})
		prefetcher.fetchers[prefetcher.trieID(owner, root)] = sf
	}

	// Concurrently call used() on all subfetchers
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		addrs := []common.Address{{0x01}, {0x02}, {0x03}}
		prefetcher.used(acctOwner, acctRoot, addrs, nil)
	}()
	for i := 0; i < storageN; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			slots := []common.Hash{{byte(idx)}, {byte(idx + 100)}}
			prefetcher.used(storageKeys[idx].owner, storageKeys[idx].root, nil, slots)
		}(i)
	}
	wg.Wait()

	// report() should not panic or race
	prefetcher.report()

	// Verify account subfetcher received its addresses
	if got := len(acctSF.usedAddr); got != 3 {
		t.Errorf("account subfetcher: len(usedAddr) = %d, want 3", got)
	}
	// Verify each storage subfetcher received its slots
	for i := 0; i < storageN; i++ {
		id := prefetcher.trieID(storageKeys[i].owner, storageKeys[i].root)
		fetcher := prefetcher.fetchers[id]
		if got := len(fetcher.usedSlot); got != 2 {
			t.Errorf("storage subfetcher %d: len(usedSlot) = %d, want 2", i, got)
		}
	}
}
