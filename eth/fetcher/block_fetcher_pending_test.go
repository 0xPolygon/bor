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
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

// pendingCredits counts the announcement records each origin holds in
// f.announced, which is exactly what f.announces must mirror.
func pendingCredits(f *BlockFetcher) map[string]int {
	credits := make(map[string]int)
	for _, announces := range f.announced {
		for _, announce := range announces {
			credits[announce.origin]++
		}
	}

	return credits
}

func assertPendingAccounting(t *testing.T, f *BlockFetcher) {
	t.Helper()
	want := pendingCredits(f)
	if len(f.announces) != len(want) {
		t.Fatalf("announces tracks %d origins, pending records span %d", len(f.announces), len(want))
	}
	for origin, count := range want {
		if f.announces[origin] != count {
			t.Fatalf("origin %q credited %d, holds %d pending records", origin, f.announces[origin], count)
		}
	}
}

func pendingAnnounce(origin string, hash common.Hash) *blockAnnounce {
	return &blockAnnounce{hash: hash, number: 1, origin: origin, time: time.Now()}
}

// seedPending registers count announcements for origin the way Notify does.
func seedPending(f *BlockFetcher, origin string, count int, first uint64) []common.Hash {
	hashes := make([]common.Hash, count)
	for i := range hashes {
		binary.BigEndian.PutUint64(hashes[i][:8], first+uint64(i))
		f.announced[hashes[i]] = append(f.announced[hashes[i]], pendingAnnounce(origin, hashes[i]))
		f.announces[origin]++
	}

	return hashes
}

func TestForgetHashDebitsOnlyPendingAnnouncements(t *testing.T) {
	for _, stage := range []string{"fetching", "fetched", "completing"} {
		t.Run(stage, func(t *testing.T) {
			f := newBlockReleaseFetcher(t)
			kept := seedPending(f, "peer", 4, 100)

			var hash common.Hash
			hash[0] = 0xaa
			announce := pendingAnnounce("peer", hash)
			switch stage {
			case "fetching":
				f.fetching[hash] = announce
			case "fetched":
				f.fetched[hash] = []*blockAnnounce{announce}
			case "completing":
				f.completing[hash] = announce
			}

			f.forgetHash(hash)

			if f.announces["peer"] != len(kept) {
				t.Fatalf("clearing a %s entry debited pending credits: %d, want %d", stage, f.announces["peer"], len(kept))
			}
			assertPendingAccounting(t, f)
			if f.fetching[hash] != nil || f.fetched[hash] != nil || f.completing[hash] != nil {
				t.Fatalf("%s metadata survived forgetHash", stage)
			}
		})
	}
}

func TestForgetHashDebitsTheAnnouncedList(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	hashes := seedPending(f, "peer", 3, 200)
	f.announced[hashes[0]] = append(f.announced[hashes[0]], pendingAnnounce("other", hashes[0]))
	f.announces["other"]++

	f.forgetHash(hashes[0])

	if f.announces["peer"] != 2 {
		t.Fatalf("peer credited %d after forgetting one of its three announcements, want 2", f.announces["peer"])
	}
	if _, ok := f.announces["other"]; ok {
		t.Fatal("an origin with no remaining announcements kept a counter")
	}
	assertPendingAccounting(t, f)
}

func TestExpirySweepPreservesPendingAccounting(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	kept := seedPending(f, "peer", 6, 300)

	// Headers already admitted carry no pending credit; admitHeaderRequest
	// released it, so the sweep must not take a second one.
	admitted := f.admitHeaderRequest(kept[0], f.announced[kept[0]][0])
	admitted.announce.time = time.Now().Add(-fetchTimeout - time.Second)

	expired := f.expiredFetches()
	if len(expired) != 1 {
		t.Fatalf("expiry sweep found %d requests, want 1", len(expired))
	}
	for _, hash := range expired {
		f.forgetHash(hash)
	}
	if f.announces["peer"] != len(kept)-1 {
		t.Fatalf("peer credited %d after expiry, want %d", f.announces["peer"], len(kept)-1)
	}
	assertPendingAccounting(t, f)
}

func TestAdmitAnnouncementOriginCap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin string
		want   bool
	}{
		{"existing origin continues at the cap", "origin-0", true},
		{"fresh origin declined at the cap", "newcomer", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBlockReleaseFetcher(t)
			for i := 0; i < maxPendingAnnounceOrigins; i++ {
				seedPending(f, fmt.Sprintf("origin-%d", i), 1, uint64(i)*1000)
			}
			if _, ok := f.admitAnnouncement(tc.origin); ok != tc.want {
				t.Fatalf("admitAnnouncement(%q) = %t, want %t", tc.origin, ok, tc.want)
			}
		})
	}
}

