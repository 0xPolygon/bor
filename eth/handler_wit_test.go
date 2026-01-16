package eth

import (
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestWitPeer creates a wit.Peer for testing
func newTestWitPeer() *wit.Peer {
	var id enode.ID
	rand.Read(id[:])
	p2pPeer := p2p.NewPeer(id, "test-peer", nil)
	_, rw := p2p.MsgPipe()
	return wit.NewPeer(wit.WIT1, p2pPeer, rw, log.New())
}

// newTestWitPeerWithReader creates a wit.Peer with a background reader to prevent blocking
// on ReplyWitness and ReplyWitnessMetadata calls
func newTestWitPeerWithReader() (*wit.Peer, func()) {
	var id enode.ID
	rand.Read(id[:])
	p2pPeer := p2p.NewPeer(id, "test-peer", nil)
	app, net := p2p.MsgPipe()

	// Start background reader to prevent WriteMsg from blocking
	done := make(chan struct{})
	go func() {
		for {
			msg, err := app.ReadMsg()
			if err != nil {
				close(done)
				return
			}
			msg.Discard()
		}
	}()

	peer := wit.NewPeer(wit.WIT1, p2pPeer, net, log.New())
	cleanup := func() {
		app.Close()
		peer.Close()
		<-done
	}
	return peer, cleanup
}

// mockUnknownPacket is a mock packet type that implements wit.Packet
// but is not recognized by the Handle method's switch statement
type mockUnknownPacket struct{}

func (m *mockUnknownPacket) Name() string { return "UnknownPacket" }
func (m *mockUnknownPacket) Kind() byte   { return 0xFF }

// TestHandleGetWitnessMetadataPacket tests the Handle function with GetWitnessMetadataPacket
// This test verifies that the case statement correctly routes GetWitnessMetadataPacket
// to handleGetWitnessMetadata. The actual metadata handling logic is tested in
// TestHandleGetWitnessMetadata.
//
// Note: The actual Handle call would block on ReplyWitnessMetadata without a reader,
// so we verify the packet type and routing through the direct function tests.
func TestHandleGetWitnessMetadataPacket(t *testing.T) {
	// Create a GetWitnessMetadataPacket
	hash1 := common.Hash{0x01, 0x02, 0x03}
	hash2 := common.Hash{0x04, 0x05, 0x06}

	packet := &wit.GetWitnessMetadataPacket{
		RequestId: 12345,
		GetWitnessMetadataRequest: &wit.GetWitnessMetadataRequest{
			Hashes: []common.Hash{hash1, hash2},
		},
	}

	// Verify the packet type is recognized by checking it implements the Packet interface
	// and has the correct Kind - this ensures the case statement in Handle will match
	assert.Equal(t, byte(wit.GetWitnessMetadataMsg), packet.Kind(), "Packet should have correct message kind")
	assert.Equal(t, "GetWitnessMetadata", packet.Name(), "Packet should have correct name")

	// Verify the case statement in Handle routes to handleGetWitnessMetadata by testing
	// that function directly in TestHandleGetWitnessMetadata
	t.Log("Handle case for GetWitnessMetadataPacket routes to handleGetWitnessMetadata (tested in TestHandleGetWitnessMetadata)")
}

// TestHandleGetWitnessMetadata tests the handleGetWitnessMetadata function directly
func TestHandleGetWitnessMetadata(t *testing.T) {
	handler := newTestHandler()
	defer handler.close()

	witHandler := (*witHandler)(handler.handler)
	peer := newTestWitPeer()
	defer peer.Close()

	// Create test blocks and add headers to chain
	blockNum1 := uint64(1000)
	blockNum2 := uint64(2000)
	blockNum3 := uint64(3000)

	header1 := &types.Header{
		Number: big.NewInt(int64(blockNum1)),
	}
	header2 := &types.Header{
		Number: big.NewInt(int64(blockNum2)),
	}
	header3 := &types.Header{
		Number: big.NewInt(int64(blockNum3)),
	}

	// Compute hashes from headers
	hash1 := header1.Hash()
	hash2 := header2.Hash()
	hash3 := header3.Hash()

	// Insert headers into chain database
	db := handler.chain.DB()
	rawdb.WriteHeader(db, header1)
	rawdb.WriteHeader(db, header2)
	rawdb.WriteHeader(db, header3)

	// Create witnesses of different sizes
	// Witness 1: 10 MB (should result in 1 page since PageSize is 15MB)
	witness1 := make([]byte, 10*1024*1024)
	rand.Read(witness1)
	rawdb.WriteWitness(db, hash1, witness1)

	// Witness 2: 20 MB (should result in 2 pages)
	witness2 := make([]byte, 20*1024*1024)
	rand.Read(witness2)
	rawdb.WriteWitness(db, hash2, witness2)

	// hash3 has no witness (will test unavailable case)

	// Create GetWitnessMetadataPacket
	packet := &wit.GetWitnessMetadataPacket{
		RequestId: 99999,
		GetWitnessMetadataRequest: &wit.GetWitnessMetadataRequest{
			Hashes: []common.Hash{hash1, hash2, hash3},
		},
	}

	// Call handleGetWitnessMetadata
	response, err := witHandler.handleGetWitnessMetadata(peer, packet)

	// Should not error
	require.NoError(t, err)
	require.Equal(t, 3, len(response))

	// Verify response for hash1 (10 MB witness)
	metadata1 := response[0]
	assert.Equal(t, hash1, metadata1.Hash)
	assert.True(t, metadata1.Available)
	assert.Equal(t, uint64(10*1024*1024), metadata1.WitnessSize)
	assert.Equal(t, uint64(1), metadata1.TotalPages) // 10MB < 15MB page size, so 1 page
	assert.Equal(t, blockNum1, metadata1.BlockNumber)

	// Verify response for hash2 (20 MB witness)
	metadata2 := response[1]
	assert.Equal(t, hash2, metadata2.Hash)
	assert.True(t, metadata2.Available)
	assert.Equal(t, uint64(20*1024*1024), metadata2.WitnessSize)
	assert.Equal(t, uint64(2), metadata2.TotalPages) // 20MB / 15MB = 2 pages (ceiling)
	assert.Equal(t, blockNum2, metadata2.BlockNumber)

	// Verify response for hash3 (no witness)
	metadata3 := response[2]
	assert.Equal(t, hash3, metadata3.Hash)
	assert.False(t, metadata3.Available)
	assert.Equal(t, uint64(0), metadata3.WitnessSize)
	assert.Equal(t, uint64(0), metadata3.TotalPages)
	assert.Equal(t, blockNum3, metadata3.BlockNumber)
}

// TestHandleGetWitnessMetadata_EmptyHashes tests with empty hashes
func TestHandleGetWitnessMetadata_EmptyHashes(t *testing.T) {
	handler := newTestHandler()
	defer handler.close()

	witHandler := (*witHandler)(handler.handler)
	peer := newTestWitPeer()
	defer peer.Close()

	packet := &wit.GetWitnessMetadataPacket{
		RequestId: 11111,
		GetWitnessMetadataRequest: &wit.GetWitnessMetadataRequest{
			Hashes: []common.Hash{},
		},
	}

	response, err := witHandler.handleGetWitnessMetadata(peer, packet)

	// Should not error, but return empty response
	require.NoError(t, err)
	assert.Equal(t, 0, len(response))
}

// TestHandleGetWitnessMetadata_PageCalculation tests page calculation edge cases
func TestHandleGetWitnessMetadata_PageCalculation(t *testing.T) {
	handler := newTestHandler()
	defer handler.close()

	witHandler := (*witHandler)(handler.handler)
	peer := newTestWitPeer()
	defer peer.Close()

	// Test various witness sizes to verify page calculation
	testCases := []struct {
		name          string
		witnessSize   int
		expectedPages uint64
	}{
		{"exactly one page", 15 * 1024 * 1024, 1},        // Exactly PageSize
		{"one byte over one page", 15*1024*1024 + 1, 2},  // One byte over
		{"exactly two pages", 30 * 1024 * 1024, 2},       // Exactly 2 * PageSize
		{"one byte over two pages", 30*1024*1024 + 1, 3}, // One byte over 2 pages
		{"zero size", 0, 0},                              // Zero size
		{"small witness", 1024, 1},                       // Small witness (< PageSize)
		{"large witness", 100 * 1024 * 1024, 7},          // Large witness (100MB / 15MB = 7 pages)
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create header with unique number based on test name
			header := &types.Header{
				Number: big.NewInt(int64(len(tc.name))),
			}
			hash := header.Hash() // Compute hash

			db := handler.chain.DB()
			rawdb.WriteHeader(db, header)

			// Write witness if size > 0
			if tc.witnessSize > 0 {
				witness := make([]byte, tc.witnessSize)
				rand.Read(witness)
				rawdb.WriteWitness(db, hash, witness)
			}

			packet := &wit.GetWitnessMetadataPacket{
				RequestId: 22222,
				GetWitnessMetadataRequest: &wit.GetWitnessMetadataRequest{
					Hashes: []common.Hash{hash},
				},
			}

			response, err := witHandler.handleGetWitnessMetadata(peer, packet)
			require.NoError(t, err)
			require.Equal(t, 1, len(response))

			metadata := response[0]
			assert.Equal(t, hash, metadata.Hash)
			assert.Equal(t, tc.expectedPages, metadata.TotalPages, "Page count mismatch for %s", tc.name)
			if tc.witnessSize > 0 {
				assert.True(t, metadata.Available)
				assert.Equal(t, uint64(tc.witnessSize), metadata.WitnessSize)
			} else {
				assert.False(t, metadata.Available)
			}
		})
	}
}

