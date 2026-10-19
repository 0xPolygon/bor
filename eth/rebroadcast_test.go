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
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
)

func TestRebroadcastUnverifiedPeerRotation(t *testing.T) {
	h, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	h.enableSyncedFeatures()
	_, td := h.chainSync.modeAndLocalHead()
	peer := registerPeerWithTD(t, h.peers, 1_000_000)
	for elapsed := time.Duration(0); elapsed <= legacypool.DefaultConfig.RebroadcastMaxAge; elapsed += legacypool.DefaultConfig.RebroadcastInterval {
		if elapsed > 0 && elapsed%time.Minute == 0 {
			if err := h.peers.unregisterPeer(peer.ID()); err != nil {
				t.Fatal(err)
			}
			peer = registerPeerWithTD(t, h.peers, 1_000_000)
		}
		if !h.rebroadcastAllowed(td) {
			t.Errorf("unverified peer suppressed recovery at %s", elapsed)
		}
	}
}

func TestRebroadcastIgnoresUnverifiedHeads(t *testing.T) {
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
			if !h.canRebroadcast() {
				t.Fatal("unverified higher peer must not suppress rebroadcast")
			}
			h.synced.Store(false)
			if h.canRebroadcast() {
				t.Fatal("initial sync must suppress rebroadcast")
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

func TestRebroadcastKnownAheadHeadUntilCatchUp(t *testing.T) {
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
	registerPeerWithTD(t, h.peers, 2_000_000)
	if h.rebroadcastAllowed(localTD) {
		t.Fatal("a locally verified ahead head must block rebroadcast until catch-up")
	}
	peer = reconnectRebroadcastPeer(t, h, peer)
	peer.SetHead(blocks[0].Hash(), new(big.Int))
	if h.rebroadcastAllowed(localTD) {
		t.Fatal("reconnection or self-reported TD changed the verified ahead head")
	}
	if !h.rebroadcastAllowed(blockTD) {
		t.Fatal("catch-up did not restore rebroadcast")
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
