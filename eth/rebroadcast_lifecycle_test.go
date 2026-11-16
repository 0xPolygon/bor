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
	"sync"
	"testing"
	"time"

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
				if h.rebroadcastAllowed(td) {
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