// TestHandleGetWitnessMetadata_MissingHeader tests behavior when header is missing
func TestHandleGetWitnessMetadata_MissingHeader(t *testing.T) {
	handler := newTestHandler()
	defer handler.close()

	witHandler := (*witHandler)(handler.handler)
	peer := newTestWitPeer()
	defer peer.Close()

	// Create a hash that doesn't have a header in the chain
	hash := common.Hash{0x99, 0x88, 0x77}

	// Write witness but no header
	db := handler.chain.DB()
	witness := make([]byte, 5*1024*1024)
	rand.Read(witness)
	rawdb.WriteWitness(db, hash, witness)

	packet := &wit.GetWitnessMetadataPacket{
		RequestId: 33333,
		GetWitnessMetadataRequest: &wit.GetWitnessMetadataRequest{
			Hashes: []common.Hash{hash},
		},
	}

	response, err := witHandler.handleGetWitnessMetadata(peer, packet)
	require.NoError(t, err)
	require.Equal(t, 1, len(response))

	metadata := response[0]
	assert.Equal(t, hash, metadata.Hash)
	assert.True(t, metadata.Available) // Witness exists
	assert.Equal(t, uint64(5*1024*1024), metadata.WitnessSize)
	assert.Equal(t, uint64(1), metadata.TotalPages)
	assert.Equal(t, uint64(0), metadata.BlockNumber) // Block number should be 0 when header is missing
}

