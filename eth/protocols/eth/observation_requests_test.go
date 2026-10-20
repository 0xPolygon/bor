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
			peer := &Peer{Peer: configuredLimitsTestPeer(t, "trusted"), rw: &observedWriter{}, knownTxs: newKnownCache(maxKnownTxs)}
			peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe("reader", e) })
			const requestAllowance = peerRequestRate*10 + peerRequestBurst
			for interval := uint64(1); interval <= 5; interval++ {
				for request := 0; request <= requestAllowance; request++ {
					msg := p2p.Msg{Code: tc.code, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
					if err := peer.handleObserved(backend, msg, tc.handler); err != nil {
						t.Fatal(err)
					}
					if request == requestAllowance-1 && tracker.Snapshot("reader").Risk != (interval-1)*20 {
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

// Pooled transaction requests are not covered by the request rate limiter, so
// they carry their own budget. A peer running both kinds of request at the
// block-data ceiling is ordinary traffic and must stay neutral; one budget's
// worth of excess on top of that is still evidence.
func TestPeerPolicyRequestFamiliesDoNotShareBudget(t *testing.T) {
	backend := newTestBackend(0)
	defer backend.close()
	const requestAllowance = peerRequestRate*10 + peerRequestBurst
	encoded := map[uint64][]byte{}
	for _, tc := range []struct {
		code  uint64
		query interface{}
	}{
		{GetBlockBodiesMsg, &GetBlockBodiesPacket{}},
		{GetPooledTransactionsMsg, &GetPooledTransactionsPacket{}},
	} {
		data, err := rlp.EncodeToBytes(tc.query)
		if err != nil {
			t.Fatal(err)
		}
		encoded[tc.code] = data
	}
	tracker := peerpolicy.New(new(mclock.Simulated))
	peer := &Peer{Peer: configuredLimitsTestPeer(t, "trusted"), rw: &observedWriter{}, knownTxs: newKnownCache(maxKnownTxs)}
	peer.SetObserver(func(e peerpolicy.Evidence) { tracker.Observe("reader", e) })
	send := func(code uint64, handler msgHandler, count int) {
		t.Helper()
		for range count {
			msg := p2p.Msg{Code: code, Size: uint32(len(encoded[code])), Payload: bytes.NewReader(encoded[code])}
			if err := peer.handleObserved(backend, msg, handler); err != nil {
				t.Fatal(err)
			}
		}
	}
	send(GetBlockBodiesMsg, handleGetBlockBodies, requestAllowance)
	send(GetPooledTransactionsMsg, handleGetPooledTransactions, requestAllowance)
	if got := tracker.Snapshot("reader"); got.Risk != 0 {
		t.Fatalf("ordinary combined request traffic was penalized: %+v", got)
	}
	send(GetPooledTransactionsMsg, handleGetPooledTransactions, 1)
	got := tracker.Snapshot("reader")
	if got.Risk != 20 || got.Windows[peerpolicy.RequestVolume.String()] != 1 {
		t.Fatalf("pooled request work beyond its own allowance was not observed: %+v", got)
	}
}
