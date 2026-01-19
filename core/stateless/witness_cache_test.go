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

package stateless

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

func TestDualWitnessCacheCreation(t *testing.T) {
	cache := NewDualWitnessCache(20, 10, 100)

	if cache == nil {
		t.Fatal("NewDualWitnessCache returned nil")
	}

	if cache.WindowSize != 20 {
		t.Errorf("Expected WindowSize 20, got %d", cache.WindowSize)
	}

	if cache.Overlap != 10 {
		t.Errorf("Expected Overlap 10, got %d", cache.Overlap)
	}

	if cache.Active == nil {
		t.Fatal("Active cache not initialized")
	}

	if cache.Next == nil {
		t.Fatal("Next cache not initialized")
	}

	if cache.Active.StartBlock != 100 {
		t.Errorf("Expected Active StartBlock 100, got %d", cache.Active.StartBlock)
	}

	if cache.Active.EndBlock != 119 {
		t.Errorf("Expected Active EndBlock 119, got %d", cache.Active.EndBlock)
	}

	if cache.Next.StartBlock != 110 {
		t.Errorf("Expected Next StartBlock 110, got %d", cache.Next.StartBlock)
	}

	if cache.Next.EndBlock != 129 {
		t.Errorf("Expected Next EndBlock 129, got %d", cache.Next.EndBlock)
	}
}

func TestWitnessCacheStatePopulation(t *testing.T) {
	cache := NewWitnessCacheState(100, 119)

	// Create a merged node set with some test nodes
	mergedSet := trienode.NewMergedNodeSet()
	nodeSet := trienode.NewNodeSet(common.Hash{})

	// Add some nodes
	node1 := trienode.New(common.HexToHash("0x1234"), []byte("data1"))
	node2 := trienode.New(common.HexToHash("0x5678"), []byte("data2"))

	nodeSet.AddNode([]byte("path1"), node1)
	nodeSet.AddNode([]byte("path2"), node2)

	mergedSet.Merge(nodeSet)

	// Populate cache
	cache.PopulateFromMergedNodeSet(mergedSet)

	// Check that nodes were added
	if cache.Size() != 2 {
		t.Errorf("Expected cache size 2, got %d", cache.Size())
	}

	// Check that specific nodes are present
	blob1 := string(node1.Blob)
	blob2 := string(node2.Blob)

	if !cache.Contains(blob1) {
		t.Error("Node 1 not found in cache")
	}

	if !cache.Contains(blob2) {
		t.Error("Node 2 not found in cache")
	}
}

func TestCachePromotion(t *testing.T) {
	cache := NewDualWitnessCache(20, 10, 100)

	// Should not promote at block 100
	if cache.ShouldPromote(100) {
		t.Error("Should not promote at block 100")
	}

	// Should not promote at block 118
	if cache.ShouldPromote(118) {
		t.Error("Should not promote at block 118")
	}

	// Should promote at block 119 (end of active window)
	if !cache.ShouldPromote(119) {
		t.Error("Should promote at block 119")
	}

	// Promote
	if err := cache.Promote(); err != nil {
		t.Fatalf("Promotion failed: %v", err)
	}

	// Check new window boundaries
	if cache.Active.StartBlock != 110 {
		t.Errorf("After promotion, expected Active StartBlock 110, got %d", cache.Active.StartBlock)
	}

	if cache.Active.EndBlock != 129 {
		t.Errorf("After promotion, expected Active EndBlock 129, got %d", cache.Active.EndBlock)
	}

	if cache.Next.StartBlock != 120 {
		t.Errorf("After promotion, expected Next StartBlock 120, got %d", cache.Next.StartBlock)
	}

	if cache.Next.EndBlock != 139 {
		t.Errorf("After promotion, expected Next EndBlock 139, got %d", cache.Next.EndBlock)
	}
}

func TestCacheEnableDisable(t *testing.T) {
	cache := NewDualWitnessCache(20, 10, 100)

	if !cache.IsEnabled() {
		t.Error("Cache should be enabled by default")
	}

	cache.Disable()

	if cache.IsEnabled() {
		t.Error("Cache should be disabled after Disable()")
	}

	cache.Enable(200)

	if !cache.IsEnabled() {
		t.Error("Cache should be enabled after Enable()")
	}

	if cache.Active.StartBlock != 200 {
		t.Errorf("After re-enable, expected Active StartBlock 200, got %d", cache.Active.StartBlock)
	}
}