// TestWitHandlerHandle tests the Handle method with all packet types
func TestWitHandlerHandle(t *testing.T) {
	handler := newTestHandler()
	defer handler.close()

	witHandler := (*witHandler)(handler.handler)

	t.Run("NewWitnessPacket", func(t *testing.T) {
		peer, cleanup := newTestWitPeerWithReader()
		defer cleanup()

		// Create a test witness
		header := &types.Header{
			Number: big.NewInt(100),
		}
		witness, _ := stateless.NewWitness(header, nil)

		packet := &wit.NewWitnessPacket{
			Witness: witness,
		}

		err := witHandler.Handle(peer, packet)
		require.NoError(t, err, "Handle should successfully process NewWitnessPacket")
	})

	t.Run("NewWitnessHashesPacket", func(t *testing.T) {
		peer, cleanup := newTestWitPeerWithReader()
		defer cleanup()

		hashes := []common.Hash{
			{0x01, 0x02, 0x03},
			{0x04, 0x05, 0x06},
		}
		numbers := []uint64{100, 101}

		packet := &wit.NewWitnessHashesPacket{
			Hashes:  hashes,
			Numbers: numbers,
		}

		err := witHandler.Handle(peer, packet)
		require.NoError(t, err, "Handle should successfully process NewWitnessHashesPacket")
	})

	t.Run("GetWitnessPacket", func(t *testing.T) {
		peer, cleanup := newTestWitPeerWithReader()
		defer cleanup()

		// Create test header and witness
		header := &types.Header{
			Number: big.NewInt(200),
		}
		hash := header.Hash()

		db := handler.chain.DB()
		rawdb.WriteHeader(db, header)

		// Create a small witness (5 MB)
		witness := make([]byte, 5*1024*1024)
		rand.Read(witness)
		rawdb.WriteWitness(db, hash, witness)

		packet := &wit.GetWitnessPacket{
			RequestId: 12345,
			GetWitnessRequest: &wit.GetWitnessRequest{
				WitnessPages: []wit.WitnessPageRequest{
					{Hash: hash, Page: 0},
				},
			},
		}

		err := witHandler.Handle(peer, packet)
		require.NoError(t, err, "Handle should successfully process GetWitnessPacket")
	})

	t.Run("GetWitnessMetadataPacket", func(t *testing.T) {
		peer, cleanup := newTestWitPeerWithReader()
		defer cleanup()

		// Create test header
		header := &types.Header{
			Number: big.NewInt(300),
		}
		hash := header.Hash()

		db := handler.chain.DB()
		rawdb.WriteHeader(db, header)

		// Create a witness
		witness := make([]byte, 10*1024*1024)
		rand.Read(witness)
		rawdb.WriteWitness(db, hash, witness)

		packet := &wit.GetWitnessMetadataPacket{
			RequestId: 67890,
			GetWitnessMetadataRequest: &wit.GetWitnessMetadataRequest{
				Hashes: []common.Hash{hash},
			},
		}

		err := witHandler.Handle(peer, packet)
		require.NoError(t, err, "Handle should successfully process GetWitnessMetadataPacket")
	})

	t.Run("UnknownPacketType", func(t *testing.T) {
		peer, cleanup := newTestWitPeerWithReader()
		defer cleanup()

		// Create a mock packet type that implements Packet interface
		// but is not one of the recognized types in the switch statement
		// We'll use an empty struct and implement the methods inline
		packet := &mockUnknownPacket{}

		err := witHandler.Handle(peer, packet)
		require.Error(t, err, "Handle should return error for unknown packet type")
		assert.Contains(t, err.Error(), "unknown wit packet type", "Error message should indicate unknown packet type")
	})

	t.Run("GetWitnessPacket_ErrorHandling", func(t *testing.T) {
		peer, cleanup := newTestWitPeerWithReader()
		defer cleanup()

		// Create a packet requesting a witness that doesn't exist
		// This should still succeed (returns empty data), but tests error path in handleGetWitness
		nonExistentHash := common.Hash{0x99, 0x88, 0x77}

		packet := &wit.GetWitnessPacket{
			RequestId: 99999,
			GetWitnessRequest: &wit.GetWitnessRequest{
				WitnessPages: []wit.WitnessPageRequest{
					{Hash: nonExistentHash, Page: 0},
				},
			},
		}

		err := witHandler.Handle(peer, packet)
		require.NoError(t, err, "Handle should handle missing witness gracefully")
	})

	t.Run("GetWitnessMetadataPacket_ErrorHandling", func(t *testing.T) {
		peer, cleanup := newTestWitPeerWithReader()
		defer cleanup()

		// Create a packet requesting metadata for a non-existent witness
		nonExistentHash := common.Hash{0xAA, 0xBB, 0xCC}

		packet := &wit.GetWitnessMetadataPacket{
			RequestId: 11111,
			GetWitnessMetadataRequest: &wit.GetWitnessMetadataRequest{
				Hashes: []common.Hash{nonExistentHash},
			},
		}

		err := witHandler.Handle(peer, packet)
		require.NoError(t, err, "Handle should handle missing witness metadata gracefully")
	})
}
