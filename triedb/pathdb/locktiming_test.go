package pathdb

import (
	"os"
	"regexp"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/metrics"
)

// withLockTimers sets the active lock timers for one test and restores the
// previous value afterwards.
func withLockTimers(t *testing.T, enable bool) *lockTimers {
	t.Helper()
	prev := activeLockTimers.Load()
	t.Cleanup(func() { activeLockTimers.Store(prev) })
	if !enable {
		activeLockTimers.Store(nil)
		return nil
	}
	metrics.Enable()
	enableLockTiming()
	return activeLockTimers.Load()
}

func TestLockTimingDisabledIsNoop(t *testing.T) {
	withLockTimers(t, false)

	lt := startLockTiming()
	if lt.timers != nil || !lt.start.IsZero() {
		t.Fatal("measurement started while lock timing is off")
	}
	lt.acquired()
	if !lt.locked.IsZero() {
		t.Fatal("acquire time set while lock timing is off")
	}
	lt.done("disk.node") // must not touch the (absent) timers
}

func TestLockTimingRecordsWaitAndHold(t *testing.T) {
	timers := withLockTimers(t, true)

	const hold = 5 * time.Millisecond
	var mu sync.RWMutex
	func() {
		lt := startLockTiming()
		mu.RLock()
		defer mu.RUnlock()
		lt.acquired()
		defer lt.done("disk.node")
		time.Sleep(hold)
	}()

	h := timers.hold["disk.node"].Snapshot()
	if h.Count() != 1 {
		t.Fatalf("hold count = %d, want 1", h.Count())
	}
	if h.Max() < hold.Nanoseconds() {
		t.Fatalf("hold max = %v, want >= %v", time.Duration(h.Max()), hold)
	}
	w := timers.wait["disk.node"].Snapshot()
	if w.Count() != 1 {
		t.Fatalf("wait count = %d, want 1", w.Count())
	}
	if w.Max() >= hold.Nanoseconds() {
		t.Fatalf("wait max = %v on an uncontended lock, want < %v", time.Duration(w.Max()), hold)
	}
	if n := timers.hold["disk.storage"].Snapshot().Count(); n != 0 {
		t.Fatalf("other site recorded %d samples, want 0", n)
	}
}

func TestLockTimingMeasuresWaitOnContendedLock(t *testing.T) {
	timers := withLockTimers(t, true)

	const held = 20 * time.Millisecond
	var mu sync.RWMutex
	mu.Lock()
	go func() {
		time.Sleep(held)
		mu.Unlock()
	}()
	func() {
		lt := startLockTiming()
		mu.RLock()
		defer mu.RUnlock()
		lt.acquired()
		defer lt.done("tree.node")
	}()

	if w := timers.wait["tree.node"].Snapshot().Max(); w < held.Nanoseconds() {
		t.Fatalf("wait max = %v, want >= %v", time.Duration(w), held)
	}
}

// A measurement that started while timing was off must not be recorded when
// timing is turned on before it ends (it would report a huge wait).
func TestLockTimingStartedWhileDisabled(t *testing.T) {
	withLockTimers(t, false)
	lt := startLockTiming()
	lt.acquired()

	metrics.Enable()
	enableLockTiming()
	lt.done("disk.node")

	if n := activeLockTimers.Load().wait["disk.node"].Snapshot().Count(); n != 0 {
		t.Fatalf("recorded %d samples for a measurement started while disabled", n)
	}
}

// Every site name used at a call site must have timers, or done would
// dereference a nil timer when lock timing is on.
func TestLockTimingCallSitesAreRegistered(t *testing.T) {
	re := regexp.MustCompile(`lt\.done\("([^"]+)"\)`)
	var used []string
	for _, file := range []string{"disklayer.go", "layertree.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			used = append(used, m[1])
		}
	}
	if len(used) != len(lockTimingSites) {
		t.Fatalf("found %d call sites, want %d (one per registered site)", len(used), len(lockTimingSites))
	}
	for _, site := range used {
		if !slices.Contains(lockTimingSites, site) {
			t.Errorf("call site %q is not in lockTimingSites", site)
		}
	}
}

// A slow acquisition must record the split between wait and hold (and log).
func TestLockTimingSlowSite(t *testing.T) {
	timers := withLockTimers(t, true)

	now := time.Now()
	lt := lockTiming{timers: timers, start: now.Add(-150 * time.Millisecond), locked: now.Add(-100 * time.Millisecond)}
	lt.done("disk.commit")

	w := time.Duration(timers.wait["disk.commit"].Snapshot().Max())
	h := time.Duration(timers.hold["disk.commit"].Snapshot().Max())
	if w != 50*time.Millisecond {
		t.Fatalf("wait = %v, want 50ms", w)
	}
	if h < 100*time.Millisecond {
		t.Fatalf("hold = %v, want >= 100ms", h)
	}
}
