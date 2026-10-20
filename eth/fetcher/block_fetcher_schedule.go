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
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

// scheduleHeaderFetches turns expired announcements into header requests, one
// request per hash. It reports whether admission left any announcement behind.
// Runs on the fetcher loop.
func (f *BlockFetcher) scheduleHeaderFetches() bool {
	requests := make(map[string][]headerRequest)
	awaitingBodies := f.peersAwaitingBodies()
	deferred := false

	for hash, announces := range f.announced {
		// In current LES protocol(les2/les3), only header announce is
		// available, no need to wait too much time for header broadcast.
		wait := arriveTimeout - gatherSlack
		if f.light {
			wait = 0
		}
		if time.Since(announces[0].time) <= wait {
			continue
		}
		if !f.readyToFetch(hash) {
			f.dropAnnouncements(hash, announces)
			continue
		}
		announce := f.acquireAnnouncer(announces, awaitingBodies)
		if announce == nil {
			// No announcer has capacity to spare for a header right now.
			// rescheduleFetch holds the retry off so this does not spin.
			deferred = true

			continue
		}
		req := f.admitHeaderRequest(hash, announce)
		requests[req.announce.origin] = append(requests[req.announce.origin], req)
	}

	for peer, batch := range requests {
		f.startHeaderRequests(peer, batch)
	}

	return deferred
}

// peersAwaitingBodies lists the peers holding headers that still need a body
// request, computed once per pass so header admission can leave their slots
// alone. Scheduling bodies first is not enough on its own: a worker can free a
// slot between the two passes.
func (f *BlockFetcher) peersAwaitingBodies() map[string]bool {
	if len(f.fetched) == 0 {
		return nil
	}
	waiting := make(map[string]bool)
	for _, announces := range f.fetched {
		for _, announce := range announces {
			waiting[announce.origin] = true
		}
	}

	return waiting
}

// expiredFetches lists outstanding header requests whose fetch phase ran out.
func (f *BlockFetcher) expiredFetches() []common.Hash {
	f.mu.RLock()
	defer f.mu.RUnlock()

	expired := make([]common.Hash, 0)
	for hash, announce := range f.fetching {
		if time.Since(announce.time) > fetchTimeout {
			expired = append(expired, hash)
		}
	}

	return expired
}

// admitHeaderRequest moves an announcement into the fetching stage. The fetch
// phase clock restarts here because an announcement may have waited for
// capacity; carrying its original age over would make expiredFetches drop the
// request before any response could arrive. The announcement time travels on
// the request so latency metrics still measure from the announcement.
func (f *BlockFetcher) admitHeaderRequest(hash common.Hash, announce *blockAnnounce) headerRequest {
	announcedAt := announce.time
	f.forgetHash(hash) // This also removes from f.announced
	announce.time = time.Now()
	f.fetching[hash] = announce

	return headerRequest{hash: hash, announce: announce, fetch: announce.fetchHeader, announcedAt: announcedAt}
}

// readyToFetch reports whether a hash still needs a header request, rather than
// already being held by the witness manager, the import queue, a later fetch
// stage, or the local chain.
func (f *BlockFetcher) readyToFetch(hash common.Hash) bool {
	if f.wm.isPending(hash) || f.queued[hash] != nil {
		return false
	}
	if f.fetched[hash] != nil || f.completing[hash] != nil {
		return false
	}
	if f.light {
		return f.getHeader(hash) == nil
	}

	return f.getBlock(hash) == nil
}

// dropAnnouncements forgets a hash that no longer needs fetching, returning the
// announcement credits it holds.
func (f *BlockFetcher) dropAnnouncements(hash common.Hash, announces []*blockAnnounce) {
	for _, announce := range announces {
		f.announces[announce.origin]--
		if f.announces[announce.origin] <= 0 {
			delete(f.announces, announce.origin)
		}
	}
	delete(f.announced, hash)

	if f.announceChangeHook != nil {
		f.announceChangeHook(hash, false)
	}
}

func (f *BlockFetcher) startHeaderRequests(peer string, batch []headerRequest) {
	hashes := make([]common.Hash, len(batch))
	for i, req := range batch {
		hashes[i] = req.hash
	}
	log.Debug("Fetching scheduled headers", "peer", peer, "list", hashes)

	go func() {
		if f.fetchingHook != nil {
			f.fetchingHook(hashes)
		}
		for _, req := range batch {
			headerFetchMeter.Mark(1)

			go f.runHeaderRequest(peer, req)
		}
	}()
}

// scheduleBodyFetches turns fetched headers into body requests, grouped so that
// one request per peer carries that peer's whole batch. It reports whether
// admission left any header behind. Runs on the fetcher loop.
func (f *BlockFetcher) scheduleBodyFetches() bool {
	requests := make(map[string]*bodyRequest)
	admitted := make(map[string]bool)
	deferred := false

	for hash, announces := range f.fetched {
		announce := f.acquireBodyAnnouncer(announces, admitted)
		if announce == nil {
			deferred = true

			continue
		}
		f.groupBodyRequest(requests, hash, announce)
	}
	f.startBodyRequests(requests, admitted)

	return deferred
}

// groupBodyRequest moves one fetched header into the completion stage and adds
// it to its peer's batch. The hash is forgotten first so a block that arrived
// by another route is dropped rather than re-requested.
func (f *BlockFetcher) groupBodyRequest(requests map[string]*bodyRequest, hash common.Hash, announce *blockAnnounce) {
	announcedAt := announce.time
	f.forgetHash(hash) // Removes from f.fetched

	if f.wm.isPending(hash) || f.getBlock(hash) != nil {
		return
	}
	f.completing[hash] = announce

	req := requests[announce.origin]
	if req == nil {
		req = &bodyRequest{fetch: announce.fetchBodies, announcedAt: announcedAt}
		requests[announce.origin] = req
	}
	req.hashes = append(req.hashes, hash)
	req.announces = append(req.announces, announce)
}

func (f *BlockFetcher) startBodyRequests(requests map[string]*bodyRequest, admitted map[string]bool) {
	// A peer admitted for a hash that turned out not to need a body holds a
	// slot with nothing to send.
	for peer := range admitted {
		if requests[peer] == nil {
			f.releaseFetchSlot(peer)
		}
	}
	for peer, req := range requests {
		f.startBodyRequest(peer, *req)
	}
}

func (f *BlockFetcher) startBodyRequest(peer string, req bodyRequest) {
	log.Debug("Fetching scheduled bodies", "peer", peer, "list", req.hashes)

	go func() {
		if f.completingHook != nil {
			f.completingHook(req.hashes)
		}
		f.runBodyRequest(peer, req)
	}()
}
