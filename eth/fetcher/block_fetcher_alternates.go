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

// Alternatives retain their announcement credits until tried or forgotten.
// A timeout therefore cannot discard the only notification from a healthy peer.
func (f *BlockFetcher) consumeAnnouncement(hash common.Hash, origin string) {
	announces := f.announced[hash]
	remaining := announces[:0]
	for _, announce := range announces {
		if announce.origin != origin {
			remaining = append(remaining, announce)
			continue
		}
		f.announces[origin]--
		if f.announces[origin] <= 0 {
			delete(f.announces, origin)
		}
	}
	clear(announces[len(remaining):])
	if len(remaining) > 0 {
		f.announced[hash] = remaining
		return
	}
	delete(f.announced, hash)
	if f.announceChangeHook != nil {
		f.announceChangeHook(hash, false)
	}
}

func (f *BlockFetcher) hasAnnouncer(hash common.Hash, origin string) bool {
	for _, active := range []*blockAnnounce{f.fetching[hash], f.completing[hash]} {
		if active != nil && active.origin == origin {
			return true
		}
	}
	for _, announces := range [][]*blockAnnounce{f.announced[hash], f.fetched[hash]} {
		for _, announce := range announces {
			if announce.origin == origin {
				return true
			}
		}
	}
	return false
}

func nextFetchRetry(delay time.Duration, deferred bool, backoff *time.Duration) time.Duration {
	if !deferred {
		*backoff = 0
		return delay
	}
	previous := *backoff
	*backoff = min(retryAfter(2*previous, true), time.Second)
	return max(delay, *backoff)
}
