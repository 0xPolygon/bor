package eth

import (
	"bytes"
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

func TestLargeReceiptsDelivery(t *testing.T) {
	for _, count := range []int{75415, 60336} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			receipts := make(types.Receipts, 5)
			for i := range receipts {
				receipts[i] = &types.Receipt{Status: 1, CumulativeGasUsed: uint64(i+1) * 21000}
			}
			for i := 0; i < count; i++ {
				r := receipts[i%len(receipts)]
				r.Logs = append(r.Logs, &types.Log{
					Address: common.Address{1}, Topics: []common.Hash{{1}, {2}, {3}, {4}}, Data: bytes.Repeat([]byte{5}, 32),
				})
			}
			for _, receipt := range receipts {
				receipt.Bloom = types.CreateBloom(receipt)
			}
			app, net := p2p.MsgPipe()
			defer app.Close()
			peer := NewPeer(ETH69, p2p.NewPeer(enode.ID{1}, "large-receipts", nil), net, nil)
			defer peer.Close()
			serverDone := make(chan error, 1)
			go func() {
				msg, err := app.ReadMsg()
				if err != nil {
					serverDone <- err
					return
				}
				var query GetReceiptsPacket
				if err := msg.Decode(&query); err != nil {
					serverDone <- err
					return
				}
				packet := &ReceiptsPacket[*ReceiptList69]{RequestId: query.RequestId, List: []*ReceiptList69{NewReceiptList69(receipts)}}
				encoded, err := rlp.EncodeToBytes(packet)
				if err != nil {
					serverDone <- err
					return
				}
				if len(encoded) <= maxMessageSize || len(encoded) > maxReceiptsMessageSize {
					serverDone <- fmt.Errorf("unexpected receipt packet size %d", len(encoded))
					return
				}
				t.Logf("Sending %d receipts with %d logs: %d bytes", len(receipts), count, len(encoded))
				serverDone <- app.WriteMsg(p2p.Msg{Code: ReceiptsMsg, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)})
			}()
			sink := make(chan *Response)
			req, err := peer.RequestReceipts([]common.Hash{{1}}, sink)
			if err != nil {
				t.Fatal(err)
			}
			defer req.Close()
			handled := make(chan error, 1)
			go func() { handled <- handleMessage(nil, peer) }()
			select {
			case res := <-sink:
				_, hash := EncodeReceiptsAndPrepareHasher(res.Res, nil)
				if hash == nil {
					t.Fatal("unexpected receipt response type")
				}
				got := hash(0, nil)
				want := types.DeriveSha(receipts, trie.NewStackTrie(nil))
				res.Done <- nil
				if got != want {
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
			if err := <-serverDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
