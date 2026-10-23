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

import "time"

// Fresh hashes keep their gather deadlines even while other hashes wait for
// capacity. Incoming announcements never move the existing retry deadline.
func (f *BlockFetcher) resetFetchTimer(timer *time.Timer) {
	var earliest time.Time
	for _, announces := range f.announced {
		announce := announces[0]
		deadline := f.gatherDeadline(announce)
		if announce.deferred {
			deadline = f.fetchRetryAt
		}
		if earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	f.fetchTimerAt = earliest
	if !earliest.IsZero() {
		timer.Reset(time.Until(earliest))
	}
}

// New announcements only compare deadlines; scanning all retained alternatives
// on every incoming announcement would make bursts quadratic in queue size.
func (f *BlockFetcher) rearmFreshFetch(timer *time.Timer, announce *blockAnnounce) {
	if f.announced[announce.hash][0] != announce {
		return
	}
	deadline := f.gatherDeadline(announce)
	if f.fetchTimerAt.IsZero() || deadline.Before(f.fetchTimerAt) {
		f.fetchTimerAt = deadline
		timer.Reset(time.Until(deadline))
	}
}

func (f *BlockFetcher) gatherDeadline(announce *blockAnnounce) time.Time {
	if f.light {
		return announce.time.Add(lightTimeout)
	}
	return announce.time.Add(arriveTimeout)
}
