package eth

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestPeerTrafficJailing(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		jail bool
	}{
		{"rate limit", fmt.Errorf("request handling: %w", eth.ErrPeerRateLimit), true},
		{"ordinary disconnect", errors.New("connection closed"), false},
		{"clean exit", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setup := setupJailPeerTest(t, true)
			src, sink := backoffTestPeers(t, setup)
			result := make(chan error, 1)
			go func() {
				result <- (*ethHandler)(setup.handler).RunPeer(sink, func(*eth.Peer) error { return tc.err })
			}()
			head := setup.chain.CurrentBlock()
			if err := src.Handshake(1, setup.chain, eth.BlockRangeUpdatePacket{
				EarliestBlock: 0, LatestBlock: head.Number.Uint64(), LatestBlockHash: head.Hash(),
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-result:
				if !errors.Is(err, tc.err) {
					t.Fatalf("handler error: got %v, want %v", err, tc.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not exit")
			}
			jail := reflect.ValueOf(setup.p2pServer).Elem().FieldByName("peerJail").Elem().FieldByName("jailed")
			entry := jail.MapIndex(reflect.ValueOf(sink.Peer.ID()))
			if got := entry.IsValid(); got != tc.jail {
				t.Fatalf("peer jailed: got %v, want %v", got, tc.jail)
			}
			if tc.jail {
				remaining := mclock.AbsTime(entry.Int()).Sub(mclock.Now())
				if remaining < peerTrafficBackoff-time.Second || remaining > peerTrafficBackoff+time.Second {
					t.Fatalf("backoff period: got %v, want %v", remaining, peerTrafficBackoff)
				}
			}
		})
	}
}

func TestPeerTrafficBackoffWithoutServer(t *testing.T) {
	setup := setupJailPeerTest(t, false)
	setup.handler.jailPeerFor(enode.ID{1}.String(), peerTrafficBackoff)
}

func backoffTestPeers(t *testing.T, setup *jailPeerTestSetup) (*eth.Peer, *eth.Peer) {
	t.Helper()
	app, net := p2p.MsgPipe()
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Error(err)
		}
	})
	src := eth.NewPeer(eth.ETH68, p2p.NewPeerPipe(enode.ID{1}, "", nil, app), app, setup.txpool)
	sink := eth.NewPeer(eth.ETH68, p2p.NewPeerPipe(enode.ID{2}, "", nil, net), net, setup.txpool)
	t.Cleanup(src.Close)
	t.Cleanup(sink.Close)
	return src, sink
}
