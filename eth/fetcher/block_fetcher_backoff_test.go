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
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func assertBlockFetchCooldown(t *testing.T, f *BlockFetcher, imported <-chan struct{}) {
	t.Helper()
	time.Sleep(fetchPeerBackoff - time.Second)
	synctest.Wait()
	require.False(t, f.acquireFetchSlot("slow"), "timed-out peer acquired a new slot during cooldown")
	select {
	case <-imported:
		t.Fatal("timed-out peer was retried before cooldown expired")
	default:
	}
	time.Sleep(time.Second)
}

func TestFetchPeerBackoffAdmission(t *testing.T) {
	for _, bodies := range []bool{false, true} {
		t.Run(fmt.Sprintf("bodies=%t", bodies), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newBlockReleaseFetcher(t)
				defer f.wm.witnessCache.Stop()
				acquire := func(announces []*blockAnnounce) *blockAnnounce {
					if bodies {
						return f.acquireBodyAnnouncer(announces, make(map[string]bool))
					}
					return f.acquireAnnouncer(announces, nil)
				}
				slow := &blockAnnounce{hash: common.Hash{1}, origin: "slow"}
				healthy := &blockAnnounce{hash: slow.hash, origin: "healthy"}
				require.True(t, f.acquireFetchSlot(slow.origin))
				f.backoffFetchPeer(slow.origin)
				f.releaseFetchSlot(slow.origin)
				require.Nil(t, acquire([]*blockAnnounce{slow}))
				require.Same(t, healthy, acquire([]*blockAnnounce{slow, healthy}))
				f.releaseFetchSlot(healthy.origin)
				require.Zero(t, f.fetchSlotsTotal)
				time.Sleep(fetchPeerBackoff - time.Nanosecond)
				require.Nil(t, acquire([]*blockAnnounce{slow}))
				time.Sleep(time.Nanosecond)
				require.Same(t, slow, acquire([]*blockAnnounce{slow}))
				f.releaseFetchSlot(slow.origin)
				require.Zero(t, f.fetchBackoff.Len())
			})
		})
	}
}

func TestFetchPeerBackoffDoesNotEscalate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newBlockReleaseFetcher(t)
		defer f.wm.witnessCache.Stop()
		for range 8 {
			f.backoffFetchPeer("slow")
			require.False(t, f.acquireFetchSlot("slow"))
			time.Sleep(fetchPeerBackoff - time.Second)
			f.backoffFetchPeer("slow")
			require.False(t, f.acquireFetchSlot("slow"))
			time.Sleep(time.Second)
			require.True(t, f.acquireFetchSlot("slow"), "overlapping timeouts extended the cooldown")
			f.releaseFetchSlot("slow")
		}
	})
}

func TestFetchPeerBackoffBound(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	defer f.wm.witnessCache.Stop()
	for i := 0; i <= maxFetchBackoffPeers; i++ {
		f.backoffFetchPeer(fmt.Sprintf("peer-%d", i))
	}
	require.Equal(t, maxFetchBackoffPeers, f.fetchBackoff.Len())
	require.False(t, f.acquireFetchSlot(fmt.Sprintf("peer-%d", maxFetchBackoffPeers)))
	require.True(t, f.acquireFetchSlot("healthy"))
	f.releaseFetchSlot("healthy")
}

func TestFetchPeerBackoffConcurrentAdmission(t *testing.T) {
	f := newBlockReleaseFetcher(t)
	defer f.wm.witnessCache.Stop()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				f.backoffFetchPeer("slow")
				if f.acquireFetchSlot("slow") {
					t.Error("acquired a slot during cooldown")
					f.releaseFetchSlot("slow")
				}
				if f.acquireFetchSlot("healthy") {
					f.releaseFetchSlot("healthy")
				}
			}
		})
	}
	wg.Wait()
	require.Zero(t, f.fetchSlotsTotal)
	require.Empty(t, f.fetchSlots)
}

func TestFetchPeerBackoffExpiredEntry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newBlockReleaseFetcher(t)
		defer f.wm.witnessCache.Stop()
		f.backoffFetchPeer("slow")
		time.Sleep(fetchPeerBackoff)
		f.backoffFetchPeer("slow")
		require.False(t, f.acquireFetchSlot("slow"))
		time.Sleep(fetchPeerBackoff)
		require.True(t, f.acquireFetchSlot("slow"))
		f.releaseFetchSlot("slow")
	})
}
