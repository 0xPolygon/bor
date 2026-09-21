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

package fetcher

import (
	"errors"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestCheckWitnessPageCountJailsTrustedProducerOnHonestDisagreement is a gap
// demonstration for bor PR #2429 (eth/downloader: restrict sync penalty
// exemptions to trusted peers).
//
// #2429 exempts a live-Trusted() peer from every penalty routed through
// eth/downloader/peer_response.go's respondToPeer. This function,
// witnessManager.CheckWitnessPageCount / verifyWitnessPageCountSync, is a
// completely separate penalty path: it is wired from block_fetcher.go's
// jailPeer/dropPeer fields, which at the handler layer (eth/handler.go) go
// straight to p2p.Server.JailPeer / peer.Disconnect with no trust check
// anywhere in the chain. #2429 does not touch this path.
//
// This test does not need to fake a "trusted" designation to make the point:
// the function signature has no trust parameter at all, so there is nothing
// to exempt. What it demonstrates is the actual trigger condition: agent-zero
// project memory (project_bor_witness_nondeterminism) recorded that witness
// page counts differ by roughly 5-9% between two HONEST bor nodes on the same
// block. That is precisely the scenario simulated here - the reporting peer
// (standing in for a trusted producer/BP) is not lying, it just saw a
// different (but equally honest) witness size than the 2 randomly-sampled
// peers used for consensus. It gets dropped AND jailed anyway, exactly like
// INC-192's whitelist-mismatch false positive that #2429 exists to fix - just
// through a sibling code path #2429 never reaches.
//
// This test passes today (nothing here has been changed). It exists to make
// the blast radius of the gap concrete and runnable rather than a prose
// claim: run `go test -run TestCheckWitnessPageCountJailsTrustedProducerOnHonestDisagreement -v`.
func TestCheckWitnessPageCountJailsTrustedProducerOnHonestDisagreement(t *testing.T) {
	quit := make(chan struct{})
	defer close(quit)

	var (
		mu           sync.Mutex
		droppedPeers []string
		jailedPeers  []string
	)

	dropPeer := peerDropFn(func(id string) {
		mu.Lock()
		droppedPeers = append(droppedPeers, id)
		mu.Unlock()
	})
	jailPeer := peerJailFn(func(id string) {
		mu.Lock()
		jailedPeers = append(jailedPeers, id)
		mu.Unlock()
	})

	getBlock := blockRetrievalFn(func(hash common.Hash) *types.Block { return nil })
	getHeader := HeaderRetrievalFn(func(hash common.Hash) *types.Header { return nil })
	chainHeight := chainHeightFn(func() uint64 { return 100 })

	// gasCeil chosen so the page-count threshold falls well below the
	// reported/consensus counts used here, forcing synchronous verification.
	const gasCeil = uint64(30_000_000)

	manager := newWitnessManager(quit, dropPeer, jailPeer, make(chan *enqueueRequest, 10), getBlock, getHeader, chainHeight, nil, gasCeil)

	hash := common.HexToHash("0xf00d")

	// trustedProducer stands in for a peer that is configured as trusted at
	// the p2p/downloader layer (and would therefore be fully exempt from
	// eth/downloader/peer_response.go's respondToPeer after #2429). Its
	// reported page count is honest, just larger than what the two randomly
	// sampled "honest" peers happen to report - the documented
	// ±5-9% real-world variance, not a lie.
	const trustedProducer = "trusted-producer-peer"
	const reportedPageCount = uint64(107) // honest, ~7% above the sampled peers

	getRandomPeers := func() []string { return []string{"honest-sample-1", "honest-sample-2"} }
	getWitnessPageCount := func(peerID string, hash common.Hash) (uint64, error) {
		switch peerID {
		case "honest-sample-1", "honest-sample-2":
			return 100, nil // also honest, just a different (smaller) node set
		}
		return 0, errors.New("unexpected peer queried")
	}

	isHonest := manager.CheckWitnessPageCount(hash, reportedPageCount, trustedProducer, getRandomPeers, getWitnessPageCount)

	if isHonest {
		t.Fatal("expected the honest-but-disagreeing producer to be flagged dishonest by the consensus check")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(droppedPeers) != 1 || droppedPeers[0] != trustedProducer {
		t.Fatalf("expected trusted producer %q to be dropped despite honest disagreement, got dropped=%v", trustedProducer, droppedPeers)
	}
	if len(jailedPeers) != 1 || jailedPeers[0] != trustedProducer {
		t.Fatalf("expected trusted producer %q to be jailed despite honest disagreement, got jailed=%v", trustedProducer, jailedPeers)
	}

	t.Logf("GAP CONFIRMED: peer %q was dropped and jailed by witnessManager's page-count "+
		"consensus check purely for an honest %d-vs-100 disagreement with a 2-peer sample; "+
		"#2429's trusted-peer exemption in eth/downloader/peer_response.go never runs for this "+
		"code path, so no trust designation could have prevented this.", trustedProducer, reportedPageCount)
}
