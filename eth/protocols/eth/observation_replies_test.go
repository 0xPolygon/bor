package eth

import (
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

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
