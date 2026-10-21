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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestHeaderSchedulingSkipsSaturatedLookups(t *testing.T) {
	p := newScheduleProbe(t)
	f := p.fetcher
	for range maxPeerFetchRequests {
		if !f.acquireFetchSlot("busy") {
			t.Fatal("could not fill slots")
		}
	}
	for range hashLimit {
		p.announce("busy", time.Second)
	}
	lookups := 0
	f.getBlock = func(common.Hash) *types.Block { lookups++; return nil }
	for range 10 {
		if !f.scheduleHeaderFetches() {
			t.Fatal("saturated announcements were not deferred")
		}
	}
	if lookups != 0 {
		t.Fatalf("full slots caused %d chain lookups", lookups)
	}
	f.releaseFetchSlot("busy")
	f.scheduleHeaderFetches()
	if lookups != 1 {
		t.Fatalf("one available slot caused %d lookups, want 1", lookups)
	}
}

func TestBlockFetchTimeoutUsesAlternative(t *testing.T) {
	hashes, blocks := makeChain(1, 42, genesis)
	block := blocks[hashes[0]]
	for _, headers := range []bool{true, false} {
		name := "body"
		if headers {
			name = "header"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tester := newTester(false)
				defer tester.fetcher.Stop()
				defer tester.fetcher.wm.witnessCache.Stop()
				imported := make(chan struct{}, 1)
				tester.fetcher.importedHook = func(*types.Header, *types.Block) { imported <- struct{}{} }
				request, _ := requestDelayedBlock(t, tester, blocks, block, headers)
				if err := tester.fetcher.Notify("healthy", block.Hash(), block.NumberU64(), time.Now(),
					tester.makeHeaderFetcher("healthy", blocks, 0), tester.makeBodyFetcher("healthy", blocks, 0), nil); err != nil {
					t.Fatal(err)
				}
				time.Sleep(3 * fetchTimeout)
				synctest.Wait()
				select {
				case <-imported:
				default:
					t.Fatal("the healthy peer's only announcement was lost on timeout")
				}
				select {
				case <-request.Cancel:
				default:
					t.Fatal("timed-out request remained open")
				}
				assertBlockPeerRetained(t, tester)
			})
		})
	}
}

func TestHeaderSchedulingPreservesAlternativeCredits(t *testing.T) {
	p := newScheduleProbe(t)
	f := p.fetcher
	hash := p.announce("slow", time.Second)
	slow := f.announced[hash][0]
	healthy := &blockAnnounce{hash: hash, origin: "healthy", time: time.Now().Add(-time.Second)}
	f.announced[hash] = append(f.announced[hash], healthy)
	f.announces["healthy"] = 1
	f.admitHeaderRequest(hash, slow)
	if f.announces["slow"] != 0 || f.announces["healthy"] != 1 || len(f.announced[hash]) != 1 {
		t.Fatal("selection consumed an alternative's announcement credit")
	}
	if !f.hasAnnouncer(hash, "slow") || !f.hasAnnouncer(hash, "healthy") || f.hasAnnouncer(hash, "other") {
		t.Fatal("pending and active announcers were not deduplicated")
	}
	f.releaseFetchOwnership(blockFetchRelease{headers: true, announces: []*blockAnnounce{slow}})
	f.admitHeaderRequest(hash, healthy)
	if len(f.announces) != 0 || len(f.announced) != 0 || f.fetching[hash] != healthy {
		t.Fatal("retry did not consume exactly the alternative's credit")
	}
	f.releaseFetchOwnership(blockFetchRelease{headers: true, announces: []*blockAnnounce{slow}})
	if f.fetching[hash] != healthy {
		t.Fatal("old request release erased the alternative")
	}
}

func TestFetchDeferredRetryBackoff(t *testing.T) {
	var backoff time.Duration
	for _, want := range []time.Duration{20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond} {
		if got := nextFetchRetry(-time.Second, true, &backoff); got != want {
			t.Fatalf("retry delay = %v, want %v", got, want)
		}
	}
	for range 10 {
		nextFetchRetry(0, true, &backoff)
	}
	if backoff != time.Second {
		t.Fatalf("retry ceiling = %v, want 1s", backoff)
	}
	if got := nextFetchRetry(2*time.Second, true, &backoff); got != 2*time.Second {
		t.Fatal("retry preceded the announcement's gather window")
	}
	if nextFetchRetry(-time.Second, false, &backoff) != -time.Second || backoff != 0 {
		t.Fatal("successful scheduling did not reset backoff")
	}
}

func TestHeaderSchedulingLightLookup(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "known"}[known], func(t *testing.T) {
			p := newScheduleProbe(t)
			f := p.fetcher
			f.light = true
			lookups := 0
			f.getHeader = func(common.Hash) *types.Header {
				lookups++
				if known {
					return genesis.Header()
				}
				return nil
			}
			f.getBlock = func(common.Hash) *types.Block { t.Error("light fetch read a full block"); return nil }
			hash := p.announce("peer", time.Millisecond)
			f.scheduleHeaderFetches()
			if lookups != 1 || (f.fetching[hash] != nil) == known {
				t.Fatal("light scheduling did not use the header lookup without a gather delay")
			}
			if known && f.fetchSlotsTotal != 0 {
				t.Fatal("known header retained a request slot")
			}
		})
	}
}
