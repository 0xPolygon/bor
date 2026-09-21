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
	"testing"

	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

// trustedFakePeer embeds Peer so it compiles; only IsTrusted is ever read.
type trustedFakePeer struct {
	Peer
	trusted bool
}

func (p *trustedFakePeer) IsTrusted() bool { return p.trusted }

// staticOnlyFakePeer is static but not trusted, which grants no exemption.
type staticOnlyFakePeer struct{ Peer }

func (staticOnlyFakePeer) IsStatic() bool { return true }

// TestTrustedPeerExemptFromResponse is the incident regression: a trusted peer
// is never benched or dropped, while an ordinary peer on the same verdict is.
func TestTrustedPeerExemptFromResponse(t *testing.T) {
	dropped := make(chan string, 1)
	d := &Downloader{peers: newPeerSet(), dropPeer: func(id string) { dropped <- id }}

	// Ordinary peer: dropped and benched.
	plain := newPeerConnection("plain", eth.ETH69, nil, log.New())
	if err := d.peers.Register(plain); err != nil {
		t.Fatalf("register plain: %v", err)
	}
	d.respondToPeer(plain, peerFailureInvalidChain, errInvalidChain)
	select {
	case <-dropped:
	default:
		t.Fatal("ordinary peer on invalid-chain was not dropped")
	}
	if plain.backoffRemaining() <= 0 {
		t.Fatal("ordinary peer on invalid-chain was not benched")
	}

	// Trusted peer, same verdict: untouched.
	trusted := newPeerConnection("trusted", eth.ETH69, &trustedFakePeer{trusted: true}, log.New())
	if err := d.peers.Register(trusted); err != nil {
		t.Fatalf("register trusted: %v", err)
	}
	d.respondToPeer(trusted, peerFailureInvalidChain, errInvalidChain)
	select {
	case id := <-dropped:
		t.Fatalf("trusted peer was dropped: %s", id)
	default:
	}
	if trusted.backoffRemaining() > 0 {
		t.Fatal("trusted peer was benched")
	}

	// The incident's reason: repeated mismatches must not escalate.
	for i := 0; i < whitelistMismatchDropThreshold+2; i++ {
		d.respondToPeer(trusted, peerFailureWhitelistMismatch, errInvalidChain)
	}
	select {
	case id := <-dropped:
		t.Fatalf("trusted peer dropped after repeated whitelist mismatch: %s", id)
	default:
	}
	if trusted.backoffRemaining() > 0 {
		t.Fatal("trusted peer benched after repeated whitelist mismatch")
	}
}

func TestPeerResponseTrustChanges(t *testing.T) {
	for _, tc := range []struct {
		name           string
		initialTrusted bool
		currentTrusted bool
		wantPenalty    bool
	}{
		{"add trusted peer", false, true, false},
		{"remove trusted peer", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := &trustedFakePeer{trusted: tc.initialTrusted}
			peer := newPeerConnection("peer", eth.ETH69, remote, log.New())
			dropped := false
			d := &Downloader{peers: newPeerSet(), dropPeer: func(id string) {
				if id != peer.id {
					t.Fatalf("dropped peer %q, want %q", id, peer.id)
				}
				dropped = true
			}}
			if err := d.peers.Register(peer); err != nil {
				t.Fatalf("register peer: %v", err)
			}

			remote.trusted = tc.currentTrusted
			d.respondToPeer(peer, peerFailureInvalidChain, errInvalidChain)
			if dropped != tc.wantPenalty {
				t.Errorf("peer dropped = %t, want %t", dropped, tc.wantPenalty)
			}
			if benched := peer.backedOff(); benched != tc.wantPenalty {
				t.Errorf("peer benched = %t, want %t", benched, tc.wantPenalty)
			}
		})
	}
}

func TestStaticPeerResponsePenalties(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reason   peerFailureReason
		err      error
		attempts int
		wantDrop bool
	}{
		{"invalid chain", peerFailureInvalidChain, errInvalidChain, 1, true},
		{"timeout", peerFailureTimeout, errTimeout, 1, false},
		{"whitelist mismatch", peerFailureWhitelistMismatch, errInvalidChain, whitelistMismatchDropThreshold, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			peer := newPeerConnection("static", eth.ETH69, staticOnlyFakePeer{}, log.New())
			dropped := false
			d := &Downloader{peers: newPeerSet(), dropPeer: func(id string) {
				if id != peer.id {
					t.Fatalf("dropped peer %q, want %q", id, peer.id)
				}
				dropped = true
			}}
			if err := d.peers.Register(peer); err != nil {
				t.Fatalf("register static peer: %v", err)
			}
			for i := 0; i < tc.attempts; i++ {
				d.respondToPeer(peer, tc.reason, tc.err)
			}
			if dropped != tc.wantDrop {
				t.Fatalf("static peer dropped = %t, want %t", dropped, tc.wantDrop)
			}
			if peer.backoffRemaining() <= 0 {
				t.Fatal("static peer was not benched")
			}
		})
	}
}
