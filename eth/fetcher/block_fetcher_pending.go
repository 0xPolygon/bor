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

// maxPendingAnnounceOrigins caps how many origins may hold pending
// announcements at once. hashLimit bounds each origin on its own, but nothing
// bounded the number of origins, and peers reconnecting under fresh identities
// keep acquiring new quota. Reusing maxTotalFetchRequests pairs the two
// node-wide bounds; together with hashLimit it caps retained announcement
// records, duplicates of one hash across origins included.
//
// This bounds metadata. It is not a fairness guarantee, and refusing a new
// origin at the cap says nothing about whether that peer is misbehaving.
const maxPendingAnnounceOrigins = maxTotalFetchRequests

// admitAnnouncement reports the credit a pending announcement from origin would
// take, and whether there is room for it. An origin that already holds pending
// announcements keeps using its own per-origin quota; a new one is declined
// until an existing origin's records are actually removed.
//
// Runs on the fetcher loop.
func (f *BlockFetcher) admitAnnouncement(origin string) (int, bool) {
	count, known := f.announces[origin]
	if !known && len(f.announces) >= maxPendingAnnounceOrigins {
		return 0, false
	}
	count++

	return count, count <= hashLimit
}
