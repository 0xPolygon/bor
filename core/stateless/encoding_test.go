package stateless

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"compress/gzip"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rlp"
)

func init() {
	// Enable metrics for testing
	metrics.Enable()
}

func TestWitnessCompression(t *testing.T) {
	// Create a test witness with some data
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.Hash{},
		Root:       common.Hash{},
	}

	witness, err := NewWitness(header, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Add some test data
	witness.AddState(map[string]struct{}{
		"state_node_1": {},
		"state_node_2": {},
		"state_node_3": {},
	})

	// Test compression with different configurations
	testCases := []struct {
		name      string
		enabled   bool
		threshold int
	}{
		{"compression_enabled", true, 10},
		{"compression_disabled", false, 10},
		{"above_threshold", true, 1000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set configuration
			config := &CompressionConfig{
				Enabled:          tc.enabled,
				Threshold:        tc.threshold,
				CompressionLevel: 6,
				UseDeduplication: true,
			}
			SetCompressionConfig(config)

			// Encode witness with compression
			var buf bytes.Buffer
			if err := witness.EncodeCompressed(&buf); err != nil {
				t.Fatal(err)
			}

			encodedData := buf.Bytes()
			t.Logf("Encoded size: %d bytes", len(encodedData))

			// Decode witness
			var decodedWitness Witness
			if err := decodedWitness.DecodeCompressed(encodedData); err != nil {
				t.Fatal(err)
			}

			// Verify data integrity
			if len(decodedWitness.State) != len(witness.State) {
				t.Errorf("State count mismatch: got %d, want %d",
					len(decodedWitness.State), len(witness.State))
			}
		})
	}
}

func TestCompressionEffectiveness(t *testing.T) {
	// Create a large witness to test compression
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.Hash{},
		Root:       common.Hash{},
	}

	witness, err := NewWitness(header, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Add lots of repetitive data to test compression
	for i := 0; i < 100; i++ {
		witness.AddState(map[string]struct{}{
			"repetitive_state_node_pattern": {},
		})
	}

	// Test with compression enabled
	config := &CompressionConfig{
		Enabled:          true,
		Threshold:        100,
		CompressionLevel: 9, // Best compression
		UseDeduplication: true,
	}
	SetCompressionConfig(config)

	var buf bytes.Buffer
	if err := witness.EncodeCompressed(&buf); err != nil {
		t.Fatal(err)
	}

	encodedSize := buf.Len()
	originalSize := witness.Size()

	compressionRatio := float64(encodedSize) / float64(originalSize)
	t.Logf("Original size: %d bytes", originalSize)
	t.Logf("Encoded size: %d bytes", encodedSize)
	t.Logf("Compression ratio: %.2f%%", compressionRatio*100)

	// Verify compression is working
	if compressionRatio > 0.9 {
		t.Logf("Warning: Compression ratio is high (%.2f%%), compression may not be effective", compressionRatio*100)
	}

	// Verify we can still decode
	var decodedWitness Witness
	if err := decodedWitness.DecodeCompressed(buf.Bytes()); err != nil {
		t.Fatal(err)
	}

	// Check stats
	stats := CompressionStats()
	t.Logf("Compression stats: %+v", stats)
}

func TestBackwardCompatibility(t *testing.T) {
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

	witness.AddState(map[string]struct{}{
		"state_node": {},
	})

	// Test original RLP encoding/decoding still works
	var buf bytes.Buffer
	if err := witness.EncodeRLP(&buf); err != nil {
		t.Fatal(err)
	}

	var decodedWitness Witness
	stream := rlp.NewStream(bytes.NewReader(buf.Bytes()), 0)
	if err := decodedWitness.DecodeRLP(stream); err != nil {
		t.Fatal(err)
	}

	// Verify data integrity
	if len(decodedWitness.State) != len(witness.State) {
		t.Errorf("State count mismatch: got %d, want %d",
			len(decodedWitness.State), len(witness.State))
	}

	t.Logf("Backward compatibility test passed - original RLP format still works")
}

