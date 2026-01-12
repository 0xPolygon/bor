package wit

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

// TestWitnessMetadataResponse tests the WitnessMetadataResponse structure
func TestWitnessMetadataResponse(t *testing.T) {
	hash := common.Hash{0x01, 0x02}
	metadata := WitnessMetadataResponse{
		Hash:        hash,
		TotalPages:  15,
		WitnessSize: 225 * 1024 * 1024,
		BlockNumber: 1000,
		Available:   true,
	}

	assert.Equal(t, hash, metadata.Hash)
	assert.Equal(t, uint64(15), metadata.TotalPages)
	assert.Equal(t, uint64(225*1024*1024), metadata.WitnessSize)
	assert.Equal(t, uint64(1000), metadata.BlockNumber)
	assert.True(t, metadata.Available)
}

// TestWitnessMetadataPacket tests the WitnessMetadataPacket structure
func TestWitnessMetadataPacket(t *testing.T) {
	hash1 := common.Hash{0x01}
	hash2 := common.Hash{0x02}
	
	packet := &WitnessMetadataPacket{
		RequestId: 123,
		Metadata: []WitnessMetadataResponse{
			{Hash: hash1, TotalPages: 10, Available: true},
			{Hash: hash2, TotalPages: 20, Available: false},
		},
	}

	assert.Equal(t, uint64(123), packet.RequestId)
	assert.Equal(t, 2, len(packet.Metadata))
	assert.Equal(t, hash1, packet.Metadata[0].Hash)
	assert.True(t, packet.Metadata[0].Available)
	assert.False(t, packet.Metadata[1].Available)
}

// TestGetWitnessMetadataPacket tests the GetWitnessMetadataPacket structure
func TestGetWitnessMetadataPacket(t *testing.T) {
	hash1 := common.Hash{0x01}
	hash2 := common.Hash{0x02}
	
	packet := &GetWitnessMetadataPacket{
		RequestId: 456,
		GetWitnessMetadataRequest: &GetWitnessMetadataRequest{
			Hashes: []common.Hash{hash1, hash2},
		},
	}

	assert.Equal(t, uint64(456), packet.RequestId)
	assert.NotNil(t, packet.GetWitnessMetadataRequest)
	assert.Equal(t, 2, len(packet.GetWitnessMetadataRequest.Hashes))
	assert.Equal(t, hash1, packet.GetWitnessMetadataRequest.Hashes[0])
	assert.Equal(t, hash2, packet.GetWitnessMetadataRequest.Hashes[1])
}

// TestWitnessPageRequest tests the WitnessPageRequest structure
func TestWitnessPageRequest(t *testing.T) {
	hash := common.Hash{0xab, 0xcd}
	req := WitnessPageRequest{
		Hash: hash,
		Page: 5,
	}

	assert.Equal(t, hash, req.Hash)
	assert.Equal(t, uint64(5), req.Page)
}

// TestWitnessPageResponse tests the WitnessPageResponse structure
func TestWitnessPageResponse(t *testing.T) {
	hash := common.Hash{0xab, 0xcd}
	data := []byte{0x01, 0x02, 0x03}
	
	resp := WitnessPageResponse{
		Page:       3,
		TotalPages: 10,
		Hash:       hash,
		Data:       data,
	}

	assert.Equal(t, uint64(3), resp.Page)
	assert.Equal(t, uint64(10), resp.TotalPages)
	assert.Equal(t, hash, resp.Hash)
	assert.Equal(t, data, resp.Data)
}

// TestGetWitnessPacket tests the GetWitnessPacket structure
func TestGetWitnessPacket(t *testing.T) {
	hash := common.Hash{0x01}
	packet := &GetWitnessPacket{
		RequestId: 789,
		GetWitnessRequest: &GetWitnessRequest{
			WitnessPages: []WitnessPageRequest{
				{Hash: hash, Page: 0},
				{Hash: hash, Page: 1},
			},
		},
	}

	assert.Equal(t, uint64(789), packet.RequestId)
	assert.NotNil(t, packet.GetWitnessRequest)
	assert.Equal(t, 2, len(packet.GetWitnessRequest.WitnessPages))
	assert.Equal(t, uint64(0), packet.GetWitnessRequest.WitnessPages[0].Page)
	assert.Equal(t, uint64(1), packet.GetWitnessRequest.WitnessPages[1].Page)
}

// TestProtocolVersionConstants tests that protocol version constants are defined correctly
func TestProtocolVersionConstants(t *testing.T) {
	assert.Equal(t, 0, WIT0, "WIT0 should be version 0")
	assert.Equal(t, 1, WIT1, "WIT1 should be version 1")
	assert.True(t, WIT1 > WIT0, "WIT1 should be greater than WIT0")
}

// TestProtocolName tests the protocol name constant
func TestProtocolName(t *testing.T) {
	assert.Equal(t, "wit", ProtocolName)
}

