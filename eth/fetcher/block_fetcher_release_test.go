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

func newBlockReleaseFetcher(t *testing.T) *BlockFetcher {
	t.Helper()
	f := NewBlockFetcher(false, nil, nil, nil, nil, nil, nil, nil, nil, func(peer string) {
		t.Errorf("release dropped peer %q", peer)
	}, false, false, 0, nil, nil)
	t.Cleanup(func() {
		select {
		case <-f.quit:
		default:
			f.Stop()
		}
		f.wm.stopAndDrainTimer()
	})

	return f
}

func pendingBlockRelease(f *BlockFetcher, headers bool) map[common.Hash]*blockAnnounce {
	if headers {
		return f.fetching
	}

	return f.completing
}

func addBlockReleaseRequest(f *BlockFetcher, headers bool, hash byte, peer string) *blockAnnounce {
	announce := &blockAnnounce{hash: common.Hash{hash}, origin: peer}
	pendingBlockRelease(f, headers)[announce.hash] = announce

	return announce
}

func TestBlockFetchReleaseBatchCleanup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers bool
	}{{"headers", true}, {"bodies", false}} {
		t.Run(tc.name, func(t *testing.T) {
			testBlockFetchReleaseBatchCleanup(t, tc.headers)
		})
	}
}

func testBlockFetchReleaseBatchCleanup(t *testing.T, headers bool) {
	f := newBlockReleaseFetcher(t)
	first := addBlockReleaseRequest(f, headers, 1, "shared")
	second := addBlockReleaseRequest(f, headers, 2, "expired")
	samePeer := addBlockReleaseRequest(f, headers, 3, "shared")
	otherPeer := addBlockReleaseRequest(f, headers, 4, "other")
	release := blockFetchRelease{headers: headers, announces: []*blockAnnounce{first, second}}
	f.releaseFetchOwnership(release)
	f.releaseFetchOwnership(release)

	pending := pendingBlockRelease(f, headers)
	if len(pending) != 2 || pending[first.hash] != nil || pending[second.hash] != nil {
		t.Fatalf("released requests remain: %v", pending)
	}
	for _, announce := range []*blockAnnounce{samePeer, otherPeer} {
		if pending[announce.hash] != announce || f.announces[announce.origin] != 0 {
			t.Errorf("unrelated request or accounting changed for %q", announce.origin)
		}
	}
	if len(f.announces) != 0 {
		t.Errorf("release created announcement counters: %v", f.announces)
	}
}

func TestBlockFetchReleasePreservesReplacement(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers bool
	}{{"headers", true}, {"bodies", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBlockReleaseFetcher(t)
			original := addBlockReleaseRequest(f, tc.headers, 1, "peer")
			replacement := &blockAnnounce{hash: original.hash, origin: original.origin}
			pending := pendingBlockRelease(f, tc.headers)
			pending[original.hash] = replacement

			f.releaseFetchOwnership(blockFetchRelease{headers: tc.headers, announces: []*blockAnnounce{original}})
			if pending[original.hash] != replacement || f.announces[original.origin] != 0 {
				t.Fatal("late release removed the replacement request or its accounting")
			}
		})
	}
}

func TestBlockFetchReleasePreservesLaterStage(t *testing.T) {
	for _, stage := range []string{"fetched", "completing"} {
		t.Run(stage, func(t *testing.T) {
			testBlockFetchReleasePreservesLaterStage(t, stage)
		})
	}
}

func testBlockFetchReleasePreservesLaterStage(t *testing.T, stage string) {
	f := newBlockReleaseFetcher(t)
	announce := addBlockReleaseRequest(f, true, 1, "peer")
	delete(f.fetching, announce.hash)
	if stage == "fetched" {
		f.fetched[announce.hash] = []*blockAnnounce{announce}
	} else {
		f.completing[announce.hash] = announce
	}

	f.releaseFetchOwnership(blockFetchRelease{headers: true, announces: []*blockAnnounce{announce}})
	current := f.completing[announce.hash]
	if stage == "fetched" {
		if len(f.fetched[announce.hash]) != 1 {
			t.Fatal("late release removed the fetched header")
		}
		current = f.fetched[announce.hash][0]
	}
	if current != announce || f.announces[announce.origin] != 0 {
		t.Fatal("late release changed the later stage or its accounting")
	}
}

func TestBlockFetchReleasePreservesCompletedWork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers bool
		witness bool
	}{
		{"header queued", true, false},
		{"header witness", true, true},
		{"body queued", false, false},
		{"body witness", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testBlockFetchReleasePreservesCompletedWork(t, tc.headers, tc.witness)
		})
	}
}