func TestAdmitAnnouncementRecoversSpaceAfterRemoval(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	var hashes []common.Hash
	for i := 0; i < maxPendingAnnounceOrigins; i++ {
		hashes = append(hashes, seedPending(f, fmt.Sprintf("origin-%d", i), 1, uint64(i)*1000)...)
	}
	if _, ok := f.admitAnnouncement("newcomer"); ok {
		t.Fatal("a fresh origin was admitted at the origin cap")
	}

	// Only an actual removal of pending records frees an origin slot.
	f.forgetHash(hashes[0])

	if _, ok := f.announces["origin-0"]; ok {
		t.Fatal("a drained origin kept its counter")
	}
	if _, ok := f.admitAnnouncement("newcomer"); !ok {
		t.Fatal("space was not recovered after pending records were removed")
	}
	assertPendingAccounting(t, f)
}

func TestAdmitAnnouncementBoundsDuplicateHashOrigins(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	var shared common.Hash
	shared[0] = 0x7f

	// Every origin announces the same hash, so len(f.announced) stays at one
	// while the retained record count tracks the number of origins.
	admitted := 0
	for i := 0; i < maxPendingAnnounceOrigins*2; i++ {
		origin := fmt.Sprintf("origin-%d", i)
		count, ok := f.admitAnnouncement(origin)
		if !ok {
			continue
		}
		f.announces[origin] = count
		f.announced[shared] = append(f.announced[shared], pendingAnnounce(origin, shared))
		admitted++
	}
	if admitted != maxPendingAnnounceOrigins {
		t.Fatalf("admitted %d origins for one hash, want the origin cap %d", admitted, maxPendingAnnounceOrigins)
	}
	if len(f.announced) != 1 {
		t.Fatalf("expected a single announced hash, got %d", len(f.announced))
	}
	assertPendingAccounting(t, f)
}

func TestAdmitAnnouncementKeepsPerOriginLimit(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	seedPending(f, "peer", hashLimit, 500)

	if _, ok := f.admitAnnouncement("peer"); ok {
		t.Fatalf("origin admitted past the per-origin limit of %d", hashLimit)
	}
	if _, ok := f.admitAnnouncement("fresh"); !ok {
		t.Fatal("a full origin blocked an unrelated origin below the origin cap")
	}
}

func TestPendingAnnouncementsStayBoundedAcrossExpiryCycles(t *testing.T) {
	tester := newTester(false)
	defer tester.fetcher.Stop()
	f := tester.fetcher

	sinks := make(chan chan *eth.Response, 4*maxPeerFetchRequests)
	fetch := func(_ common.Hash, sink chan *eth.Response) (*eth.Request, error) {
		sinks <- sink

		return &eth.Request{Cancel: make(chan struct{})}, nil
	}
	next := uint64(0)
	announce := func(count int) {
		for i := 0; i < count; i++ {
			next++
			var hash common.Hash
			binary.BigEndian.PutUint64(hash[:8], next)
			if err := f.Notify("slow", hash, 1, time.Now().Add(-arriveTimeout), fetch, nil, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	announce(maxPeerFetchRequests)
	active := awaitSinks(t, sinks, maxPeerFetchRequests)
	announce(hashLimit)

	for cycle := 0; cycle < 3; cycle++ {
		expirePendingFetches(t, f)
		announce(maxPeerFetchRequests)
		active = replyEmptyHeaders(t, sinks, active)
		announce(maxPeerFetchRequests)
	}

	resume := pauseBlockTimeoutLoop(t, f)
	defer resume()
	f.mu.RLock()
	defer f.mu.RUnlock()

	if queued := len(f.announced); queued > hashLimit {
		t.Fatalf("pending metadata grew to %d hashes, limit %d (credited %d)", queued, hashLimit, f.announces["slow"])
	}
	assertPendingAccounting(t, f)
}

func awaitSinks(t *testing.T, sinks <-chan chan *eth.Response, count int) []chan *eth.Response {
	t.Helper()
	active := make([]chan *eth.Response, count)
	for i := range active {
		select {
		case active[i] = <-sinks:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d header requests started", i, count)
		}
	}

	return active
}

// expirePendingFetches ages the outstanding requests past the fetch deadline so
// the loop's own sweep runs, without waiting out fetchTimeout.
func expirePendingFetches(t *testing.T, f *BlockFetcher) {
	t.Helper()
	resume := pauseBlockTimeoutLoop(t, f)
	f.mu.Lock()
	if len(f.fetching) != maxPeerFetchRequests {
		f.mu.Unlock()
		resume()
		t.Fatalf("outstanding requests = %d, want %d", len(f.fetching), maxPeerFetchRequests)
	}
	for _, owned := range f.fetching {
		owned.time = time.Now().Add(-fetchTimeout - time.Second)
	}
	f.mu.Unlock()
	resume()

	resume = pauseBlockTimeoutLoop(t, f)
	outstanding := len(f.fetching)
	resume()

	if outstanding != 0 {
		t.Fatalf("expiry sweep left %d outstanding requests", outstanding)
	}
}

func replyEmptyHeaders(t *testing.T, sinks <-chan chan *eth.Response, active []chan *eth.Response) []chan *eth.Response {
	t.Helper()
	empty := eth.BlockHeadersRequest{}
	for _, sink := range active {
		select {
		case sink <- &eth.Response{Res: &empty, Done: make(chan error, 1)}:
		case <-time.After(5 * time.Second):
			t.Fatal("worker did not accept a late empty response")
		}
	}

	return awaitSinks(t, sinks, len(active))
}
