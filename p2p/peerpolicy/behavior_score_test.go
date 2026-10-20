package peerpolicy

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/stretchr/testify/require"
)

func assertBehaviorRisk(t *testing.T, tracker *Tracker, risk, abuse, behavior uint64, reason string) Snapshot {
	t.Helper()
	got := tracker.Snapshot("peer")
	require.Equal(t, "observe", got.Mode)
	require.Equal(t, risk, got.Risk, "overall risk")
	require.Equal(t, abuse, got.AbuseRisk, "abuse risk")
	require.NotNil(t, got.Contribution)
	require.Equal(t, behavior, got.Contribution.Risk, "behavior risk")
	require.Equal(t, reason, got.Contribution.RiskReason)
	return got
}

func TestBehaviorScoreGraceBoundary(t *testing.T) {
	tracker, clock := newContributionTracker()
	assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	clock.Run(activityGrace - time.Nanosecond)
	assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	clock.Run(time.Nanosecond)
	got := assertBehaviorRisk(t, tracker, 20, 0, 20, "idle")
	require.Equal(t, "idle", got.Contribution.Status)
	require.Equal(t, "none", got.Action)
	require.Empty(t, got.Windows)
}

func TestBehaviorScoreRequestOnlyMinutes(t *testing.T) {
	for _, test := range []struct {
		name      string
		minutes   int
		responses uint64
		want      uint64
		reason    string
	}{
		{"one-busy-minute", 1, 100, 0, ""},
		{"two-minutes", 2, 1, 0, ""},
		{"three-minutes", 3, 1, 20, "request-only"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracker, clock := newContributionTracker()
			for range test.minutes {
				tracker.Observe("peer", Evidence{Activity: ActivityEvent{
					ReceivedMessages: test.responses, ServedResponses: test.responses, ServedBytes: 128,
				}})
				clock.Run(time.Minute)
			}
			assertBehaviorRisk(t, tracker, 0, 0, 0, "")
			clock.Run(activityGrace - time.Duration(test.minutes)*time.Minute)
			got := assertBehaviorRisk(t, tracker, test.want, 0, test.want, test.reason)
			require.Equal(t, uint64(test.minutes), got.Contribution.ServedMinutes)
			require.Empty(t, got.Windows)
		})
	}
}

func TestBehaviorScoreNeutralActivity(t *testing.T) {
	for _, test := range []struct {
		name   string
		event  ActivityEvent
		status string
	}{
		{"announcement", ActivityEvent{ReceivedMessages: 1, ReceivedBytes: 32}, "unknown"},
		{"block-validation-pending", ActivityEvent{ReceivedMessages: 1, ReceivedBytes: 256, UnclassifiedMessages: 1}, "unknown"},
		{"incomplete-coverage", ActivityEvent{UnclassifiedMessages: 1}, "unknown"},
		{"accepted-transaction", ActivityEvent{AcceptedTransactions: 1}, "contributing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracker, clock := newContributionTracker()
			clock.Run(activityGrace)
			assertBehaviorRisk(t, tracker, 20, 0, 20, "idle")
			tracker.Observe("peer", Evidence{Activity: test.event})
			got := assertBehaviorRisk(t, tracker, 0, 0, 0, "")
			require.Equal(t, test.status, got.Contribution.Status)
		})
	}
}

func TestBehaviorScoreProtectedConnection(t *testing.T) {
	tracker, clock := newContributionTracker()
	protected := true
	tracker.SetClassResolver(func(string) (bool, bool) { return protected, true })
	clock.Run(activityGrace)
	got := assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	require.True(t, got.Contribution.Protected)
	protected = false
	assertBehaviorRisk(t, tracker, 20, 0, 20, "idle")
	protected = true
	assertBehaviorRisk(t, tracker, 0, 0, 0, "")
}

func TestBehaviorScoreReadinessRestart(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(activityGrace)
	assertBehaviorRisk(t, tracker, 20, 0, 20, "idle")
	tracker.SetReady(false)
	assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	clock.Run(time.Minute)
	tracker.SetReady(true)
	assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	clock.Run(activityGrace - time.Second)
	assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	clock.Run(time.Second)
	assertBehaviorRisk(t, tracker, 20, 0, 20, "idle")
}

func TestBehaviorScoreShortPauseDelaysGrace(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(activityGrace - 10*time.Second)
	tracker.SetReady(false)
	clock.Run(20 * time.Second)
	tracker.SetReady(true)
	assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	clock.Run(9 * time.Second)
	assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	clock.Run(time.Second)
	assertBehaviorRisk(t, tracker, 20, 0, 20, "idle")
}

