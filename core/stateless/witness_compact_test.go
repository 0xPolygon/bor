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
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestCompactWitness(t *testing.T) {
	// Create a test witness
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.HexToHash("0x1234"),
		Root:       common.HexToHash("0x5678"),
	}

	witness, err := NewWitness(header, nil)
	if err == nil { // Will fail without parent, but that's ok for this test
		t.Log("NewWitness created successfully")
	}

	if witness == nil {
		witness = &Witness{
			context: header,
			Headers: []*types.Header{header},
			Codes:   make(map[string]struct{}),
			State:   make(map[string]struct{}),
		}
	}

	// Add some state nodes
	witness.State["node1"] = struct{}{}
	witness.State["node2"] = struct{}{}
	witness.State["node3"] = struct{}{}
	witness.State["node4"] = struct{}{}

	// Create cached nodes (subset of witness state)
	cachedNodes := make(map[string]struct{})
	cachedNodes["node2"] = struct{}{}
	cachedNodes["node4"] = struct{}{}

	// Compact the witness
	compact, stats, err := CompactWitness(witness, cachedNodes)
	if err != nil {
		t.Fatalf("CompactWitness failed: %v", err)
	}

	// Verify stats
	if stats.OriginalNodes != 4 {
		t.Errorf("Expected OriginalNodes 4, got %d", stats.OriginalNodes)
	}

	if stats.RemovedNodes != 2 {
		t.Errorf("Expected RemovedNodes 2, got %d", stats.RemovedNodes)
	}

	if stats.CompactNodes != 2 {
		t.Errorf("Expected CompactNodes 2, got %d", stats.CompactNodes)
	}

	// Verify compact witness has correct nodes
	if len(compact.State) != 2 {
		t.Errorf("Expected compact witness to have 2 nodes, got %d", len(compact.State))
	}

	if _, exists := compact.State["node1"]; !exists {
		t.Error("node1 should be in compact witness")
	}

	if _, exists := compact.State["node3"]; !exists {
		t.Error("node3 should be in compact witness")
	}

	if _, exists := compact.State["node2"]; exists {
		t.Error("node2 should not be in compact witness (it was cached)")
	}

	if _, exists := compact.State["node4"]; exists {
		t.Error("node4 should not be in compact witness (it was cached)")
	}
}

func TestDecompactWitness(t *testing.T) {
	// Create a compact witness
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.HexToHash("0x1234"),
		Root:       common.HexToHash("0x5678"),
	}

	compact := &Witness{
		context: header,
		Headers: []*types.Header{header},
		Codes:   make(map[string]struct{}),
		State:   make(map[string]struct{}),
	}

	// Add non-cached nodes
	compact.State["node1"] = struct{}{}
	compact.State["node3"] = struct{}{}

	// Create cached nodes
	cachedNodes := make(map[string]struct{})
	cachedNodes["node2"] = struct{}{}
	cachedNodes["node4"] = struct{}{}

	// Decompress
	full, err := DecompactWitness(compact, cachedNodes)
	if err != nil {
		t.Fatalf("DecompactWitness failed: %v", err)
	}

	// Verify full witness has all nodes
	if len(full.State) != 4 {
		t.Errorf("Expected full witness to have 4 nodes, got %d", len(full.State))
	}

	expectedNodes := []string{"node1", "node2", "node3", "node4"}
	for _, node := range expectedNodes {
		if _, exists := full.State[node]; !exists {
			t.Errorf("Node %s should be in full witness", node)
		}
	}
}

func TestCompactDecompactRoundtrip(t *testing.T) {
	// Create original witness
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.HexToHash("0x1234"),
		Root:       common.HexToHash("0x5678"),
	}

	original := &Witness{
		context: header,
		Headers: []*types.Header{header},
		Codes:   make(map[string]struct{}),
		State:   make(map[string]struct{}),
	}

	// Add state nodes
	original.State["node1"] = struct{}{}
	original.State["node2"] = struct{}{}
	original.State["node3"] = struct{}{}
	original.State["node4"] = struct{}{}

	// Create cached nodes
	cachedNodes := make(map[string]struct{})
	cachedNodes["node2"] = struct{}{}
	cachedNodes["node4"] = struct{}{}

	// Compact
	compact, _, err := CompactWitness(original, cachedNodes)
	if err != nil {
		t.Fatalf("CompactWitness failed: %v", err)
	}

	// Decompress
	restored, err := DecompactWitness(compact, cachedNodes)
	if err != nil {
		t.Fatalf("DecompactWitness failed: %v", err)
	}

	// Verify restored witness matches original
	if len(restored.State) != len(original.State) {
		t.Errorf("Restored witness has different node count: expected %d, got %d",
			len(original.State), len(restored.State))
	}

	for node := range original.State {
		if _, exists := restored.State[node]; !exists {
			t.Errorf("Node %s missing from restored witness", node)
		}
	}
}

