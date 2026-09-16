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
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

func TestRebroadcastRechecksPeerState(t *testing.T) {
	for _, updateHead := range []bool{false, true} {
		name := "registration"
		if updateHead {
			name = "head announcement"
		}
		t.Run(name, func(t *testing.T) {
			h, cleanup := newChainSyncerTestHandler(t)
			defer cleanup()
			h.enableSyncedFeatures()
			if !h.canRebroadcast() {
				t.Fatal("caught-up node should rebroadcast")
			}
			if updateHead {
				peer := registerPeerWithTD(t, h.peers, 0)
				peer.SetHead(common.Hash{1}, big.NewInt(1_000_000))
			} else {
				registerPeerWithTD(t, h.peers, 1_000_000)
			}
			if h.canRebroadcast() {
				t.Fatal("current higher peer must suppress rebroadcast before the syncer runs")
			}
			h.synced.Store(false)
			if h.canRebroadcast() {
				t.Fatal("initial sync must suppress rebroadcast")
			}
		})
	}
}

func TestRebroadcastPeerClaimWindow(t *testing.T) {
	for _, benched := range []bool{false, true} {
		name := "available"
		if benched {
			name = "benched"
		}
		t.Run(name, func(t *testing.T) {
			h, cleanup := newChainSyncerTestHandler(t)
			defer cleanup()
			h.enableSyncedFeatures()
			peer := registerPeerWithTD(t, h.peers, 1_000_000)
			if err := h.downloader.RegisterPeer(peer.ID(), eth.ETH68, &ethPeer{Peer: peer}); err != nil {
				t.Fatal(err)
			}
			if benched {
				setDownloaderPeerBackoff(t, h.downloader, peer.ID(), time.Hour)
			}
			_, td := h.chainSync.modeAndLocalHead()
			start := time.Now().Add(-2 * rebroadcastPeerGrace)
			for _, check := range []struct {
				elapsed time.Duration
				want    bool
			}{
				{0, false}, {rebroadcastPeerGrace - time.Nanosecond, false}, {rebroadcastPeerGrace, true},
			} {
				if got := h.rebroadcastAllowed(td, start.Add(check.elapsed)); got != check.want {
					t.Fatalf("eligibility after %s: have %v, want %v", check.elapsed, got, check.want)
				}
			}
			peer.SetHead(common.Hash{2}, big.NewInt(2_000_000))
			h.chainSync.nextSyncOp()
			if !h.canRebroadcast() {
				t.Fatal("announcements and sync retries must not renew an expired claim")
			}
			peer.SetHead(common.Hash{3}, big.NewInt(0))
			h.rebroadcastAllowed(td, time.Now())
			peer.SetHead(common.Hash{4}, big.NewInt(3_000_000))
			if !h.canRebroadcast() {
				t.Fatal("lowering and raising an unverified claim must not renew it")
			}
			h.synced.Store(false)
			if h.canRebroadcast() {
				t.Fatal("expiry must not enable an initially unsynced node")
			}
		})
	}
}

func TestRebroadcastUsesLocalHeadTD(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	peer := registerPeerWithTD(t, h.peers, 1_000_000)
	peer.SetHead(h.chain.CurrentBlock().Hash(), big.NewInt(1_000_000))
	if !h.canRebroadcast() {
		t.Fatal("a known head must use locally verified TD")
	}
}

func TestRebroadcastKnownAheadHeadDoesNotExpire(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	blocks, _ := core.GenerateChain(h.chain.Config(), h.chain.Genesis(), ethash.NewFaker(), h.database, 1, nil)
	if _, err := h.chain.InsertChain(blocks, false); err != nil {
		t.Fatal(err)
	}
	blockTD := h.chain.GetTd(blocks[0].Hash(), blocks[0].NumberU64())
	peer := registerPeerWithTD(t, h.peers, blockTD.Int64())
	peer.SetHead(blocks[0].Hash(), blockTD)
	localTD := new(big.Int).Sub(blockTD, big.NewInt(1))
	start := time.Now()
	if h.rebroadcastAllowed(localTD, start) || h.rebroadcastAllowed(localTD, start.Add(2*rebroadcastPeerGrace)) {
		t.Fatal("a locally verified ahead head must block rebroadcast until catch-up")
	}
}

