package eth

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestPeerPolicyQueueRefusals(t *testing.T) {
	for _, kind := range []string{"reservation", "generic", "body", "closed"} {
		t.Run(kind, func(t *testing.T) {
			p := &Peer{Peer: p2p.NewPeer(enode.ID{1}, "", nil), term: make(chan struct{}), txReplies: newPeerReplies(), blockReplies: newPeerReplies()}
			p.txReplies.bytes = peerByteBurst / 2
			p.blockReplies.bytes = peerByteBurst / 2
			calls := 0
			p.SetObserver(func(e peerpolicy.Evidence) {
				calls++
				if e.Activity != (peerpolicy.ActivityEvent{}) {
					t.Fatal("refused reply counted completed activity")
				}
				if !e.Refused {
					t.Fatal("missing refusal flag")
				}
				if kind == "body" && (len(e.Hashes) != 1 || e.Hashes[0] != (common.Hash{1})) {
					t.Fatal("missing body provenance")
				}
				// The callback must run after releasing the queue mutex.
				if !p.txReplies.mu.TryLock() {
					t.Fatal("transaction queue locked during observation")
				}
				p.txReplies.mu.Unlock()
				if !p.blockReplies.mu.TryLock() {
					t.Fatal("block queue locked during observation")
				}
				p.blockReplies.mu.Unlock()
			})
			var err error
			switch kind {
			case "reservation":
				_, err = p.reservePooledReply()
			case "generic":
				err = p.queueReply(BlockBodiesMsg, []rlp.RawValue{{0xc0}})
			case "body":
				err = p.writeObservedBodies(1, []rlp.RawValue{{0xc0}}, peerpolicy.Evidence{Hashes: []common.Hash{{1}}})
			case "closed":
				close(p.term)
				err = p.queueReply(BlockBodiesMsg, []rlp.RawValue{{0xc0}})
			}
			if kind == "closed" {
				if !errors.Is(err, ErrDisconnected) || calls != 0 {
					t.Fatalf("disconnect treated as refusal: %v %d", err, calls)
				}
			} else if !errors.Is(err, errReplyQueueFull) || calls != 1 {
				t.Fatalf("refusal: %v calls=%d", err, calls)
			}
		})
	}
}