func TestBehaviorScoreRequiresConnection(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := New(clock)
	tracker.SetReady(true)
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ServedResponses: 100}})
	clock.Run(activityGrace)
	got := assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	require.False(t, got.Contribution.ConnectionKnown)
}

func TestBehaviorScoreDisconnectedMetricsAgree(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)
	connected := true
	tracker.SetClassResolver(func(string) (bool, bool) { return false, connected })
	tracker.SetReady(true)
	tracker.Connected("peer")
	clock.Run(activityGrace)
	assertBehaviorRisk(t, tracker, 20, 0, 20, "idle")
	require.Equal(t, int64(20), gauge(t, registry, "peer_risk")["peer"])
	require.Equal(t, int64(20), gauge(t, registry, "connected_risk_sum")["ordinary"])
	connected = false
	assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	require.NotContains(t, gauge(t, registry, "peer_risk"), "peer")
	require.Zero(t, gauge(t, registry, "connected_risk_sum")["ordinary"])
	require.Zero(t, gauge(t, registry, "connected_risk_count")["ordinary"])
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	assertBehaviorRisk(t, tracker, 60, 60, 0, "")
	require.Equal(t, int64(60), gauge(t, registry, "peer_risk")["peer"])
	require.Zero(t, gauge(t, registry, "peer_connected")["peer"])
	require.Zero(t, gauge(t, registry, "connected_risk_sum")["ordinary"])
	clock.Run(windowCount * windowWidth)
	assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	require.NotContains(t, gauge(t, registry, "peer_risk"), "peer")
}

func TestBehaviorScoreTakesMaximum(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(activityGrace)
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	got := assertBehaviorRisk(t, tracker, 60, 60, 20, "idle")
	require.Equal(t, "throttle", got.Action)
	require.Equal(t, map[string]uint64{"invalid-block": 1}, got.Windows)
	clock.Run(windowCount * windowWidth)
	got = assertBehaviorRisk(t, tracker, 20, 0, 20, "idle")
	require.Equal(t, "none", got.Action)
	require.Empty(t, got.Windows)
}

func TestBehaviorScoreContributionPreservesAbuse(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(activityGrace)
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	assertBehaviorRisk(t, tracker, 60, 60, 20, "idle")
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{AcceptedTransactions: 1}})
	got := assertBehaviorRisk(t, tracker, 60, 60, 0, "")
	require.Equal(t, "contributing", got.Contribution.Status)
	require.Equal(t, map[string]uint64{"invalid-block": 1}, got.Windows)
}

func TestBehaviorScoreReadFrequencyDoesNotAccumulate(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)
	tracker.SetReady(true)
	tracker.Connected("peer")
	clock.Run(activityGrace)
	samplesBefore := tracker.metrics.risk.Snapshot().Count()
	for range 12 {
		for range 10 {
			got := assertBehaviorRisk(t, tracker, 20, 0, 20, "idle")
			require.Empty(t, got.Windows)
			require.Equal(t, int64(20), gauge(t, registry, "peer_risk")["peer"])
		}
		clock.Run(windowWidth)
	}
	clock.Run(2 * time.Hour)
	got := assertBehaviorRisk(t, tracker, 20, 0, 20, "idle")
	require.Equal(t, "none", got.Action)
	require.Empty(t, got.Windows)
	require.Equal(t, samplesBefore, tracker.metrics.risk.Snapshot().Count())
}

func TestBehaviorScoreReconnectRestartsGrace(t *testing.T) {
	tracker, clock := newContributionTracker()
	clock.Run(activityGrace)
	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	assertBehaviorRisk(t, tracker, 60, 60, 20, "idle")
	clock.Run(time.Second)
	tracker.Connected("peer")
	assertBehaviorRisk(t, tracker, 60, 60, 0, "")
	require.True(t, tracker.ObserveSession("peer", false, 0, Evidence{
		Activity: ActivityEvent{AcceptedTransactions: 1, ServedResponses: 1},
	}))
	got := assertBehaviorRisk(t, tracker, 60, 60, 0, "")
	require.Zero(t, got.Contribution.AcceptedTransactions)
	require.Zero(t, got.Contribution.ServedResponses)
	clock.Run(windowCount * windowWidth)
	assertBehaviorRisk(t, tracker, 0, 0, 0, "")
	clock.Run(activityGrace - windowCount*windowWidth)
	assertBehaviorRisk(t, tracker, 20, 0, 20, "idle")
}
