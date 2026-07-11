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

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/ethereum/go-ethereum/triedb"
)

// mergedSet builds a MergedNodeSet with one account-trie node per entry, keyed
// by path with an arbitrary-but-stable hash derived from the blob.
func mergedSet(t *testing.T, owner common.Hash, entries map[string][]byte) *trienode.MergedNodeSet {
	t.Helper()
	set := trienode.NewNodeSet(owner)
	for path, blob := range entries {
		set.AddNode([]byte(path), trienode.NewNodeWithPrev(common.BytesToHash(blob), blob, nil))
	}
	merged := trienode.NewMergedNodeSet()
	require.NoError(t, merged.Merge(set))
	return merged
}

func TestWarmNodeRing_AddLookupEvict(t *testing.T) {
	ring := newWarmNodeRing(2, 1<<30)
	owner := common.Hash{}

	ring.Add(mergedSet(t, owner, map[string][]byte{"\x01": []byte("blob-one")}))
	ring.Add(mergedSet(t, owner, map[string][]byte{"\x02": []byte("blob-two")}))

	blob, ok := ring.Lookup(owner, []byte{0x01}, common.BytesToHash([]byte("blob-one")))
	require.True(t, ok)
	require.Equal(t, []byte("blob-one"), blob)
	_, ok = ring.Lookup(owner, []byte{0x02}, common.BytesToHash([]byte("blob-two")))
	require.True(t, ok)

	// Third generation evicts the first.
	ring.Add(mergedSet(t, owner, map[string][]byte{"\x03": []byte("blob-three")}))
	_, ok = ring.Lookup(owner, []byte{0x01}, common.BytesToHash([]byte("blob-one")))
	require.False(t, ok, "oldest generation must be evicted past maxGens")
	_, ok = ring.Lookup(owner, []byte{0x03}, common.BytesToHash([]byte("blob-three")))
	require.True(t, ok)
	require.Equal(t, 2, ring.Len())

	// Wrong expected hash at a live path is a structural miss.
	_, ok = ring.Lookup(owner, []byte{0x03}, common.BytesToHash([]byte("other")))
	require.False(t, ok)
}

func TestWarmNodeRing_DuplicateKeyRefreshSurvivesEviction(t *testing.T) {
	ring := newWarmNodeRing(2, 1<<30)
	owner := common.Hash{}
	shared := map[string][]byte{"\x0a": []byte("shared-blob")}

	ring.Add(mergedSet(t, owner, shared)) // gen 1
	ring.Add(mergedSet(t, owner, shared)) // gen 2 refreshes ownership

	// Evicting gen 1 must not drop the key gen 2 also produced.
	ring.Add(mergedSet(t, owner, map[string][]byte{"\x0b": []byte("newer")})) // gen 3, evicts gen 1
	_, ok := ring.Lookup(owner, []byte{0x0a}, common.BytesToHash([]byte("shared-blob")))
	require.True(t, ok, "key refreshed by a newer generation must survive old-generation eviction")

	// Once its owning generation (2) is evicted, the key goes away.
	ring.Add(mergedSet(t, owner, map[string][]byte{"\x0c": []byte("newest")})) // gen 4, evicts gen 2
	_, ok = ring.Lookup(owner, []byte{0x0a}, common.BytesToHash([]byte("shared-blob")))
	require.False(t, ok)
}

func TestWarmNodeRing_ByteBudgetEviction(t *testing.T) {
	// Budget fits roughly one 64-byte generation.
	ring := newWarmNodeRing(128, 100)
	owner := common.Hash{}
	blobA := make([]byte, 64)
	blobA[0] = 0xa1
	blobB := make([]byte, 64)
	blobB[0] = 0xb2

	ring.Add(mergedSet(t, owner, map[string][]byte{"\x01": blobA}))
	// Second generation pushes bytes over budget; the first is evicted, the
	// newest is always retained even if it alone exceeds the budget.
	ring.Add(mergedSet(t, owner, map[string][]byte{"\x02": blobB}))
	_, ok := ring.Lookup(owner, []byte{0x01}, common.BytesToHash(blobA))
	require.False(t, ok)
	_, ok = ring.Lookup(owner, []byte{0x02}, common.BytesToHash(blobB))
	require.True(t, ok)
	require.Equal(t, 1, ring.Len())
}

func TestWarmNodeRing_NilSafety(t *testing.T) {
	var ring *WarmNodeRing
	ring.Add(nil)
	require.Equal(t, 0, ring.Len())
	_, ok := ring.Lookup(common.Hash{}, []byte{0x01}, common.Hash{})
	require.False(t, ok)
}

// TestNewWithCommitSnapshot_RingChainRootParity chains three blocks through
// the witness-off SRC flow with a WarmNodeRing accumulating each commit's
// nodes, and checks every root matches the un-warmed baseline. Block 3
// touches an account last written by block 1, exercising depth > 1.
func TestNewWithCommitSnapshot_RingChainRootParity(t *testing.T) {
	dbWarm := NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), nil)
	dbBase := NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), nil)
	ring := NewWarmNodeRing()

	addrA := common.HexToAddress("0xdd01")
	addrB := common.HexToAddress("0xdd02")
	mutations := []func(s *StateDB){
		func(s *StateDB) {
			s.SetBalance(addrA, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
			s.SetState(addrA, common.HexToHash("0x01"), common.HexToHash("0x11"))
			s.SetBalance(addrB, uint256.NewInt(2), tracing.BalanceChangeUnspecified)
		},
		func(s *StateDB) {
			s.SetBalance(addrB, uint256.NewInt(20), tracing.BalanceChangeUnspecified)
			s.SetState(addrB, common.HexToHash("0x02"), common.HexToHash("0x22"))
		},
		func(s *StateDB) {
			// addrA was last written two blocks ago — a depth-2 ring hit.
			s.SetBalance(addrA, uint256.NewInt(100), tracing.BalanceChangeUnspecified)
			s.SetState(addrA, common.HexToHash("0x01"), common.HexToHash("0x33"))
		},
	}

	rootWarm, rootBase := types.EmptyRootHash, types.EmptyRootHash
	for i, mutate := range mutations {
		warm, err := NewWithCommitSnapshot(rootWarm, dbWarm, ring)
		require.NoError(t, err)
		mutate(warm)
		var update *stateUpdate
		rootWarm, update, err = warm.CommitWithUpdate(uint64(i+1), true, false)
		require.NoError(t, err)
		ring.Add(update.TrieNodes())

		base, err := New(rootBase, dbBase)
		require.NoError(t, err)
		mutate(base)
		rootBase, _, err = base.CommitWithUpdate(uint64(i+1), true, false)
		require.NoError(t, err)

		require.Equal(t, rootBase, rootWarm, "block %d: ring-warmed root must match baseline", i+1)
	}
	require.Positive(t, ring.Len())
}
