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

const (
	fetchPeerBackoff = 10 * time.Second
	// Keep cooldown metadata bounded across reconnects and peer identity churn.
	maxFetchBackoffPeers = 2 * maxTotalFetchRequests
)

// Concurrent requests can time out together. They share one cooldown instead
// of extending it each time another outstanding request expires.
func (f *BlockFetcher) backoffFetchPeer(peer string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	if until, ok := f.fetchBackoff.Peek(peer); ok && now.Before(until) {
		return
	}
	f.fetchBackoff.Add(peer, now.Add(fetchPeerBackoff))
}