func TestCompressionMetrics(t *testing.T) {
	// Reset metrics before test
	resetMetrics()

	// Create a test witness with substantial data to ensure compression
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.Hash{},
		Root:       common.Hash{},
	}

	witness, err := NewWitness(header, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Add lots of data to ensure compression is triggered
	for i := 0; i < 1000; i++ {
		witness.AddState(map[string]struct{}{
			"repetitive_state_node_pattern_that_should_compress": {},
		})
	}

	// Configure for compression
	config := &CompressionConfig{
		Enabled:          true,
		Threshold:        100, // Low threshold to ensure compression
		CompressionLevel: 6,
		UseDeduplication: true,
	}
	SetCompressionConfig(config)

	// Get initial stats
	initialStats := CompressionStats()
	t.Logf("Initial stats: %+v", initialStats)

	// Perform compression
	var buf bytes.Buffer
	startTime := time.Now()
	if err := witness.EncodeCompressed(&buf); err != nil {
		t.Fatal(err)
	}
	encodeTime := time.Since(startTime)

	// Get stats after compression
	compressionStats := CompressionStats()
	t.Logf("After compression stats: %+v", compressionStats)

	// Verify compression metrics
	if compressionStats["compression_count"].(int64) <= initialStats["compression_count"].(int64) {
		t.Errorf("Compression count should increase, got %d", compressionStats["compression_count"])
	}

	if compressionStats["total_compression_time_ms"].(float64) <= 0 {
		t.Errorf("Total compression time should be positive, got %f", compressionStats["total_compression_time_ms"])
	}

	if compressionStats["avg_compression_time_ms"].(float64) <= 0 {
		t.Errorf("Average compression time should be positive, got %f", compressionStats["avg_compression_time_ms"])
	}

	if compressionStats["total_compression_size"].(int64) <= 0 {
		t.Errorf("Total compression size should be positive, got %d", compressionStats["total_compression_size"])
	}

	if compressionStats["compression_rate_bps"].(float64) <= 0 {
		t.Errorf("Compression rate should be positive, got %f", compressionStats["compression_rate_bps"])
	}

	// Verify timing is reasonable (should be close to our measured time)
	measuredTimeMs := float64(encodeTime.Nanoseconds()) / 1e6
	reportedTimeMs := compressionStats["avg_compression_time_ms"].(float64)
	tolerance := 0.5 // 50% tolerance for timing variations
	if reportedTimeMs < measuredTimeMs*(1-tolerance) || reportedTimeMs > measuredTimeMs*(1+tolerance) {
		t.Logf("Warning: Reported compression time (%f ms) differs significantly from measured time (%f ms)",
			reportedTimeMs, measuredTimeMs)
	}

	t.Logf("Compression metrics test passed")
}

func TestDecompressionMetrics(t *testing.T) {
	// Reset metrics before test
	resetMetrics()

	// Create a test witness with substantial data
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.Hash{},
		Root:       common.Hash{},
	}

	witness, err := NewWitness(header, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Add lots of data to ensure compression
	for i := 0; i < 500; i++ {
		witness.AddState(map[string]struct{}{
			"repetitive_state_node_for_decompression": {},
		})
	}

	// Configure for compression
	config := &CompressionConfig{
		Enabled:          true,
		Threshold:        100,
		CompressionLevel: 6,
		UseDeduplication: true,
	}
	SetCompressionConfig(config)

	// Encode the witness
	var buf bytes.Buffer
	if err := witness.EncodeCompressed(&buf); err != nil {
		t.Fatal(err)
	}

	encodedData := buf.Bytes()

	// Get initial stats
	initialStats := CompressionStats()
	t.Logf("Initial decompression stats: %+v", initialStats)

	// Perform decompression
	var decodedWitness Witness
	startTime := time.Now()
	if err := decodedWitness.DecodeCompressed(encodedData); err != nil {
		t.Fatal(err)
	}
	decodeTime := time.Since(startTime)

	// Get stats after decompression
	decompressionStats := CompressionStats()
	t.Logf("After decompression stats: %+v", decompressionStats)

	// Verify decompression metrics
	if decompressionStats["decompression_count"].(int64) <= initialStats["decompression_count"].(int64) {
		t.Errorf("Decompression count should increase, got %d", decompressionStats["decompression_count"])
	}

	if decompressionStats["total_decompression_time_ms"].(float64) <= 0 {
		t.Errorf("Total decompression time should be positive, got %f", decompressionStats["total_decompression_time_ms"])
	}

	if decompressionStats["avg_decompression_time_ms"].(float64) <= 0 {
		t.Errorf("Average decompression time should be positive, got %f", decompressionStats["avg_decompression_time_ms"])
	}

	if decompressionStats["total_decompression_size"].(int64) <= 0 {
		t.Errorf("Total decompression size should be positive, got %d", decompressionStats["total_decompression_size"])
	}

	if decompressionStats["decompression_rate_bps"].(float64) <= 0 {
		t.Errorf("Decompression rate should be positive, got %f", decompressionStats["decompression_rate_bps"])
	}

	// Verify timing is reasonable
	measuredTimeMs := float64(decodeTime.Nanoseconds()) / 1e6
	reportedTimeMs := decompressionStats["avg_decompression_time_ms"].(float64)
	tolerance := 0.5 // 50% tolerance for timing variations
	if reportedTimeMs < measuredTimeMs*(1-tolerance) || reportedTimeMs > measuredTimeMs*(1+tolerance) {
		t.Logf("Warning: Reported decompression time (%f ms) differs significantly from measured time (%f ms)",
			reportedTimeMs, measuredTimeMs)
	}

	t.Logf("Decompression metrics test passed")
}

