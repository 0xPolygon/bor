// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package eth

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

func TestMessageSizeLimits(t *testing.T) {
	tests := []struct {
		name     string
		code     uint64
		size     uint32
		tooLarge bool
	}{
		{"regular message at limit", BlockBodiesMsg, maxMessageSize, false},
		{"regular message above limit", BlockBodiesMsg, maxMessageSize + 1, true},
		{"receipt request above regular limit", GetReceiptsMsg, maxMessageSize + 1, true},
		{"receipt response above regular limit", ReceiptsMsg, maxMessageSize + 1, false},
		{"receipt response at RLPx limit", ReceiptsMsg, 16_777_215, false},
		{"receipt response above RLPx limit", ReceiptsMsg, 16_777_216, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rw := &limitTestRW{msg: p2p.Msg{Code: tt.code, Size: tt.size, Payload: bytes.NewReader(nil)}}
			peer := NewPeer(ETH69, p2p.NewPeer(enode.ID{1}, "", nil), rw, nil)
			defer peer.Close()
			err := handleMessage(nil, peer)
			if got := errors.Is(err, errMsgTooLarge); got != tt.tooLarge {
				t.Fatalf("message size rejection = %v, want %v (error: %v)", got, tt.tooLarge, err)
			}
		})
	}
}

func TestLargeReceiptsDelivery(t *testing.T) {
	for _, count := range []int{75415, 60336} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			testLargeReceiptsDelivery(t, largeTestReceipts(count))
		})
	}
}

func largeTestReceipts(count int) types.Receipts {
	receipts := make(types.Receipts, 5)
	for i := range receipts {
		receipts[i] = &types.Receipt{Status: 1, CumulativeGasUsed: uint64(i+1) * 21000}
	}
	for i := 0; i < count; i++ {
		receipt := receipts[i%len(receipts)]
		receipt.Logs = append(receipt.Logs, &types.Log{
			Address: common.Address{1},
			Topics:  []common.Hash{{1}, {2}, {3}, {4}},
			Data:    bytes.Repeat([]byte{5}, 32),
		})
	}
	for _, receipt := range receipts {
		receipt.Bloom = types.CreateBloom(receipt)
	}
	return receipts
}

func testLargeReceiptsDelivery(t *testing.T, receipts types.Receipts) {
	t.Helper()
	app, net := p2p.MsgPipe()
	defer app.Close()
	client := NewPeer(ETH69, p2p.NewPeer(enode.ID{1}, "receipt-client", nil), net, nil)
	defer client.Close()
	server := NewPeer(ETH69, p2p.NewPeer(enode.ID{2}, "receipt-server", nil), app, nil)
	defer server.Close()

	replied := make(chan error, 1)
	go func() { replied <- queueLargeReceiptReply(server, receipts) }()
	sink := make(chan *Response)
	req, err := client.RequestReceipts([]common.Hash{{1}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer req.Close()
	if err := <-replied; err != nil {
		t.Fatalf("queue receipt reply: %v", err)
	}

	handled := make(chan error, 1)
	go func() { handled <- handleMessage(nil, client) }()
	select {
	case res := <-sink:
		res.Done <- nil
		_, hash := EncodeReceiptsAndPrepareHasher(res.Res, nil)
		if hash == nil {
			t.Fatal("unexpected receipt response type")
		}
		if got, want := hash(0, nil), types.DeriveSha(receipts, trie.NewStackTrie(nil)); got != want {
			t.Fatalf("receipt root mismatch: got %s want %s", got, want)
		}
	case err := <-handled:
		t.Fatalf("receipt delivery failed: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("receipt delivery timed out")
	}
	if err := <-handled; err != nil {
		t.Fatal(err)
	}
}

func queueLargeReceiptReply(server *Peer, receipts types.Receipts) error {
	msg, err := server.rw.ReadMsg()
	if err != nil {
		return err
	}
	var query GetReceiptsPacket
	if err := msg.Decode(&query); err != nil {
		return err
	}
	encoded, err := rlp.EncodeToBytes(NewReceiptList69(receipts))
	if err != nil {
		return err
	}
	size := rlp.ListSize(uint64(rlp.IntSize(query.RequestId)) + rlp.ListSize(uint64(len(encoded))))
	if size <= maxMessageSize || size > maxReceiptsMessageSize {
		return fmt.Errorf("unexpected receipt packet size %d", size)
	}
	return server.ReplyReceiptsRLP(query.RequestId, []rlp.RawValue{encoded})
}