func TestRebroadcastVerifiedClaimRenewsWindow(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	blocks, _ := core.GenerateChain(h.chain.Config(), h.chain.Genesis(), ethash.NewFaker(), h.database, 2, nil)
	_, td := h.chainSync.modeAndLocalHead()
	firstTD := new(big.Int).Add(td, blocks[0].Difficulty())
	peer := registerPeerWithTD(t, h.peers, firstTD.Int64())
	peer.SetHead(blocks[0].Hash(), firstTD)
	start := time.Now()
	if h.rebroadcastAllowed(td, start) {
		t.Fatal("a new higher head should suppress rebroadcast")
	}
	if _, err := h.chain.InsertChain(blocks[:1], false); err != nil {
		t.Fatal(err)
	}
	secondTD := new(big.Int).Add(firstTD, blocks[1].Difficulty())
	peer.SetHead(blocks[1].Hash(), secondTD)
	if h.rebroadcastAllowed(firstTD, start.Add(2*rebroadcastPeerGrace)) {
		t.Fatal("catching up to the verified previous claim should allow suppression for the next head")
	}
	if _, err := h.chain.InsertChain(blocks[1:], false); err != nil {
		t.Fatal(err)
	}
	if !h.canRebroadcast() {
		t.Fatal("catching up should restore rebroadcast")
	}
}

func TestRebroadcastUnrelatedProgressDoesNotRenewWindow(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	registerPeerWithTD(t, h.peers, 1_000_000)
	_, td := h.chainSync.modeAndLocalHead()
	start := time.Now().Add(-2 * rebroadcastPeerGrace)
	if h.rebroadcastAllowed(td, start) {
		t.Fatal("initial claim should suppress rebroadcast")
	}
	blocks, _ := core.GenerateChain(h.chain.Config(), h.chain.Genesis(), ethash.NewFaker(), h.database, 1, nil)
	if _, err := h.chain.InsertChain(blocks, false); err != nil {
		t.Fatal(err)
	}
	if !h.canRebroadcast() {
		t.Fatal("unrelated chain progress must not renew an unverified claim")
	}
	registerPeerWithTD(t, h.peers, 2_000_000)
	if h.canRebroadcast() {
		t.Fatal("a second, newly ahead peer must still suppress rebroadcast")
	}
}

func TestRebroadcastClaimRequiresMatchingTD(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	blocks, _ := core.GenerateChain(h.chain.Config(), h.chain.Genesis(), ethash.NewFaker(), h.database, 2, nil)
	_, td := h.chainSync.modeAndLocalHead()
	claimed := new(big.Int).Add(td, blocks[0].Difficulty())
	claimed.Add(claimed, big.NewInt(1))
	peer := registerPeerWithTD(t, h.peers, claimed.Int64())
	peer.SetHead(blocks[0].Hash(), claimed)
	if h.rebroadcastAllowed(td, time.Now().Add(-2*rebroadcastPeerGrace)) {
		t.Fatal("new claim should suppress rebroadcast")
	}
	if _, err := h.chain.InsertChain(blocks, false); err != nil {
		t.Fatal(err)
	}
	peer.SetHead(common.Hash{3}, new(big.Int).Mul(claimed, big.NewInt(10)))
	if !h.canRebroadcast() {
		t.Fatal("importing the advertised block with a different TD must not renew its claim")
	}
}

func TestRebroadcastObservesAllPeerClaims(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	registerPeerWithTD(t, h.peers, 1_000_000)
	registerPeerWithTD(t, h.peers, 2_000_000)
	_, td := h.chainSync.modeAndLocalHead()
	if h.rebroadcastAllowed(td, time.Now().Add(-2*rebroadcastPeerGrace)) {
		t.Fatal("new claims should suppress rebroadcast")
	}
	if !h.canRebroadcast() {
		t.Fatal("all claims must expire even when another peer already suppressed rebroadcast")
	}
}

func TestRebroadcastConcurrentPeerUpdates(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	peer := registerPeerWithTD(t, h.peers, 1_000_000)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			peer.SetHead(common.Hash{byte(i)}, big.NewInt(int64(i)))
			h.canRebroadcast()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			h.canRebroadcast()
		}
	}()
	wg.Wait()
}
