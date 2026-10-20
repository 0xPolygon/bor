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
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

// scheduleProbe drives the production scheduling passes directly, so a legal
// interleaving of header completion and header refill is reproducible without
// timers.
type scheduleProbe struct {
	fetcher *BlockFetcher
	started chan common.Hash
	next    byte
}

func newScheduleProbe(t *testing.T) *scheduleProbe {
	t.Helper()
	f := NewBlockFetcher(false, nil, func(common.Hash) *types.Block { return nil }, nil, nil,
		func() uint64 { return 0 }, nil, nil, nil,
		func(peer string) { t.Errorf("scheduling dropped peer %q", peer) }, false, false, 0, nil, nil)
	t.Cleanup(func() {
		select {
		case <-f.quit:
		default:
			f.Stop()
		}
		f.wm.stopAndDrainTimer()
	})

	return &scheduleProbe{fetcher: f, started: make(chan common.Hash, 4*maxPeerFetchRequests)}
}

func (p *scheduleProbe) announce(peer string, age time.Duration) common.Hash {
	p.next++
	hash := common.Hash{p.next, byte(len(peer))}
	announce := &blockAnnounce{
		hash:   hash,
		number: 1,
		origin: peer,
		time:   time.Now().Add(-age),
		fetchHeader: func(h common.Hash, _ chan *eth.Response) (*eth.Request, error) {
			p.started <- h

			return &eth.Request{Cancel: make(chan struct{})}, nil
		},
		fetchBodies: func([]common.Hash, chan *eth.Response) (*eth.Request, error) {
			return &eth.Request{Cancel: make(chan struct{})}, nil
		},
	}
	p.fetcher.announced[hash] = []*blockAnnounce{announce}
	p.fetcher.announces[peer]++

	return hash
}

// completeHeader mirrors the loop's fetching to fetched transition and the
// worker's slot release for one outstanding request.
func (p *scheduleProbe) completeHeader(t *testing.T, hash common.Hash) {
	t.Helper()
	f := p.fetcher
	announce := f.fetching[hash]
	if announce == nil {
		t.Fatalf("no outstanding header request for %x", hash[:4])
	}
	delete(f.fetching, hash)
	f.fetched[hash] = []*blockAnnounce{announce}
	f.releaseFetchSlot(announce.origin)
}

func TestHeaderSchedulingYieldsToWaitingBodies(t *testing.T) {
	probe := newScheduleProbe(t)
	f := probe.fetcher

	for i := 0; i < maxPeerFetchRequests; i++ {
		probe.announce("peer", time.Second)
	}
	if f.scheduleHeaderFetches() {
		t.Fatal("initial batch was refused")
	}
	if f.fetchSlotsTotal != maxPeerFetchRequests {
		t.Fatalf("slots held = %d, want %d", f.fetchSlotsTotal, maxPeerFetchRequests)
	}
	probe.completeHeader(t, <-probe.started)

	// A new announcement from the same peer must not reclaim the slot the
	// waiting body needs, or fetched metadata grows without bound.
	probe.announce("peer", time.Second)
	if !f.scheduleHeaderFetches() {
		t.Fatal("header scheduling refilled the slot a waiting body needs")
	}
	if f.fetchSlotsTotal != maxPeerFetchRequests-1 {
		t.Fatalf("slots held = %d, want the freed slot left for the body", f.fetchSlotsTotal)
	}
	if f.scheduleBodyFetches() {
		t.Fatal("body scheduling was refused the slot header scheduling yielded")
	}
	if len(f.completing) != 1 || len(f.fetched) != 0 {
		t.Fatalf("completing=%d fetched=%d, want the body admitted and fetched drained", len(f.completing), len(f.fetched))
	}
}

