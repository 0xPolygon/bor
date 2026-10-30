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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state/snapshot"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/trie/bintrie"
	"github.com/ethereum/go-ethereum/triedb"
)

// UBTDatabase is an implementation of Database interface for Unified Binary Trie.
// It provides the same functionality as MPTDatabase but uses unified binary
// trie for state hashing instead of Merkle Patricia Tries.
//
// Bor never constructs this outside of tooling and tests: the Verkle/UBT fork is
// dormant on every preset, so NewDatabase only reaches it when a triedb is
// explicitly opened with triedb.UBTDefaults.
type UBTDatabase struct {
	disk          ethdb.KeyValueStore
	triedb        *triedb.Database
	codeCache     *lru.SizeConstrainedCache[common.Hash, []byte]
	codeSizeCache *lru.Cache[common.Hash, int]
	recorder      *bintrie.Recorder
}

// EnableAllocRecording installs an alloc recorder shared across every binary
// trie opened from this database. The recorder captures account, storage, and
// code writes keyed by their original (unhashed) addresses, which is required
// for tooling like evm t8n to render the post-state as a types.GenesisAlloc.
func (db *UBTDatabase) EnableAllocRecording() *bintrie.Recorder {
	if db.recorder == nil {
		db.recorder = bintrie.NewRecorder()
	}
	return db.recorder
}

// AllocRecorder returns the attached recorder, or nil if recording was never
// enabled on this database.
func (db *UBTDatabase) AllocRecorder() *bintrie.Recorder { return db.recorder }

// Type returns TypeUBT, indicating this database is backed by a Unified Binary Trie.
func (db *UBTDatabase) Type() DatabaseType { return TypeUBT }

// NewUBTDatabase creates a state database with the Unified binary trie manner.
func NewUBTDatabase(tdb *triedb.Database) *UBTDatabase {
	return &UBTDatabase{
		disk:          tdb.Disk(),
		triedb:        tdb,
		codeCache:     lru.NewSizeConstrainedCache[common.Hash, []byte](codeCacheSize),
		codeSizeCache: lru.NewCache[common.Hash, int](codeSizeCacheSize),
	}
}

// Reader returns a state reader associated with the specified state root.
func (db *UBTDatabase) Reader(stateRoot common.Hash) (Reader, error) {
	var readers []StateReader

	// Configure the state reader using the path database in path mode.
	// This reader offers improved performance but is optional and only
	// partially useful if the snapshot data in path database is not
	// fully generated.
	if db.TrieDB().Scheme() == rawdb.PathScheme {
		reader, err := db.triedb.StateReader(stateRoot)
		if err == nil {
			readers = append(readers, newFlatReader(reader))
		}
	}
	// Configure the trie reader, which is expected to be available as the
	// gatekeeper unless the state is corrupted.
	tr, err := newUBTTrieReader(stateRoot, db.triedb)
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

// OpenTrie opens the main account trie at a specific root hash.
func (db *UBTDatabase) OpenTrie(root common.Hash) (Trie, error) {
	tr, err := bintrie.NewBinaryTrie(root, db.triedb, db.triedb.BinTrieGroupDepth())
	if err != nil {
		return nil, err
	}
	if db.recorder != nil {
		tr.SetRecorder(db.recorder)
	}
	return tr, nil
}

// OpenStorageTrie opens the storage trie of an account. In binary trie mode,
// all state objects share one unified trie, so the main trie is returned.
func (db *UBTDatabase) OpenStorageTrie(stateRoot common.Hash, address common.Address, root common.Hash, self Trie) (Trie, error) {
	return self, nil
}

// TrieDB retrieves any intermediate trie-node caching layer.
func (db *UBTDatabase) TrieDB() *triedb.Database {
	return db.triedb
}

// Snapshot implements Database. A unified binary trie has no snapshot layer.
func (db *UBTDatabase) Snapshot() *snapshot.Tree {
	return nil
}

// Iteratee returns a state iteratee associated with the specified state root,
// through which the account iterator and storage iterator can be created.
func (db *UBTDatabase) Iteratee(root common.Hash) (Iteratee, error) {
	return newStateIteratee(false, root, db.triedb, nil)
}
