// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package eth

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

type observedDecoder struct {
	p2p.Msg
	evidence   peerpolicy.Evidence
	diagnostic peerpolicy.Reason
}

func (d *observedDecoder) Decode(value interface{}) error {
	err := d.Msg.Decode(value)
	if err != nil {
		d.evidence.Reason = peerpolicy.InvalidEncoding
	}
	return err
}

// SetObserver must be called before the inbound message loop starts.
func (p *Peer) SetObserver(observer func(peerpolicy.Evidence)) {
	p.observer = observer
}

func (p *Peer) observeFraming(msg p2p.Msg) {
	if p.observer != nil {
		p.observer(peerpolicy.Evidence{Reason: peerpolicy.InvalidEncoding, Bytes: uint64(msg.Size), Activity: receivedActivity(msg)})
	}
}

func (p *Peer) handleObserved(backend Backend, msg p2p.Msg, handle msgHandler) error {
	if p.observer == nil {
		return handle(backend, msg, p)
	}
	d := &observedDecoder{Msg: msg, evidence: peerpolicy.Evidence{Family: messageFamily(msg.Code), Items: 1, Bytes: uint64(msg.Size), Activity: receivedActivity(msg)}}
	err := handle(backend, d, p)
	if d.diagnostic != peerpolicy.None {
		p.observer(peerpolicy.Evidence{Reason: d.diagnostic})
	}
	p.observer(d.evidence)
	return err
}

func receivedActivity(msg p2p.Msg) peerpolicy.ActivityEvent {
	activity := peerpolicy.ActivityEvent{ReceivedMessages: 1, ReceivedBytes: uint64(msg.Size)}
	switch msg.Code {
	case NewBlockMsg, BlockHeadersMsg, BlockBodiesMsg, ReceiptsMsg:
		// Validation is deferred to consumers outside the message handler.
		activity.UnclassifiedMessages = 1
	}
	return activity
}

func messageFamily(code uint64) peerpolicy.Family {
	switch code {
	case NewBlockMsg:
		return peerpolicy.Blocks
	case NewBlockHashesMsg:
		return peerpolicy.BlockAnnouncements
	case NewPooledTransactionHashesMsg:
		return peerpolicy.TransactionAnnouncements
	case GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg:
		return peerpolicy.Requests
	case GetPooledTransactionsMsg:
		// checkMessageRate does not cover this code; reservePooledReply does.
		return peerpolicy.TransactionRequests
	default:
		return peerpolicy.Other
	}
}

func observeInvalid(msg Decoder, reason peerpolicy.Reason) {
	if d, ok := msg.(*observedDecoder); ok {
		d.evidence.Reason = reason
	}
}

func observeIgnoredTransactions(msg Decoder) {
	if d, ok := msg.(*observedDecoder); ok {
		// Count wire bytes without decoding ignored payloads. Unknown correlation
		// is a separate diagnostic so it cannot suppress volume accounting.
		if d.Code == PooledTransactionsMsg {
			d.diagnostic = peerpolicy.UnknownTransactionDelivery
		}
		d.evidence.Family = peerpolicy.Transactions
		d.evidence.Items = 0
		d.evidence.Activity.UnclassifiedMessages = 1
	}
}

func observeForwardedTransactions(msg Decoder) {
	if d, ok := msg.(*observedDecoder); ok {
		// The fetcher owns traffic accounting once it accepts the delivery.
		d.evidence.Items = 0
		d.evidence.Bytes = 0
	}
}

func observeAnnouncements(msg Decoder, hashes []common.Hash) {
	if d, ok := msg.(*observedDecoder); ok {
		d.evidence.Hashes = hashes
		d.evidence.Items = uint64(len(hashes))
	}
}

func observeBlockAnnouncements(msg Decoder, packet NewBlockHashesPacket) {
	d, ok := msg.(*observedDecoder)
	if !ok {
		return
	}
	d.evidence.Items = uint64(len(packet))
	d.evidence.Hashes = make([]common.Hash, len(packet))
	for i := range d.evidence.Hashes {
		d.evidence.Hashes[i] = packet[i].Hash
	}
}

func (p *Peer) observeRateLimit(msg p2p.Msg) {
	if p.observer != nil {
		p.observer(peerpolicy.Evidence{Reason: peerpolicy.RateLimited, Family: messageFamily(msg.Code), Items: 1, Bytes: uint64(msg.Size), Activity: receivedActivity(msg)})
	}
}

func (p *Peer) observeReplyRefusal(err error, hashes []common.Hash) {
	if err == errReplyQueueFull && p.observer != nil {
		p.observer(peerpolicy.Evidence{Refused: true, Hashes: hashes})
	}
}

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
