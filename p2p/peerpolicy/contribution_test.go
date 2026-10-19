package peerpolicy

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
)

func newContributionTracker() (*Tracker, *mclock.Simulated) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	tracker.SetReady(true)
	tracker.Connected("peer")
	return tracker, clock
}

func contributionStatus(t *testing.T, tracker *Tracker, want string) *ContributionSnapshot {
	t.Helper()
	snapshot := tracker.Snapshot("peer")
	if snapshot.Contribution == nil || snapshot.Contribution.Status != want {
		t.Fatalf("contribution = %+v, want status %s", snapshot.Contribution, want)
	}
	if snapshot.Contribution.Scope != "eth" || snapshot.Contribution.Mode != "observe" || snapshot.Contribution.UsefulDataCoverage != "transaction-admission" {
		t.Fatalf("missing observation scope: %+v", snapshot.Contribution)
	}
	if want != "unknown" && !snapshot.Contribution.ConnectionKnown {
		t.Fatalf("classified peer without connection coverage: %+v", snapshot.Contribution)
	}
	return snapshot.Contribution
}

func TestContributionClassification(t *testing.T) {
	for _, test := range []struct {
		name   string
		event  ActivityEvent
		status string
	}{
		{"idle", ActivityEvent{}, "idle"},
		{"announcements", ActivityEvent{ReceivedMessages: 1, ReceivedBytes: 20}, "unknown"},
		{"requests-only", ActivityEvent{ReceivedMessages: 1}, "unknown"},
		{"partial-coverage", ActivityEvent{UnclassifiedMessages: 1}, "unknown"},
		{"accepted-tx", ActivityEvent{AcceptedTransactions: 1}, "contributing"},
		{"positive-with-unknown", ActivityEvent{AcceptedTransactions: 1, UnclassifiedMessages: 1}, "contributing"},
		{"one-serving-minute", ActivityEvent{ServedResponses: 100, ServedBytes: 1024}, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracker, clock := newContributionTracker()
			tracker.Observe("peer", Evidence{Activity: test.event})
			contributionStatus(t, tracker, "warming-up")
			clock.Run(activityGrace - time.Second)
			contributionStatus(t, tracker, "warming-up")
			clock.Run(time.Second)
			got := contributionStatus(t, tracker, test.status)
			if got.ObservedSeconds != 600 || got.ConnectionSeconds != 600 || got.WindowSeconds != 1800 || got.GraceSeconds != 600 {
				t.Fatalf("wrong observation profile: %+v", got)
			}
			if test.event.AcceptedTransactions == 0 && got.LastContributionAgeSeconds != nil {
				t.Fatalf("non-contributing traffic counted as useful: %+v", got)
			}
		})
	}
}

func TestContributionRequestOnlyMinutes(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(activityGrace)
	for minute := 1; minute <= 3; minute++ {
		for range 2 {
			tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1, ServedResponses: 1, ServedBytes: 100}})
		}
		want := "unknown"
		if minute == 3 {
			want = "request-only"
		}
		got := contributionStatus(t, tracker, want)
		if got.ServedMinutes != uint64(minute) || got.ServedResponses != uint64(2*minute) || got.ServedBytes != uint64(200*minute) {
			t.Fatalf("serving counters: %+v", got)
		}
		clock.Run(time.Minute)
	}
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{UnclassifiedMessages: 1}})
	contributionStatus(t, tracker, "unknown")
	assertRisk(t, tracker, "peer", 0, "none")
}

