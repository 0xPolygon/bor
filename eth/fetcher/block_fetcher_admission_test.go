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
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func TestFetchSlotPerPeerBound(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	for i := 0; i < maxPeerFetchRequests; i++ {
		if !f.acquireFetchSlot("peer") {
			t.Fatalf("peer refused at %d of %d", i, maxPeerFetchRequests)
		}
	}
	if f.acquireFetchSlot("peer") {
		t.Fatal("peer admitted past its bound")
	}
	if !f.acquireFetchSlot("other") {
		t.Fatal("a full peer consumed another peer's capacity")
	}
	f.releaseFetchSlot("peer")
	if !f.acquireFetchSlot("peer") {
		t.Fatal("released capacity was not reusable")
	}
}

func TestFetchSlotNodeWideBound(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	admitted := 0
	// Identity churn: each new peer id gets a fresh per-peer budget, so only
	// the node-wide ceiling bounds the aggregate.
	for i := 0; i < maxTotalFetchRequests+maxPeerFetchRequests; i++ {
		if f.acquireFetchSlot(string(rune('a' + i%26))) {
			admitted++
		}
	}
	if admitted != maxTotalFetchRequests {
		t.Fatalf("admitted %d requests, want the node-wide ceiling %d", admitted, maxTotalFetchRequests)
	}
	if f.fetchSlotsTotal != maxTotalFetchRequests {
		t.Fatalf("total accounting = %d, want %d", f.fetchSlotsTotal, maxTotalFetchRequests)
	}
}

func TestFetchSlotAccountingClearsWhenIdle(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	for _, peer := range []string{"a", "a", "b"} {
		if !f.acquireFetchSlot(peer) {
			t.Fatalf("peer %q refused", peer)
		}
	}
	for _, peer := range []string{"a", "a", "b"} {
		f.releaseFetchSlot(peer)
	}
	if len(f.fetchSlots) != 0 || f.fetchSlotsTotal != 0 {
		t.Fatalf("bookkeeping survived the last worker: %v total=%d", f.fetchSlots, f.fetchSlotsTotal)
	}
	// A release without a matching acquire must not corrupt the totals a
	// reconnecting peer's live workers still rely on.
	f.releaseFetchSlot("a")
	if !f.acquireFetchSlot("a") || f.fetchSlotsTotal != 1 {
		t.Fatalf("unmatched release corrupted accounting: total=%d", f.fetchSlotsTotal)
	}
}

func TestAcquireAnnouncerPrefersAvailablePeer(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	for i := 0; i < maxPeerFetchRequests; i++ {
		f.acquireFetchSlot("full")
	}
	announces := []*blockAnnounce{
		{hash: common.Hash{1}, origin: "full"},
		{hash: common.Hash{1}, origin: "healthy"},
	}
	for i := 0; i < 8; i++ {
		announce := f.acquireAnnouncer(announces, nil)
		if announce == nil || announce.origin != "healthy" {
			t.Fatalf("round %d chose %v, want the peer with capacity", i, announce)
		}
		f.releaseFetchSlot(announce.origin)
	}
}

func TestAcquireAnnouncerRefusesWhenAllFull(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	for _, peer := range []string{"a", "b"} {
		for i := 0; i < maxPeerFetchRequests; i++ {
			f.acquireFetchSlot(peer)
		}
	}
	announces := []*blockAnnounce{
		{hash: common.Hash{1}, origin: "a"},
		{hash: common.Hash{1}, origin: "b"},
	}
	if announce := f.acquireAnnouncer(announces, nil); announce != nil {
		t.Fatalf("admitted %q with no capacity", announce.origin)
	}
	if f.acquireAnnouncer(nil, nil) != nil {
		t.Fatal("admitted an empty announcer set")
	}
}

func TestAcquireAnnouncerReservesSlotForWaitingBodies(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	announces := []*blockAnnounce{{hash: common.Hash{1}, origin: "waiting"}}
	waiting := map[string]bool{"waiting": true}

	// Headers still pipeline alongside a pending body, up to the reserve.
	for i := 0; i < maxPeerFetchRequests-1; i++ {
		if f.acquireAnnouncer(announces, waiting) == nil {
			t.Fatalf("header %d refused below the reserved limit", i)
		}
	}
	if f.acquireAnnouncer(announces, waiting) != nil {
		t.Fatal("header request consumed the slot reserved for the waiting body")
	}
	if !f.acquireFetchSlot("waiting") {
		t.Fatal("the reserved slot was not available to the body request")
	}
	if f.acquireAnnouncer(announces, nil) != nil {
		t.Fatal("peer admitted past its outstanding-request bound")
	}
}

func TestAcquireBodyAnnouncerChargesOnePerBatch(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	admitted := make(map[string]bool)
	announces := []*blockAnnounce{{hash: common.Hash{1}, origin: "peer"}}
	for i := 0; i < maxPeerFetchRequests+4; i++ {
		if f.acquireBodyAnnouncer(announces, admitted) == nil {
			t.Fatalf("hash %d refused although the batch already holds a slot", i)
		}
	}
	if f.fetchSlots["peer"] != 1 || f.fetchSlotsTotal != 1 {
		t.Fatalf("batch took %d slots, want 1", f.fetchSlots["peer"])
	}
}

func TestRetryAfterFloorsOnlyDeferredPasses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		delay    time.Duration
		deferred bool
		want     time.Duration
	}{
		{"placed pass keeps an immediate retry", -time.Second, false, -time.Second},
		{"placed pass keeps its delay", time.Second, false, time.Second},
		{"deferred pass floors an expired delay", -time.Second, true, fetchRetryInterval},
		{"deferred pass floors a short delay", fetchRetryInterval - 1, true, fetchRetryInterval},
		{"deferred pass keeps a longer delay", time.Second, true, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryAfter(tc.delay, tc.deferred); got != tc.want {
				t.Fatalf("retryAfter(%v, %t) = %v, want %v", tc.delay, tc.deferred, got, tc.want)
			}
		})
	}
}
