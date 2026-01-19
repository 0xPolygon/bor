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
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/trie/trienode"
)

const (
	// DefaultWitnessCacheWindowSize is the number of blocks in each cache window.
	// This MUST be the same across all nodes to ensure deterministic caching.
	// DO NOT make this configurable - it would break network-wide cache consistency.
	DefaultWitnessCacheWindowSize = 20

	// DefaultWitnessCacheOverlap is the number of overlapping blocks between windows.
	// This MUST be the same across all nodes to ensure deterministic caching.
	// DO NOT make this configurable - it would break network-wide cache consistency.
	DefaultWitnessCacheOverlap = 10
)

// WitnessCacheState stores the state nodes for a single cache window.
// It maintains a map of all MPT trie nodes that have been witnessed
// during block execution within the window's block range.
type WitnessCacheState struct {
	StartBlock uint64              // First block number in this window
	EndBlock   uint64              // Last block number in this window (inclusive)
	Nodes      map[string]struct{} // Set of cached state node blobs (same format as witness.State)
	lock       sync.RWMutex        // Protects concurrent access to Nodes
}

// NewWitnessCacheState creates a new cache state for the given block range.
func NewWitnessCacheState(startBlock, endBlock uint64) *WitnessCacheState {
	return &WitnessCacheState{
		StartBlock: startBlock,
		EndBlock:   endBlock,
		Nodes:      make(map[string]struct{}),
	}
}

// PopulateFromMergedNodeSet adds nodes from a MergedNodeSet to the cache.
// This is called after block execution to cache the state nodes produced.
func (w *WitnessCacheState) PopulateFromMergedNodeSet(nodes *trienode.MergedNodeSet) {
	if nodes == nil {
		return
	}

	w.lock.Lock()
	defer w.lock.Unlock()

	// Iterate over all node sets (account trie + storage tries)
	for owner := range nodes.Sets {
		nodeSet := nodes.Sets[owner]
		if nodeSet == nil {
			continue
		}
		// Add all nodes from this set to the cache
		// Use ForEachWithOrder to ensure deterministic iteration
		nodeSet.ForEachWithOrder(func(path string, node *trienode.Node) {
			if node != nil && !node.IsDeleted() && node.Blob != nil {
				// Use the node blob as the key to match witness.State format
				w.Nodes[string(node.Blob)] = struct{}{}
			}
		})
	}
}

// PopulateFromWitnessState adds nodes from a witness state map to the cache.
// This is used when importing the first block in a window to cache incoming state.
func (w *WitnessCacheState) PopulateFromWitnessState(state map[string]struct{}) {
	if state == nil {
		return
	}

	w.lock.Lock()
	defer w.lock.Unlock()

	for node := range state {
		w.Nodes[node] = struct{}{}
	}
}

// Contains checks if a node key exists in the cache.
func (w *WitnessCacheState) Contains(key string) bool {
	w.lock.RLock()
	defer w.lock.RUnlock()

	_, exists := w.Nodes[key]
	return exists
}

// Size returns the number of nodes in the cache.
func (w *WitnessCacheState) Size() int {
	w.lock.RLock()
	defer w.lock.RUnlock()

	return len(w.Nodes)
}