func TestContributionServingExpiryAndRefusals(t *testing.T) {
	tracker, clock := newContributionTracker()
	for range 3 {
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{ServedResponses: 1}})
		clock.Run(time.Minute)
	}
	clock.Run(activityGrace - 3*time.Minute)
	contributionStatus(t, tracker, "request-only")
	clock.Run(activityCount*activityWidth - activityGrace)
	got := contributionStatus(t, tracker, "unknown")
	if got.ServedMinutes != 2 || got.ServedResponses != 2 {
		t.Fatalf("old serving minute did not expire: %+v", got)
	}
	clock.Run(2 * time.Minute)
	contributionStatus(t, tracker, "idle")
	for range 3 {
		tracker.Observe("peer", Evidence{Refused: true, Family: BodyReplies, Activity: ActivityEvent{ReceivedMessages: 1}})
		clock.Run(time.Minute)
	}
	got = contributionStatus(t, tracker, "unknown")
	if got.ServedMinutes != 0 || got.ServedResponses != 0 {
		t.Fatalf("refused responses counted as delivered: %+v", got)
	}
}

func TestContributionWindowExpiry(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(59 * time.Second)
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{AcceptedTransactions: 1}})
	clock.Run(activityGrace)
	got := contributionStatus(t, tracker, "contributing")
	if got.LastContributionAgeSeconds == nil || *got.LastContributionAgeSeconds != 600 {
		t.Fatalf("last contribution age: %+v", got)
	}
	clock.Run(19 * time.Minute)
	contributionStatus(t, tracker, "contributing")
	clock.Run(time.Second)
	got = contributionStatus(t, tracker, "idle")
	if got.AcceptedTransactions != 0 || got.LastContributionAgeSeconds == nil || *got.LastContributionAgeSeconds != 1741 {
		t.Fatalf("expired contribution: %+v", got)
	}
}

func TestContributionLongPauseRetainsUnclassifiedEvidence(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(activityGrace)
	contributionStatus(t, tracker, "idle")
	tracker.SetReady(true)
	contributionStatus(t, tracker, "idle")
	tracker.SetReady(false)
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{AcceptedTransactions: 1}})
	clock.Run(activityGrace)
	got := contributionStatus(t, tracker, "unknown")
	if got.ObservedSeconds != 600 {
		t.Fatalf("unready interval counted: %+v", got)
	}
	tracker.SetReady(true)
	got = contributionStatus(t, tracker, "warming-up")
	if got.AcceptedTransactions != 0 || got.UnclassifiedMessages != 1 || got.LastContributionAgeSeconds != nil || got.ObservedSeconds != 0 {
		t.Fatalf("unclassified evidence or new grace lost: %+v", got)
	}
	clock.Run(activityGrace - time.Second)
	contributionStatus(t, tracker, "warming-up")
	clock.Run(time.Second)
	contributionStatus(t, tracker, "unknown")
}

func TestContributionLongPausePreservesContributions(t *testing.T) {
	tracker, clock := newContributionTracker()
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{AcceptedTransactions: 1}})
	clock.Run(activityGrace)
	contributionStatus(t, tracker, "contributing")
	tracker.SetReady(false)
	got := contributionStatus(t, tracker, "unknown")
	if got.AcceptedTransactions != 1 || got.LastContributionAgeSeconds == nil {
		t.Fatalf("readiness erased observed contribution: %+v", got)
	}
	clock.Run(time.Minute)
	tracker.SetReady(true)
	got = contributionStatus(t, tracker, "warming-up")
	if got.AcceptedTransactions != 1 || got.ObservedSeconds != 0 {
		t.Fatalf("long pause lost counters or failed to restart grace: %+v", got)
	}
	clock.Run(activityGrace)
	got = contributionStatus(t, tracker, "contributing")
	if got.ObservedSeconds != 600 || got.ConnectionSeconds != 1260 {
		t.Fatalf("connection or observation age reset incorrectly: %+v", got)
	}
}

func TestContributionInitialReadinessAndRegistration(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	contributionStatus(t, tracker, "unknown")
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{AcceptedTransactions: 1}})
	clock.Run(activityGrace)
	contributionStatus(t, tracker, "unknown")
	tracker.SetReady(true)
	clock.Run(activityGrace)
	contributionStatus(t, tracker, "unknown")
	tracker.Connected("peer")
	contributionStatus(t, tracker, "warming-up")
	clock.Run(activityGrace)
	contributionStatus(t, tracker, "idle")
}

