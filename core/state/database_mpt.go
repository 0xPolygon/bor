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
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state/snapshot"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

// MPTDatabase is an implementation of Database interface for Merkle Patricia Tries.
// It leverages both trie and state snapshot to provide functionalities for state
// access. It's meant to be a long-live object and has a few caches inside for
// sharing between blocks.
type MPTDatabase struct {
	disk            ethdb.KeyValueStore
	triedb          *triedb.Database
	snap            *snapshot.Tree
	codeCache       *lru.SizeConstrainedCache[common.Hash, []byte]
	codeSizeCache   *lru.Cache[common.Hash, int]
	snapMu          sync.RWMutex // Protects useSnapInReader
	useSnapInReader bool
}

// Type returns TypeMPT, indicating this database is backed by a Merkle Patricia Trie.
func (db *MPTDatabase) Type() DatabaseType { return TypeMPT }

// NewMPTDatabase creates a state database with the Merkle Patricia Trie manner.
func NewMPTDatabase(tdb *triedb.Database, snap *snapshot.Tree) *MPTDatabase {
	return &MPTDatabase{
		disk:            tdb.Disk(),
		triedb:          tdb,
		snap:            snap,
		codeCache:       lru.NewSizeConstrainedCache[common.Hash, []byte](codeCacheSize),
		codeSizeCache:   lru.NewCache[common.Hash, int](codeSizeCacheSize),
		useSnapInReader: true,
	}
}

func (db *MPTDatabase) DisableSnapInReader() {
	db.snapMu.Lock()
	db.useSnapInReader = false
	db.snapMu.Unlock()
}

func (db *MPTDatabase) EnableSnapInReader() {
	db.snapMu.Lock()
	db.useSnapInReader = true
	db.snapMu.Unlock()
}

// Reader returns a state reader associated with the specified state root.
func (db *MPTDatabase) Reader(stateRoot common.Hash) (Reader, error) {
	var readers []StateReader

	// Configure the state reader using the standalone snapshot in hash mode.
	// This reader offers improved performance but is optional and only
	// partially useful if the snapshot is not fully generated.
	db.snapMu.RLock()
	useSnap := db.useSnapInReader
	db.snapMu.RUnlock()
	if db.TrieDB().Scheme() == rawdb.HashScheme && db.snap != nil && useSnap {
		snap := db.snap.Snapshot(stateRoot)
		if snap != nil {
			readers = append(readers, newFlatReader(snap))
		}
	}
	// Configure the state reader using the path database in path mode.
	// This reader offers improved performance but is optional and only
	// partially useful if the snapshot data in path database is not
	// fully generated.
	if db.TrieDB().Scheme() == rawdb.PathScheme && useSnap {
		reader, err := db.triedb.StateReader(stateRoot)
		if err == nil {
			readers = append(readers, newFlatReader(reader))
		}
	}
	// Configure the trie reader, which is expected to be available as the
	// gatekeeper unless the state is corrupted.
	tr, err := newTrieReader(stateRoot, db.triedb)
	if err != nil {
		return nil, err
	}
	readers = append(readers, tr)

	combined, err := newMultiStateReader(readers...)
	if err != nil {
		return nil, err
	}
	return newReader(newCachingCodeReader(db.disk, db.codeCache, db.codeSizeCache), combined), nil
}

// ReaderTrieOnly creates a state reader that only uses the trie, skipping
// snapshot layers. Useful for V2 parallel execution where the snapshot reader
// may have thread-safety issues under concurrent access from multiple workers.
func (db *MPTDatabase) ReaderTrieOnly(stateRoot common.Hash) (Reader, error) {
	tr, err := newTrieReader(stateRoot, db.triedb)
	if err != nil {
		return nil, err
	}
	combined, err := newMultiStateReader(tr)
	if err != nil {
		return nil, err
	}
	return newReader(newCachingCodeReader(db.disk, db.codeCache, db.codeSizeCache), combined), nil
}

// ReadersWithCacheStats creates a pair of state readers sharing the same internal cache and
// same backing Reader, but exposing separate statistics.
func (db *MPTDatabase) ReadersWithCacheStats(stateRoot common.Hash) (ReaderWithStats, ReaderWithStats, error) {
	reader, err := db.Reader(stateRoot)
	if err != nil {
		return nil, nil, err
	}
	shared := newReaderWithCache(reader)
	return newReaderWithCacheStats(shared, rolePrefetch), newReaderWithCacheStats(shared, roleProcess), nil
}

// ReadersWithCacheStatsTriple creates three state readers sharing the same
// internal cache: prefetch, process (serial), and parallel (V2).
// The shared cache means prefetcher warms data that V2 reads for free.
func (db *MPTDatabase) ReadersWithCacheStatsTriple(stateRoot common.Hash) (ReaderWithStats, ReaderWithStats, ReaderWithStats, error) {
	reader, err := db.Reader(stateRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	shared := newReaderWithCache(reader)
	return newReaderWithCacheStats(shared, rolePrefetch),
		newReaderWithCacheStats(shared, roleProcess),
		newReaderWithCacheStats(shared, roleProcess), // V2 shares same cache
		nil
}

// OpenTrie opens the main account trie at a specific root hash.
func (db *MPTDatabase) OpenTrie(root common.Hash) (Trie, error) {
	tr, err := trie.NewStateTrie(trie.StateTrieID(root), db.triedb)
	if err != nil {
		return nil, err
	}

	return tr, nil
}

// OpenStorageTrie opens the storage trie of an account.
func (db *MPTDatabase) OpenStorageTrie(stateRoot common.Hash, address common.Address, root common.Hash, self Trie) (Trie, error) {
	tr, err := trie.NewStateTrie(trie.StorageTrieID(stateRoot, crypto.Keccak256Hash(address.Bytes()), root), db.triedb)
	if err != nil {
		return nil, err
	}

	return tr, nil
}

// ContractCodeWithPrefix retrieves a particular contract's code. If the
// code can't be found in the cache, then check the existence with **new**
// db scheme.
func (db *MPTDatabase) ContractCodeWithPrefix(address common.Address, codeHash common.Hash) []byte {
	code, _ := db.codeCache.Get(codeHash)
	if len(code) > 0 {
		return code
	}

	code = rawdb.ReadCodeWithPrefix(db.disk, codeHash)

	if len(code) > 0 {
		db.codeCache.Add(codeHash, code)
		db.codeSizeCache.Add(codeHash, len(code))
	}
	return code
}

// TrieDB retrieves any intermediate trie-node caching layer.
func (db *MPTDatabase) TrieDB() *triedb.Database {
	return db.triedb
}

// Snapshot returns the underlying state snapshot.
func (db *MPTDatabase) Snapshot() *snapshot.Tree {
	return db.snap
}

// Iteratee returns a state iteratee associated with the specified state root.
func (db *MPTDatabase) Iteratee(root common.Hash) (Iteratee, error) {
	return newStateIteratee(true, root, db.triedb, db.snap)
}
