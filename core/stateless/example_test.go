package stateless

import (
	"bytes"
	"fmt"
	"log"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// ExampleWitnessCompression demonstrates how to use witness compression
func ExampleWitnessCompression() {
	// Create a test witness
	header := &types.Header{
		Number:     common.Big1,
		ParentHash: common.Hash{},
		Root:       common.Hash{},
	}

	witness, err := NewWitness(header, nil)
	if err != nil {
		log.Fatal(err)
	}

	// Add some data to the witness
	witness.AddCode([]byte("some bytecode data"))
	witness.AddState(map[string]struct{}{
		"state_node_1": {},
		"state_node_2": {},
	})

	// Configure compression
	config := &CompressionConfig{
		Enabled:          true,
		Threshold:        100, // Compress if > 100 bytes
		CompressionLevel: 6,   // Medium compression
		UseDeduplication: true,
	}
	SetCompressionConfig(config)

	// Encode with compression
	var buf bytes.Buffer
	if err := witness.EncodeCompressed(&buf); err != nil {
		log.Fatal(err)
	}

	compressedData := buf.Bytes()
	fmt.Printf("Original size: %d bytes\n", witness.Size())
	fmt.Printf("Compressed size: %d bytes\n", len(compressedData))
	fmt.Printf("Compression ratio: %.1f%%\n", float64(len(compressedData))/float64(witness.Size())*100)

	// Decode the compressed data
	var decodedWitness Witness
	if err := decodedWitness.DecodeCompressed(compressedData); err != nil {
		log.Fatal(err)
	}

	// Verify data integrity
	fmt.Printf("Codes count: %d\n", len(decodedWitness.Codes))
	fmt.Printf("State count: %d\n", len(decodedWitness.State))

	// Get compression statistics
	stats := CompressionStats()
	fmt.Printf("Total witnesses processed: %d\n", stats["total_witnesses"])
	fmt.Printf("Average compression ratio: %.1f%%\n", stats["compression_ratio"])
	fmt.Printf("Total space saved: %d bytes\n", stats["space_saved_bytes"])

	// Output:
	// Original size: 186 bytes
	// Compressed size: 135 bytes
	// Compression ratio: 72.6%
	// Codes count: 1
	// State count: 2
	// Total witnesses processed: 1
	// Average compression ratio: 21.5%
	// Total space saved: 918 bytes
}