func testBlockFetchReleasePreservesCompletedWork(t *testing.T, headers, pendingWitness bool) {
	f := newBlockReleaseFetcher(t)
	announce := addBlockReleaseRequest(f, headers, 1, "peer")
	op := &blockOrHeaderInject{origin: announce.origin}
	witness := &witnessRequestState{op: op, announce: announce}
	if pendingWitness {
		f.wm.pending[announce.hash] = witness
	} else {
		f.queued[announce.hash] = op
	}

	f.releaseFetchOwnership(blockFetchRelease{headers: headers, announces: []*blockAnnounce{announce}})
	if pendingBlockRelease(f, headers)[announce.hash] != announce || f.announces[announce.origin] != 0 {
		t.Fatal("late release changed a completed request or its accounting")
	}
	if pendingWitness && f.wm.pending[announce.hash] != witness {
		t.Error("late release removed pending witness work")
	}
	if !pendingWitness && f.queued[announce.hash] != op {
		t.Error("late release removed the queued block")
	}
}

func TestBlockFetchReleaseOnlyClearsMatchingStage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers bool
	}{{"headers", true}, {"bodies", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBlockReleaseFetcher(t)
			original := addBlockReleaseRequest(f, tc.headers, 1, "peer")
			otherStage := addBlockReleaseRequest(f, !tc.headers, 1, "peer")

			f.releaseFetchOwnership(blockFetchRelease{headers: tc.headers, announces: []*blockAnnounce{original}})
			if pendingBlockRelease(f, tc.headers)[original.hash] != nil {
				t.Error("released request remains")
			}
			if pendingBlockRelease(f, !tc.headers)[original.hash] != otherStage || f.announces[original.origin] != 0 {
				t.Fatal("release changed another stage or its accounting")
			}
		})
	}
}

func TestFetchAnnouncementsSnapshot(t *testing.T) {
	first := &blockAnnounce{hash: common.Hash{1}, origin: "peer"}
	second := &blockAnnounce{hash: common.Hash{2}, origin: "peer"}
	pending := map[common.Hash]*blockAnnounce{first.hash: first, second.hash: second}
	snapshot := fetchAnnouncements(pending, []common.Hash{second.hash, first.hash})
	pending[first.hash] = &blockAnnounce{hash: first.hash, origin: first.origin}
	delete(pending, second.hash)

	if len(snapshot) != 2 || snapshot[0] != second || snapshot[1] != first {
		t.Fatalf("snapshot = %v, want the original requests in hash order", snapshot)
	}
	if empty := fetchAnnouncements(pending, nil); len(empty) != 0 {
		t.Errorf("empty batch snapshot = %v", empty)
	}
	if missing := fetchAnnouncements(pending, []common.Hash{{9}}); len(missing) != 0 {
		t.Errorf("unknown hash snapshot = %v", missing)
	}
}

func TestBlockFetchReleaseNotification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers bool
	}{{"headers", true}, {"bodies", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBlockReleaseFetcher(t)
			announce := &blockAnnounce{hash: common.Hash{1}, origin: "peer"}
			done := make(chan struct{})
			go func() {
				f.notifyFetchRelease(tc.headers, []*blockAnnounce{announce})
				close(done)
			}()

			select {
			case release := <-f.fetchRelease:
				if release.headers != tc.headers || len(release.announces) != 1 || release.announces[0] != announce {
					t.Errorf("release notification = %+v", release)
				}
			case <-time.After(time.Second):
				t.Fatal("release notification was not delivered")
			}
			waitBlockReleaseNotification(t, done)
		})
	}
}

func TestBlockFetchReleaseNotificationSkipsEmpty(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	done := make(chan struct{})
	go func() {
		f.notifyFetchRelease(true, nil)
		close(done)
	}()

	select {
	case release := <-f.fetchRelease:
		t.Fatalf("empty release was delivered: %+v", release)
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("empty release notification did not return")
	}
}

func TestBlockFetchReleaseNotificationStops(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		name := "while notifying"
		if stopped {
			name = "before notifying"
		}
		t.Run(name, func(t *testing.T) {
			f := newBlockReleaseFetcher(t)
			if stopped {
				f.Stop()
			}
			announce := &blockAnnounce{hash: common.Hash{1}, origin: "peer"}
			done, started := make(chan struct{}), make(chan struct{})
			go func() {
				close(started)
				f.notifyFetchRelease(true, []*blockAnnounce{announce})
				close(done)
			}()
			<-started
			if !stopped {
				f.Stop()
			}
			waitBlockReleaseNotification(t, done)
		})
	}
}

func waitBlockReleaseNotification(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("release notification did not finish")
	}
}

func TestBlockFetchReleaseMixedBatch(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	served := addBlockReleaseRequest(f, false, 1, "peer")
	skipped := addBlockReleaseRequest(f, false, 2, "peer")
	// A partial response advances the hashes it covered to the import queue and
	// leaves the rest owned by the finished request.
	op := &blockOrHeaderInject{origin: "peer"}
	f.queued[served.hash] = op

	f.releaseFetchOwnership(blockFetchRelease{announces: []*blockAnnounce{served, skipped}})

	if f.completing[served.hash] != served || f.queued[served.hash] != op {
		t.Fatal("partial release discarded the body that was served")
	}
	if f.completing[skipped.hash] != nil {
		t.Fatal("partial release retained the hash the response skipped")
	}
}
