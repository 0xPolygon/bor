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
)

// A header held by a cooling supplier must not prevent healthy alternatives
// from retrieving the block. No body request has been admitted at this stage.
func (f *BlockFetcher) rerouteCoolingBody(hash common.Hash, announces []*blockAnnounce) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for _, announce := range announces {
		until, ok := f.fetchBackoff.Peek(announce.origin)
		if !ok || !now.Before(until) {
			return false
		}
	}
	for _, alternate := range f.announced[hash] {
		until, ok := f.fetchBackoff.Peek(alternate.origin)
		if !ok || !now.Before(until) {
			delete(f.fetched, hash)
			return true
		}
	}
	return false
}

func (f *BlockFetcher) resetCompletionTimer(timer *time.Timer) {
	var earliest time.Time
	for _, announces := range f.fetched {
		announce := announces[0]
		deadline := announce.time.Add(gatherSlack)
		if announce.bodyDeferred {
			deadline = f.completeRetryAt
		}
		if earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	f.completeTimerAt = earliest
	if !earliest.IsZero() {
		timer.Reset(time.Until(earliest))
	}
}

func (f *BlockFetcher) rearmFreshCompletion(timer *time.Timer, announce *blockAnnounce) {
	deadline := announce.time.Add(gatherSlack)
	if f.completeTimerAt.IsZero() || deadline.Before(f.completeTimerAt) {
		f.completeTimerAt = deadline
		timer.Reset(time.Until(deadline))
	}
}
