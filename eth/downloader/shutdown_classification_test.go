package downloader

import (
	"context"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

func TestEngineShutdownAbortHasNoPeerPenalty(t *testing.T) {
	dropped := make(chan string, 1)
	d := &Downloader{peers: newPeerSet(), dropPeer: func(id string) { dropped <- id }}
	peer := newPeerConnection("peer", eth.ETH69, nil, log.New())
	if err := d.peers.Register(peer); err != nil {
		t.Fatal(err)
	}
	abort := fmt.Errorf("import: %w: %w", core.ErrStateSyncProcessing, consensus.NewEngineStoppedError(context.Canceled))
	if reason, ok := classifySyncFailure(abort); ok {
		t.Fatalf("shutdown abort classified for peer response %q", reason)
	}
	if d.handleSyncFailure(peer, peer.id, abort) {
		t.Fatal("shutdown abort reached peer response handling")
	}
	if peer.backedOff() {
		t.Fatal("shutdown abort backed off the supplier")
	}
	select {
	case id := <-dropped:
		t.Fatalf("shutdown abort dropped supplier %q", id)
	default:
	}
}

func TestInvalidChainStillDropsWhenDownloaderIsStopping(t *testing.T) {
	dropped := make(chan string, 1)
	d := &Downloader{peers: newPeerSet(), quitCh: make(chan struct{}), dropPeer: func(id string) { dropped <- id }}
	peer := newPeerConnection("peer", eth.ETH69, nil, log.New())
	if err := d.peers.Register(peer); err != nil {
		t.Fatal(err)
	}
	close(d.quitCh)
	failure := fmt.Errorf("%w: %w", errInvalidChain, core.ErrStateSyncMismatch)
	if !d.handleSyncFailure(peer, peer.id, failure) {
		t.Fatal("genuine invalid-chain failure was not handled")
	}
	select {
	case id := <-dropped:
		if id != peer.id {
			t.Fatalf("dropped wrong peer %q", id)
		}
	default:
		t.Fatal("genuine invalid-chain failure was hidden by shutdown")
	}
}
