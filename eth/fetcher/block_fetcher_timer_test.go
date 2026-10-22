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
)

func TestHeaderSchedulingFreshDeadlineDuringBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newScheduleProbe(t)
		f := p.fetcher
		stalled := p.announce("alternate", time.Second)
		f.fetching[stalled] = &blockAnnounce{origin: "silent", time: time.Now()}
		require.True(t, f.scheduleHeaderFetches())
		f.fetchRetry = time.Second
		timer := time.NewTimer(time.Hour)
		defer timer.Stop()
		f.rescheduleFetch(timer, true)
		retryAt := f.fetchRetryAt
		time.Sleep(time.Millisecond)
		fresh := p.announce("available", 0)
		start := time.Now()
		f.rearmFreshFetch(timer, f.announced[fresh][0])
		<-timer.C
		require.Equal(t, arriveTimeout, time.Since(start))
		require.Equal(t, retryAt, f.fetchRetryAt)
		require.Equal(t, time.Second, f.fetchRetry)
		require.True(t, f.scheduleHeaderFetches())
		require.Equal(t, fresh, <-p.started)
		require.Len(t, f.announced[stalled], 1)
	})
}

func TestHeaderSchedulingAnnouncementPreservesRetryDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newScheduleProbe(t)
		f := p.fetcher
		hash := p.announce("alternate", time.Second)
		f.completing[hash] = &blockAnnounce{origin: "silent"}
		require.True(t, f.scheduleHeaderFetches())
		timer := time.NewTimer(time.Hour)
		defer timer.Stop()
		f.fetchRetry = time.Second
		f.rescheduleFetch(timer, true)
		deadline := f.fetchRetryAt
		time.Sleep(800 * time.Millisecond)
		fresh := p.announce("fresh", 0)
		f.rearmFreshFetch(timer, f.announced[fresh][0])
		<-timer.C
		require.Equal(t, deadline, time.Now())
	})
}

func TestHeaderSchedulingInitialTimer(t *testing.T) {
	for _, light := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			p := newScheduleProbe(t)
			f := p.fetcher
			f.light = light
			timer := time.NewTimer(time.Hour)
			defer timer.Stop()
			fresh := p.announce("fresh", 0)
			start := time.Now()
			f.rearmFreshFetch(timer, f.announced[fresh][0])
			<-timer.C
			expected := arriveTimeout
			if light {
				expected = lightTimeout
			}
			require.Equal(t, expected, time.Since(start))
		})
	}
}

func TestHeaderSchedulingDeferredTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := newScheduleProbe(t)
		f := p.fetcher
		hash := p.announce("alternate", time.Second)
		f.completing[hash] = &blockAnnounce{origin: "silent"}
		require.True(t, f.scheduleHeaderFetches())
		timer := time.NewTimer(time.Hour)
		defer timer.Stop()
		start := time.Now()
		f.rescheduleFetch(timer, true)
		<-timer.C
		require.Equal(t, fetchRetryInterval, time.Since(start))
		require.True(t, f.scheduleHeaderFetches())
		f.rescheduleFetch(timer, true)
		<-timer.C
		require.Equal(t, 3*fetchRetryInterval, time.Since(start))
	})
}
