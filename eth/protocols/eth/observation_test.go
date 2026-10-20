package eth

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestPeerPolicyMessageObservation(t *testing.T) {
	var got []peerpolicy.Evidence
	peer := &Peer{}
	peer.SetObserver(func(e peerpolicy.Evidence) { got = append(got, e) })
	wantErr := errors.New("local failure")
	err := peer.handleObserved(nil, p2p.Msg{Code: NewBlockMsg, Size: 123}, func(_ Backend, msg Decoder, _ *Peer) error {
		observeInvalid(msg, peerpolicy.InvalidBlock)
		return wantErr
	})
	if !errors.Is(err, wantErr) || len(got) != 1 {
		t.Fatalf("changed handler result: %v %+v", err, got)
	}
	if got[0].Reason != peerpolicy.InvalidBlock || got[0].Family != peerpolicy.Blocks || got[0].Bytes != 123 {
		t.Fatalf("wrong evidence: %+v", got[0])
	}
}

func TestPeerPolicyDecodeAndLocalErrors(t *testing.T) {
	for _, decode := range []bool{false, true} {
		var got peerpolicy.Evidence
		p := &Peer{observer: func(e peerpolicy.Evidence) { got = e }}
		err := p.handleObserved(nil, p2p.Msg{Payload: bytes.NewReader(nil)}, func(_ Backend, msg Decoder, _ *Peer) error {
			if decode {
				return msg.Decode(new(GetBlockBodiesPacket))
			}
			return errors.New("database unavailable")
		})
		if err == nil {
			t.Fatal("expected error")
		}
		want := peerpolicy.None
		if decode {
			want = peerpolicy.InvalidEncoding
		}
		if got.Reason != want {
			t.Fatalf("reason = %v want %v", got.Reason, want)
		}
	}
}

func TestPeerPolicyDisabledPreservesDecoder(t *testing.T) {
	peer := &Peer{}
	called := false
	err := peer.handleObserved(nil, p2p.Msg{}, func(_ Backend, msg Decoder, _ *Peer) error {
		_, called = msg.(p2p.Msg)
		return nil
	})
	if err != nil || !called {
		t.Fatal("disabled observation changed decoder")
	}
}

func TestPeerPolicyAnnouncementCapture(t *testing.T) {
	d := &observedDecoder{}
	packet := make(NewBlockHashesPacket, 1024)
	packet[0].Hash = common.Hash{1}
	observeBlockAnnouncements(d, packet)
	if d.evidence.Items != 1024 || len(d.evidence.Hashes) != len(packet) || d.evidence.Hashes[0] != packet[0].Hash {
		t.Fatal("block announcement capture omitted hashes")
	}
	hashes := []common.Hash{{2}, {3}}
	observeAnnouncements(d, hashes)
	if d.evidence.Items != 2 || d.evidence.Hashes[1] != hashes[1] {
		t.Fatal("missing hashes")
	}
}

func TestPeerPolicyMessageFamilies(t *testing.T) {
	for code, want := range map[uint64]peerpolicy.Family{
		NewBlockMsg: peerpolicy.Blocks, NewBlockHashesMsg: peerpolicy.BlockAnnouncements,
		NewPooledTransactionHashesMsg: peerpolicy.TransactionAnnouncements,
		GetBlockHeadersMsg:            peerpolicy.Requests, GetBlockBodiesMsg: peerpolicy.Requests,
		GetReceiptsMsg: peerpolicy.Requests, GetPooledTransactionsMsg: peerpolicy.TransactionRequests, BlockBodiesMsg: peerpolicy.Other,
		TransactionsMsg: peerpolicy.Other, PooledTransactionsMsg: peerpolicy.Other,
	} {
		if got := messageFamily(code); got != want {
			t.Fatalf("%d: %v != %v", code, got, want)
		}
	}
}

type observationReader struct{ p2p.Msg }

func (r observationReader) ReadMsg() (p2p.Msg, error) { return r.Msg, nil }
func (r observationReader) WriteMsg(p2p.Msg) error    { return errors.New("not used") }

func TestPeerPolicyFraming(t *testing.T) {
	for _, msg := range []p2p.Msg{
		{Code: NewBlockMsg, Size: maxMessageSize + 1},
		{Code: GetPooledTransactionsMsg, Size: uint32(rlp.ListSize(uint64(rlp.IntSize(math.MaxUint64))+rlp.ListSize(maxPooledTxsServe*(common.HashLength+1)))) + 1},
		{Code: 0xff, Payload: bytes.NewReader(nil)},
	} {
		var got peerpolicy.Evidence
		p := NewPeer(ETH68, p2p.NewPeer(enode.ID{1}, "", nil), observationReader{msg}, nil)
		defer p.Close()
		p.SetObserver(func(e peerpolicy.Evidence) { got = e })
		if err := handleMessage(nil, p); err == nil {
			t.Fatal("expected framing failure")
		}
		if got.Reason != peerpolicy.InvalidEncoding || got.Bytes != uint64(msg.Size) {
			t.Fatalf("wrong framing evidence: %+v", got)
		}
	}
}

