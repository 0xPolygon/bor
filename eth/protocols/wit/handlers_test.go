package wit

import (
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Equal(t, 1, WIT0, "WIT0 should be version 1")
	assert.Equal(t, 2, WIT1, "WIT1 should be version 2")
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

// mockDecoder is a mock implementation of the Decoder interface for testing
type mockDecoder struct {
	decodeFunc func(val interface{}) error
	timeFunc   func() time.Time
}

func (m *mockDecoder) Decode(val interface{}) error {
	if m.decodeFunc != nil {
		return m.decodeFunc(val)
	}
	return nil
}

func (m *mockDecoder) Time() time.Time {
	if m.timeFunc != nil {
		return m.timeFunc()
	}
	return time.Now()
}

// mockBackend is a mock implementation of the Backend interface for testing
type mockBackend struct {
	handleFunc func(peer *Peer, packet Packet) error
}

func (m *mockBackend) Chain() *core.BlockChain {
	panic("not implemented")
}

func (m *mockBackend) RunPeer(peer *Peer, handler Handler) error {
	panic("not implemented")
}

func (m *mockBackend) PeerInfo(id enode.ID) interface{} {
	panic("not implemented")
}

func (m *mockBackend) Handle(peer *Peer, packet Packet) error {
	if m.handleFunc != nil {
		return m.handleFunc(peer, packet)
	}
	return nil
}

// TestHandleGetWitnessMetadata tests the handleGetWitnessMetadata function
func TestHandleGetWitnessMetadata(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		backend := &mockBackend{}

		// Create a real peer to use with the handler
		var id enode.ID
		id[0] = 0x01
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		_, rw := p2p.MsgPipe()
		realPeer := NewPeer(WIT1, p2pPeer, rw, log.New())
		defer realPeer.Close()

		hashes := []common.Hash{
			{0x01, 0x02, 0x03},
			{0x04, 0x05, 0x06},
		}

		packet := &GetWitnessMetadataPacket{
			RequestId: 12345,
			GetWitnessMetadataRequest: &GetWitnessMetadataRequest{
				Hashes: hashes,
			},
		}

		var receivedPacket Packet
		backend.handleFunc = func(p *Peer, pk Packet) error {
			receivedPacket = pk
			return nil
		}

		decoder := &mockDecoder{
			decodeFunc: func(val interface{}) error {
				// Simulate decoding - val is **GetWitnessMetadataPacket when using &req
				if reqPtr, ok := val.(**GetWitnessMetadataPacket); ok {
					*reqPtr = packet
				} else if req, ok := val.(*GetWitnessMetadataPacket); ok {
					// Fallback if val is *GetWitnessMetadataPacket
					*req = *packet
					if req.GetWitnessMetadataRequest == nil {
						req.GetWitnessMetadataRequest = packet.GetWitnessMetadataRequest
					}
				}
				return nil
			},
		}

		err := handleGetWitnessMetadata(backend, decoder, realPeer)

		require.NoError(t, err, "handleGetWitnessMetadata should not return error")
		require.NotNil(t, receivedPacket, "Backend.Handle should be called with packet")

		receivedReq, ok := receivedPacket.(*GetWitnessMetadataPacket)
		require.True(t, ok, "Received packet should be *GetWitnessMetadataPacket")
		assert.Equal(t, packet.RequestId, receivedReq.RequestId, "Request ID should match")
		assert.Equal(t, len(hashes), len(receivedReq.Hashes), "Hashes count should match")
		assert.Equal(t, hashes[0], receivedReq.Hashes[0], "First hash should match")
		assert.Equal(t, hashes[1], receivedReq.Hashes[1], "Second hash should match")
	})

	t.Run("DecodeError", func(t *testing.T) {
		backend := &mockBackend{}
		var id enode.ID
		id[0] = 0x02
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		_, rw := p2p.MsgPipe()
		peer := NewPeer(WIT1, p2pPeer, rw, log.New())
		defer peer.Close()

		expectedErr := errors.New("decode error")
		decoder := &mockDecoder{
			decodeFunc: func(val interface{}) error {
				return expectedErr
			},
		}

		err := handleGetWitnessMetadata(backend, decoder, peer)

		require.Error(t, err, "handleGetWitnessMetadata should return error on decode failure")
		assert.Contains(t, err.Error(), "failed to decode GetWitnessMetadataPacket", "Error should mention decode failure")
		assert.Contains(t, err.Error(), "decode error", "Error should contain original error")
	})

	t.Run("EmptyHashes", func(t *testing.T) {
		backend := &mockBackend{}
		var id enode.ID
		id[0] = 0x03
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		_, rw := p2p.MsgPipe()
		peer := NewPeer(WIT1, p2pPeer, rw, log.New())
		defer peer.Close()

		packet := &GetWitnessMetadataPacket{
			RequestId: 67890,
			GetWitnessMetadataRequest: &GetWitnessMetadataRequest{
				Hashes: []common.Hash{}, // Empty hashes
			},
		}

		decoder := &mockDecoder{
			decodeFunc: func(val interface{}) error {
				if reqPtr, ok := val.(**GetWitnessMetadataPacket); ok {
					*reqPtr = packet
				} else if req, ok := val.(*GetWitnessMetadataPacket); ok {
					*req = *packet
					if req.GetWitnessMetadataRequest == nil {
						req.GetWitnessMetadataRequest = packet.GetWitnessMetadataRequest
					}
				}
				return nil
			},
		}

		handleCalled := false
		backend.handleFunc = func(p *Peer, pk Packet) error {
			handleCalled = true
			return nil
		}

		err := handleGetWitnessMetadata(backend, decoder, peer)

		require.Error(t, err, "handleGetWitnessMetadata should return error for empty hashes")
		assert.Contains(t, err.Error(), "invalid GetWitnessMetadataPacket", "Error should mention invalid packet")
		assert.Contains(t, err.Error(), "Hashes cannot be empty", "Error should mention empty hashes")
		assert.False(t, handleCalled, "Backend.Handle should not be called for invalid request")
	})

	t.Run("BackendHandleError", func(t *testing.T) {
		backend := &mockBackend{}
		var id enode.ID
		id[0] = 0x04
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		_, rw := p2p.MsgPipe()
		peer := NewPeer(WIT1, p2pPeer, rw, log.New())
		defer peer.Close()

		hashes := []common.Hash{{0x01, 0x02}}

		packet := &GetWitnessMetadataPacket{
			RequestId: 11111,
			GetWitnessMetadataRequest: &GetWitnessMetadataRequest{
				Hashes: hashes,
			},
		}

		decoder := &mockDecoder{
			decodeFunc: func(val interface{}) error {
				if reqPtr, ok := val.(**GetWitnessMetadataPacket); ok {
					*reqPtr = packet
				} else if req, ok := val.(*GetWitnessMetadataPacket); ok {
					*req = *packet
					if req.GetWitnessMetadataRequest == nil {
						req.GetWitnessMetadataRequest = packet.GetWitnessMetadataRequest
					}
				}
				return nil
			},
		}

		expectedErr := errors.New("backend handle error")
		backend.handleFunc = func(p *Peer, pk Packet) error {
			return expectedErr
		}

		err := handleGetWitnessMetadata(backend, decoder, peer)

		require.Error(t, err, "handleGetWitnessMetadata should return error when backend.Handle fails")
		assert.Equal(t, expectedErr, err, "Error should be the one returned by backend.Handle")
	})

	t.Run("SingleHash", func(t *testing.T) {
		backend := &mockBackend{}
		var id enode.ID
		id[0] = 0x05
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		_, rw := p2p.MsgPipe()
		peer := NewPeer(WIT1, p2pPeer, rw, log.New())
		defer peer.Close()

		hash := common.Hash{0xAA, 0xBB, 0xCC}

		packet := &GetWitnessMetadataPacket{
			RequestId: 22222,
			GetWitnessMetadataRequest: &GetWitnessMetadataRequest{
				Hashes: []common.Hash{hash},
			},
		}

		var receivedPacket Packet
		decoder := &mockDecoder{
			decodeFunc: func(val interface{}) error {
				if reqPtr, ok := val.(**GetWitnessMetadataPacket); ok {
					*reqPtr = packet
				} else if req, ok := val.(*GetWitnessMetadataPacket); ok {
					*req = *packet
					if req.GetWitnessMetadataRequest == nil {
						req.GetWitnessMetadataRequest = packet.GetWitnessMetadataRequest
					}
				}
				return nil
			},
		}

		backend.handleFunc = func(p *Peer, pk Packet) error {
			receivedPacket = pk
			return nil
		}

		err := handleGetWitnessMetadata(backend, decoder, peer)

		require.NoError(t, err, "handleGetWitnessMetadata should not return error")
		receivedReq, ok := receivedPacket.(*GetWitnessMetadataPacket)
		require.True(t, ok, "Received packet should be *GetWitnessMetadataPacket")
		assert.Equal(t, 1, len(receivedReq.Hashes), "Should have one hash")
		assert.Equal(t, hash, receivedReq.Hashes[0], "Hash should match")
	})
}

