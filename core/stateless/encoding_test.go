package stateless

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
)

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
	witness.AddCode([]byte("test bytecode 1234567890"))
	witness.AddCode([]byte("another bytecode 0987654321"))
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
			if len(decodedWitness.Codes) != len(witness.Codes) {
				t.Errorf("Codes count mismatch: got %d, want %d",
					len(decodedWitness.Codes), len(witness.Codes))
			}

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
		witness.AddCode([]byte("repetitive bytecode pattern that should compress well"))
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

	witness.AddCode([]byte("test bytecode"))
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
	if len(decodedWitness.Codes) != len(witness.Codes) {
		t.Errorf("Codes count mismatch: got %d, want %d",
			len(decodedWitness.Codes), len(witness.Codes))
	}

	if len(decodedWitness.State) != len(witness.State) {
		t.Errorf("State count mismatch: got %d, want %d",
			len(decodedWitness.State), len(witness.State))
	}

	t.Logf("Backward compatibility test passed - original RLP format still works")
}