func TestCacheReset(t *testing.T) {
	cache := NewDualWitnessCache(20, 10, 100)

	// Populate some data
	mergedSet := trienode.NewMergedNodeSet()
	nodeSet := trienode.NewNodeSet(common.Hash{})
	node := trienode.New(common.HexToHash("0x1234"), []byte("data"))
	nodeSet.AddNode([]byte("path"), node)
	mergedSet.Merge(nodeSet)

	cache.PopulateFromStateUpdate(100, mergedSet)

	if cache.Active.Size() != 1 {
		t.Error("Cache should have 1 node before reset")
	}

	// Reset to new block
	cache.Reset(300)

	if cache.Active.Size() != 0 {
		t.Error("Cache should be empty after reset")
	}

	if cache.Active.StartBlock != 300 {
		t.Errorf("After reset, expected Active StartBlock 300, got %d", cache.Active.StartBlock)
	}
}

func TestCacheStats(t *testing.T) {
	cache := NewDualWitnessCache(20, 10, 100)

	stats := cache.Stats()

	if !stats.Enabled {
		t.Error("Stats should show cache as enabled")
	}

	if stats.WindowSize != 20 {
		t.Errorf("Stats WindowSize should be 20, got %d", stats.WindowSize)
	}

	if stats.Overlap != 10 {
		t.Errorf("Stats Overlap should be 10, got %d", stats.Overlap)
	}

	if stats.ActiveStartBlock != 100 {
		t.Errorf("Stats ActiveStartBlock should be 100, got %d", stats.ActiveStartBlock)
	}
}

func TestCacheDeterministicHash(t *testing.T) {
	// Create two caches with same configuration
	cache1 := NewDualWitnessCache(20, 10, 100)
	cache2 := NewDualWitnessCache(20, 10, 100)

	// Populate both with same data
	mergedSet := trienode.NewMergedNodeSet()
	nodeSet := trienode.NewNodeSet(common.Hash{})

	node1 := trienode.New(common.HexToHash("0x1234"), []byte("data1"))
	node2 := trienode.New(common.HexToHash("0x5678"), []byte("data2"))

	nodeSet.AddNode([]byte("path1"), node1)
	nodeSet.AddNode([]byte("path2"), node2)
	mergedSet.Merge(nodeSet)

	cache1.PopulateFromStateUpdate(100, mergedSet)
	cache2.PopulateFromStateUpdate(100, mergedSet)

	// Hashes should be identical
	hash1 := cache1.ComputeHash()
	hash2 := cache2.ComputeHash()

	if hash1 != hash2 {
		t.Errorf("Deterministic hashes should match, got %x and %x", hash1, hash2)
	}
}

func TestCacheWindowAlignment(t *testing.T) {
	// Test that cache windows are aligned to deterministic boundaries
	// regardless of when nodes start

	// Window size = 20, overlap = 10, step = 10
	// Windows should start at: 0, 10, 20, 30, 40, ...

	testCases := []struct {
		startBlock          uint64
		expectedActiveStart uint64
		expectedActiveEnd   uint64
		expectedNextStart   uint64
		expectedNextEnd     uint64
	}{
		{0, 0, 19, 10, 29},      // Start at 0
		{5, 0, 19, 10, 29},      // Start at 5 -> aligns to 0
		{10, 10, 29, 20, 39},    // Start at 10
		{15, 10, 29, 20, 39},    // Start at 15 -> aligns to 10
		{20, 20, 39, 30, 49},    // Start at 20
		{23, 20, 39, 30, 49},    // Start at 23 -> aligns to 20
		{30, 30, 49, 40, 59},    // Start at 30
		{99, 90, 109, 100, 119}, // Start at 99 -> aligns to 90
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("startBlock=%d", tc.startBlock), func(t *testing.T) {
			cache := NewDualWitnessCache(20, 10, tc.startBlock)

			if cache.Active.StartBlock != tc.expectedActiveStart {
				t.Errorf("Active StartBlock: expected %d, got %d",
					tc.expectedActiveStart, cache.Active.StartBlock)
			}
			if cache.Active.EndBlock != tc.expectedActiveEnd {
				t.Errorf("Active EndBlock: expected %d, got %d",
					tc.expectedActiveEnd, cache.Active.EndBlock)
			}
			if cache.Next.StartBlock != tc.expectedNextStart {
				t.Errorf("Next StartBlock: expected %d, got %d",
					tc.expectedNextStart, cache.Next.StartBlock)
			}
			if cache.Next.EndBlock != tc.expectedNextEnd {
				t.Errorf("Next EndBlock: expected %d, got %d",
					tc.expectedNextEnd, cache.Next.EndBlock)
			}
		})
	}
}

