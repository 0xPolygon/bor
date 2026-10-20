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
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestHeaderSchedulingWaitsForAnnouncement(t *testing.T) {
	p := newScheduleProbe(t)
	hash := p.announce("peer", 0)
	announcement := p.fetcher.announced[hash][0]
	if p.fetcher.scheduleHeaderFetches() {
		t.Fatal("fresh announcement marked capacity-deferred")
	}
	if p.fetcher.fetching[hash] != nil || p.fetcher.fetchSlotsTotal != 0 || len(p.fetcher.announced[hash]) != 1 || p.fetcher.announced[hash][0] != announcement {
		t.Fatal("fresh announcement was fetched before its gather window")
	}
}

func TestHeaderSchedulingKeepsLaterStages(t *testing.T) {
	for _, stage := range []string{"fetched", "completing"} {
		t.Run(stage, func(t *testing.T) {
			p := newScheduleProbe(t)
			f := p.fetcher
			hash := p.announce("new", time.Second)
			owned := &blockAnnounce{hash: hash, origin: "existing", time: time.Now()}
			if stage == "fetched" {
				f.fetched[hash] = []*blockAnnounce{owned}
			} else {
				f.completing[hash] = owned
			}
			if f.scheduleHeaderFetches() {
				t.Fatal("duplicate announcement deferred")
			}
			if f.fetchSlotsTotal != 0 || f.fetching[hash] != nil || len(f.announced[hash]) != 0 {
				t.Fatal("duplicate announcement started another header request")
			}
			if stage == "fetched" && (len(f.fetched[hash]) != 1 || f.fetched[hash][0] != owned) {
				t.Fatal("lost fetched ownership")
			}
			if stage == "completing" && f.completing[hash] != owned {
				t.Fatal("lost completing ownership")
			}
		})
	}
}

func TestHeaderSchedulingDropsKnownAnnouncements(t *testing.T) {
	p := newScheduleProbe(t)
	f := p.fetcher
	hash := p.announce("peer", time.Second)
	f.getBlock = func(common.Hash) *types.Block { return genesis }
	f.scheduleHeaderFetches()
	if _, exists := f.announces["peer"]; exists {
		t.Fatal("empty per-peer announcement entry retained")
	}
	if len(f.announced[hash]) != 0 || f.fetchSlotsTotal != 0 {
		t.Fatal("known block announcement retained or fetched")
	}
}

func TestAcquireBodyAnnouncerRefusesEmpty(t *testing.T) {
	for _, announces := range [][]*blockAnnounce{nil, {}} {
		f := newBlockReleaseFetcher(t)
		admitted := make(map[string]bool)
		if f.acquireBodyAnnouncer(announces, admitted) != nil {
			t.Fatal("admitted empty body announcer set")
		}
		if f.fetchSlotsTotal != 0 || len(admitted) != 0 {
			t.Fatal("empty announcer set reserved capacity")
		}
	}
}
