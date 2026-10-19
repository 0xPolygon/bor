package peerpolicy

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
)

func TestContributionShortPausesPreserveEstablishedPeers(t *testing.T) {
	for _, useful := range []bool{false, true} {
		tracker, clock := newContributionTracker()
		clock.Run(18 * time.Minute)
		for range 3 {
			tracker.Observe("peer", Evidence{Activity: ActivityEvent{ServedResponses: 1, ServedBytes: 100}})
			clock.Run(time.Minute)
		}
		want := "request-only"
		if useful {
			tracker.Observe("peer", Evidence{Activity: ActivityEvent{AcceptedTransactions: 1}})
			want = "contributing"
		}
		for range 10 {
			clock.Run(58 * time.Second)
			tracker.SetReady(false)
			clock.Run(2 * time.Second)
			contributionStatus(t, tracker, "unknown")
			tracker.SetReady(true)
			contributionStatus(t, tracker, want)
		}
		got := contributionStatus(t, tracker, want)
		if got.ServedResponses != 3 || got.ServedBytes != 300 || got.ServedMinutes != 3 || got.ObservedSeconds != 1840 {
			t.Fatalf("short pauses lost history or restarted grace: %+v", got)
		}
		if useful && (got.AcceptedTransactions != 1 || got.LastContributionAgeSeconds == nil || *got.LastContributionAgeSeconds != 600) {
			t.Fatalf("short pauses erased useful evidence: %+v", got)
		}
	}
}

func TestContributionShortPauseDelaysGraceByDuration(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(activityGrace - 10*time.Second)
	tracker.SetReady(false)
	clock.Run(20 * time.Second)
	tracker.SetReady(true)
	got := contributionStatus(t, tracker, "warming-up")
	if got.ObservedSeconds != 590 {
		t.Fatalf("short pause counted as ready time: %+v", got)
	}
	clock.Run(9 * time.Second)
	contributionStatus(t, tracker, "warming-up")
	clock.Run(time.Second)
	contributionStatus(t, tracker, "idle")
}

func TestContributionPauseLengthBoundary(t *testing.T) {
	for _, pause := range []time.Duration{activityWidth - time.Second, activityWidth} {
		tracker, clock := newContributionTracker()
		clock.Run(20 * time.Minute)
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{ServedResponses: 1}})
		tracker.SetReady(false)
		clock.Run(pause)
		tracker.SetReady(true)
		want, observed := "unknown", uint64(1200)
		if pause == activityWidth {
			want, observed = "warming-up", 0
		}
		got := contributionStatus(t, tracker, want)
		if got.ObservedSeconds != observed || got.ServedResponses != 1 {
			t.Fatalf("pause=%v: %+v", pause, got)
		}
	}
}

func TestContributionConnectionDuringPause(t *testing.T) {
	for _, remaining := range []time.Duration{20 * time.Second, 40 * time.Second} {
		tracker, clock := newContributionTracker()
		clock.Run(20 * time.Minute)
		tracker.SetReady(false)
		clock.Run(20 * time.Second)
		tracker.Connected("peer")
		clock.Run(remaining)
		got := contributionStatus(t, tracker, "unknown")
		if got.ObservedSeconds != 0 {
			t.Fatalf("unready connection accumulated coverage: %+v", got)
		}
		tracker.SetReady(true)
		got = contributionStatus(t, tracker, "warming-up")
		if got.ObservedSeconds != 0 {
			t.Fatalf("pre-connection pause charged to peer: %+v", got)
		}
		clock.Run(activityGrace)
		got = contributionStatus(t, tracker, "idle")
		if got.ObservedSeconds != 600 || got.ConnectionSeconds != uint64((activityGrace+remaining)/time.Second) {
			t.Fatalf("connection pause overlap incorrect: %+v", got)
		}
	}
}

func TestContributionLazyLongThenShortPause(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(20 * time.Minute)
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ServedResponses: 1}})
	tracker.SetReady(false)
	clock.Run(time.Minute)
	tracker.SetReady(true)
	clock.Run(5 * time.Minute)
	tracker.SetReady(false)
	clock.Run(10 * time.Second)
	tracker.SetReady(true)
	clock.Run(5 * time.Minute)
	got := contributionStatus(t, tracker, "unknown")
	if got.ObservedSeconds != 600 || got.ServedResponses != 1 {
		t.Fatalf("lazy grace update lost intermediate pauses: %+v", got)
	}
}

func TestContributionFirstReadyRetainsObservedTraffic(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	tracker.Connected("peer")
	clock.Run(20 * time.Minute)
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ServedResponses: 1, AcceptedTransactions: 1}})
	if got := contributionStatus(t, tracker, "unknown"); got.ObservedSeconds != 0 {
		t.Fatalf("coverage counted before initial readiness: %+v", got)
	}
	tracker.SetReady(true)
	got := contributionStatus(t, tracker, "warming-up")
	if got.ObservedSeconds != 0 || got.ServedResponses != 1 || got.AcceptedTransactions != 1 {
		t.Fatalf("first readiness erased actual traffic: %+v", got)
	}
	clock.Run(activityGrace)
	contributionStatus(t, tracker, "contributing")
}

func TestContributionRepeatedReadinessNotifications(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(20 * time.Minute)
	tracker.SetReady(false)
	clock.Run(59 * time.Second)
	tracker.SetReady(false)
	clock.Run(time.Second)
	tracker.SetReady(true)
	contributionStatus(t, tracker, "warming-up")
	clock.Run(activityGrace)
	tracker.SetReady(true)
	contributionStatus(t, tracker, "idle")
}

func TestContributionSameClockReadinessToggles(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(activityGrace)
	for range 10 {
		tracker.SetReady(false)
		contributionStatus(t, tracker, "unknown")
		tracker.SetReady(true)
		got := contributionStatus(t, tracker, "idle")
		if got.ObservedSeconds != 600 {
			t.Fatalf("zero-duration readiness gap changed observation: %+v", got)
		}
	}
}
