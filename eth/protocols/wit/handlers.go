package wit

import (
	"fmt"

	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

// handleGetWitness processes a GetWitnessPacket request from a peer.
func handleGetWitness(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the GetWitnessPacket request
	req := new(GetWitnessPacket)
	if err := msg.Decode(&req); err != nil {
		return fmt.Errorf("failed to decode GetWitnessPacket: %w", err)
	}

	// Validate request parameters
	if len(req.Hashes) == 0 {
		return fmt.Errorf("invalid GetWitnessPacket: Hashes cannot be empty")
	}

	return backend.Handle(peer, req)
}

// handleWitness processes an incoming witness response from a peer.
func handleWitness(backend Backend, msg Decoder, peer *Peer) error {
	// Decode the WitnessPacketRLPPacket response
	packet := new(WitnessPacketRLPPacket)
	if err := msg.Decode(&packet); err != nil {
		log.Error("Failed to decode witness response packet", "err", err)
		return fmt.Errorf("%w: message %v: %v", errDecode, msg, err)
	}

	// Validate each witness in the response.
	for i, witnessRLP := range packet.WitnessPacketResponse {
		witness := new(stateless.Witness)
		if err := rlp.DecodeBytes(witnessRLP, witness); err != nil {
			log.Error("Failed to decode witness RLP in response",
				"peer", peer.ID(),
				"requestID", packet.RequestId,
				"witnessIndex", i,
				"err", err,
			)
			continue
		}

		// Validate witness pre-state root.
		if err := stateless.ValidateWitnessPreState(witness, backend.Chain(), peer.ID()); err != nil {
			log.Error("Witness pre-state validation failed",
				"peer", peer.ID(),
				"requestID", packet.RequestId,
				"witnessIndex", i,
				"err", err,
			)
			continue
		}
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

	if req.Witness != nil {
		if err := stateless.ValidateWitnessPreState(req.Witness, backend.Chain(), peer.ID()); err != nil {
			log.Error("Witness pre-state validation failed for new witness broadcast",
				"peer", peer.ID(),
				"err", err,
			)
			return fmt.Errorf("invalid witness broadcast: %w", err)
		}
	} else {
		return fmt.Errorf("received nil witness in NewWitnessPacket")
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
