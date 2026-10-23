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

import "github.com/ethereum/go-ethereum/common"

// blockFetchRelease hands back announcements whose request ended without the
// block reaching a later stage, covering timeouts as well as dispatch failures
// and responses that did not carry the requested data.
type blockFetchRelease struct {
	retryBodies bool
	headers     bool
	announces   []*blockAnnounce
}

// fetchAnnouncements snapshots the announcements a scheduled request owns, so
// the worker can identify them later even though the maps keep changing.
func fetchAnnouncements(pending map[common.Hash]*blockAnnounce, hashes []common.Hash) []*blockAnnounce {
	announces := make([]*blockAnnounce, 0, len(hashes))
	for _, hash := range hashes {
		if announce := pending[hash]; announce != nil {
			announces = append(announces, announce)
		}
	}

	return announces
}

func (f *BlockFetcher) notifyFetchRelease(headers bool, announces []*blockAnnounce) {
	if len(announces) == 0 {
		return
	}
	f.notifyRequestRelease(blockFetchRelease{headers: headers, announces: announces})
}

func (f *BlockFetcher) notifyRequestRelease(release blockFetchRelease) {
	select {
	case f.fetchRelease <- release:
	case <-f.quit:
	}
}

// releaseFetchOwnership runs on the fetcher loop so a finished request cannot
// erase a newer attempt for the same hash, nor a block that has already
// advanced to the import queue or to witness retrieval.
func (f *BlockFetcher) releaseFetchOwnership(release blockFetchRelease) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	requeued := false
	pending := f.completing
	if release.headers {
		pending = f.fetching
	}
	for _, announce := range release.announces {
		if announce == nil || pending[announce.hash] != announce {
			continue
		}
		if f.queued[announce.hash] != nil || f.wm.isPending(announce.hash) {
			continue
		}
		// Scheduling already released this announcement's count; the counts
		// still held belong to other announcements from the same peer.
		delete(pending, announce.hash)
		if release.retryBodies && !release.headers {
			// A fresh token prevents an old release from clearing the retry.
			retry := *announce
			retry.bodyDeferred = true
			f.fetched[announce.hash] = []*blockAnnounce{&retry}
			requeued = true
		}
	}
	return requeued
}