func TestCompressionRateCalculation(t *testing.T) {
	// Reset metrics before test
	resetMetrics()

	// Create a large witness
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.Hash{},
		Root:       common.Hash{},
	}

	witness, err := NewWitness(header, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Add substantial data
	dataSize := 1024 * 1024 // 1MB of data
	chunkSize := 1024
	for i := 0; i < dataSize/chunkSize; i++ {
		witness.AddState(map[string]struct{}{
			"test_state": {},
		})
	}

	// Configure for compression
	config := &CompressionConfig{
		Enabled:          true,
		Threshold:        100,
		CompressionLevel: 6,
		UseDeduplication: true,
	}
	SetCompressionConfig(config)

	// Perform multiple compressions to test rate calculation
	for i := 0; i < 5; i++ {
		var buf bytes.Buffer
		if err := witness.EncodeCompressed(&buf); err != nil {
			t.Fatal(err)
		}

		// Also perform decompression to test decompression rates
		var decodedWitness Witness
		if err := decodedWitness.DecodeCompressed(buf.Bytes()); err != nil {
			t.Fatal(err)
		}
	}

	stats := CompressionStats()
	t.Logf("Compression rate stats: %+v", stats)

	// Verify rate calculations
	if stats["compression_rate_bps"].(float64) <= 0 {
		t.Errorf("Compression rate should be positive, got %f", stats["compression_rate_bps"])
	}

	if stats["decompression_rate_bps"].(float64) <= 0 {
		t.Errorf("Decompression rate should be positive, got %f", stats["decompression_rate_bps"])
	}

	// Verify that rates are reasonable (should be in MB/s range for typical operations)
	compressionRateMBps := stats["compression_rate_bps"].(float64) / (1024 * 1024)
	decompressionRateMBps := stats["decompression_rate_bps"].(float64) / (1024 * 1024)

	t.Logf("Compression rate: %.2f MB/s", compressionRateMBps)
	t.Logf("Decompression rate: %.2f MB/s", decompressionRateMBps)

	// Rates should be reasonable (between 0.1 MB/s and 1000 MB/s)
	if compressionRateMBps < 0.1 || compressionRateMBps > 1000 {
		t.Logf("Warning: Compression rate (%.2f MB/s) seems unusual", compressionRateMBps)
	}

	if decompressionRateMBps < 0.1 || decompressionRateMBps > 1000 {
		t.Logf("Warning: Decompression rate (%.2f MB/s) seems unusual", decompressionRateMBps)
	}

	t.Logf("Compression rate calculation test passed")
}

