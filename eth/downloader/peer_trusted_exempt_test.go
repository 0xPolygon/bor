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

// trustedFakePeer embeds the Peer interface so it satisfies it at compile time
// without implementing every method. newPeerConnection only reads the optional
// IsTrusted assertion and never calls the embedded interface, so the nil
// embedding is never dereferenced.
type trustedFakePeer struct{ Peer }

func (trustedFakePeer) IsTrusted() bool { return true }

// TestTrustedPeerCaptured checks that newPeerConnection records the trusted bit
// from a peer exposing IsTrusted, and defaults to false otherwise.
func TestTrustedPeerCaptured(t *testing.T) {
	if pc := newPeerConnection("trusted", eth.ETH69, trustedFakePeer{}, log.New()); !pc.trusted {
		t.Fatal("trusted peer was not captured at registration")
	}
	if pc := newPeerConnection("plain", eth.ETH69, nil, log.New()); pc.trusted {
		t.Fatal("peer with no trusted signal was marked trusted")
	}
}

// TestTrustedPeerExemptFromResponse is the regression for the incident: a
// trusted or static peer must never be benched or dropped by the sync
// peer-response policy, even on a drop-worthy verdict, while an ordinary peer
// on the same verdict still is.
func TestTrustedPeerExemptFromResponse(t *testing.T) {
	dropped := make(chan string, 1)
	d := &Downloader{peers: newPeerSet(), dropPeer: func(id string) { dropped <- id }}

	// Ordinary peer on an invalid-chain verdict: dropped and benched.
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

	// Trusted peer on the same verdict: neither dropped nor benched.
	trusted := newPeerConnection("trusted", eth.ETH69, trustedFakePeer{}, log.New())
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

	// The incident's exact reason: repeated whitelist mismatches must never
	// escalate a trusted peer to a jail or drop.
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