func TestCacheDeterminismAcrossRestarts(t *testing.T) {
	// Simulate two nodes restarting at different blocks
	// They should end up with the same cache windows after alignment

	// Node A restarts at block 10
	cacheA := NewDualWitnessCache(20, 10, 10)

	// Node B restarts at block 15
	cacheB := NewDualWitnessCache(20, 10, 15)

	// Both should have aligned to block 10
	if cacheA.Active.StartBlock != 10 {
		t.Errorf("Node A: expected Active StartBlock 10, got %d", cacheA.Active.StartBlock)
	}
	if cacheB.Active.StartBlock != 10 {
		t.Errorf("Node B: expected Active StartBlock 10, got %d", cacheB.Active.StartBlock)
	}

	// Both should have the same window boundaries
	if cacheA.Active.StartBlock != cacheB.Active.StartBlock {
		t.Errorf("Active StartBlock mismatch: A=%d, B=%d",
			cacheA.Active.StartBlock, cacheB.Active.StartBlock)
	}
	if cacheA.Active.EndBlock != cacheB.Active.EndBlock {
		t.Errorf("Active EndBlock mismatch: A=%d, B=%d",
			cacheA.Active.EndBlock, cacheB.Active.EndBlock)
	}
	if cacheA.Next.StartBlock != cacheB.Next.StartBlock {
		t.Errorf("Next StartBlock mismatch: A=%d, B=%d",
			cacheA.Next.StartBlock, cacheB.Next.StartBlock)
	}
	if cacheA.Next.EndBlock != cacheB.Next.EndBlock {
		t.Errorf("Next EndBlock mismatch: A=%d, B=%d",
			cacheA.Next.EndBlock, cacheB.Next.EndBlock)
	}

	// Now both nodes process blocks 10-19 with the same data
	// They should end up with identical cache contents
	for blockNum := uint64(10); blockNum < 20; blockNum++ {
		mergedSet := trienode.NewMergedNodeSet()
		nodeSet := trienode.NewNodeSet(common.Hash{})

		// Add some deterministic test data
		node := trienode.New(common.Hash{byte(blockNum)}, []byte(fmt.Sprintf("block%d", blockNum)))
		nodeSet.AddNode([]byte(fmt.Sprintf("path%d", blockNum)), node)
		mergedSet.Merge(nodeSet)

		cacheA.PopulateFromStateUpdate(blockNum, mergedSet)
		cacheB.PopulateFromStateUpdate(blockNum, mergedSet)
	}

	// Verify both caches have the same content
	if cacheA.Active.Size() != cacheB.Active.Size() {
		t.Errorf("Cache size mismatch: A=%d, B=%d",
			cacheA.Active.Size(), cacheB.Active.Size())
	}

	// Verify hashes match
	hashA := cacheA.ComputeHash()
	hashB := cacheB.ComputeHash()
	if hashA != hashB {
		t.Errorf("Cache hashes don't match: A=%x, B=%x", hashA, hashB)
	}
}

func TestCacheEnableAlignment(t *testing.T) {
	// Test that Enable also aligns to boundaries
	cache := NewDualWitnessCache(20, 10, 0)
	cache.Disable()

	// Enable at block 15
	cache.Enable(15)

	// Should align to block 10
	if cache.Active.StartBlock != 10 {
		t.Errorf("After Enable(15), expected Active StartBlock 10, got %d",
			cache.Active.StartBlock)
	}
}

func TestCacheResetAlignment(t *testing.T) {
	// Test that Reset also aligns to boundaries
	cache := NewDualWitnessCache(20, 10, 0)

	// Reset to block 27
	cache.Reset(27)

	// Should align to block 20
	if cache.Active.StartBlock != 20 {
		t.Errorf("After Reset(27), expected Active StartBlock 20, got %d",
			cache.Active.StartBlock)
	}
	if cache.Active.EndBlock != 39 {
		t.Errorf("After Reset(27), expected Active EndBlock 39, got %d",
			cache.Active.EndBlock)
	}
}