func TestCompressionRatio(t *testing.T) {
	stats := &CompactionStats{
		OriginalNodes: 100,
		RemovedNodes:  40,
		CompactNodes:  60,
	}

	ratio := stats.CompressionRatio()
	if ratio != 40.0 {
		t.Errorf("Expected compression ratio 40.0, got %f", ratio)
	}

	reduction := stats.SizeReduction()
	if reduction != 0.4 {
		t.Errorf("Expected size reduction 0.4, got %f", reduction)
	}
}

func TestEstimateCompactionBenefit(t *testing.T) {
	witnessState := make(map[string]struct{})
	witnessState["node1"] = struct{}{}
	witnessState["node2"] = struct{}{}
	witnessState["node3"] = struct{}{}
	witnessState["node4"] = struct{}{}
	witnessState["node5"] = struct{}{}

	cachedNodes := make(map[string]struct{})
	cachedNodes["node2"] = struct{}{}
	cachedNodes["node4"] = struct{}{}
	cachedNodes["node6"] = struct{}{}

	benefit := EstimateCompactionBenefit(witnessState, cachedNodes)

	// node2 and node4 are in both, so benefit is 2
	if benefit != 2 {
		t.Errorf("Expected benefit 2, got %d", benefit)
	}
}

func TestShouldCompact(t *testing.T) {
	witnessState := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		witnessState[string(rune(i))] = struct{}{}
	}

	cachedNodes := make(map[string]struct{})
	for i := 0; i < 50; i++ {
		cachedNodes[string(rune(i))] = struct{}{}
	}

	// 50% reduction, should compact with 10% threshold
	if !ShouldCompact(witnessState, cachedNodes, 10.0) {
		t.Error("Should compact with 50% reduction and 10% threshold")
	}

	// 50% reduction, should not compact with 60% threshold
	if ShouldCompact(witnessState, cachedNodes, 60.0) {
		t.Error("Should not compact with 50% reduction and 60% threshold")
	}
}

func TestCompactWitnessWithNilCache(t *testing.T) {
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.HexToHash("0x1234"),
		Root:       common.HexToHash("0x5678"),
	}

	witness := &Witness{
		context: header,
		Headers: []*types.Header{header},
		Codes:   make(map[string]struct{}),
		State:   make(map[string]struct{}),
	}

	witness.State["node1"] = struct{}{}
	witness.State["node2"] = struct{}{}

	// Compact with nil cache
	compact, stats, err := CompactWitness(witness, nil)
	if err != nil {
		t.Fatalf("CompactWitness failed with nil cache: %v", err)
	}

	// Should return witness unchanged
	if len(compact.State) != len(witness.State) {
		t.Error("Compact witness with nil cache should be unchanged")
	}

	if stats.RemovedNodes != 0 {
		t.Errorf("No nodes should be removed with nil cache, got %d", stats.RemovedNodes)
	}
}

func TestValidateDecompaction(t *testing.T) {
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.HexToHash("0x1234"),
		Root:       common.HexToHash("0x5678"),
	}

	compact := &Witness{
		context: header,
		Headers: []*types.Header{header},
		Codes:   make(map[string]struct{}),
		State:   make(map[string]struct{}),
	}
	compact.State["node1"] = struct{}{}

	full := &Witness{
		context: header,
		Headers: []*types.Header{header},
		Codes:   make(map[string]struct{}),
		State:   make(map[string]struct{}),
	}
	full.State["node1"] = struct{}{}
	full.State["node2"] = struct{}{}

	// Should pass validation
	if err := ValidateDecompaction(compact, full); err != nil {
		t.Errorf("Validation should pass: %v", err)
	}

	// Create invalid full witness (missing compact nodes)
	invalidFull := &Witness{
		context: header,
		Headers: []*types.Header{header},
		Codes:   make(map[string]struct{}),
		State:   make(map[string]struct{}),
	}
	invalidFull.State["node2"] = struct{}{} // Missing node1

	// Should fail validation
	if err := ValidateDecompaction(compact, invalidFull); err == nil {
		t.Error("Validation should fail for invalid decompaction")
	}
}
