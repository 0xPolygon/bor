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
		if len(event.Hashes) < 128 {
			event.Hashes = append(event.Hashes, hash)
			event.ObjectBytes = append(event.ObjectBytes, uint64(size))
		}
	})
	return peer.writeObservedBodies(query.RequestId, response, event)
}

func (p *Peer) writeObservedBodies(id uint64, bodies []rlp.RawValue, event peerpolicy.Evidence) error {
	if err := p.ReplyBlockBodiesRLP(id, bodies); err != nil {
		return err
	}
	// Write completion is local, not a remote acknowledgement. The tracker
	// permits a retry and only scores sustained excess repeated body bytes.
	p.observer(event)
	return nil
}
