package eth

import (
	"errors"
	"fmt"
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

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
