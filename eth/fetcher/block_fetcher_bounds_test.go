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
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

// stalledDispatch models a peer that stops draining its connection: the request
// callback blocks before it can hand back a Request, which is where the real
// dispatcher blocks and the reason the response deadline cannot bound workers.
type stalledDispatch struct {
	entered chan struct{}
	release chan struct{}
	inside  atomic.Int32
	peak    atomic.Int32
	calls   atomic.Int32
}

func newStalledDispatch(capacity int) *stalledDispatch {
	return &stalledDispatch{
		entered: make(chan struct{}, capacity),
		release: make(chan struct{}),
	}
}

func (s *stalledDispatch) headerFetcher() headerRequesterFn {
	return func(common.Hash, chan *eth.Response) (*eth.Request, error) {
		s.enter()
		defer s.inside.Add(-1)
		<-s.release

		return nil, errors.New("peer stalled")
	}
}

func (s *stalledDispatch) enter() {
	s.calls.Add(1)
	if depth := s.inside.Add(1); depth > s.peak.Load() {
		s.peak.Store(depth)
	}
	select {
	case s.entered <- struct{}{}:
	default:
	}
}

func (s *stalledDispatch) await(t *testing.T, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		select {
		case <-s.entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d requests reached dispatch", i, count)
		}
	}
}