// Hash computes a deterministic hash of the cache contents for verification.
// This is used to ensure all nodes have identical cache state.
func (w *WitnessCacheState) Hash() common.Hash {
	w.lock.RLock()
	defer w.lock.RUnlock()

	// Sort keys for deterministic hashing
	keys := make([]string, 0, len(w.Nodes))
	for key := range w.Nodes {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// Hash all keys together
	hasher := sha256.New()
	for _, key := range keys {
		hasher.Write([]byte(key))
	}

	var hash common.Hash
	copy(hash[:], hasher.Sum(nil))
	return hash
}

// Clear removes all nodes from the cache.
func (w *WitnessCacheState) Clear() {
	w.lock.Lock()
	defer w.lock.Unlock()

	w.Nodes = make(map[string]struct{})
}

// DualWitnessCache manages two overlapping cache windows for witness state.
// It maintains an active cache and a next cache with a configurable overlap.
type DualWitnessCache struct {
	Active *WitnessCacheState // Current active cache window
	Next   *WitnessCacheState // Next cache window (overlaps with active)

	WindowSize uint64 // Number of blocks in each window (default: 20)
	Overlap    uint64 // Number of overlapping blocks (default: 10)

	enabled            bool         // Whether the cache is enabled
	lastPopulatedBlock uint64       // Last block number that was populated into cache
	cacheComplete      bool         // Whether cache has been populated continuously since window start
	lock               sync.RWMutex // Protects cache state transitions
}

// alignToWindowBoundary calculates the deterministic starting block for a cache window.
// Windows are aligned to a grid based on (WindowSize - Overlap) to ensure all nodes
// have the same cache boundaries regardless of when they start.
//
// For example, with WindowSize=20 and Overlap=10:
// - Step size = 10
// - Windows start at: 0, 10, 20, 30, 40, ...
// - If currentBlock=15, it aligns to 10 (window [10-29])
// - If currentBlock=23, it aligns to 20 (window [20-39])
func (d *DualWitnessCache) alignToWindowBoundary(currentBlock uint64) uint64 {
	step := d.WindowSize - d.Overlap
	if step == 0 {
		step = d.WindowSize
	}
	// Align to the start of the current window
	return (currentBlock / step) * step
}

// NewDualWitnessCache creates a new dual witness cache with the specified parameters.
// The startBlock is aligned to deterministic window boundaries to ensure all nodes
// have identical cache state regardless of when they start.
func NewDualWitnessCache(windowSize, overlap uint64, startBlock uint64) *DualWitnessCache {
	if windowSize == 0 {
		windowSize = 20
	}
	if overlap == 0 || overlap >= windowSize {
		overlap = windowSize / 2
	}

	cache := &DualWitnessCache{
		WindowSize:         windowSize,
		Overlap:            overlap,
		enabled:            true,
		lastPopulatedBlock: 0,
		cacheComplete:      false, // Cache starts incomplete until first block in window is imported
	}

	// Align start block to deterministic window boundary
	alignedStart := cache.alignToWindowBoundary(startBlock)

	// Initialize both windows
	cache.Active = NewWitnessCacheState(alignedStart, alignedStart+windowSize-1)
	cache.Next = NewWitnessCacheState(alignedStart+windowSize-overlap, alignedStart+2*windowSize-overlap-1)

	return cache
}

// IsEnabled returns whether the cache is currently enabled.
func (d *DualWitnessCache) IsEnabled() bool {
	d.lock.RLock()
	defer d.lock.RUnlock()
	return d.enabled
}

// Disable disables the cache and clears all state.
func (d *DualWitnessCache) Disable() {
	d.lock.Lock()
	defer d.lock.Unlock()

	d.enabled = false
	d.cacheComplete = false
	d.lastPopulatedBlock = 0
	if d.Active != nil {
		d.Active.Clear()
	}
	if d.Next != nil {
		d.Next.Clear()
	}
}

// Enable enables the cache starting from the specified block.
// The start block is aligned to deterministic window boundaries.
// Cache starts incomplete and will become complete after importing blocks.
func (d *DualWitnessCache) Enable(startBlock uint64) {
	d.lock.Lock()
	defer d.lock.Unlock()

	d.enabled = true
	d.cacheComplete = false
	d.lastPopulatedBlock = 0

	// Align start block to deterministic window boundary
	alignedStart := d.alignToWindowBoundary(startBlock)

	d.Active = NewWitnessCacheState(alignedStart, alignedStart+d.WindowSize-1)
	d.Next = NewWitnessCacheState(alignedStart+d.WindowSize-d.Overlap, alignedStart+2*d.WindowSize-d.Overlap-1)
}

// PopulateFromStateUpdate adds nodes from a state update to the appropriate cache window(s).
// This is called after each block execution to incrementally build the cache.
// It also tracks cache completeness to determine when compact witnesses can be used.
func (d *DualWitnessCache) PopulateFromStateUpdate(blockNum uint64, nodes *trienode.MergedNodeSet) {
	d.lock.Lock()
	defer d.lock.Unlock()

	if !d.enabled || nodes == nil {
		return
	}

	// Add to active cache if block is in its range
	if d.Active != nil && blockNum >= d.Active.StartBlock && blockNum <= d.Active.EndBlock {
		d.Active.PopulateFromMergedNodeSet(nodes)

		// Track cache completeness for the active window
		// Cache becomes complete only when we've populated blocks continuously since the window started
		if d.lastPopulatedBlock == 0 {
			// First block populated in this cache instance
			d.lastPopulatedBlock = blockNum
			// Cache is complete only if we're starting from the window's first block
			if blockNum == d.Active.StartBlock {
				d.cacheComplete = true
			}
			// Otherwise cache is incomplete (restarted mid-window)
		} else if blockNum == d.lastPopulatedBlock+1 {
			// Continuous block sequence - maintain existing completeness state
			d.lastPopulatedBlock = blockNum
			// Don't set cacheComplete = true here!
			// Cache stays incomplete if it started incomplete (mid-window restart)
		} else if blockNum > d.lastPopulatedBlock+1 {
			// Gap detected - cache becomes incomplete
			d.cacheComplete = false
			d.lastPopulatedBlock = blockNum
		}
		// If blockNum < lastPopulatedBlock, it's a reorg - keep existing state
	}

	// Add to next cache if block is in its range
	if d.Next != nil && blockNum >= d.Next.StartBlock && blockNum <= d.Next.EndBlock {
		d.Next.PopulateFromMergedNodeSet(nodes)
	}
}

// PopulateFromWitness adds nodes from a witness to the appropriate cache window(s).
// This is used when importing blocks to cache incoming witness state.
func (d *DualWitnessCache) PopulateFromWitness(blockNum uint64, witness *Witness) {
	d.lock.RLock()
	defer d.lock.RUnlock()

	if !d.enabled || witness == nil {
		return
	}

	// Add to active cache if block is in its range
	if d.Active != nil && blockNum >= d.Active.StartBlock && blockNum <= d.Active.EndBlock {
		d.Active.PopulateFromWitnessState(witness.State)
	}

	// Add to next cache if block is in its range
	if d.Next != nil && blockNum >= d.Next.StartBlock && blockNum <= d.Next.EndBlock {
		d.Next.PopulateFromWitnessState(witness.State)
	}
}

// ShouldPromote checks if the cache windows should be promoted at the given block number.
// Promotion occurs when we reach the end of the active window.
func (d *DualWitnessCache) ShouldPromote(blockNum uint64) bool {
	d.lock.RLock()
	defer d.lock.RUnlock()

	if !d.enabled || d.Active == nil {
		return false
	}

	// Promote when we've finished the active window
	return blockNum >= d.Active.EndBlock
}

// Promote advances the cache windows: discard active, promote next to active, create new next.
// This should be called when ShouldPromote returns true.
// After promotion, the new active cache is marked complete since it was populated during overlap.
func (d *DualWitnessCache) Promote() error {
	d.lock.Lock()
	defer d.lock.Unlock()

	if !d.enabled {
		return fmt.Errorf("cache is disabled")
	}

	if d.Active == nil || d.Next == nil {
		return fmt.Errorf("cache not properly initialized")
	}

	// Discard active and promote next
	oldActive := d.Active
	d.Active = d.Next

	// Create new next window
	newStartBlock := d.Active.StartBlock + d.WindowSize - d.Overlap
	newEndBlock := newStartBlock + d.WindowSize - 1
	d.Next = NewWitnessCacheState(newStartBlock, newEndBlock)

	// Clear old active to free memory
	oldActive.Clear()

	// After promotion, the new active window is complete because it was populated
	// during the overlap period. We've been continuously importing blocks through
	// the entire overlap region, so the cache is ready for compact witnesses.
	d.cacheComplete = true
	// lastPopulatedBlock is already tracking correctly, no need to update

	return nil
}

// GetCachedNodes returns a map of all nodes currently in the active cache.
// This is used during witness compaction.
func (d *DualWitnessCache) GetCachedNodes() map[string]struct{} {
	d.lock.RLock()
	defer d.lock.RUnlock()

	if !d.enabled || d.Active == nil {
		return make(map[string]struct{})
	}

	d.Active.lock.RLock()
	defer d.Active.lock.RUnlock()

	// Return a copy to avoid concurrent access issues
	nodes := make(map[string]struct{}, len(d.Active.Nodes))
	for key := range d.Active.Nodes {
		nodes[key] = struct{}{}
	}

	return nodes
}

// IsValid checks if the cache is valid for the given block number.
// A cache is valid if the block falls within the active window.
func (d *DualWitnessCache) IsValid(blockNum uint64) bool {
	d.lock.RLock()
	defer d.lock.RUnlock()

	if !d.enabled || d.Active == nil {
		return false
	}

	return blockNum >= d.Active.StartBlock && blockNum <= d.Active.EndBlock
}

// IsCacheComplete checks if the cache has been populated continuously since the window started.
// This is used to determine if compact witnesses can be safely used.
// Returns false if:
// - Cache is disabled
// - Node restarted mid-window (missing earlier blocks in window)
// - There was a gap in block imports
// Returns true if:
// - Node started importing from the window's first block
// - Node promoted from a previous complete window (overlap region populated)
func (d *DualWitnessCache) IsCacheComplete() bool {
	d.lock.RLock()
	defer d.lock.RUnlock()

	if !d.enabled || d.Active == nil {
		return false
	}

	return d.cacheComplete
}

// Reset clears the cache and reinitializes it from the given start block.
// This is used during reorgs that affect cached blocks.
// The start block is aligned to deterministic window boundaries.
// Cache becomes incomplete after reset and needs to be repopulated.
func (d *DualWitnessCache) Reset(startBlock uint64) {
	d.lock.Lock()
	defer d.lock.Unlock()

	if d.Active != nil {
		d.Active.Clear()
	}
	if d.Next != nil {
		d.Next.Clear()
	}

	d.cacheComplete = false
	d.lastPopulatedBlock = 0

	// Align start block to deterministic window boundary
	alignedStart := d.alignToWindowBoundary(startBlock)

	d.Active = NewWitnessCacheState(alignedStart, alignedStart+d.WindowSize-1)
	d.Next = NewWitnessCacheState(alignedStart+d.WindowSize-d.Overlap, alignedStart+2*d.WindowSize-d.Overlap-1)
}

// Stats returns statistics about the cache state.
type CacheStats struct {
	Enabled          bool
	ActiveStartBlock uint64
	ActiveEndBlock   uint64
	ActiveSize       int
	NextStartBlock   uint64
	NextEndBlock     uint64
	NextSize         int
	WindowSize       uint64
	Overlap          uint64
}

// Stats returns current cache statistics.
func (d *DualWitnessCache) Stats() CacheStats {
	d.lock.RLock()
	defer d.lock.RUnlock()

	stats := CacheStats{
		Enabled:    d.enabled,
		WindowSize: d.WindowSize,
		Overlap:    d.Overlap,
	}

	if d.Active != nil {
		stats.ActiveStartBlock = d.Active.StartBlock
		stats.ActiveEndBlock = d.Active.EndBlock
		stats.ActiveSize = d.Active.Size()
	}

	if d.Next != nil {
		stats.NextStartBlock = d.Next.StartBlock
		stats.NextEndBlock = d.Next.EndBlock
		stats.NextSize = d.Next.Size()
	}

	return stats
}

// ComputeHash computes a deterministic hash representing the entire cache state.
// This can be used to verify cache consistency across nodes.
func (d *DualWitnessCache) ComputeHash() common.Hash {
	d.lock.RLock()
	defer d.lock.RUnlock()

	hasher := sha256.New()

	// Write window configuration
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], d.WindowSize)
	hasher.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], d.Overlap)
	hasher.Write(buf[:])

	// Write active cache hash
	if d.Active != nil {
		activeHash := d.Active.Hash()
		hasher.Write(activeHash[:])
	}

	// Write next cache hash
	if d.Next != nil {
		nextHash := d.Next.Hash()
		hasher.Write(nextHash[:])
	}

	var hash common.Hash
	copy(hash[:], hasher.Sum(nil))
	return hash
}
