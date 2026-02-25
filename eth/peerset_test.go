package eth

import (
	"crypto/rand"
	"testing"

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
