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
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
)

func TestBodyRetryCoolingSupplier(t *testing.T) {
	for _, mode := range []string{"healthy alternative", "cooling alternative", "no alternative", "supplier healthy", "expired cooldown"} {
		t.Run(mode, func(t *testing.T) {
			p := newScheduleProbe(t)
			f := p.fetcher
			hash := p.announce("other", time.Second)
			f.fetched[hash] = []*blockAnnounce{{origin: "slow", time: time.Now()}}
			if mode == "no alternative" {
				f.dropAnnouncements(hash, f.announced[hash])
			}
			if mode != "supplier healthy" {
				f.backoffFetchPeer("slow")
			}
			if mode == "expired cooldown" {
				f.fetchBackoff.Add("slow", time.Now().Add(-time.Second))
			}
			if mode == "cooling alternative" {
				f.backoffFetchPeer("other")
			}
			want := mode == "healthy alternative"
			require.Equal(t, want, f.rerouteCoolingBody(hash, f.fetched[hash]))
			if want {
				require.Nil(t, f.fetched[hash])
				require.Equal(t, 1, f.announces["other"])
				require.False(t, f.scheduleHeaderFetches())
				require.Equal(t, hash, <-p.started)
				require.Equal(t, "other", f.fetching[hash].origin)
			} else {
				require.Len(t, f.fetched[hash], 1)
			}
		})
	}
}
func TestBodyRetryFreshCompletionDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newScheduleProbe(t)
		f := p.fetcher
		stalled := common.Hash{1}
		f.fetched[stalled] = []*blockAnnounce{{origin: "slow", time: time.Now().Add(-time.Second)}}
		f.backoffFetchPeer("slow")
		require.True(t, f.scheduleBodyFetches())
		f.completeRetry = time.Second
		timer := time.NewTimer(time.Hour)
		defer timer.Stop()
		f.rescheduleComplete(timer, true)
		retryAt := f.completeRetryAt
		fresh := &blockAnnounce{origin: "fresh", time: time.Now()}
		f.fetched[common.Hash{2}] = []*blockAnnounce{fresh}
		start := time.Now()
		f.rearmFreshCompletion(timer, fresh)
		<-timer.C
		require.Equal(t, gatherSlack, time.Since(start))
		require.Equal(t, retryAt, f.completeRetryAt)
		require.Equal(t, time.Second, f.completeRetry)
		delete(f.fetched, common.Hash{2})
		f.resetCompletionTimer(timer)
		<-timer.C
		require.Equal(t, retryAt, time.Now())
	})
}

func TestBodyRetryLateHeaderUsesAlternative(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hashes, blocks := makeChain(1, 85, genesis)
		block := blocks[hashes[0]]
		tester := newTester(false)
		f := tester.fetcher
		defer f.Stop()
		defer f.wm.witnessCache.Stop()
		imported := make(chan struct{}, 1)
		f.importedHook = func(*types.Header, *types.Block) { imported <- struct{}{} }
		requests := make(chan chan *ethproto.Response, 1)
		request := &ethproto.Request{Cancel: make(chan struct{})}
		fetch := func(_ common.Hash, sink chan *ethproto.Response) (*ethproto.Request, error) {
			requests <- sink
			return request, nil
		}
		require.NoError(t, f.Notify("slow", block.Hash(), block.NumberU64(), time.Now(), fetch, tester.makeBodyFetcher("slow", blocks, 0), nil))
		sink := <-requests
		f.backoffFetchPeer("slow")
		start := time.Now()
		require.NoError(t, f.Notify("healthy", block.Hash(), block.NumberU64(), time.Now(), tester.makeHeaderFetcher("healthy", blocks, 0), tester.makeBodyFetcher("healthy", blocks, 0), nil))
		headers := ethproto.BlockHeadersRequest{block.Header()}
		sink <- &ethproto.Response{Req: request, Res: &headers, Done: make(chan error, 1)}
		select {
		case <-imported:
		case <-time.After(2 * time.Second):
			t.Fatal("healthy alternative did not recover block during supplier cooldown")
		}
		require.Less(t, time.Since(start), fetchPeerBackoff)
		resume := pauseBlockTimeoutLoop(t, f)
		defer resume()
		require.Empty(t, f.fetched)
	})
}
