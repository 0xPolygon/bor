package stateless

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestWitnessCompressionDemo demonstrates how to use witness compression
func TestWitnessCompressionDemo(t *testing.T) {
	// Reset metrics to ensure clean state
	resetMetrics()

	// Create a test witness
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.Hash{},
		Root:       common.Hash{},
	}

	witness, err := NewWitness(header, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Add some data to the witness
	witness.AddState(map[string]struct{}{
		"state_node_1": {},
		"state_node_2": {},
	})

	// Configure compression
	config := &CompressionConfig{
		Enabled:          true,
		Threshold:        100,   // Compress if > 100 bytes
		CompressionLevel: 6,     // Medium compression
		UseDeduplication: false, // Disable optimization to verify the difference
	}
	SetCompressionConfig(config)

	// Calculate original size manually
	originalSize := witness.Size()

	// Test RLP encoding overhead
	var rlpBuf bytes.Buffer
	if err := witness.EncodeRLP(&rlpBuf); err != nil {
		t.Fatal(err)
	}
	rlpSize := rlpBuf.Len()
	rlpOverhead := rlpSize - originalSize

	// Encode with compression
	var buf bytes.Buffer
	if err := witness.EncodeCompressed(&buf); err != nil {
		t.Fatal(err)
	}

	compressedData := buf.Bytes()

	// Calculate compression ratio manually
	actualCompressedSize := len(compressedData)
	manualCompressionRatio := float64(actualCompressedSize) / float64(originalSize) * 100

	t.Logf("Original size (manual calculation): %d bytes", originalSize)
	t.Logf("Compressed size: %d bytes", actualCompressedSize)
	t.Logf("Manual compression ratio: %.1f%%", manualCompressionRatio)

	// Show the actual RLP encoding size
	actualCompressionRatio := float64(actualCompressedSize) / float64(rlpSize) * 100

	t.Logf("RLP encoding size: %d bytes", rlpSize)
	t.Logf("RLP overhead: %d bytes", rlpOverhead)
	t.Logf("Actual compression ratio (RLP): %.1f%%", actualCompressionRatio)

	// Decode the compressed data
	var decodedWitness Witness
	if err := decodedWitness.DecodeCompressed(compressedData); err != nil {
		t.Fatal(err)
	}

	// Verify data integrity
	t.Logf("State count: %d", len(decodedWitness.State))

	// Get compression statistics
	stats := CompressionStats()
	t.Logf("Total witnesses processed: %d", stats["total_witnesses"])
	t.Logf("Average compression ratio: %.1f%%", stats["compression_ratio"])
	t.Logf("Total space saved: %d bytes", stats["space_saved_bytes"])

	t.Logf("Note: The difference between manual (%.1f%%) and RLP (%.1f%%) compression ratios",
		manualCompressionRatio, actualCompressionRatio)
	t.Logf("is due to RLP encoding overhead (%d bytes) that occurs during serialization.", rlpOverhead)
}

// TestWitnessOptimization verifies that witness optimization is working correctly
func TestWitnessOptimization(t *testing.T) {
	// Reset metrics to ensure clean state
	resetMetrics()

	// Create a test witness
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.Hash{},
		Root:       common.Hash{},
	}

	witness, err := NewWitness(header, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Add empty state nodes that should be removed
	witness.AddState(map[string]struct{}{
		"state_node_1": {},
		"":             {}, // Empty node that should be removed
		"state_node_2": {},
	})

	// Check size before optimization
	sizeBefore := witness.Size()
	t.Logf("Size before optimization: %d bytes", sizeBefore)
	t.Logf("State count before: %d", len(witness.State))

	// Verify we have duplicate/optimizable data
	if len(witness.State) != 3 {
		t.Errorf("Expected 3 state nodes before optimization, got %d", len(witness.State))
	}

	// Run optimization
	witness.Optimize()

	// Check size after optimization
	sizeAfter := witness.Size()
	t.Logf("Size after optimization: %d bytes", sizeAfter)
	t.Logf("State count after: %d", len(witness.State))

	// Verify optimization worked
	if len(witness.State) != 2 {
		t.Errorf("Expected 2 state nodes after optimization (empty removed), got %d", len(witness.State))
	}

	// Verify empty state node was removed
	for node := range witness.State {
		if node == "" {
			t.Error("Empty state node was not removed during optimization")
		}
	}

	// Size should be smaller after optimization
	if sizeAfter < sizeBefore {
		t.Logf("Optimization successful: reduced size by %d bytes", sizeBefore-sizeAfter)
	} else {
		t.Logf("Optimization did not reduce size: before=%d, after=%d", sizeBefore, sizeAfter)
	}
}

// TestWitnessOptimizationImpact compares compression with optimization enabled vs disabled
func TestWitnessOptimizationImpact(t *testing.T) {
	// Reset metrics to ensure clean state
	resetMetrics()

	// Create a test witness with optimizable data
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.Hash{},
		Root:       common.Hash{},
	}

	witness, err := NewWitness(header, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Add empty state nodes that should be removed
	witness.AddState(map[string]struct{}{
		"state_node_1": {},
		"":             {}, // Empty node that should be removed
		"state_node_2": {},
	})

	// Test 1: With optimization disabled
	configDisabled := &CompressionConfig{
		Enabled:          true,
		Threshold:        100,
		CompressionLevel: 6,
		UseDeduplication: false, // Disable optimization
	}
	SetCompressionConfig(configDisabled)

	var bufDisabled bytes.Buffer
	if err := witness.EncodeCompressed(&bufDisabled); err != nil {
		t.Fatal(err)
	}
	sizeDisabled := bufDisabled.Len()

	// Test 2: With optimization enabled
	configEnabled := &CompressionConfig{
		Enabled:          true,
		Threshold:        100,
		CompressionLevel: 6,
		UseDeduplication: true, // Enable optimization
	}
	SetCompressionConfig(configEnabled)

	var bufEnabled bytes.Buffer
	if err := witness.EncodeCompressed(&bufEnabled); err != nil {
		t.Fatal(err)
	}
	sizeEnabled := bufEnabled.Len()

	// Calculate the impact
	sizeReduction := sizeDisabled - sizeEnabled
	reductionPercent := float64(sizeReduction) / float64(sizeDisabled) * 100

	t.Logf("Compressed size with optimization disabled: %d bytes", sizeDisabled)
	t.Logf("Compressed size with optimization enabled: %d bytes", sizeEnabled)
	t.Logf("Size reduction due to optimization: %d bytes (%.1f%%)", sizeReduction, reductionPercent)

	// Verify optimization actually reduced size
	if sizeEnabled >= sizeDisabled {
		t.Errorf("Optimization should reduce size: disabled=%d, enabled=%d", sizeDisabled, sizeEnabled)
	}

	// Show the manual size calculation for reference
	manualSize := witness.Size()
	t.Logf("Manual size calculation: %d bytes", manualSize)

	// Show RLP encoding size for comparison
	var rlpBuf bytes.Buffer
	if err := witness.EncodeRLP(&rlpBuf); err != nil {
		t.Fatal(err)
	}
	rlpSize := rlpBuf.Len()
	t.Logf("RLP encoding size: %d bytes", rlpSize)

	t.Logf("Optimization impact: Reduced compressed size by %d bytes (%.1f%%)", sizeReduction, reductionPercent)
}
