// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package eth

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

// TestResendAfterCancelIsDropped covers a cancellation landing between
// requestPartialReceipts releasing receiptBufferLock and the dispatcher
// servicing the continuation. The continuation must be dropped rather than
// become the pending entry, so the peer's final response is discarded instead
// of blocking the read loop on a request nobody is waiting for.
func TestResendAfterCancelIsDropped(t *testing.T) {
	app, net := p2p.MsgPipe()
	defer app.Close()
	go func() {
		for {
			msg, err := app.ReadMsg()
			if err != nil {
				return
			}
			msg.Discard()
		}
	}()
	var id enode.ID
	rand.Read(id[:])
	p := NewPeer(ETH70, p2p.NewPeer(id, "x", nil), net, nil, params.TestChainConfig)
	defer p.Close()

	sink := make(chan *Response, 1)
	req, err := p.RequestReceipts([]common.Hash{{1}}, []uint64{1_000_000}, []uint64{1}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.bufferReceipts(req.id, []*ReceiptList69{{items: []Receipt{{Logs: rlp.EmptyList}}}}, true); err != nil {
		t.Fatal(err)
	}
	p.receiptBufferLock.Lock()
	hashes := p.receiptBuffer[req.id].request
	p.receiptBufferLock.Unlock()

	req.Close()

	if err := p.dispatchResend(req.id, GetReceiptsMsg, &GetReceiptsPacket70{
		RequestId: req.id, FirstBlockReceiptIndex: 1, GetReceiptsRequest: hashes,
	}); err != nil {
		t.Fatal(err)
	}
	p.receiptBufferLock.Lock()
	_, buffered := p.receiptBuffer[req.id]
	p.receiptBufferLock.Unlock()
	if buffered {
		t.Fatal("receipt buffer entry survived the cancellation")
	}

	done := make(chan error, 1)
	go func() {
		lists := []*ReceiptList69{{}}
		done <- p.dispatchResponse(&Response{id: req.id, code: ReceiptsMsg, Res: &lists}, nil)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatchResponse blocked on a cancelled request")
	}
	select {
	case <-sink:
		t.Fatal("late response was delivered for a cancelled request")
	default:
	}
}
