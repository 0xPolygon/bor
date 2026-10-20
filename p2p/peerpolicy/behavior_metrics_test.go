package peerpolicy

import (
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
)

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
	require.Len(t, gauge(t, registry, "peer_behavior_risk"), maxPeerMetricIdentities)
	require.Len(t, gauge(t, registry, "peer_abuse_risk"), maxPeerMetricIdentities)

	for i := range maxPeers {
		tracker.Connected(fmt.Sprintf("next-%d", i))
	}
	clock.Run(activityGrace)
	require.Empty(t, gauge(t, registry, "peer_risk"))
	require.Empty(t, gauge(t, registry, "peer_abuse_risk"))
	require.Empty(t, gauge(t, registry, "peer_behavior_risk"))
	require.Equal(t, int64(maxPeers), gauge(t, registry, "peer_risk_export")["omitted"])
	require.Equal(t, int64(maxPeerMetricIdentities), gauge(t, registry, "peer_risk_export")["admitted"])
	require.Equal(t, int64(20*maxPeers), gauge(t, registry, "connected_risk_sum")["ordinary"])
	require.Equal(t, int64(maxPeers), gauge(t, registry, "connected_risk_count")["ordinary"])
}