// TestHandleWitnessMetadata tests the handleWitnessMetadata function
func TestHandleWitnessMetadata(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		var id enode.ID
		id[0] = 0x10
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		app, net := p2p.MsgPipe()
		peer := NewPeer(WIT1, p2pPeer, net, log.New())
		defer peer.Close()

		// Start background reader to prevent dispatchResponse from blocking
		go func() {
			for {
				msg, err := app.ReadMsg()
				if err != nil {
					return
				}
				msg.Discard()
			}
		}()

		requestID := uint64(12345)
		metadata := []WitnessMetadataResponse{
			{
				Hash:        common.Hash{0x01, 0x02, 0x03},
				TotalPages:  10,
				WitnessSize: 1024 * 1024,
				BlockNumber: 100,
				Available:   true,
			},
			{
				Hash:        common.Hash{0x04, 0x05, 0x06},
				TotalPages:  20,
				WitnessSize: 2048 * 1024,
				BlockNumber: 200,
				Available:   true,
			},
		}

		packet := &WitnessMetadataPacket{
			RequestId: requestID,
			Metadata:  metadata,
		}

		decoder := &mockDecoder{
			decodeFunc: func(val interface{}) error {
				if pkt, ok := val.(*WitnessMetadataPacket); ok {
					*pkt = *packet
				}
				return nil
			},
		}

		backend := &mockBackend{}

		err := handleWitnessMetadata(backend, decoder, peer)

		require.NoError(t, err, "handleWitnessMetadata should not return error")
		app.Close()
	})

	t.Run("DecodeError", func(t *testing.T) {
		var id enode.ID
		id[0] = 0x11
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		_, rw := p2p.MsgPipe()
		peer := NewPeer(WIT1, p2pPeer, rw, log.New())
		defer peer.Close()

		expectedErr := errors.New("decode error")
		decoder := &mockDecoder{
			decodeFunc: func(val interface{}) error {
				return expectedErr
			},
		}

		backend := &mockBackend{}

		err := handleWitnessMetadata(backend, decoder, peer)

		require.Error(t, err, "handleWitnessMetadata should return error on decode failure")
		assert.Contains(t, err.Error(), "invalid message", "Error should mention invalid message")
		assert.Contains(t, err.Error(), "decode error", "Error should contain original error")
	})

	t.Run("EmptyMetadata", func(t *testing.T) {
		var id enode.ID
		id[0] = 0x12
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		app, net := p2p.MsgPipe()
		peer := NewPeer(WIT1, p2pPeer, net, log.New())
		defer peer.Close()

		// Start background reader
		go func() {
			for {
				msg, err := app.ReadMsg()
				if err != nil {
					return
				}
				msg.Discard()
			}
		}()

		requestID := uint64(33333)
		packet := &WitnessMetadataPacket{
			RequestId: requestID,
			Metadata:  []WitnessMetadataResponse{}, // Empty metadata
		}

		decoder := &mockDecoder{
			decodeFunc: func(val interface{}) error {
				if pkt, ok := val.(*WitnessMetadataPacket); ok {
					*pkt = *packet
				}
				return nil
			},
		}

		backend := &mockBackend{}

		err := handleWitnessMetadata(backend, decoder, peer)

		require.NoError(t, err, "handleWitnessMetadata should not return error for empty metadata")
		app.Close()
	})

	t.Run("SingleMetadata", func(t *testing.T) {
		var id enode.ID
		id[0] = 0x13
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		app, net := p2p.MsgPipe()
		peer := NewPeer(WIT1, p2pPeer, net, log.New())
		defer peer.Close()

		// Start background reader
		go func() {
			for {
				msg, err := app.ReadMsg()
				if err != nil {
					return
				}
				msg.Discard()
			}
		}()

		requestID := uint64(44444)
		metadata := []WitnessMetadataResponse{
			{
				Hash:        common.Hash{0xFF, 0xEE, 0xDD},
				TotalPages:  5,
				WitnessSize: 512 * 1024,
				BlockNumber: 50,
				Available:   false,
			},
		}

		packet := &WitnessMetadataPacket{
			RequestId: requestID,
			Metadata:  metadata,
		}

		decoder := &mockDecoder{
			decodeFunc: func(val interface{}) error {
				if pkt, ok := val.(*WitnessMetadataPacket); ok {
					*pkt = *packet
				}
				return nil
			},
		}

		backend := &mockBackend{}

		err := handleWitnessMetadata(backend, decoder, peer)

		require.NoError(t, err, "handleWitnessMetadata should not return error")
		app.Close()
	})

	t.Run("LargeMetadata", func(t *testing.T) {
		var id enode.ID
		id[0] = 0x14
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		app, net := p2p.MsgPipe()
		peer := NewPeer(WIT1, p2pPeer, net, log.New())
		defer peer.Close()

		// Start background reader
		go func() {
			for {
				msg, err := app.ReadMsg()
				if err != nil {
					return
				}
				msg.Discard()
			}
		}()

		requestID := uint64(55555)
		// Create a larger metadata array
		metadata := make([]WitnessMetadataResponse, 100)
		for i := 0; i < 100; i++ {
			var hash common.Hash
			hash[0] = byte(i)
			metadata[i] = WitnessMetadataResponse{
				Hash:        hash,
				TotalPages:  uint64(i + 1),
				WitnessSize: uint64(i * 1024),
				BlockNumber: uint64(i * 10),
				Available:   i%2 == 0,
			}
		}

		packet := &WitnessMetadataPacket{
			RequestId: requestID,
			Metadata:  metadata,
		}

		decoder := &mockDecoder{
			decodeFunc: func(val interface{}) error {
				if pkt, ok := val.(*WitnessMetadataPacket); ok {
					*pkt = *packet
				}
				return nil
			},
		}

		backend := &mockBackend{}

		err := handleWitnessMetadata(backend, decoder, peer)

		require.NoError(t, err, "handleWitnessMetadata should handle large metadata arrays")
		app.Close()
	})
}

