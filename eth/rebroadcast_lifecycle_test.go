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

package eth

import (
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
)

func reconnectRebroadcastPeer(t *testing.T, h *handler, previous *eth.Peer) *eth.Peer {
	t.Helper()
	head, td := previous.Head()
	if err := h.peers.unregisterPeer(previous.ID()); err != nil {
		t.Fatal(err)
	}
	app, net := p2p.MsgPipe()
	t.Cleanup(func() {
		app.Close()
		net.Close()
	})
	peer := eth.NewPeer(eth.ETH68, p2p.NewPeer(previous.Node().ID(), "reconnected", nil), net, nil)
	t.Cleanup(peer.Close)
	peer.SetHead(head, td)
	if err := h.peers.registerPeer(peer, nil, nil); err != nil {
		t.Fatal(err)
	}
	return peer
}

func TestRebroadcastReconnectPreservesWindow(t *testing.T) {
	for _, elapsed := range []time.Duration{30 * time.Second, rebroadcastPeerGrace, 61 * time.Second} {
		t.Run(elapsed.String(), func(t *testing.T) {
			h, cleanup := newChainSyncerTestHandler(t)
			defer cleanup()
			h.enableSyncedFeatures()
			peer := registerPeerWithTD(t, h.peers, 1_000_000)
			_, td := h.chainSync.modeAndLocalHead()
			start := time.Now()
			if h.rebroadcastAllowed(td, start) {
				t.Fatal("initial ahead claim should suppress rebroadcast")
			}
			for i := 0; i < 3; i++ {
				peer.SetHead(common.Hash{byte(i + 1)}, big.NewInt(1_000_000+int64(i)))
				peer = reconnectRebroadcastPeer(t, h, peer)
				now := start.Add(elapsed + time.Duration(i)*rebroadcastPeerGrace)
				want := !now.Before(start.Add(rebroadcastPeerGrace))
				if got := h.rebroadcastAllowed(td, now); got != want {
					t.Fatalf("eligibility after reconnect at %s: have %v, want %v", now.Sub(start), got, want)
				}
			}
		})
	}
}

func TestChainSyncerChecksAvailabilityBeforeReadingChain(t *testing.T) {
	for _, condition := range []string{"running", "cooldown", "below minimum", "no peers", "all benched"} {
		t.Run(condition, func(t *testing.T) {
			h, cleanup := newChainSyncerTestHandler(t)
			defer cleanup()
			h.maxPeers = defaultMinSyncPeers
			cs := h.chainSync
			switch condition {
			case "running":
				cs.doneCh = make(chan error, 1)
			case "cooldown":
				cs.peersUnavailableUntil = time.Now().Add(time.Hour)
			case "no peers":
				h.maxPeers = 0
			case "all benched":
				h.maxPeers = 1
				peer := registerPeerWithTD(t, h.peers, 1_000_000)
				if err := h.downloader.RegisterPeer(peer.ID(), eth.ETH68, &ethPeer{Peer: peer}); err != nil {
					t.Fatal(err)
				}
				setDownloaderPeerBackoff(t, h.downloader, peer.ID(), time.Hour)
			}
			// No chain access is needed when no sync operation can be scheduled.
			chain := h.chain
			h.chain = nil
			defer func() { h.chain = chain }()
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("chain accessed before %s guard: %v", condition, recovered)
				}
			}()
			if op, _ := cs.nextSyncOp(); op != nil {
				t.Fatal("unavailable peers should not produce a sync operation")
			}
		})
	}
}

func TestRebroadcastInitialSyncDoesNotReadChain(t *testing.T) {
	h := new(handler)
	if h.canRebroadcast() {
		t.Fatal("initial sync must suppress rebroadcast without reading chain state")
	}
}

func fillRebroadcastHistory(h *handler, until time.Time) {
	for i := 0; len(h.rebroadcast.claims) < maxRebroadcastPeerClaims; i++ {
		id := fmt.Sprintf("history-%d", i)
		h.rebroadcast.addClaim(id, &rebroadcastPeerClaim{
			head: common.Hash{255}, td: big.NewInt(1_000_000), until: until,
		})
	}
}

