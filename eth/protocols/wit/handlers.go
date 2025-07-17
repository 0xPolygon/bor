package wit

import (
	"fmt"

	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

// validateWitnessPreState validates that the witness pre-state root matches the parent block's state root
func validateWitnessPreState(witness *stateless.Witness, backend Backend, peerID string) error {
	if witness == nil {
		return fmt.Errorf("witness is nil")
	}

	// Check if witness has any headers
	if len(witness.Headers) == 0 {
		return fmt.Errorf("witness has no headers")
	}

	// Get the witness context header (the block this witness is for)
	contextHeader := witness.Header()
	if contextHeader == nil {
		return fmt.Errorf("witness context header is nil")
	}

	// Get the parent block header from the chain
	parentHeader := backend.Chain().GetHeader(contextHeader.ParentHash, contextHeader.Number.Uint64()-1)
	if parentHeader == nil {
		return fmt.Errorf("parent block header not found: parentHash=%x, parentNumber=%d",
			contextHeader.ParentHash, contextHeader.Number.Uint64()-1)
	}

	// Get witness pre-state root (from first header which should be parent)
	witnessPreStateRoot := witness.Root()

	// Compare with actual parent block's state root
	if witnessPreStateRoot != parentHeader.Root {
		return fmt.Errorf("witness pre-state root mismatch: witness=%x, parent=%x, blockNumber=%d",
			witnessPreStateRoot, parentHeader.Root, contextHeader.Number.Uint64())
	}

	// Log successful validation
	log.Info("[Stateless] Witness pre-state validation successful",
		"peer", peerID,
		"blockNumber", contextHeader.Number.Uint64(),
		"blockHash", contextHeader.Hash(),
		"preStateRoot", witnessPreStateRoot,
		"parentHash", contextHeader.ParentHash)

	return nil
}

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

	// Log witness reception from peer
	log.Info("[Stateless] Received witness response from peer",
		"peer", peer.ID(),
		"requestID", packet.RequestId,
		"witnessCount", len(packet.WitnessPacketResponse))

	// Validate each witness in the response
	for i, witnessRLP := range packet.WitnessPacketResponse {
		witness := new(stateless.Witness)
		if err := rlp.DecodeBytes(witnessRLP, witness); err != nil {
			log.Error("[Stateless] Failed to decode witness RLP in response",
				"peer", peer.ID(),
				"requestID", packet.RequestId,
				"witnessIndex", i,
				"err", err)
			continue // Skip invalid witness but continue processing others
		}

		// Validate witness pre-state root
		if err := validateWitnessPreState(witness, backend, peer.ID()); err != nil {
			log.Error("[Stateless] Witness pre-state validation failed",
				"peer", peer.ID(),
				"requestID", packet.RequestId,
				"witnessIndex", i,
				"err", err)
			// Continue processing other witnesses even if one fails
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

	// Log witness reception from peer
	if req.Witness != nil {
		log.Info("[Stateless] Received new witness broadcast from peer",
			"peer", peer.ID(),
			"blockNumber", req.Witness.Header().Number,
			"blockHash", req.Witness.Header().Hash(),
			"preStateRoot", req.Witness.Root(),
			"headerCount", len(req.Witness.Headers),
			"stateNodeCount", len(req.Witness.State),
			"codeCount", len(req.Witness.Codes))

		// Validate witness pre-state root
		if err := validateWitnessPreState(req.Witness, backend, peer.ID()); err != nil {
			log.Error("[Stateless] Witness pre-state validation failed for new witness broadcast",
				"peer", peer.ID(),
				"err", err)
			// Don't forward invalid witness to backend
			return fmt.Errorf("invalid witness broadcast: %w", err)
		}
	} else {
		log.Info("[Stateless] Received new witness broadcast from peer with nil witness", "peer", peer.ID())
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
