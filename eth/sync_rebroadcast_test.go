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
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

func TestEnableSyncedFeaturesRebroadcastWithPeers(t *testing.T) {
	for _, test := range []struct {
		name        string
		tdDelta     int64
		wasEligible bool
		want        bool
	}{
		{"higher peer on startup", 1, false, true},
		{"higher peer after sync", 1, true, true},
		{"equal peer", 0, false, true},
		{"lower peer", -1, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, cleanup := newChainSyncerTestHandler(t)
			defer cleanup()
			handler.synced.Store(test.wasEligible)
			_, ourTD := handler.chainSync.modeAndLocalHead()
			peer := registerPeerWithTD(t, handler.peers, ourTD.Int64()+test.tdDelta)
			handler.enableSyncedFeatures()
			if !handler.synced.Load() || handler.snapSync.Load() {
				t.Fatal("enabling synced features should enable transaction processing and disable snap sync")
			}
			if got := handler.canRebroadcast(); got != test.want {
				t.Fatalf("rebroadcast gate: have %v, want %v", got, test.want)
			}
			head, _ := peer.Head()
			peer.SetHead(head, ourTD)
			handler.enableSyncedFeatures()
			if !handler.canRebroadcast() {
				t.Fatal("caught-up handler should resume rebroadcast")
			}
		})
	}
}

func TestChainSyncerRebroadcastWithoutLocalTD(t *testing.T) {
	for _, test := range []struct {
		name   string
		synced bool
		td     int64
		want   bool
	}{
		{"higher peer", true, 1, true},
		{"equal peer", true, 0, true},
		{"initial sync with higher peer", false, 1, false},
		{"initial sync with equal peer", false, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, cleanup := newChainSyncerTestHandler(t)
			defer cleanup()
			handler.synced.Store(test.synced)
			registerPeerWithTD(t, handler.peers, test.td)
			if got := handler.rebroadcastAllowed(nil); got != test.want {
				t.Fatalf("rebroadcast gate without local TD: have %v, want %v", got, test.want)
			}
		})
	}
}

func TestChainSyncerRebroadcastOnSyncFailureWhenCaughtUp(t *testing.T) {
	for _, test := range []struct {
		name   string
		synced bool
		err    error
	}{
		{"initial sync", false, context.DeadlineExceeded},
		{"catch-up timeout", true, context.DeadlineExceeded},
		{"catch-up cancellation", true, context.Canceled},
		{"catch-up peer unavailable", true, downloader.ErrPeersUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, cleanup := newChainSyncerTestHandler(t)
			defer cleanup()
			handler.synced.Store(test.synced)
			cs := handler.chainSync
			cs.force = time.NewTimer(time.Hour)
			defer cs.force.Stop()
			peer := registerPeerWithTD(t, handler.peers, 1_000_000)
			if op, _ := cs.nextSyncOp(); op == nil {
				t.Fatal("higher peer should require catch-up")
			}
			cs.doneCh = make(chan error, 1)
			head, _ := peer.Head()
			_, localTD := cs.modeAndLocalHead()
			peer.SetHead(head, localTD)
			cs.onSyncDone(test.err)
			if got := handler.canRebroadcast(); got != test.synced {
				t.Fatalf("rebroadcast gate after failed sync with no peer ahead: have %v, want %v", got, test.synced)
			}
		})
	}
}

func TestChainSyncerRebroadcastAfterSyncFailure(t *testing.T) {
	for _, test := range []struct {
		name   string
		synced bool
		err    error
	}{
		{"initial sync failure", false, errors.New("peer dropped")},
		{"catch-up peer drop", true, errors.New("peer dropped")},
		{"catch-up timeout", true, context.DeadlineExceeded},
		{"catch-up cancellation", true, context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, cleanup := newChainSyncerTestHandler(t)
			defer cleanup()
			handler.synced.Store(test.synced)
			cs := newChainSyncer(handler)
			cs.force = time.NewTimer(time.Hour)
			defer cs.force.Stop()
			peer := registerPeerWithTD(t, handler.peers, 1_000_000)
			if op, _ := cs.nextSyncOp(); op == nil || handler.canRebroadcast() != test.synced {
				t.Fatal("unverified catch-up target changed rebroadcast eligibility")
			}
			cs.doneCh = make(chan error, 1)
			cs.onSyncDone(test.err)
			if handler.canRebroadcast() != test.synced {
				t.Fatal("failed sync with an unverified peer changed rebroadcast eligibility")
			}
			head, _ := peer.Head()
			_, localTD := cs.modeAndLocalHead()
			peer.SetHead(head, localTD)
			if op, _ := cs.nextSyncOp(); op != nil {
				t.Fatal("caught-up node should not schedule another sync")
			}
			if got := handler.canRebroadcast(); got != test.synced {
				t.Fatalf("rebroadcast gate after failed sync: have %v, want %v", got, test.synced)
			}
			peer.SetHead(head, big.NewInt(1_000_000))
			if op, _ := cs.nextSyncOp(); op == nil || handler.canRebroadcast() != test.synced {
				t.Fatal("another unverified catch-up target changed rebroadcast eligibility")
			}
		})
	}
}