func TestRebroadcastClaimVerificationIsBounded(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.rebroadcast.claims = make(map[string]*rebroadcastPeerClaim, maxRebroadcastPeerClaims)
	fillRebroadcastHistory(h, time.Now())
	h.rebroadcast.clearVerifiedClaims(h.chain, big.NewInt(1_000_000))
	if h.rebroadcast.claimCursor != maxRebroadcastClaimChecks {
		t.Fatalf("verified %d claims, want %d", h.rebroadcast.claimCursor, maxRebroadcastClaimChecks)
	}
}

func TestRebroadcastClaimOrderReusesVerifiedSlots(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.rebroadcast.claims = make(map[string]*rebroadcastPeerClaim, maxRebroadcastPeerClaims)
	genesis := h.chain.Genesis()
	td := h.chain.GetTd(genesis.Hash(), genesis.NumberU64())
	for i := 0; i < maxRebroadcastPeerClaims; i++ {
		id := fmt.Sprintf("verified-%d", i)
		h.rebroadcast.addClaim(id, &rebroadcastPeerClaim{head: genesis.Hash(), td: td, until: time.Now()})
	}
	h.rebroadcast.clearVerifiedClaims(h.chain, td)
	for i := 0; i < maxRebroadcastClaimChecks; i++ {
		id := fmt.Sprintf("replacement-%d", i)
		h.rebroadcast.addClaim(id, &rebroadcastPeerClaim{head: common.Hash{1}, td: big.NewInt(1_000_000)})
	}
	if len(h.rebroadcast.claims) != maxRebroadcastPeerClaims || len(h.rebroadcast.claimOrder) != maxRebroadcastPeerClaims {
		t.Fatal("claim verification queue exceeded its bound")
	}
}

func TestRebroadcastHistoryCapacityPreservesClaims(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	peer := registerPeerWithTD(t, h.peers, 1_000_000)
	_, td := h.chainSync.modeAndLocalHead()
	start := time.Now()
	if h.rebroadcastAllowed(td, start) {
		t.Fatal("initial claim must suppress rebroadcast")
	}
	original := h.rebroadcast.claims[peer.ID()]
	fillRebroadcastHistory(h, start.Add(rebroadcastPeerGrace))
	for i := 0; i < 3; i++ {
		newPeer := registerPeerWithTD(t, h.peers, 2_000_000)
		if !h.rebroadcastAllowed(td, start.Add(2*rebroadcastPeerGrace)) {
			t.Fatal("capacity exhaustion must not grant another suppression window")
		}
		if len(h.rebroadcast.claims) != maxRebroadcastPeerClaims || h.rebroadcast.claims[newPeer.ID()] != nil {
			t.Fatal("claim history exceeded its bound")
		}
	}
	peer = reconnectRebroadcastPeer(t, h, peer)
	if !h.rebroadcastAllowed(td, start.Add(3*rebroadcastPeerGrace)) {
		t.Fatal("reconnect at capacity must retain the original deadline")
	}
	if h.rebroadcast.claims[peer.ID()] != original {
		t.Fatal("unresolved claim was replaced")
	}
}

func TestRebroadcastVerifiedHistoryReclaimsCapacity(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	blocks, _ := core.GenerateChain(h.chain.Config(), h.chain.Genesis(), ethash.NewFaker(), h.database, 1, nil)
	_, td := h.chainSync.modeAndLocalHead()
	claimedTD := new(big.Int).Add(td, blocks[0].Difficulty())
	peer := registerPeerWithTD(t, h.peers, claimedTD.Int64())
	peer.SetHead(blocks[0].Hash(), claimedTD)
	start := time.Now()
	if h.rebroadcastAllowed(td, start) {
		t.Fatal("initial claim must suppress rebroadcast")
	}
	fillRebroadcastHistory(h, start.Add(rebroadcastPeerGrace))
	peer = reconnectRebroadcastPeer(t, h, peer)
	if _, err := h.chain.InsertChain(blocks, false); err != nil {
		t.Fatal(err)
	}
	if !h.rebroadcastAllowed(claimedTD, start.Add(rebroadcastPeerGrace)) {
		t.Fatal("verified catch-up must restore rebroadcast")
	}
	if len(h.rebroadcast.claims) != maxRebroadcastPeerClaims-1 || h.rebroadcast.claims[peer.ID()] != nil {
		t.Fatal("verified catch-up must release the history entry")
	}
	peer.SetHead(common.Hash{2}, new(big.Int).Add(claimedTD, big.NewInt(1)))
	if h.rebroadcastAllowed(claimedTD, start.Add(2*rebroadcastPeerGrace)) {
		t.Fatal("verified catch-up must allow a new claim after reconnect")
	}
	if len(h.rebroadcast.claims) != maxRebroadcastPeerClaims {
		t.Fatal("new claim did not reuse the released capacity")
	}
}

