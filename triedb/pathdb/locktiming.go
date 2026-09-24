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

package pathdb

import (
	"os"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

// slowLockThreshold is the wait+hold time above which a pathdb lock site is
// logged. Normal acquisitions are microseconds; anything this slow is a stall.
// For the disk layer read sites, hold time is roughly the database read
// latency, since the read lock is held across the read.
const slowLockThreshold = 100 * time.Millisecond

// lockTimingSites lists every instrumented lock site.
var lockTimingSites = []string{
	"tree.add", "tree.cap", "tree.node", "tree.lookupAccount", "tree.lookupStorage", "tree.bottom",
	"disk.commit", "disk.node", "disk.account", "disk.storage", "disk.waitFlush",
}

// lockTimers holds the wait and hold timer of every lock site.
type lockTimers struct {
	wait, hold map[string]*metrics.Timer
}

// activeLockTimers is nil unless lock timing is on. It is off by default and
// turned on by the BOR_PATHDB_LOCKTIMING environment variable at start-up;
// when off, each site costs one atomic load.
var activeLockTimers atomic.Pointer[lockTimers]

func init() {
	if os.Getenv("BOR_PATHDB_LOCKTIMING") != "" {
		enableLockTiming()
	}
}

// enableLockTiming registers the pathdb/lock/<site>/{wait,hold} timers and
// turns lock timing on.
func enableLockTiming() {
	t := &lockTimers{wait: map[string]*metrics.Timer{}, hold: map[string]*metrics.Timer{}}
	for _, site := range lockTimingSites {
		t.wait[site] = metrics.NewRegisteredTimer("pathdb/lock/"+site+"/wait", nil)
		t.hold[site] = metrics.NewRegisteredTimer("pathdb/lock/"+site+"/hold", nil)
	}
	activeLockTimers.Store(t)
}

// lockTiming measures one acquisition of a lock site. Usage:
//
//	lt := startLockTiming()
//	l.RLock()
//	defer l.RUnlock()
//	lt.acquired()
//	defer lt.done("site")
type lockTiming struct {
	timers        *lockTimers // nil when lock timing is off
	start, locked time.Time
}

// startLockTiming is called right before the lock is taken.
func startLockTiming() lockTiming {
	t := activeLockTimers.Load()
	if t == nil {
		return lockTiming{}
	}
	return lockTiming{timers: t, start: time.Now()}
}

// acquired is called right after the lock is taken.
func (lt *lockTiming) acquired() {
	if lt.timers != nil {
		lt.locked = time.Now()
	}
}

// done records the wait and hold time of the site, and logs it when the total
// reaches slowLockThreshold.
func (lt lockTiming) done(site string) {
	if lt.timers == nil {
		return
	}
	wait, hold := lt.locked.Sub(lt.start), time.Since(lt.locked)
	lt.timers.wait[site].Update(wait)
	lt.timers.hold[site].Update(hold)
	if wait+hold >= slowLockThreshold {
		log.Warn("pathdb: slow lock", "site", site, "wait", wait, "hold", hold)
	}
}