func TestIsCacheComplete(t *testing.T) {
	// Test IsCacheComplete behavior for cache warmup scenarios

	// Scenario 1: Starting from window start (block 100)
	cache := NewDualWitnessCache(20, 10, 100)
	if cache.IsCacheComplete() {
		t.Error("Cache should not be complete immediately after creation")
	}

	// Populate first block (window start)
	mergedSet := trienode.NewMergedNodeSet()
	nodeSet := trienode.NewNodeSet(common.Hash{})
	node := trienode.New(common.HexToHash("0x1234"), []byte("data"))
	nodeSet.AddNode([]byte("path"), node)
	mergedSet.Merge(nodeSet)

	cache.PopulateFromStateUpdate(100, mergedSet) // Block 100 is window start

	// Cache should be complete after populating window start
	if !cache.IsCacheComplete() {
		t.Error("Cache should be complete after populating window start block")
	}

	// Scenario 2: Restarting mid-window (block 105)
	cache2 := NewDualWitnessCache(20, 10, 105)
	if cache2.IsCacheComplete() {
		t.Error("Cache should not be complete after mid-window restart")
	}

	// Populate blocks 105, 106, 107...
	for blockNum := uint64(105); blockNum < 110; blockNum++ {
		mergedSet := trienode.NewMergedNodeSet()
		nodeSet := trienode.NewNodeSet(common.Hash{})
		node := trienode.New(common.Hash{byte(blockNum)}, []byte(fmt.Sprintf("block%d", blockNum)))
		nodeSet.AddNode([]byte(fmt.Sprintf("path%d", blockNum)), node)
		mergedSet.Merge(nodeSet)
		cache2.PopulateFromStateUpdate(blockNum, mergedSet)
	}

	// Cache should still be incomplete (started mid-window at 105, window starts at 100)
	if cache2.IsCacheComplete() {
		t.Error("Cache should remain incomplete when started mid-window")
	}

	// Reset cache - should mark incomplete
	cache.Reset(200)
	if cache.IsCacheComplete() {
		t.Error("Cache should not be complete after reset")
	}

	// Disable cache - should return false
	cache.Disable()
	if cache.IsCacheComplete() {
		t.Error("Disabled cache should not be complete")
	}
}

func TestCacheWarmupScenario(t *testing.T) {
	// Simulate a node restarting 1000 blocks behind (stopped at 1003, syncing from 1004)
	// Node was at block 1003, cache aligns to 1000, active window [1000-1019]
	// Cache should start incomplete and remain incomplete until window promotion

	cache := NewDualWitnessCache(20, 10, 1003) // Aligned to 1000, but starting at block 1003

	// Block 1004: Cache is valid but INCOMPLETE (should use FULL witness)
	if !cache.IsValid(1004) {
		t.Error("Cache should be valid for block 1004")
	}
	if cache.IsCacheComplete() {
		t.Error("Cache should be incomplete - restarted mid-window")
	}

	// Simulate importing blocks 1004-1019 (rest of current window)
	for blockNum := uint64(1004); blockNum <= 1019; blockNum++ {
		mergedSet := trienode.NewMergedNodeSet()
		nodeSet := trienode.NewNodeSet(common.Hash{})
		node := trienode.New(common.Hash{byte(blockNum)}, []byte(fmt.Sprintf("block%d", blockNum)))
		nodeSet.AddNode([]byte(fmt.Sprintf("path%d", blockNum)), node)
		mergedSet.Merge(nodeSet)

		cache.PopulateFromStateUpdate(blockNum, mergedSet)
	}

	// After importing blocks 1004-1019, cache still INCOMPLETE (missing 1000-1003)
	if cache.IsCacheComplete() {
		t.Error("Cache should remain incomplete - missing blocks 1000-1003")
	}

	// Promote to next window when reaching end of active window
	if !cache.ShouldPromote(1019) {
		t.Error("Should promote at block 1019")
	}
	if err := cache.Promote(); err != nil {
		t.Fatalf("Failed to promote: %v", err)
	}

	// After promotion: Active window is now [1010-1029]
	// This window was fully populated during overlap (blocks 1010-1019)
	// Cache is now COMPLETE (should use COMPACT witness)
	if cache.Active.StartBlock != 1010 {
		t.Errorf("After promotion, Active should start at 1010, got %d", cache.Active.StartBlock)
	}
	if !cache.IsCacheComplete() {
		t.Error("Cache should be complete after promotion - overlap was fully populated")
	}

	// Verify cache has nodes from the overlap region
	if cache.Active.Size() != 10 {
		t.Errorf("Cache should have 10 nodes from overlap [1010-1019], got %d", cache.Active.Size())
	}
}
