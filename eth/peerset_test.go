package eth

import (
	"crypto/rand"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestPeerSetForgetTransactions(t *testing.T) {
	t.Parallel()

	ps := newPeerSet()
	defer ps.close()

	// Create multiple test peers
	apps := make([]*p2p.MsgPipeRW, 3)

	for i := 0; i < 3; i++ {
		app, net := p2p.MsgPipe()
		apps[i] = app

		var id enode.ID
		rand.Read(id[:])

		peer := eth.NewPeer(eth.ETH68, p2p.NewPeer(id, "test", nil), net, nil)

		// Register the peer
		if err := ps.registerPeer(peer, nil, nil); err != nil {
			t.Fatalf("failed to register peer %d: %v", i, err)
		}
	}

	// Clean up
	defer func() {
		for _, app := range apps {
			app.Close()
		}
	}()

	// Verify we have 3 peers
	if ps.len() != 3 {
		t.Fatalf("expected 3 peers, got %d", ps.len())
	}

	// ForgetTransactions should not panic with registered peers
	// (the actual forgetting logic is tested in eth/protocols/eth/peer_test.go)
	hashes := []common.Hash{{1}, {2}, {3}}
	ps.ForgetTransactions(hashes)
}

func TestPeerSetForgetTransactionsEmpty(t *testing.T) {
	t.Parallel()

	ps := newPeerSet()
	defer ps.close()

	// ForgetTransactions should not panic with no peers
	ps.ForgetTransactions([]common.Hash{{1}, {2}, {3}})
}

func TestPeerWithHighestTDSkipsBackedOffPeers(t *testing.T) {
	ps := newPeerSet()
	defer ps.close()

	low := registerPeerWithTD(t, ps, 10)
	high := registerPeerWithTD(t, ps, 20)

	best, retry := ps.peerWithHighestTD(func(id string) time.Duration {
		if id == high.ID() {
			return 30 * time.Second
		}
		return 0
	})
	if best == nil || best.ID() != low.ID() {
		t.Fatalf("best peer mismatch: have %v, want %v", best, low.ID())
	}
	if retry != 30*time.Second {
		t.Fatalf("retry delay mismatch: have %v, want %v", retry, 30*time.Second)
	}

	best, retry = ps.peerWithHighestTD(func(id string) time.Duration {
		if id == low.ID() {
			return 10 * time.Second
		}
		return time.Minute
	})
	if best != nil {
		t.Fatalf("unexpected peer selected while all peers were backed off: %v", best.ID())
	}
	if retry != 10*time.Second {
		t.Fatalf("retry delay mismatch: have %v, want %v", retry, 10*time.Second)
	}
}

func registerPeerWithTD(t *testing.T, ps *peerSet, td int64) *eth.Peer {
	t.Helper()

	app, net := p2p.MsgPipe()
	t.Cleanup(func() {
		app.Close()
		net.Close()
	})

	var id enode.ID
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatalf("failed to create peer id: %v", err)
	}

	peer := eth.NewPeer(eth.ETH68, p2p.NewPeer(id, "test", nil), net, nil)
	peer.SetHead(common.Hash{byte(td)}, big.NewInt(td))
	t.Cleanup(peer.Close)

	if err := ps.registerPeer(peer, nil, nil); err != nil {
		t.Fatalf("failed to register peer: %v", err)
	}
	return peer
}