// TestWit1HandlerMap tests that the wit1 map contains all expected handlers
func TestWit1HandlerMap(t *testing.T) {
	// Verify that wit1 map contains all expected message codes
	expectedHandlers := map[uint64]string{
		GetMsgWitness:         "handleGetWitness",
		MsgWitness:            "handleWitness",
		NewWitnessMsg:         "handleNewWitness",
		NewWitnessHashesMsg:   "handleNewWitnessHashes",
		GetWitnessMetadataMsg: "handleGetWitnessMetadata",
		WitnessMetadataMsg:    "handleWitnessMetadata",
	}

	// Check that all expected handlers are present in wit1
	for msgCode, handlerName := range expectedHandlers {
		handler, exists := wit1[msgCode]
		assert.True(t, exists, "wit1 map should contain handler for message code %d (%s)", msgCode, handlerName)
		assert.NotNil(t, handler, "Handler for message code %d (%s) should not be nil", msgCode, handlerName)
	}

	// Verify that wit1 has exactly 6 handlers (WIT1 has 6 message types)
	assert.Equal(t, 6, len(wit1), "wit1 map should contain exactly 6 handlers")

	// Verify that wit1 includes the WIT1-specific handlers that are not in wit0
	_, exists := wit1[GetWitnessMetadataMsg]
	assert.True(t, exists, "wit1 should contain GetWitnessMetadataMsg handler")
	_, exists = wit1[WitnessMetadataMsg]
	assert.True(t, exists, "wit1 should contain WitnessMetadataMsg handler")
}

