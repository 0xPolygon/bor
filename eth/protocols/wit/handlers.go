package wit

import (
	"fmt"

	"github.com/ethereum/go-ethereum/log"
)

// handleGetWitness processes a GetWitnessPacket request from a peer.
func handleGetWitness(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the GetWitnessPacket request
	req := new(GetWitnessPacket)
	if err := msg.Decode(&req); err != nil {
		return fmt.Errorf("failed to decode GetWitnessPacket: %w", err)
	}

	// Validate request parameters
	if len(req.WitnessPages) == 0 {
		return fmt.Errorf("invalid GetWitnessPacket: Hashes cannot be empty")
	}

	return backend.Handle(peer, req)
}

// handleWitness processes an incoming witness response from a peer.
func handleWitness(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the WitnessPacketRLPPacket response
	packet := new(WitnessPacketRLPPacket)
	if err := msg.Decode(packet); err != nil {
		log.Error("Failed to decode witness response packet", "err", err)
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}

	// Construct the response object, putting the entire decoded packet into Res
	res := &Response{
		id:   packet.RequestId,
		code: MsgWitness,
		Res:  packet, // Assign the *entire* packet, not just packet.WitnessPacketResponse
	}

	// Forward the response to the dispatcher
	log.Debug("Dispatching witness response packet", "peer", peer.ID(), "reqID", packet.RequestId, "count", len(packet.WitnessPacketResponse))
	return peer.dispatchResponse(res, nil)
}

func handleNewWitness(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the NewWitnessPacket request
	req := new(NewWitnessPacket)
	if err := msg.Decode(&req); err != nil {
		return fmt.Errorf("failed to decode NewWitnessPacket: %w", err)
	}

	return backend.Handle(peer, req)
}

func handleNewWitnessHashes(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the NewWitnessHashesPacket request
	req := new(NewWitnessHashesPacket)
	if err := msg.Decode(&req); err != nil {
		return fmt.Errorf("failed to decode NewWitnessHashesPacket: %w", err)
	}

	return backend.Handle(peer, req)
}

// handleGetWitnessMetadata processes a GetWitnessMetadataPacket request from a peer.
func handleGetWitnessMetadata(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the GetWitnessMetadataPacket request
	req := new(GetWitnessMetadataPacket)
	if err := msg.Decode(&req); err != nil {
		return fmt.Errorf("failed to decode GetWitnessMetadataPacket: %w", err)
	}

	// Validate request parameters
	if len(req.Hashes) == 0 {
		return fmt.Errorf("invalid GetWitnessMetadataPacket: Hashes cannot be empty")
	}

	return backend.Handle(peer, req)
}

// handleWitnessMetadata processes an incoming witness metadata response from a peer.
func handleWitnessMetadata(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the WitnessMetadataPacket response
	packet := new(WitnessMetadataPacket)
	if err := msg.Decode(packet); err != nil {
		log.Error("Failed to decode witness metadata response packet", "err", err)
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}

	// Construct the response object
	res := &Response{
		id:   packet.RequestId,
		code: WitnessMetadataMsg,
		Res:  packet,
	}

	// Forward the response to the dispatcher
	log.Debug("Dispatching witness metadata response packet", "peer", peer.ID(), "reqID", packet.RequestId, "count", len(packet.Metadata))
	return peer.dispatchResponse(res, nil)
}

// handleGetCompactWitness processes a GetCompactWitnessPacket request from a peer.
// Uses GetCompactWitnessPacket marker type to distinguish from regular witness requests.
func handleGetCompactWitness(backend Backend, msg Decoder, peer *Peer) error {
	// Decode into GetWitnessRequest first (wire format)
	wireReq := new(GetWitnessPacket)
	if err := msg.Decode(&wireReq); err != nil {
		return fmt.Errorf("failed to decode GetCompactWitnessPacket: %w", err)
	}

	// Validate request parameters
	if len(wireReq.WitnessPages) == 0 {
		return fmt.Errorf("invalid GetCompactWitnessPacket: WitnessPages cannot be empty")
	}

	// Wrap in marker type for backend
	req := &GetCompactWitnessPacket{
		RequestId:         wireReq.RequestId,
		GetWitnessRequest: wireReq.GetWitnessRequest,
	}

	return backend.Handle(peer, req)
}

// handleCompactWitness processes an incoming compact witness response from a peer.
// Reuses WitnessPacketRLPPacket structure since format is identical.
func handleCompactWitness(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the WitnessPacketRLPPacket response (same structure for compact witness)
	packet := new(WitnessPacketRLPPacket)
	if err := msg.Decode(packet); err != nil {
		log.Error("Failed to decode compact witness response packet", "err", err)
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}

	// Construct the response object
	res := &Response{
		id:   packet.RequestId,
		code: CompactWitnessMsg, // Different message code for routing
		Res:  packet,
	}

	// Forward the response to the dispatcher
	log.Debug("Dispatching compact witness response packet", "peer", peer.ID(), "reqID", packet.RequestId, "count", len(packet.WitnessPacketResponse))
	return peer.dispatchResponse(res, nil)
}