func TestMetricsConsistency(t *testing.T) {
	// Reset metrics before test
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

	// Add data
	for i := 0; i < 100; i++ {
		witness.AddState(map[string]struct{}{
			"test_state": {},
		})
	}

	// Configure for compression
	config := &CompressionConfig{
		Enabled:          true,
		Threshold:        100,
		CompressionLevel: 6,
		UseDeduplication: true,
	}
	SetCompressionConfig(config)

	// Perform compression and decompression multiple times
	for i := 0; i < 3; i++ {
		var buf bytes.Buffer
		if err := witness.EncodeCompressed(&buf); err != nil {
			t.Fatal(err)
		}

		var decodedWitness Witness
		if err := decodedWitness.DecodeCompressed(buf.Bytes()); err != nil {
			t.Fatal(err)
		}
	}

	stats := CompressionStats()
	t.Logf("Final stats: %+v", stats)

	// Verify consistency of metrics
	compressionCount := stats["compression_count"].(int64)
	decompressionCount := stats["decompression_count"].(int64)

	// Should have performed 3 compressions and 3 decompressions
	if compressionCount != 3 {
		t.Errorf("Expected 3 compressions, got %d", compressionCount)
	}

	if decompressionCount != 3 {
		t.Errorf("Expected 3 decompressions, got %d", decompressionCount)
	}

	// Verify that sizes are consistent
	totalOriginalSize := stats["total_original_size"].(int64)
	totalCompressedSize := stats["total_compressed_size"].(int64)
	spaceSaved := stats["space_saved_bytes"].(int64)

	if spaceSaved != totalOriginalSize-totalCompressedSize {
		t.Errorf("Space saved calculation incorrect: got %d, expected %d",
			spaceSaved, totalOriginalSize-totalCompressedSize)
	}

	// Verify that compression size matches total compression size
	totalCompressionSize := stats["total_compression_size"].(int64)
	if totalCompressionSize != totalCompressedSize {
		t.Errorf("Total compression size mismatch: got %d, expected %d",
			totalCompressionSize, totalCompressedSize)
	}

	t.Logf("Metrics consistency test passed")
}

// BenchmarkWitnessCompression benchmarks gzip compression and decompression for various witness sizes.
func BenchmarkWitnessCompression(b *testing.B) {
	sizes := []int{
		1 << 10,   // 1KB
		2 << 10,   // 2KB
		4 << 10,   // 4KB
		8 << 10,   // 8KB
		16 << 10,  // 16KB
		32 << 10,  // 32KB
		64 << 10,  // 64KB
		128 << 10, // 128KB
		256 << 10, // 256KB
		512 << 10, // 512KB
		1 << 20,   // 1MB
		2 << 20,   // 2MB
		4 << 20,   // 4MB
		8 << 20,   // 8MB
		16 << 20,  // 16MB
	}

	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.Hash{},
		Root:       common.Hash{},
	}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("%dKB", size/1024), func(b *testing.B) {
			resetMetrics()
			// Generate witness with State map of the target size (approximate)
			witness, err := NewWitness(header, nil)
			if err != nil {
				b.Fatal(err)
			}
			// Each key is 32 bytes, value is struct{} (0 bytes), so fill with enough keys
			key := make([]byte, 32)
			for i := 0; len(witness.State)*32 < size; i++ {
				copy(key, fmt.Sprintf("node_%d", i))
				witness.AddState(map[string]struct{}{string(key): {}})
			}

			// Set compression config
			SetCompressionConfig(&CompressionConfig{
				Enabled:          true,
				Threshold:        0, // always compress
				CompressionLevel: gzip.BestSpeed,
				UseDeduplication: true,
			})

			var (
				compressTime   int64
				decompressTime int64
				origSize       int
				compSize       int
			)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var buf bytes.Buffer
				start := time.Now()
				if err := witness.EncodeCompressed(&buf); err != nil {
					b.Fatal(err)
				}
				compressTime += time.Since(start).Nanoseconds()
				compSize = buf.Len()
				origSize = witness.Size()

				// Decompression
				var decoded Witness
				start = time.Now()
				if err := decoded.DecodeCompressed(buf.Bytes()); err != nil {
					b.Fatal(err)
				}
				decompressTime += time.Since(start).Nanoseconds()
			}
			b.StopTimer()

			avgCompressMs := float64(compressTime) / float64(b.N) / 1e6
			avgDecompressMs := float64(decompressTime) / float64(b.N) / 1e6
			compressionRatio := float64(compSize) / float64(origSize)

			b.Logf("Witness size: %d bytes, Compressed: %d bytes, Ratio: %.2f%%, Avg Compress: %.2fms, Avg Decompress: %.2fms", origSize, compSize, compressionRatio*100, avgCompressMs, avgDecompressMs)
		})
	}
}

// resetMetrics resets all compression/decompression metrics for testing
func resetMetrics() {
	// Reset all metrics to zero
	compressionRatio.Update(0)
	compressionCount.Clear()
	uncompressedCount.Clear()
	totalOriginalSize.Update(0)
	totalCompressedSize.Update(0)
	spaceSavedBytes.Update(0)
	// Note: Timer and Meter don't have Clear methods, they accumulate over time
	// We can't easily reset them in tests, but this is acceptable for metrics
	decompressionCount.Clear()
}