func TestContributionReconnectPreservesRisk(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(activityGrace)
	tracker.Observe("peer", Evidence{Reason: InvalidBlock, Activity: ActivityEvent{AcceptedTransactions: 1}})
	contributionStatus(t, tracker, "contributing")
	tracker.Connected("peer")
	got := contributionStatus(t, tracker, "warming-up")
	if got.AcceptedTransactions != 0 || got.ConnectionSeconds != 0 || got.LastContributionAgeSeconds != nil {
		t.Fatalf("reconnect retained activity: %+v", got)
	}
	assertRisk(t, tracker, "peer", 60, "throttle")
}

func TestContributionProtectedPeersObserved(t *testing.T) {
	tracker, clock := newContributionTracker()
	tracker.SetExempt("peer", true)
	clock.Run(activityGrace)
	tracker.ObserveWithClass("peer", true, Evidence{Reason: InvalidBlock, Activity: ActivityEvent{ReceivedMessages: 1, AcceptedTransactions: 1}})
	got := contributionStatus(t, tracker, "contributing")
	if !got.Protected || got.AcceptedTransactions != 1 || got.ReceivedMessages != 1 {
		t.Fatalf("protected peer activity lost: %+v", got)
	}
	assertRisk(t, tracker, "peer", 0, "none")
	tracker.SetExempt("peer", false)
	got = contributionStatus(t, tracker, "contributing")
	if got.Protected || got.AcceptedTransactions != 1 {
		t.Fatalf("class change erased activity: %+v", got)
	}
}

func TestContributionActivityDoesNotChangeRiskMetrics(t *testing.T) {
	if !metrics.Enabled() {
		metrics.Enable()
	}
	tracker, _ := newContributionTracker()
	before := tracker.metrics.risk.Snapshot().Count()
	traffic := tracker.metrics.items[Other].Snapshot().Count()
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1}})
	tracker.ObserveWithClass("peer", false, Evidence{Activity: ActivityEvent{ServedResponses: 1}})
	if tracker.metrics.risk.Snapshot().Count() != before || tracker.metrics.items[Other].Snapshot().Count() != traffic {
		t.Fatal("activity-only event affected scoring metrics")
	}
	assertRisk(t, tracker, "peer", 0, "none")
}

func TestContributionCountersSaturate(t *testing.T) {
	tracker, clock := newContributionTracker()
	maxima := ActivityEvent{ReceivedMessages: math.MaxUint64, ReceivedBytes: math.MaxUint64,
		ServedResponses: math.MaxUint64, ServedBytes: math.MaxUint64, AcceptedTransactions: math.MaxUint64,
		UnclassifiedMessages: math.MaxUint64}
	for range 2 {
		tracker.Observe("peer", Evidence{Activity: maxima})
	}
	clock.Run(time.Minute)
	tracker.Observe("peer", Evidence{Activity: maxima})
	got := tracker.Snapshot("peer").Contribution
	if got.ActivityEvent != maxima || got.ServedMinutes != 2 {
		t.Fatalf("overflow in activity aggregation: %+v", got)
	}
}

func TestContributionEvictionAndInputBounds(t *testing.T) {
	tracker, clock := newContributionTracker()
	for i := 0; i < maxPeers; i++ {
		tracker.Connected(fmt.Sprint(i))
	}
	clock.Run(activityGrace)
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedMessages: 1}})
	got := contributionStatus(t, tracker, "unknown")
	if got.ConnectionKnown {
		t.Fatal("evicted connection coverage was reconstructed from activity")
	}
	if tracker.peers.Len() != maxPeers {
		t.Fatal("unbounded contribution records")
	}
	tracker.Connected("")
	tracker.Connected(string(make([]byte, 129)))
	if tracker.peers.Contains("") || tracker.peers.Contains(string(make([]byte, 129))) {
		t.Fatal("invalid ID retained")
	}
	tracker.Connected(strings.Repeat("a", 128))
	if !tracker.peers.Contains(strings.Repeat("a", 128)) {
		t.Fatal("maximum valid ID rejected")
	}
}

