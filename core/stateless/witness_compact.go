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
)

// CompactWitness creates a compact witness by removing cached state nodes from the full witness.
// It returns the compact witness and statistics about the compaction.
func CompactWitness(witness *Witness, cachedNodes map[string]struct{}) (*Witness, *CompactionStats, error) {
	if witness == nil {
		return nil, nil, fmt.Errorf("witness is nil")
	}

	if cachedNodes == nil || len(cachedNodes) == 0 {
		// No cached nodes, return the original witness
		return witness, &CompactionStats{
			OriginalNodes: len(witness.State),
			RemovedNodes:  0,
			CompactNodes:  len(witness.State),
		}, nil
	}

	// Create a copy of the witness
	compact := witness.Copy()

	// Track removed nodes
	removed := 0

	// Remove cached nodes from the compact witness state
	for key := range witness.State {
		if _, cached := cachedNodes[key]; cached {
			delete(compact.State, key)
			removed++
		}
	}

	stats := &CompactionStats{
		OriginalNodes: len(witness.State),
		RemovedNodes:  removed,
		CompactNodes:  len(compact.State),
	}

	return compact, stats, nil
}

// DecompactWitness reconstructs a full witness by merging a compact witness with cached nodes.
// This is called before witness execution to restore the complete state.
func DecompactWitness(compact *Witness, cachedNodes map[string]struct{}) (*Witness, error) {
	if compact == nil {
		return nil, fmt.Errorf("compact witness is nil")
	}

	if cachedNodes == nil || len(cachedNodes) == 0 {
		// No cached nodes, return the compact witness as-is
		return compact, nil
	}

	// Create a copy of the compact witness
	full := compact.Copy()

	// Add all cached nodes to the witness state
	for key := range cachedNodes {
		full.State[key] = struct{}{}
	}

	return full, nil
}

// CompactionStats holds statistics about witness compaction.
type CompactionStats struct {
	OriginalNodes int // Number of nodes in original witness
	RemovedNodes  int // Number of nodes removed (cached)
	CompactNodes  int // Number of nodes in compact witness
}

// CompressionRatio returns the compression ratio as a percentage (0-100).
// Higher values indicate better compression.
func (c *CompactionStats) CompressionRatio() float64 {
	if c.OriginalNodes == 0 {
		return 0
	}
	return float64(c.RemovedNodes) * 100.0 / float64(c.OriginalNodes)
}

// SizeReduction returns the size reduction as a fraction (0.0-1.0).
// This represents the proportion of nodes removed.
func (c *CompactionStats) SizeReduction() float64 {
	if c.OriginalNodes == 0 {
		return 0
	}
	return float64(c.RemovedNodes) / float64(c.OriginalNodes)
}

// String returns a human-readable representation of the stats.
func (c *CompactionStats) String() string {
	return fmt.Sprintf("Compaction: %d -> %d nodes (-%d, %.1f%% reduction)",
		c.OriginalNodes, c.CompactNodes, c.RemovedNodes, c.CompressionRatio())
}

// ValidateDecompaction verifies that decompaction produced a valid witness.
// It checks that the decompacted witness has at least as many nodes as the compact witness.
func ValidateDecompaction(compact, full *Witness) error {
	if compact == nil {
		return fmt.Errorf("compact witness is nil")
	}
	if full == nil {
		return fmt.Errorf("decompacted witness is nil")
	}

	compactSize := len(compact.State)
	fullSize := len(full.State)

	if fullSize < compactSize {
		return fmt.Errorf("decompacted witness has fewer nodes (%d) than compact witness (%d)",
			fullSize, compactSize)
	}

	// Verify all compact witness nodes are present in the full witness
	for key := range compact.State {
		if _, exists := full.State[key]; !exists {
			return fmt.Errorf("compact witness node missing in decompacted witness")
		}
	}

	return nil
}

// EstimateCompactionBenefit estimates the potential benefit of compaction.
// Returns the estimated number of nodes that would be removed.
func EstimateCompactionBenefit(witnessState map[string]struct{}, cachedNodes map[string]struct{}) int {
	if witnessState == nil || cachedNodes == nil {
		return 0
	}

	overlap := 0
	for key := range witnessState {
		if _, cached := cachedNodes[key]; cached {
			overlap++
		}
	}

	return overlap
}

// ShouldCompact determines whether compaction would be beneficial.
// Returns true if the compression ratio would exceed the threshold (e.g., 10% reduction).
func ShouldCompact(witnessState map[string]struct{}, cachedNodes map[string]struct{}, minReductionPct float64) bool {
	if len(witnessState) == 0 {
		return false
	}

	removedNodes := EstimateCompactionBenefit(witnessState, cachedNodes)
	reductionPct := float64(removedNodes) * 100.0 / float64(len(witnessState))

	return reductionPct >= minReductionPct
}