func TestRebroadcastCaughtUpPeersDoNotAllocateHistory(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	registerPeerWithTD(t, h.peers, 0)
	if !h.canRebroadcast() || h.rebroadcast.claims != nil {
		t.Fatal("caught-up peers must not allocate claim history")
	}
}

func TestRebroadcastDisconnectedClaimReclaimsCapacity(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	blocks, _ := core.GenerateChain(h.chain.Config(), h.chain.Genesis(), ethash.NewFaker(), h.database, 1, nil)
	_, td := h.chainSync.modeAndLocalHead()
	claimedTD := new(big.Int).Add(td, blocks[0].Difficulty())
	peer := registerPeerWithTD(t, h.peers, claimedTD.Int64())
	peer.SetHead(blocks[0].Hash(), claimedTD)
	start := time.Now()
	if h.rebroadcastAllowed(td, start) {
		t.Fatal("initial claim must suppress rebroadcast")
	}
	fillRebroadcastHistory(h, start.Add(rebroadcastPeerGrace))
	if err := h.peers.unregisterPeer(peer.ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.chain.InsertChain(blocks, false); err != nil {
		t.Fatal(err)
	}
	newPeer := registerPeerWithTD(t, h.peers, 2_000_000)
	if h.rebroadcastAllowed(claimedTD, start.Add(2*rebroadcastPeerGrace)) {
		t.Fatal("verified disconnected claim must free capacity for a new ahead peer")
	}
	if h.rebroadcast.claims[peer.ID()] != nil || h.rebroadcast.claims[newPeer.ID()] == nil {
		t.Fatal("disconnected claim was not replaced with the new claim")
	}
	if len(h.rebroadcast.claims) != maxRebroadcastPeerClaims {
		t.Fatal("unverified disconnected claims must remain recorded")
	}
}

func TestRebroadcastUsesSelectedLocalHead(t *testing.T) {
	for _, mode := range []string{"full", "snap", "stateless"} {
		t.Run(mode, func(t *testing.T) {
			h, cleanup := newChainSyncerTestHandler(t)
			defer cleanup()
			h.enableSyncedFeatures()
			blocks, _ := core.GenerateChain(h.chain.Config(), h.chain.Genesis(), ethash.NewFaker(), h.database, 1, nil)
			if _, err := h.chain.InsertChain(blocks, false); err != nil {
				t.Fatal(err)
			}
			if err := h.chain.SnapSyncCommitHead(h.chain.Genesis().Hash()); err != nil {
				t.Fatal(err)
			}
			snap := h.chain.CurrentSnapBlock()
			if snap.Hash() == h.chain.CurrentBlock().Hash() {
				t.Fatal("test requires distinct full and snap heads")
			}
			td := h.chain.GetTd(snap.Hash(), snap.Number.Uint64())
			peer := registerPeerWithTD(t, h.peers, td.Int64())
			peer.SetHead(snap.Hash(), td)
			h.snapSync.Store(mode == "snap")
			h.statelessSync.Store(mode == "stateless")
			h.chainSync = nil
			if got := h.canRebroadcast(); got != (mode == "snap") {
				t.Fatalf("eligibility for %s: have %v", mode, got)
			}
			if mode != "snap" {
				local := h.chain.CurrentBlock()
				td := h.chain.GetTd(local.Hash(), local.Number.Uint64())
				if h.rebroadcastAllowed(td, time.Now().Add(2*rebroadcastPeerGrace)) {
					t.Fatal("a locally known ahead header must block rebroadcast until catch-up")
				}
			}
		})
	}
}

func TestRebroadcastConcurrentReconnects(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	peer := registerPeerWithTD(t, h.peers, 1_000_000)
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			h.canRebroadcast()
		}
	}()
	for i := 0; i < 20; i++ {
		peer = reconnectRebroadcastPeer(t, h, peer)
	}
}