func announceSynthetic(t *testing.T, f *BlockFetcher, peer string, count int, fetch headerRequesterFn) {
	t.Helper()
	for i := 0; i < count; i++ {
		hash := common.Hash{}
		hash[0], hash[1] = byte(i), byte(i>>8)
		hash[31] = peer[0]
		// Number 1 keeps every hash inside the queue distance window.
		if err := f.Notify(peer, hash, 1, time.Now().Add(-arriveTimeout), fetch, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFetchWorkersBoundedWhenDispatchBlocks(t *testing.T) {
	tester := newTester(false)
	defer tester.fetcher.Stop()

	stalled := newStalledDispatch(maxPeerFetchRequests)
	defer close(stalled.release)

	announceSynthetic(t, tester.fetcher, "stalled", maxPeerFetchRequests+24, stalled.headerFetcher())
	stalled.await(t, maxPeerFetchRequests)

	// Give the loop several scheduling passes to try the surplus announcements.
	time.Sleep(10 * fetchRetryInterval)

	if peak := stalled.peak.Load(); peak > maxPeerFetchRequests {
		t.Fatalf("%d concurrent requests reached dispatch, want at most %d", peak, maxPeerFetchRequests)
	}
	if calls := stalled.calls.Load(); calls > maxPeerFetchRequests {
		t.Fatalf("%d dispatch attempts, want at most %d: refused announcements were redispatched", calls, maxPeerFetchRequests)
	}

	tester.fetcher.mu.RLock()
	held, total := tester.fetcher.fetchSlots["stalled"], tester.fetcher.fetchSlotsTotal
	tester.fetcher.mu.RUnlock()

	if held != maxPeerFetchRequests || total != maxPeerFetchRequests {
		t.Fatalf("slots held=%d total=%d, want %d", held, total, maxPeerFetchRequests)
	}
}

func TestFetchAdmissionKeepsHealthyPeerProgressing(t *testing.T) {
	hashes, blocks := makeChain(1, 61, genesis)
	block := blocks[hashes[0]]

	tester := newTester(false)
	defer tester.fetcher.Stop()

	imported := make(chan struct{}, 1)
	tester.fetcher.importedHook = func(*types.Header, *types.Block) { imported <- struct{}{} }

	stalled := newStalledDispatch(maxPeerFetchRequests)
	defer close(stalled.release)

	announceSynthetic(t, tester.fetcher, "stalled", maxPeerFetchRequests+8, stalled.headerFetcher())
	stalled.await(t, maxPeerFetchRequests)

	err := tester.fetcher.Notify("healthy", block.Hash(), block.NumberU64(), time.Now(),
		tester.makeHeaderFetcher("healthy", blocks, -gatherSlack),
		tester.makeBodyFetcher("healthy", blocks, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-imported:
	case <-time.After(5 * time.Second):
		t.Fatal("a saturated peer blocked an unrelated healthy peer")
	}
}

func TestFetchAdmissionKeepsAlternativeAnnouncers(t *testing.T) {
	hashes, blocks := makeChain(1, 62, genesis)
	block := blocks[hashes[0]]

	tester := newTester(false)
	defer tester.fetcher.Stop()
	f := tester.fetcher

	imported := make(chan struct{}, 1)
	f.importedHook = func(*types.Header, *types.Block) { imported <- struct{}{} }

	stalled := newStalledDispatch(maxPeerFetchRequests)
	defer close(stalled.release)

	announceSynthetic(t, f, "stalled", maxPeerFetchRequests, stalled.headerFetcher())
	stalled.await(t, maxPeerFetchRequests)

	// The full peer announces first, so a refusal that discarded the whole
	// announcement list would take the healthy alternative down with it.
	if err := f.Notify("stalled", block.Hash(), block.NumberU64(), time.Now(), stalled.headerFetcher(), nil, nil); err != nil {
		t.Fatal(err)
	}
	err := f.Notify("healthy", block.Hash(), block.NumberU64(), time.Now(),
		tester.makeHeaderFetcher("healthy", blocks, -gatherSlack),
		tester.makeBodyFetcher("healthy", blocks, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-imported:
	case <-time.After(5 * time.Second):
		t.Fatal("a full announcer prevented the healthy announcer from supplying the block")
	}
}

func TestFetchAdmissionDoesNotBusyLoop(t *testing.T) {
	tester := newTester(false)
	defer tester.fetcher.Stop()

	stalled := newStalledDispatch(maxPeerFetchRequests)
	defer close(stalled.release)

	announceSynthetic(t, tester.fetcher, "stalled", maxPeerFetchRequests+32, stalled.headerFetcher())
	stalled.await(t, maxPeerFetchRequests)

	settled := runtime.NumGoroutine()
	time.Sleep(15 * fetchRetryInterval)

	if grown := runtime.NumGoroutine() - settled; grown > maxPeerFetchRequests {
		t.Fatalf("goroutines grew by %d while admission was full", grown)
	}
	// A spinning timer would keep redispatching the announcements it refused.
	if calls := stalled.calls.Load(); calls != maxPeerFetchRequests {
		t.Fatalf("%d dispatch attempts over the window, want %d", calls, maxPeerFetchRequests)
	}
}

func TestFetchSlotReleasedOnEveryExit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		exit    string
		release bool
	}{
		{"nil fetch function", "nil", true},
		{"dispatch error", "error", true},
		{"nil response", "nilres", true},
		{"wrong response type", "badtype", true},
		{"shutdown", "quit", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testFetchSlotReleasedOnExit(t, tc.exit, tc.release)
		})
	}
}

func testFetchSlotReleasedOnExit(t *testing.T, exit string, wantRelease bool) {
	f := newBlockReleaseFetcher(t)
	released := drainFetchReleases(f)

	announce := &blockAnnounce{hash: common.Hash{1}, origin: "peer"}
	f.fetching[announce.hash] = announce
	if !f.acquireFetchSlot("peer") {
		t.Fatal("could not take a slot")
	}
	req := headerRequest{hash: announce.hash, announce: announce, fetch: exitFetcher(t, f, exit)}
	if exit == "quit" {
		f.Stop()
	}
	f.runHeaderRequest("peer", req)

	f.mu.RLock()
	leaked, total := len(f.fetchSlots), f.fetchSlotsTotal
	f.mu.RUnlock()

	if leaked != 0 || total != 0 {
		t.Fatalf("slot leaked on the %q exit: %d peers total=%d", exit, leaked, total)
	}
	assertRelease(t, released, announce, wantRelease)
}

func assertRelease(t *testing.T, released <-chan blockFetchRelease, announce *blockAnnounce, want bool) {
	t.Helper()
	if !want {
		select {
		case release := <-released:
			t.Fatalf("unexpected release notification: %+v", release)
		case <-time.After(100 * time.Millisecond):
		}

		return
	}
	select {
	case release := <-released:
		if !release.headers || len(release.announces) != 1 || release.announces[0] != announce {
			t.Fatalf("release notification = %+v", release)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("exit did not return ownership of its announcement")
	}
}

func exitFetcher(t *testing.T, f *BlockFetcher, exit string) headerRequesterFn {
	t.Helper()
	if exit == "nil" {
		return nil
	}

	return func(_ common.Hash, sink chan *eth.Response) (*eth.Request, error) {
		if exit == "error" {
			return nil, errors.New("dispatch failed")
		}
		request := &eth.Request{Peer: "peer"}
		go func() {
			switch exit {
			case "nilres":
				sink <- nil
			case "badtype":
				sink <- &eth.Response{Req: request, Res: nil, Done: make(chan error, 1)}
			case "quit":
				<-f.quit
			}
		}()

		return request, nil
	}
}

// drainFetchReleases forwards release notifications so workers under test do
// not block on the loop, which is not running here.
func drainFetchReleases(f *BlockFetcher) <-chan blockFetchRelease {
	released := make(chan blockFetchRelease, 8)
	go func() {
		for {
			select {
			case release := <-f.fetchRelease:
				released <- release
			case <-f.quit:
				return
			}
		}
	}()

	return released
}