func TestPeerActivityReceivesWithoutContribution(t *testing.T) {
	for _, code := range []uint64{NewBlockMsg, NewBlockHashesMsg, NewPooledTransactionHashesMsg, GetBlockHeadersMsg, GetBlockBodiesMsg, GetReceiptsMsg, GetPooledTransactionsMsg, BlockHeadersMsg, BlockBodiesMsg, ReceiptsMsg} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			tracker := peerpolicy.New(new(mclock.Simulated))
			var got peerpolicy.Evidence
			p := &Peer{observer: func(e peerpolicy.Evidence) { got = e; tracker.Observe("peer", e) }}
			if err := p.handleObserved(nil, p2p.Msg{Code: code, Size: 50}, func(Backend, Decoder, *Peer) error { return nil }); err != nil {
				t.Fatal(err)
			}
			want := peerpolicy.ActivityEvent{ReceivedMessages: 1, ReceivedBytes: 50}
			if code == NewBlockMsg || code == BlockHeadersMsg || code == BlockBodiesMsg || code == ReceiptsMsg {
				want.UnclassifiedMessages = 1
			}
			if got.Activity != want || tracker.Snapshot("peer").Risk != 0 {
				t.Fatalf("ordinary message produced contribution or risk: %+v", got)
			}
		})
	}
}

func TestPeerActivityCompletedResponses(t *testing.T) {
	for _, mode := range []string{"ordinary", "trusted", "static"} {
		t.Run(mode, func(t *testing.T) {
			local, remote := p2p.MsgPipe()
			base := p2p.NewPeer(enode.ID{1}, "", nil)
			if mode != "ordinary" {
				base = configuredLimitsTestPeer(t, mode)
			}
			p := NewPeer(ETH69, base, local, nil)
			t.Cleanup(func() { p.Close(); local.Close(); remote.Close() })
			events := make(chan peerpolicy.Evidence, 4)
			p.SetObserver(func(e peerpolicy.Evidence) { events <- e })
			for _, code := range []uint64{BlockHeadersMsg, BlockBodiesMsg, ReceiptsMsg, PooledTransactionsMsg} {
				t.Run(fmt.Sprint(code), func(t *testing.T) { checkCompletedResponse(t, p, remote, events, code) })
			}
		})
	}
}

func checkCompletedResponse(t *testing.T, p *Peer, remote *p2p.MsgPipeRW, events <-chan peerpolicy.Evidence, code uint64) {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- sendActivityReply(p, code) }()
	msg, err := remote.ReadMsg()
	if err != nil || msg.Code != code {
		t.Fatalf("reply code=%d, err=%v", msg.Code, err)
	}
	select {
	case event := <-events:
		t.Fatalf("counted a reply before payload consumption: %+v", event)
	default:
	}
	if err := msg.Discard(); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		want := peerpolicy.ActivityEvent{ServedResponses: 1, ServedBytes: uint64(msg.Size)}
		if event.Activity != want || event.Reason != peerpolicy.None || event.Items != 0 || event.Bytes != 0 {
			t.Fatalf("completed response activity: %+v, want %+v", event, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completed response missing observation")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func sendActivityReply(p *Peer, code uint64) error {
	data := []rlp.RawValue{{0xc0}}
	switch code {
	case BlockHeadersMsg:
		return p.ReplyBlockHeadersRLP(7, data)
	case BlockBodiesMsg:
		return p.ReplyBlockBodiesRLP(7, data)
	case ReceiptsMsg:
		return p.ReplyReceiptsRLP(7, data)
	default:
		return p.ReplyPooledTransactionsRLP(7, nil, data)
	}
}

func TestPeerActivityUnsentResponses(t *testing.T) {
	for _, failure := range []string{"write", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rw := &replyTestRW{sent: make(chan p2p.Msg, 4)}
				p := NewPeer(ETH69, p2p.NewPeer(enode.ID{1}, "", nil), rw, nil)
				events := make(chan peerpolicy.Evidence, 4)
				p.SetObserver(func(e peerpolicy.Evidence) { events <- e })
				if failure == "write" {
					rw.err = io.ErrClosedPipe
				} else {
					p.blockReplies.limiter.SetLimit(1)
					if !p.blockReplies.limiter.AllowN(time.Now(), peerByteBurst) {
						t.Fatal("could not exhaust reply allowance")
					}
				}
				if err := p.ReplyReceiptsRLP(7, nil); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				p.Close()
				synctest.Wait()
				if len(events) != 0 || len(rw.sent) != 0 {
					t.Fatal("unsent response counted as completed activity")
				}
			})
		})
	}
}

func TestPeerActivityFailedConfiguredResponse(t *testing.T) {
	p := &Peer{Peer: configuredLimitsTestPeer(t, "trusted"), rw: &observedWriter{err: io.ErrClosedPipe}}
	p.SetObserver(func(e peerpolicy.Evidence) { t.Fatalf("failed write observed: %+v", e) })
	for _, code := range []uint64{BlockHeadersMsg, BlockBodiesMsg, ReceiptsMsg, PooledTransactionsMsg} {
		if err := sendActivityReply(p, code); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("failed reply returned %v", err)
		}
	}
}