func TestContributionMixedEvidenceStillScores(t *testing.T) {
	if !metrics.Enabled() {
		metrics.Enable()
	}
	for _, event := range []Evidence{
		{Reason: InvalidBlock}, {Family: Blocks}, {Items: 1}, {Bytes: 1},
		{Refused: true}, {Hashes: []common.Hash{{1}}}, {ObjectBytes: []uint64{1}},
	} {
		tracker, _ := newContributionTracker()
		event.Activity.ReceivedMessages = 1
		before := tracker.metrics.risk.Snapshot().Count()
		tracker.Observe("peer", event)
		if tracker.metrics.risk.Snapshot().Count() != before+1 {
			t.Fatalf("mixed activity suppressed evidence: %+v", event)
		}
	}
}

func TestContributionIgnoresPriorConnection(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(time.Second)
	started := tracker.Connected("peer")
	if started != clock.Now() {
		t.Fatal("connection timestamp is not current")
	}
	tracker.Observe("peer", Evidence{Reason: InvalidBlock, Activity: ActivityEvent{StartedAt: started - 1, ServedResponses: 1, AcceptedTransactions: 1}})
	got := contributionStatus(t, tracker, "warming-up")
	if got.ServedResponses != 0 || got.AcceptedTransactions != 0 || got.LastContributionAgeSeconds != nil {
		t.Fatalf("previous session contaminated new connection: %+v", got)
	}
	assertRisk(t, tracker, "peer", 60, "throttle")
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{StartedAt: started, ServedResponses: 1, AcceptedTransactions: 1}})
	got = contributionStatus(t, tracker, "warming-up")
	if got.ServedResponses != 1 || got.AcceptedTransactions != 1 {
		t.Fatalf("current session discarded: %+v", got)
	}
}

func TestContributionDelayedAdmissionAcrossReadiness(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(time.Second)
	tracker.SetReady(false)
	clock.Run(time.Second)
	tracker.SetReady(true)
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{StartedAt: 0, AcceptedTransactions: 1, ServedResponses: 1}})
	clock.Run(activityGrace)
	got := contributionStatus(t, tracker, "unknown")
	if got.AcceptedTransactions != 0 || got.UnclassifiedMessages != 1 || got.ServedResponses != 1 {
		t.Fatalf("incomplete interval admission credited: %+v", got)
	}
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{StartedAt: 2 * mclock.AbsTime(time.Second), AcceptedTransactions: 1}})
	contributionStatus(t, tracker, "contributing")
}

func TestContributionServingAcrossReadiness(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(time.Second)
	tracker.SetReady(false)
	clock.Run(time.Second)
	tracker.SetReady(true)
	for range 3 {
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{StartedAt: 0, ReceivedMessages: 1, ServedResponses: 1}})
		clock.Run(time.Minute)
	}
	clock.Run(activityGrace - 3*time.Minute)
	got := contributionStatus(t, tracker, "request-only")
	if got.UnclassifiedMessages != 0 || got.LastContributionAgeSeconds != nil {
		t.Fatalf("current-session serving classified as delayed admission: %+v", got)
	}
}

func TestContributionConcurrentLifecycle(t *testing.T) {
	tracker, clock := newContributionTracker()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				tracker.SetReady(false)
				tracker.Connected("peer")
				tracker.SetReady(true)
				tracker.ObserveWithClass("peer", false, Evidence{Activity: ActivityEvent{ReceivedMessages: 1, AcceptedTransactions: 1}})
				tracker.Snapshot("peer")
			}
		})
	}
	wg.Wait()
	tracker.Connected("peer")
	clock.Run(activityGrace)
	contributionStatus(t, tracker, "idle")
	assertRisk(t, tracker, "peer", 0, "none")
}
