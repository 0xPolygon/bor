// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package eth

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

func observedBodyReply(backend Backend, peer *Peer, query GetBlockBodiesPacket) error {
	if peer.observer == nil {
		response := ServiceGetBlockBodiesQuery(backend.Chain(), query.GetBlockBodiesRequest)
		return peer.ReplyBlockBodiesRLP(query.RequestId, response)
	}
	event := peerpolicy.Evidence{Family: peerpolicy.BodyReplies}
	response := serviceGetBlockBodiesQuery(backend.Chain(), query.GetBlockBodiesRequest, func(hash common.Hash, size int) {
		event.Items++
		event.Bytes += uint64(size)
		event.Hashes = append(event.Hashes, hash)
		event.ObjectBytes = append(event.ObjectBytes, uint64(size))
	})
	return peer.writeObservedBodies(query.RequestId, response, event)
}

func (p *Peer) writeObservedBodies(id uint64, bodies []rlp.RawValue, event peerpolicy.Evidence) error {
	// Queue admission is not a completed download. Observe only after the
	// transport write succeeds, including when serving is delayed by a budget.
	err := p.queueReplyWithCompletion(BlockBodiesMsg, &BlockBodiesRLPPacket{
		RequestId:              id,
		BlockBodiesRLPResponse: bodies,
	}, nil, p.replyObserver(event))
	p.observeReplyRefusal(err, event.Hashes)
	return err
}

func (p *Peer) replyObserver(event peerpolicy.Evidence) func(uint64) {
	observer := p.observer
	if observer == nil {
		return nil
	}
	// Capture before queueing: response workers start before SetObserver runs.
	return func(bytes uint64) {
		event.Activity.ServedResponses = 1
		event.Activity.ServedBytes = bytes
		observer(event)
	}
}