func TestHeaderSchedulingPrefersPeerWithoutWaitingBodies(t *testing.T) {
	probe := newScheduleProbe(t)
	f := probe.fetcher

	for i := 0; i < maxPeerFetchRequests; i++ {
		probe.announce("busy", time.Second)
	}
	if f.scheduleHeaderFetches() {
		t.Fatal("initial batch was refused")
	}
	probe.completeHeader(t, <-probe.started)

	// Each hash is announced by both peers. Choosing at random would land on
	// the peer with waiting bodies about half the time.
	const rounds = 8

	hashes := make([]common.Hash, rounds)
	for i := range hashes {
		hashes[i] = probe.announce("busy", time.Second)
		f.announced[hashes[i]] = append(f.announced[hashes[i]], &blockAnnounce{
			hash: hashes[i], number: 1, origin: "idle", time: time.Now().Add(-time.Second),
			fetchHeader: func(h common.Hash, _ chan *eth.Response) (*eth.Request, error) {
				probe.started <- h

				return &eth.Request{Cancel: make(chan struct{})}, nil
			},
		})
		f.announces["idle"]++
	}
	if f.scheduleHeaderFetches() {
		t.Fatal("an alternative announcer without waiting bodies was not used")
	}
	for _, hash := range hashes {
		if f.fetching[hash] == nil || f.fetching[hash].origin != "idle" {
			t.Fatalf("header request for %x went to %v, want the peer with no waiting bodies", hash[:4], f.fetching[hash])
		}
	}
}

func TestAdmittedHeaderRequestRefreshesFetchClock(t *testing.T) {
	probe := newScheduleProbe(t)
	f := probe.fetcher

	stale := fetchTimeout + time.Second
	hash := probe.announce("peer", stale)
	announcedAt := f.announced[hash][0].time

	req := f.admitHeaderRequest(hash, f.announced[hash][0])

	// The loop sweeps fetching entries older than fetchTimeout, so an
	// announcement that waited for capacity must not arrive already expired.
	if elapsed := time.Since(f.fetching[hash].time); elapsed > fetchTimeout {
		t.Fatalf("admitted request is already %v old, the expiry sweep will drop it before a response arrives", elapsed)
	}
	if !req.announcedAt.Equal(announcedAt) {
		t.Fatalf("announcedAt = %v, want the original announcement time %v", req.announcedAt, announcedAt)
	}
	if req.hash != hash || req.announce != f.fetching[hash] {
		t.Fatal("admitted request does not own the fetching entry")
	}
}

func TestDeferredAnnouncementSurvivesExpirySweep(t *testing.T) {
	probe := newScheduleProbe(t)
	f := probe.fetcher

	for i := 0; i < maxPeerFetchRequests; i++ {
		if !f.acquireFetchSlot("peer") {
			t.Fatal("could not fill peer slots")
		}
	}
	probe.announce("peer", fetchTimeout+time.Second)
	if !f.scheduleHeaderFetches() {
		t.Fatal("full peer did not defer the announcement")
	}
	for i := 0; i < maxPeerFetchRequests; i++ {
		f.releaseFetchSlot("peer")
	}
	if f.scheduleHeaderFetches() {
		t.Fatal("freed peer did not admit the deferred announcement")
	}
	if expired := f.expiredFetches(); len(expired) != 0 {
		t.Fatalf("expiry sweep drops %d freshly dispatched request(s)", len(expired))
	}
}

func TestHeaderSchedulingStopsAtAggregateBacklog(t *testing.T) {
	for _, backlog := range []int{maxTotalFetchRequests - 1, maxTotalFetchRequests, maxTotalFetchRequests + 1} {
		t.Run(fmt.Sprint(backlog), func(t *testing.T) {
			probe := newScheduleProbe(t)
			f := probe.fetcher

			// Headers left by earlier identities; the per-peer body preference
			// does not apply to the fresh announcer used below.
			for i := 0; i < backlog; i++ {
				var hash common.Hash
				binary.BigEndian.PutUint64(hash[:8], uint64(i+1))
				f.fetched[hash] = []*blockAnnounce{{hash: hash, origin: fmt.Sprintf("prior-%d", i), time: time.Now()}}
			}
			hash := probe.announce("fresh", time.Second)
			original := f.announced[hash][0]

			deferred := f.scheduleHeaderFetches()
			if backlog < maxTotalFetchRequests {
				if deferred || f.fetching[hash] != original {
					t.Fatal("a fresh peer was refused below the aggregate backlog ceiling")
				}
				return
			}
			if !deferred {
				t.Fatalf("admitted a header with %d hashes already waiting for bodies", backlog)
			}
			if f.fetching[hash] != nil || len(f.announced[hash]) != 1 || f.announced[hash][0] != original {
				t.Fatal("aggregate refusal did not retain the pending announcement")
			}
			if f.fetchSlotsTotal != 0 {
				t.Fatalf("aggregate refusal reserved %d worker slots", f.fetchSlotsTotal)
			}
			if len(f.fetched) != backlog {
				t.Fatalf("aggregate refusal altered waiting bodies: %d, want %d", len(f.fetched), backlog)
			}
		})
	}
}
