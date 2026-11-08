// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package downloader

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

func TestTimeoutsOnlyBackOff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{"timeout", errTimeout},
		{"deadline", context.DeadlineExceeded},
		{"wrapped timeout", fmt.Errorf("%w: %w", errInvalidChain, errTimeout)},
		{"wrapped deadline", fmt.Errorf("%w: %w", errBadPeer, context.DeadlineExceeded)},
		{"flattened deadline", fmt.Errorf("header request: %s", context.DeadlineExceeded)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Downloader{peers: newPeerSet(), dropPeer: func(id string) { t.Errorf("timeout dropped peer %q", id) }}
			peer := newPeerConnection("peer", eth.ETH69, nil, log.New())
			if err := d.peers.Register(peer); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2*softFailureJailThreshold; i++ {
				clearPeerBackoff(peer)
				if !d.handleSyncFailure(peer, peer.id, tt.err) {
					t.Fatal("timeout was not handled")
				}
				checkTimeoutBackoff(t, d.peers, peer, 0)
			}
		})
	}
}

func TestTimeoutsPreserveOtherFailureStrikes(t *testing.T) {
	t.Parallel()

	d := &Downloader{peers: newPeerSet()}
	peer := newPeerConnection("peer", eth.ETH69, nil, log.New())
	if err := d.peers.Register(peer); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < softFailureJailThreshold-1; i++ {
		d.respondToPeer(peer, peerFailureStalling, errStallingPeer)
		clearPeerBackoff(peer)
	}
	for i := 0; i < 2*softFailureJailThreshold; i++ {
		d.respondToPeer(peer, peerFailureTimeout, errTimeout)
		checkTimeoutBackoff(t, d.peers, peer, softFailureJailThreshold-1)
		clearPeerBackoff(peer)
	}
	d.respondToPeer(peer, peerFailureStalling, errStallingPeer)
	if remaining := peer.backoffRemaining(); remaining <= peerSoftBackoff {
		t.Fatalf("non-timeout failures should still escalate: remaining %v", remaining)
	}
}

func TestSkeletonTimeoutsOnlyBackOff(t *testing.T) {
	t.Parallel()

	peers := newPeerSet()
	peer := newPeerConnection("peer", eth.ETH69, nil, log.New())
	if err := peers.Register(peer); err != nil {
		t.Fatal(err)
	}
	s := &skeleton{peers: peers}
	for i := 0; i < 2*softFailureJailThreshold; i++ {
		clearPeerBackoff(peer)
		req := &headerRequest{
			peer:   peer.id,
			stale:  make(chan struct{}),
			cancel: make(chan struct{}),
			revert: make(chan *headerRequest, 1),
		}
		s.handleHeaderTimeout(peer, req, time.Second)
		checkTimeoutBackoff(t, peers, peer, 0)
		select {
		case reverted := <-req.revert:
			if reverted != req {
				t.Fatal("requeued a different request")
			}
		default:
			t.Fatal("timed-out headers were not requeued")
		}
	}
}

func TestStaleTimeoutsOnlyBackOff(t *testing.T) {
	t.Parallel()

	d := &Downloader{peers: newPeerSet()}
	peer := newPeerConnection("peer", eth.ETH69, nil, log.New())
	if err := d.peers.Register(peer); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2*softFailureJailThreshold; i++ {
		clearPeerBackoff(peer)
		req := &eth.Request{Peer: peer.id, Sent: time.Now().Add(-2 * timeoutGracePeriod), Cancel: make(chan struct{})}
		stales := map[string]*eth.Request{peer.id: req}
		d.classifyPeer(fakeTypedQueue{k: headerQueueKind}, peer, nil, stales)
		checkTimeoutBackoff(t, d.peers, peer, 0)
		if len(stales) != 0 {
			t.Fatal("timed-out request was not removed")
		}
		select {
		case <-req.Cancel:
		default:
			t.Fatal("timed-out request was not closed")
		}
	}
}

func checkTimeoutBackoff(t *testing.T, peers *peerSet, peer *peerConnection, strikes int) {
	t.Helper()

	if remaining := peer.backoffRemaining(); remaining <= 0 || remaining > peerSoftBackoff {
		t.Fatalf("timeout backoff should be in (0, %v], got %v", peerSoftBackoff, remaining)
	}
	if remaining := peers.persistentBackoff(peer.id); remaining <= 0 || remaining > peerSoftBackoff {
		t.Fatalf("persistent timeout backoff should be in (0, %v], got %v", peerSoftBackoff, remaining)
	}
	if got := softStrikeTally(peers, peer.id); got != strikes {
		t.Fatalf("timeout changed escalation strikes: have %d, want %d", got, strikes)
	}
	if peers.Peer(peer.id) != peer {
		t.Fatal("timeout removed the peer")
	}
}