func TestChainSyncerRebroadcastWhilePeerBenched(t *testing.T) {
	for _, test := range []struct {
		name     string
		td       int64
		eligible bool
		cooldown bool
		minPeers int
		want     bool
	}{
		{"higher peer with eligible peer", 1_000_000, true, false, 0, true},
		{"only higher peer", 1_000_000, false, false, 0, true},
		{"lower peer", 0, true, false, 0, true},
		{"higher peer during cooldown", 1_000_000, true, true, 0, true},
		{"lower peer during cooldown", 0, true, true, 0, true},
		{"higher peer below minimum count", 1_000_000, false, false, defaultMinSyncPeers, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler, cleanup := newChainSyncerTestHandler(t)
			defer cleanup()
			handler.synced.Store(true)
			handler.maxPeers = test.minPeers
			cs := newChainSyncer(handler)
			if test.cooldown {
				cs.peersUnavailableUntil = time.Now().Add(time.Hour)
			}
			peer := registerPeerWithTD(t, handler.peers, test.td)
			if err := handler.downloader.RegisterPeer(peer.ID(), eth.ETH68, &ethPeer{Peer: peer}); err != nil {
				t.Fatal(err)
			}
			setDownloaderPeerBackoff(t, handler.downloader, peer.ID(), time.Hour)
			if test.eligible {
				registerPeerWithTD(t, handler.peers, 0)
			}
			if op, _ := cs.nextSyncOp(); op != nil {
				t.Fatal("benched peer should not produce a sync operation")
			}
			if got := handler.canRebroadcast(); got != test.want {
				t.Fatalf("rebroadcast gate: have %v, want %v", got, test.want)
			}
			head, _ := peer.Head()
			peer.SetHead(head, big.NewInt(0))
			cs.nextSyncOp()
			if !handler.canRebroadcast() {
				t.Fatal("caught-up node should resume rebroadcast without another sync")
			}
		})
	}
}

func TestChainSyncerRebroadcastAfterPeerRemoval(t *testing.T) {
	handler, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	handler.synced.Store(true)
	cs := newChainSyncer(handler)
	cs.force = time.NewTimer(time.Hour)
	defer cs.force.Stop()
	peer := registerPeerWithTD(t, handler.peers, 1_000_000)
	if op, _ := cs.nextSyncOp(); op == nil {
		t.Fatal("higher peer should require sync")
	}
	cs.doneCh = make(chan error, 1)
	if err := handler.peers.unregisterPeer(peer.ID()); err != nil {
		t.Fatal(err)
	}
	cs.onSyncDone(downloader.ErrPeersUnavailable)
	if !handler.canRebroadcast() {
		t.Fatal("previously synced node should resume rebroadcast after its higher peer disconnects")
	}
	if op, wait := cs.nextSyncOp(); op != nil || wait <= 0 || !handler.canRebroadcast() {
		t.Fatal("peer cooldown should preserve rebroadcast after the higher peer disconnects")
	}
}

func TestChainSyncerRebroadcastAfterBenchedPeerUnregister(t *testing.T) {
	handler, cleanup := newChainSyncerTestHandler(t)
	defer cleanup()
	handler.txFetcher.Start()
	defer handler.txFetcher.Stop()
	handler.synced.Store(true)
	peer := registerPeerWithTD(t, handler.peers, 1_000_000)
	if err := handler.downloader.RegisterPeer(peer.ID(), eth.ETH68, &ethPeer{Peer: peer}); err != nil {
		t.Fatal(err)
	}
	setDownloaderPeerBackoff(t, handler.downloader, peer.ID(), time.Hour)
	cs := handler.chainSync
	if op, retry := cs.nextSyncOp(); op != nil || retry <= 0 || !handler.canRebroadcast() {
		t.Fatal("benched peer with an unverified head must not suppress rebroadcast")
	}
	handler.unregisterPeer(peer.ID())
	if !handler.canRebroadcast() {
		t.Fatal("peer removal should restore rebroadcast without waiting for the syncer")
	}
}
