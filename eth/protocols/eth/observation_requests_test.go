package eth

import (
	"bytes"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestPeerPolicyRequestOnlyHandlers(t *testing.T) {
	backend := newTestBackend(0)
	defer backend.close()
	for _, tc := range []struct {
		name    string
		code    uint64
		query   interface{}
		handler msgHandler
	}{
		{"headers", GetBlockHeadersMsg, &GetBlockHeadersPacket{GetBlockHeadersRequest: &GetBlockHeadersRequest{}}, handleGetBlockHeaders},
		{"bodies", GetBlockBodiesMsg, &GetBlockBodiesPacket{}, handleGetBlockBodies},
		{"receipts68", GetReceiptsMsg, &GetReceiptsPacket{}, handleGetReceipts68},
		{"receipts69", GetReceiptsMsg, &GetReceiptsPacket{}, handleGetReceipts69},
		{"pooled transactions", GetPooledTransactionsMsg, &GetPooledTransactionsPacket{}, handleGetPooledTransactions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := rlp.EncodeToBytes(tc.query)
			if err != nil {
				t.Fatal(err)
			}
			clock := new(mclock.Simulated)
			tracker := peerpolicy.New(clock)
			peer := &Peer{rw: &observedWriter{}, knownTxs: newKnownCache(maxKnownTxs)}
			peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe("reader", e) })
			for interval := uint64(1); interval <= 5; interval++ {
				for request := 0; request < 641; request++ {
					msg := p2p.Msg{Code: tc.code, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
					if err := peer.handleObserved(backend, msg, tc.handler); err != nil {
						t.Fatal(err)
					}
					if request == 639 && tracker.Snapshot("reader").Risk != (interval-1)*20 {
						t.Fatal("requests within allowance were penalized")
					}
				}
				got := tracker.Snapshot("reader")
				if got.Risk != interval*20 || got.Windows[peerpolicy.RequestVolume.String()] != interval {
					t.Fatalf("interval %d: %+v", interval, got)
				}
				clock.Run(10 * time.Second)
			}
		})
	}
}
