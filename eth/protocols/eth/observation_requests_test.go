package eth

import (
	"bytes"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
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

func TestPeerPolicyQueuedBodyCompletion(t *testing.T) {
	for _, failure := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			p, rw := newObservedReplyTestPeer(t)
			if failure {
				rw.err = errors.New("write failed")
			}
			var got []peerpolicy.Evidence
			p.SetObserver(func(e peerpolicy.Evidence) { got = append(got, e) })
			event := peerpolicy.Evidence{Family: peerpolicy.BodyReplies, Items: 1, Bytes: 1}
			if err := p.writeObservedBodies(1, []rlp.RawValue{{0xc0}}, event); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if failure {
				if len(got) != 0 || len(rw.sent) != 0 {
					t.Fatal("failed write was recorded as a completed download")
				}
			} else if len(got) != 1 || len(rw.sent) != 1 || got[0].Family != event.Family || got[0].Bytes != 1 || got[0].Items != 1 {
				t.Fatalf("missing completed download: %+v", got)
			}
		})
	}
}

func TestPeerPolicyQueuedBodyDelay(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			p, rw := newObservedReplyTestPeer(t)
			p.blockReplies.limiter.SetLimit(10)
			if !p.blockReplies.limiter.AllowN(time.Now(), peerByteBurst) {
				t.Fatal("could not exhaust reply allowance")
			}
			var calls atomic.Int64
			p.SetObserver(func(peerpolicy.Evidence) { calls.Add(1) })
			if err := p.writeObservedBodies(1, nil, peerpolicy.Evidence{Family: peerpolicy.BodyReplies}); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			if calls.Load() != 0 || len(rw.sent) != 0 {
				t.Fatal("queue admission was recorded as a completed download")
			}
			if cancel {
				p.Close()
			}
			time.Sleep(time.Second)
			synctest.Wait()
			want := 1
			if cancel {
				want = 0
			}
			if calls.Load() != int64(want) || len(rw.sent) != want {
				t.Fatalf("completion calls=%d sends=%d want=%d", calls.Load(), len(rw.sent), want)
			}
		})
	}
}

func newObservedReplyTestPeer(t *testing.T) (*Peer, *replyTestRW) {
	t.Helper()
	rw := &replyTestRW{sent: make(chan p2p.Msg, peerRequestBurst)}
	p := NewPeer(ETH68, p2p.NewPeer(enode.ID{1}, "", nil), rw, nil)
	t.Cleanup(func() {
		select {
		case <-p.term:
		default:
			p.Close()
		}
	})
	return p, rw
}