func TestPeerActivityDirectReplySize(t *testing.T) {
	for _, size := range []int{maxMessageSize - 1, maxMessageSize, maxMessageSize + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			writer := new(observedWriter)
			peer := &Peer{rw: writer}
			var served uint64
			err := peer.sendReplyWithCompletion(BlockBodiesMsg, rlp.RawValue(make([]byte, size)), func(bytes uint64) { served = bytes })
			if size > maxMessageSize {
				if !errors.Is(err, errMsgTooLarge) || served != 0 || writer.completed {
					t.Fatalf("oversized reply sent or counted: err=%v served=%d", err, served)
				}
			} else if err != nil || served != uint64(size) || !writer.completed {
				t.Fatalf("allowed reply size changed: err=%v served=%d", err, served)
			}
		})
	}
}

type transactionObservationBackend struct {
	Backend
	accept     bool
	deliveries int
}

func (b *transactionObservationBackend) AcceptTxs() bool { return b.accept }
func (b *transactionObservationBackend) Handle(*Peer, Packet) error {
	b.deliveries++
	return nil
}

func TestPeerPolicyIgnoredTransactions(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for code, handle := range map[uint64]msgHandler{TransactionsMsg: handleTransactions, PooledTransactionsMsg: handlePooledTransactions} {
			t.Run(fmt.Sprintf("%d/observer=%t", code, enabled), func(t *testing.T) {
				tracker := peerpolicy.New(new(mclock.Simulated))
				backend := new(transactionObservationBackend)
				peer := &Peer{}
				observations := 0
				if enabled {
					peer.SetObserver(func(e peerpolicy.Evidence) {
						observations++
						if e.Reason == peerpolicy.UnknownTransactionDelivery {
							if code != PooledTransactionsMsg || e.Bytes != 0 || e.Items != 0 {
								t.Fatalf("incorrect diagnostic: %+v", e)
							}
						} else if e.Family != peerpolicy.Transactions || e.Reason != peerpolicy.None || e.Items != 0 || e.Bytes != 8<<20 {
							t.Fatalf("unexpected ignored delivery: %+v", e)
						}
						tracker.Observe("sync-peer", e)
					})
				}
				for delivery := 1; delivery <= 9; delivery++ {
					payload := bytes.NewReader([]byte{0xff})
					msg := p2p.Msg{Code: code, Size: 8 << 20, Payload: payload}
					if err := peer.handleObserved(backend, msg, handle); err != nil {
						t.Fatal(err)
					}
					if payload.Len() != 1 || backend.deliveries != 0 {
						t.Fatal("ignored packet was decoded or forwarded")
					}
					want := uint64(0)
					if enabled && delivery == 9 {
						want = 20
					}
					if got := tracker.Snapshot("sync-peer"); got.Risk != want || got.Windows[peerpolicy.TransactionVolume.String()] != want/20 {
						t.Fatalf("delivery %d: %+v, want risk %d", delivery, got, want)
					}
				}
				wantObservations := 9
				if code == PooledTransactionsMsg {
					wantObservations = 18
				}
				if enabled && observations != wantObservations {
					t.Fatalf("observations=%d want=%d", observations, wantObservations)
				}
				if enabled && code == PooledTransactionsMsg && tracker.Snapshot("sync-peer").Windows[peerpolicy.UnknownTransactionDelivery.String()] != 1 {
					t.Fatal("missing provenance diagnostic")
				}
			})
		}
	}
}

func TestPeerPolicyAcceptedTransactionsDeferCorrelation(t *testing.T) {
	for _, tc := range []struct {
		code   uint64
		packet Packet
		handle msgHandler
	}{
		{TransactionsMsg, &TransactionsPacket{}, handleTransactions},
		{PooledTransactionsMsg, &PooledTransactionsPacket{RequestId: 42}, handlePooledTransactions},
	} {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			encoded, err := rlp.EncodeToBytes(tc.packet)
			if err != nil {
				t.Fatal(err)
			}
			backend := &transactionObservationBackend{accept: true}
			var events []peerpolicy.Evidence
			peer := &Peer{observer: func(e peerpolicy.Evidence) { events = append(events, e) }}
			msg := p2p.Msg{Code: tc.code, Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
			if err := peer.handleObserved(backend, msg, tc.handle); err != nil {
				t.Fatal(err)
			}
			if backend.deliveries != 1 || len(events) != 1 {
				t.Fatalf("accepted delivery bypassed fetcher correlation: deliveries=%d events=%+v", backend.deliveries, events)
			}
			event := events[0]
			if event.Items != 0 || event.Bytes != 0 || event.Reason != peerpolicy.None || event.Activity != (peerpolicy.ActivityEvent{ReceivedMessages: 1, ReceivedBytes: uint64(len(encoded))}) {
				t.Fatalf("forwarded delivery counted risk traffic or lost wire activity: %+v", event)
			}
		})
	}
}
