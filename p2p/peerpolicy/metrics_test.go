package peerpolicy

import (
	"math"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
)

func TestPeerPolicyCountersAndTransitions(t *testing.T) {
	if !metrics.Enabled() {
		metrics.Enable()
	}
	clock := new(mclock.Simulated)
	tracker := New(clock)
	m := tracker.metrics
	events := m.reasons[RequestVolume].Snapshot().Count()
	windows := m.windows[RequestVolume].Snapshot().Count()
	items := m.items[Requests].Snapshot().Count()
	bytes := m.bytes[Requests].Snapshot().Count()
	throttle := m.throttle.Snapshot().Count()
	jail := m.jail.Snapshot().Count()
	samples := m.risk.Snapshot().Count()
	e := Evidence{Family: Requests, Items: 641, Bytes: 1024}
	tracker.Observe("a", e)
	tracker.Observe("a", e)
	clock.Run(windowWidth)
	tracker.Observe("a", e)
	assertRisk(t, tracker, "a", 40, "throttle")
	clock.Run(windowWidth)
	tracker.Observe("a", Evidence{Reason: InvalidBlock})
	assertRisk(t, tracker, "a", 100, "jail")
	if m.reasons[RequestVolume].Snapshot().Count()-events != 3 || m.windows[RequestVolume].Snapshot().Count()-windows != 2 {
		t.Fatal("events and reason windows must be counted separately")
	}
	if m.items[Requests].Snapshot().Count()-items != 1923 || m.bytes[Requests].Snapshot().Count()-bytes != 3072 {
		t.Fatal("missing traffic accounting")
	}
	if m.throttle.Snapshot().Count()-throttle != 1 || m.jail.Snapshot().Count()-jail != 1 || m.risk.Snapshot().Count()-samples != 4 {
		t.Fatal("action transitions or risk samples were counted incorrectly")
	}
}

func TestPeerPolicyInputBoundaries(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	id := strings.Repeat("a", 128)
	tracker.Observe(id, Evidence{Reason: InvalidEncoding})
	assertRisk(t, tracker, id, 60, "throttle")
	tracker.Observe(id+"a", Evidence{Reason: InvalidEncoding})
	assertRisk(t, tracker, id+"a", 0, "none")
	if reasonCount.String() != "unknown" || Reason(255).String() != "unknown" {
		t.Fatal("unbounded reason name")
	}
}

func TestPeerPolicyEvictionCounter(t *testing.T) {
	tracker := New(new(mclock.Simulated))
	before := tracker.metrics.evictions.Snapshot().Count()
	for i := 0; i <= maxPeers; i++ {
		tracker.Observe(string(rune(i+1)), Evidence{})
	}
	if tracker.metrics.evictions.Snapshot().Count()-before != 1 {
		t.Fatal("missing eviction counter")
	}
}

func TestPeerPolicySaturatingAccounting(t *testing.T) {
	for _, tc := range []struct{ current, delta, limit, want uint64 }{
		{0, 1, 10, 1}, {3, 4, 10, 7}, {3, 7, 10, 10},
		{3, 8, 10, 10}, {10, 1, 10, 10}, {9, math.MaxUint64, 10, 10},
	} {
		if got := saturatingAdd(tc.current, tc.delta, tc.limit); got != tc.want {
			t.Fatalf("%+v: got %d", tc, got)
		}
	}
}
