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
	"math/rand"
	"time"
)

// Request dispatch is synchronous and completes only once the message has been
// written to the peer, which happens before the worker creates its response
// timer. A peer that stops draining its connection therefore holds workers for
// longer than the response deadline suggests, so outstanding requests are
// bounded on admission rather than relying on that deadline.
const (
	// A peer legitimately needs one outstanding request per block it announced
	// but has not served yet. At a 1.5s block time and a 5s fetch deadline that
	// is under four, so 16 absorbs catch-up bursts while still bounding a peer
	// that never drains.
	maxPeerFetchRequests = 16

	// Peers reconnecting under fresh identities each get their own per-peer
	// budget while already dispatched workers from earlier identities may still
	// be waiting, so an aggregate ceiling covers that overlap. 256 is five times
	// the default maxpeers at one outstanding request each, which keeps honest
	// peers clear of the ceiling in steady state.
	maxTotalFetchRequests = 256
)

// acquireFetchSlot reserves capacity for one outstanding request to peer. It
// never blocks and never queues, so a refused caller simply leaves its
// announcements for another round or another announcer. Called from the loop,
// before the worker is spawned.
func (f *BlockFetcher) acquireFetchSlot(peer string) bool {
	return f.acquireFetchSlotWithin(peer, maxPeerFetchRequests)
}

// acquireFetchSlotWithin is acquireFetchSlot against a lowered per-peer limit,
// used to hold capacity back for a peer's pending body request.
func (f *BlockFetcher) acquireFetchSlotWithin(peer string, limit int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	if until, ok := f.fetchBackoff.Peek(peer); ok {
		if time.Now().Before(until) {
			return false
		}
		f.fetchBackoff.Remove(peer)
	}
	if f.fetchSlotsTotal >= maxTotalFetchRequests {
		return false
	}
	if f.fetchSlots[peer] >= limit {
		return false
	}
	f.fetchSlots[peer]++
	f.fetchSlotsTotal++

	return true
}

// releaseFetchSlot returns capacity taken by acquireFetchSlot. Workers defer it
// immediately so every exit releases exactly once. The per-peer entry
// disappears with its last outstanding request, while workers that are still
// running keep theirs counted across a same-id reconnect.
func (f *BlockFetcher) releaseFetchSlot(peer string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	count, ok := f.fetchSlots[peer]
	if !ok {
		return
	}
	if count <= 1 {
		delete(f.fetchSlots, peer)
	} else {
		f.fetchSlots[peer] = count - 1
	}
	f.fetchSlotsTotal--
}

// acquireAnnouncer reserves a slot for one announcer of a hash, starting from a
// random offset so a single peer does not end up owning every announced hash.
// An announcer whose already fetched headers still need a body keeps one slot
// in reserve for that grouped body request, so header refills cannot starve it
// while headers and bodies still pipeline normally below the limit.
//
// Headers waiting for a body are also capped in aggregate. The per-peer reserve
// alone lets a fresh identity keep taking capacity that earlier identities'
// bodies then cannot get, so admission stops once that backlog reaches the
// node-wide ceiling.
//
// Returns nil when no announcer qualifies, leaving the alternatives in place
// instead of discarding them along with the unavailable peer.
func (f *BlockFetcher) acquireAnnouncer(announces []*blockAnnounce, awaitingBodies map[string]bool) *blockAnnounce {
	if len(announces) == 0 || len(f.fetched) >= maxTotalFetchRequests {
		return nil
	}
	offset := rand.Intn(len(announces))
	for i := range announces {
		candidate := announces[(offset+i)%len(announces)]
		limit := maxPeerFetchRequests
		if awaitingBodies[candidate.origin] {
			limit--
		}
		if f.acquireFetchSlotWithin(candidate.origin, limit) {
			return candidate
		}
	}

	return nil
}

// acquireBodyAnnouncer is acquireAnnouncer for body retrieval, where one
// request carries a batch of hashes and so costs a single slot. Hashes landing
// on a peer already admitted this round ride that peer's slot.
func (f *BlockFetcher) acquireBodyAnnouncer(announces []*blockAnnounce, admitted map[string]bool) *blockAnnounce {
	if len(announces) == 0 {
		return nil
	}
	offset := rand.Intn(len(announces))
	for i := range announces {
		candidate := announces[(offset+i)%len(announces)]
		if admitted[candidate.origin] {
			return candidate
		}
		if f.acquireFetchSlot(candidate.origin) {
			admitted[candidate.origin] = true
			return candidate
		}
	}

	return nil
}