// TestHandlerMapVersionSelection tests the switch statement that selects handlers based on protocol version
func TestHandlerMapVersionSelection(t *testing.T) {
	backend := &mockBackend{}

	t.Run("WIT1VersionUsesWit1Map", func(t *testing.T) {
		var id enode.ID
		id[0] = 0x20
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		app, net := p2p.MsgPipe()
		peer := NewPeer(WIT1, p2pPeer, net, log.New())
		defer peer.Close()
		defer app.Close()

		// Test that WIT1 peer can handle GetWitnessMetadataMsg (WIT1-specific)
		packet := &GetWitnessMetadataPacket{
			RequestId: 99999,
			GetWitnessMetadataRequest: &GetWitnessMetadataRequest{
				Hashes: []common.Hash{{0x01, 0x02, 0x03}},
			},
		}

		// Verify backend receives the packet
		var receivedPacket Packet
		backend.handleFunc = func(p *Peer, pk Packet) error {
			receivedPacket = pk
			return nil
		}

		// Call handleMessage in a goroutine first (it will block waiting for message)
		done := make(chan error, 1)
		go func() {
			done <- handleMessage(backend, peer)
		}()

		// Send the message through the pipe (send on app, peer reads from net)
		err := p2p.Send(app, GetWitnessMetadataMsg, packet)
		require.NoError(t, err, "Should be able to send GetWitnessMetadataMsg")

		// Wait for handleMessage to complete
		err = <-done
		require.NoError(t, err, "handleMessage should handle GetWitnessMetadataMsg for WIT1 peer")
		require.NotNil(t, receivedPacket, "Backend should receive the packet")
		_, ok := receivedPacket.(*GetWitnessMetadataPacket)
		assert.True(t, ok, "Received packet should be GetWitnessMetadataPacket")
	})

	t.Run("WIT0VersionUsesWit0Map", func(t *testing.T) {
		var id enode.ID
		id[0] = 0x21
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		app, net := p2p.MsgPipe()
		peer := NewPeer(WIT0, p2pPeer, net, log.New())
		defer peer.Close()
		defer app.Close()

		// Test that WIT0 peer uses wit0 map (which doesn't have GetWitnessMetadataMsg)
		// Send GetWitnessMetadataMsg which should not be handled by WIT0
		packet := &GetWitnessMetadataPacket{
			RequestId: 88888,
			GetWitnessMetadataRequest: &GetWitnessMetadataRequest{
				Hashes: []common.Hash{{0x04, 0x05, 0x06}},
			},
		}

		// Call handleMessage in a goroutine first (it will block waiting for message)
		done := make(chan error, 1)
		go func() {
			done <- handleMessage(backend, peer)
		}()

		// Send the message through the pipe (send on app, peer reads from net)
		err := p2p.Send(app, GetWitnessMetadataMsg, packet)
		require.NoError(t, err, "Should be able to send GetWitnessMetadataMsg")

		// Wait for handleMessage to complete
		err = <-done
		require.Error(t, err, "handleMessage should return error for unsupported message in WIT0")
		assert.Contains(t, err.Error(), "invalid message code", "Error should indicate invalid message code")
	})

	t.Run("DefaultVersionFallsBackToWit0Map", func(t *testing.T) {
		var id enode.ID
		id[0] = 0x22
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		app, net := p2p.MsgPipe()
		// Create peer with unknown version (e.g., 99)
		peer := NewPeer(99, p2pPeer, net, log.New())
		defer peer.Close()
		defer app.Close()

		// Test that unknown version falls back to wit0 map
		// Send GetWitnessMetadataMsg which should not be handled (falls back to wit0)
		packet := &GetWitnessMetadataPacket{
			RequestId: 77777,
			GetWitnessMetadataRequest: &GetWitnessMetadataRequest{
				Hashes: []common.Hash{{0x07, 0x08, 0x09}},
			},
		}

		// Call handleMessage in a goroutine first (it will block waiting for message)
		done := make(chan error, 1)
		go func() {
			done <- handleMessage(backend, peer)
		}()

		// Send the message through the pipe (send on app, peer reads from net)
		err := p2p.Send(app, GetWitnessMetadataMsg, packet)
		require.NoError(t, err, "Should be able to send GetWitnessMetadataMsg")

		// Wait for handleMessage to complete
		err = <-done
		require.Error(t, err, "handleMessage should return error for unsupported message in default version")
		assert.Contains(t, err.Error(), "invalid message code", "Error should indicate invalid message code")
	})

	t.Run("WIT1HandlesAllWit1Messages", func(t *testing.T) {
		// Test all message codes in wit1 map (excluding NewWitnessMsg which requires complex witness structure)
		testCases := []struct {
			name    string
			msgCode uint64
			packet  interface{}
		}{
			{
				name:    "GetMsgWitness",
				msgCode: GetMsgWitness,
				packet: &GetWitnessPacket{
					RequestId: 11111,
					GetWitnessRequest: &GetWitnessRequest{
						WitnessPages: []WitnessPageRequest{
							{Hash: common.Hash{0x01}, Page: 0},
						},
					},
				},
			},
			{
				name:    "NewWitnessHashesMsg",
				msgCode: NewWitnessHashesMsg,
				packet: &NewWitnessHashesPacket{
					Hashes:  []common.Hash{{0x02}},
					Numbers: []uint64{100},
				},
			},
			{
				name:    "GetWitnessMetadataMsg",
				msgCode: GetWitnessMetadataMsg,
				packet: &GetWitnessMetadataPacket{
					RequestId: 22222,
					GetWitnessMetadataRequest: &GetWitnessMetadataRequest{
						Hashes: []common.Hash{{0x03}},
					},
				},
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				var id enode.ID
				id[0] = byte(0x23 + len(tc.name))
				p2pPeer := p2p.NewPeer(id, "test-peer", nil)
				app, net := p2p.MsgPipe()
				peer := NewPeer(WIT1, p2pPeer, net, log.New())
				defer peer.Close()
				defer app.Close()

				var receivedPacket Packet
				backend.handleFunc = func(p *Peer, pk Packet) error {
					receivedPacket = pk
					return nil
				}

				// Call handleMessage in a goroutine first (it will block waiting for message)
				done := make(chan error, 1)
				go func() {
					done <- handleMessage(backend, peer)
				}()

				// Send the message through the pipe (send on app, peer reads from net)
				err := p2p.Send(app, tc.msgCode, tc.packet)
				require.NoError(t, err, "Should be able to send %s", tc.name)

				// Wait for handleMessage to complete
				err = <-done
				require.NoError(t, err, "handleMessage should handle %s for WIT1 peer", tc.name)
				require.NotNil(t, receivedPacket, "Backend should receive the packet for %s", tc.name)
			})
		}
	})

	t.Run("WIT0HandlesWit0Messages", func(t *testing.T) {
		var id enode.ID
		id[0] = 0x24
		p2pPeer := p2p.NewPeer(id, "test-peer", nil)
		app, net := p2p.MsgPipe()
		peer := NewPeer(WIT0, p2pPeer, net, log.New())
		defer peer.Close()
		defer app.Close()

		// Test that WIT0 can handle its supported messages
		var receivedPacket Packet
		backend.handleFunc = func(p *Peer, pk Packet) error {
			receivedPacket = pk
			return nil
		}

		packet := &GetWitnessPacket{
			RequestId: 33333,
			GetWitnessRequest: &GetWitnessRequest{
				WitnessPages: []WitnessPageRequest{
					{Hash: common.Hash{0x04}, Page: 0},
				},
			},
		}

		// Call handleMessage in a goroutine first (it will block waiting for message)
		done := make(chan error, 1)
		go func() {
			done <- handleMessage(backend, peer)
		}()

		// Send the message through the pipe (send on app, peer reads from net)
		err := p2p.Send(app, GetMsgWitness, packet)
		require.NoError(t, err, "Should be able to send GetMsgWitness")

		// Wait for handleMessage to complete
		err = <-done
		require.NoError(t, err, "handleMessage should handle GetMsgWitness for WIT0 peer")
		require.NotNil(t, receivedPacket, "Backend should receive the packet")
	})
}
