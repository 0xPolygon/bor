// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package eth

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

type observedDecoder struct {
	p2p.Msg
	evidence peerpolicy.Evidence
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
		p.observer(peerpolicy.Evidence{Reason: peerpolicy.InvalidEncoding, Bytes: uint64(msg.Size)})
	}
}

func (p *Peer) handleObserved(backend Backend, msg p2p.Msg, handle msgHandler) error {
	if p.observer == nil {
		return handle(backend, msg, p)
	}
	d := &observedDecoder{Msg: msg, evidence: peerpolicy.Evidence{Family: messageFamily(msg.Code), Items: 1, Bytes: uint64(msg.Size)}}
	err := handle(backend, d, p)
	p.observer(d.evidence)
	return err
}

func messageFamily(code uint64) peerpolicy.Family {
	switch code {
	case NewBlockMsg:
		return peerpolicy.Blocks
	case NewBlockHashesMsg:
		return peerpolicy.BlockAnnouncements
	case NewPooledTransactionHashesMsg:
		return peerpolicy.TransactionAnnouncements
	case GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg, GetPooledTransactionsMsg:
		return peerpolicy.Requests
	default:
		return peerpolicy.Other
	}
}

func observeInvalid(msg Decoder, reason peerpolicy.Reason) {
	if d, ok := msg.(*observedDecoder); ok {
		d.evidence.Reason = reason
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
	d.evidence.Hashes = make([]common.Hash, min(len(packet), 128))
	for i := range d.evidence.Hashes {
		d.evidence.Hashes[i] = packet[i].Hash
	}
}