// TestProtocolVersions tests that protocol versions are correctly defined
func TestProtocolVersions(t *testing.T) {
	assert.Contains(t, ProtocolVersions, uint(WIT0))
	assert.Contains(t, ProtocolVersions, uint(WIT1))
	assert.True(t, len(ProtocolVersions) >= 2, "Should have at least 2 protocol versions")
}

// TestProtocolMessages tests that message codes are unique and sequential
func TestProtocolMessages(t *testing.T) {
	messages := map[uint64]string{
		NewWitnessMsg:         "NewWitnessMsg",
		NewWitnessHashesMsg:   "NewWitnessHashesMsg",
		GetMsgWitness:         "GetMsgWitness",
		MsgWitness:            "MsgWitness",
		GetWitnessMetadataMsg: "GetWitnessMetadataMsg",
		WitnessMetadataMsg:    "WitnessMetadataMsg",
	}

	// Check all message codes are unique
	seen := make(map[uint64]bool)
	for code := range messages {
		assert.False(t, seen[code], "Message code %d is duplicated", code)
		seen[code] = true
	}

	// Verify expected message codes exist
	assert.Equal(t, 0x00, NewWitnessMsg)
	assert.Equal(t, 0x01, NewWitnessHashesMsg)
	assert.Equal(t, 0x02, GetMsgWitness)
	assert.Equal(t, 0x03, MsgWitness)
	assert.Equal(t, 0x04, GetWitnessMetadataMsg)
	assert.Equal(t, 0x05, WitnessMetadataMsg)
}

// TestNewWitnessPacket tests the NewWitnessPacket structure
func TestNewWitnessPacket(t *testing.T) {
	// Test with nil witness (edge case)
	packet := &NewWitnessPacket{Witness: nil}
	assert.Nil(t, packet.Witness)
}

// TestNewWitnessHashesPacket tests the NewWitnessHashesPacket structure
func TestNewWitnessHashesPacket(t *testing.T) {
	hash1 := common.Hash{0x01}
	hash2 := common.Hash{0x02}
	
	packet := &NewWitnessHashesPacket{
		Hashes:  []common.Hash{hash1, hash2},
		Numbers: []uint64{100, 101},
	}

	assert.Equal(t, 2, len(packet.Hashes))
	assert.Equal(t, 2, len(packet.Numbers))
	assert.Equal(t, hash1, packet.Hashes[0])
	assert.Equal(t, hash2, packet.Hashes[1])
	assert.Equal(t, uint64(100), packet.Numbers[0])
	assert.Equal(t, uint64(101), packet.Numbers[1])
}

// TestWitnessPacketRLPPacket tests the WitnessPacketRLPPacket structure
func TestWitnessPacketRLPPacket(t *testing.T) {
	hash := common.Hash{0xab}
	data := []byte{0x01, 0x02}
	
	packet := &WitnessPacketRLPPacket{
		RequestId: 999,
		WitnessPacketResponse: WitnessPacketResponse{
			{Page: 0, TotalPages: 2, Hash: hash, Data: data},
			{Page: 1, TotalPages: 2, Hash: hash, Data: data},
		},
	}

	assert.Equal(t, uint64(999), packet.RequestId)
	assert.Equal(t, 2, len(packet.WitnessPacketResponse))
	assert.Equal(t, uint64(0), packet.WitnessPacketResponse[0].Page)
	assert.Equal(t, uint64(1), packet.WitnessPacketResponse[1].Page)
}

// TestGetWitnessRequest tests the GetWitnessRequest structure
func TestGetWitnessRequest(t *testing.T) {
	hash1 := common.Hash{0x01}
	hash2 := common.Hash{0x02}
	
	req := &GetWitnessRequest{
		WitnessPages: []WitnessPageRequest{
			{Hash: hash1, Page: 0},
			{Hash: hash1, Page: 1},
			{Hash: hash2, Page: 0},
		},
	}

	assert.Equal(t, 3, len(req.WitnessPages))
	assert.Equal(t, hash1, req.WitnessPages[0].Hash)
	assert.Equal(t, hash2, req.WitnessPages[2].Hash)
}

// TestGetWitnessMetadataRequest tests the GetWitnessMetadataRequest structure
func TestGetWitnessMetadataRequest(t *testing.T) {
	hash1 := common.Hash{0x01}
	hash2 := common.Hash{0x02}
	hash3 := common.Hash{0x03}
	
	req := &GetWitnessMetadataRequest{
		Hashes: []common.Hash{hash1, hash2, hash3},
	}

	assert.Equal(t, 3, len(req.Hashes))
	assert.Equal(t, hash1, req.Hashes[0])
	assert.Equal(t, hash2, req.Hashes[1])
	assert.Equal(t, hash3, req.Hashes[2])
}
