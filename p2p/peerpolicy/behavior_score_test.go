package peerpolicy

import (
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
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

func requirePeerRiskMetrics(t *testing.T, registry metrics.Registry, total, abuse, behavior int64) {
	t.Helper()
	require.Equal(t, map[string]int64{"peer": total}, gauge(t, registry, "peer_risk"))
	require.Equal(t, map[string]int64{"peer": abuse}, gauge(t, registry, "peer_abuse_risk"))
	require.Equal(t, map[string]int64{"peer": behavior}, gauge(t, registry, "peer_behavior_risk"))
}

func TestPeerPolicyExportsIdleRiskAfterGrace(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)
	tracker.SetReady(true)
	tracker.Connected("peer")
	requirePeerRiskMetrics(t, registry, 0, 0, 0)

	clock.Run(activityGrace)
	requirePeerRiskMetrics(t, registry, 20, 0, 20)
	require.Equal(t, int64(20), gauge(t, registry, "connected_risk_sum")["ordinary"])
	require.Equal(t, int64(1), gauge(t, registry, "connected_risk_count")["ordinary"])
	require.Contains(t, scrapePeerMetrics(registry), `eth_peerpolicy_peer_behavior_risk{peer_id="peer"} 20`)
	require.Contains(t, scrapePeerMetrics(registry), `eth_peerpolicy_peer_abuse_risk{peer_id="peer"} 0`)

	for range 3 {
		clock.Run(activityWidth)
		requirePeerRiskMetrics(t, registry, 20, 0, 20)
	}
	require.Equal(t, tracker.Snapshot("peer").Risk, uint64(gauge(t, registry, "peer_risk")["peer"]))
}

func TestPeerPolicyExportsRequestOnlyRiskAndRecovery(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)
	tracker.SetReady(true)
	tracker.Connected("peer")
	for range 3 {
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{ServedResponses: 1, ServedBytes: 64}})
		clock.Run(activityWidth)
	}
	clock.Run(activityGrace - 3*activityWidth)
	requirePeerRiskMetrics(t, registry, 20, 0, 20)
	require.Equal(t, int64(3), gauge(t, registry, "peer_contribution_status")["peer"])

	tracker.Observe("peer", Evidence{Activity: ActivityEvent{AcceptedTransactions: 1}})
	requirePeerRiskMetrics(t, registry, 0, 0, 0)
	require.Zero(t, gauge(t, registry, "connected_risk_sum")["ordinary"])
}

func TestPeerPolicyExportsMaximumOfBehaviorAndAbuse(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)
	tracker.SetReady(true)
	tracker.Connected("peer")
	clock.Run(activityGrace)

	tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	requirePeerRiskMetrics(t, registry, 60, 60, 20)
	require.Equal(t, int64(60), gauge(t, registry, "connected_risk_sum")["ordinary"])

	clock.Run(windowWidth * windowCount)
	requirePeerRiskMetrics(t, registry, 20, 0, 20)
}

func TestPeerPolicyExportBehaviorFollowsLiveState(t *testing.T) {
	for _, state := range []string{"not-ready", "protected", "disconnected", "unclassified"} {
		t.Run(state, func(t *testing.T) {
			clock := new(mclock.Simulated)
			tracker, registry := exportTestTracker(t, clock)
			tracker.SetReady(true)
			protected, live := false, true
			tracker.SetClassResolver(func(string) (bool, bool) { return protected, live })
			tracker.Connected("peer")
			clock.Run(activityGrace)
			requirePeerRiskMetrics(t, registry, 20, 0, 20)

			switch state {
			case "not-ready":
				tracker.SetReady(false)
			case "protected":
				protected = true
			case "disconnected":
				live = false
			case "unclassified":
				tracker.Observe("peer", Evidence{Activity: ActivityEvent{UnclassifiedMessages: 1}})
			}
			if live {
				requirePeerRiskMetrics(t, registry, 0, 0, 0)
			} else {
				require.Empty(t, gauge(t, registry, "peer_risk"))
				require.Empty(t, gauge(t, registry, "peer_abuse_risk"))
				require.Empty(t, gauge(t, registry, "peer_behavior_risk"))
			}
			require.Zero(t, gauge(t, registry, "connected_risk_sum")["ordinary"])
		})
	}
}

func TestPeerPolicyExportDisconnectedAbuseHasNoBehaviorRisk(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)
	tracker.SetReady(true)
	live := true
	tracker.SetClassResolver(func(string) (bool, bool) { return false, live })
	tracker.Connected("peer")
	clock.Run(activityGrace)
	tracker.Observe("peer", Evidence{Reason: InvalidTransaction})
	requirePeerRiskMetrics(t, registry, 60, 60, 20)

	live = false
	requirePeerRiskMetrics(t, registry, 60, 60, 0)
	require.Zero(t, gauge(t, registry, "connected_risk_sum")["ordinary"])
	require.Zero(t, gauge(t, registry, "connected_risk_count")["ordinary"])
}

func TestPeerPolicyBehaviorMetricsShareIdentityCap(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)
	tracker.SetReady(true)
	for i := range maxPeerMetricIdentities {
		tracker.Connected(fmt.Sprintf("first-%d", i))
	}
	require.Len(t, gauge(t, registry, "peer_behavior_risk"), maxGeneralPeerMetricIdentities)
	require.Len(t, gauge(t, registry, "peer_abuse_risk"), maxGeneralPeerMetricIdentities)

	for i := range maxPeers {
		tracker.Connected(fmt.Sprintf("next-%d", i))
	}
	clock.Run(activityGrace)
	require.Empty(t, gauge(t, registry, "peer_risk"))
	require.Empty(t, gauge(t, registry, "peer_abuse_risk"))
	require.Empty(t, gauge(t, registry, "peer_behavior_risk"))
	require.Equal(t, int64(maxPeers), gauge(t, registry, "peer_risk_export")["omitted"])
	require.Equal(t, int64(maxGeneralPeerMetricIdentities), gauge(t, registry, "peer_risk_export")["admitted"])
	require.Equal(t, int64(20*maxPeers), gauge(t, registry, "connected_risk_sum")["ordinary"])
	require.Equal(t, int64(maxPeers), gauge(t, registry, "connected_risk_count")["ordinary"])
}
