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
	"time"

	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
)

// TestDropBackoffIsSharedAcrossSeverityClasses is a gap demonstration for bor
// PR #2429 (eth/downloader: restrict sync penalty exemptions to trusted
// peers).
//
// #2429 cuts peerDropBackoff from 30 minutes to 1 minute (and peerJailBackoff
// from 5 minutes to 30 seconds) to make INC-192's whitelist-mismatch false
// positive re-admit a peer quickly. But dropPeerForResponse benches EVERY
// drop reason with the same peerDropBackoff constant - there is no separate
// backoff for "definitely malicious" (invalid-chain / bad-peer /
// invalid-ancestor, PR #2283's actual DoS defense target) versus "possibly a
// false positive" (whitelist-mismatch, the incident this PR is fixing).
//
// This test drives a non-trusted peer through both severity classes and
// shows the resulting ban duration is identical, to make the shared-constant
// claim concrete: run `go test -run TestDropBackoffIsSharedAcrossSeverityClasses -v`.
func TestDropBackoffIsSharedAcrossSeverityClasses(t *testing.T) {
	t.Parallel()

	newDownloader := func() (*Downloader, chan string) {
		dropped := make(chan string, 1)
		return &Downloader{peers: newPeerSet(), dropPeer: func(id string) { dropped <- id }}, dropped
	}

	// Severity class 1: invalid-chain. responseDecision routes this straight
	// to peerResponseDrop - this is PR #2283's bad-peer/DoS defense, aimed at
	// peers that are lying about chain data.
	d1, dropped1 := newDownloader()
	maliciousPeer := newPeerConnection("malicious-peer", eth.ETH69, nil, log.New())
	if err := d1.peers.Register(maliciousPeer); err != nil {
		t.Fatalf("register malicious peer: %v", err)
	}
	d1.respondToPeer(maliciousPeer, peerFailureInvalidChain, errInvalidChain)
	select {
	case <-dropped1:
	default:
		t.Fatal("malicious peer on invalid-chain was not dropped")
	}
	maliciousBackoff := maliciousPeer.backoffRemaining()

	// Severity class 2: whitelist-mismatch, escalated to a drop after
	// repeated strikes. This is exactly INC-192's mechanism - a false
	// positive against a peer that served nothing wrong.
	d2, dropped2 := newDownloader()
	falsePositivePeer := newPeerConnection("false-positive-peer", eth.ETH69, nil, log.New())
	if err := d2.peers.Register(falsePositivePeer); err != nil {
		t.Fatalf("register false-positive peer: %v", err)
	}
	for i := 0; i < whitelistMismatchDropThreshold; i++ {
		d2.respondToPeer(falsePositivePeer, peerFailureWhitelistMismatch, errInvalidChain)
	}
	select {
	case <-dropped2:
	default:
		t.Fatal("peer was not dropped after crossing the whitelist-mismatch drop threshold")
	}
	falsePositiveBackoff := falsePositivePeer.backoffRemaining()

	// backoffRemaining() is computed relative to time.Now() at call time, so
	// allow a small tolerance for the wall-clock gap between the two calls
	// above rather than requiring bit-for-bit equality.
	delta := maliciousBackoff - falsePositiveBackoff
	if delta < 0 {
		delta = -delta
	}
	if delta > time.Second {
		t.Fatalf("expected both severity classes to share one backoff constant (that is the gap being demonstrated), got malicious=%s false-positive=%s",
			maliciousBackoff, falsePositiveBackoff)
	}
	if maliciousBackoff <= 0 || maliciousBackoff > peerDropBackoff {
		t.Fatalf("unexpected backoff value %s (want a positive value bounded by peerDropBackoff=%s)", maliciousBackoff, peerDropBackoff)
	}

	t.Logf("GAP CONFIRMED: a peer proven to have sent invalid chain data and a peer that hit the "+
		"incident's whitelist-mismatch false positive are both re-eligible to sync in the same %s "+
		"(peerDropBackoff), down from 30 minutes pre-#2429 - the constant does not distinguish "+
		"'confirmed malicious' from 'possibly innocent'.", maliciousBackoff)
}
