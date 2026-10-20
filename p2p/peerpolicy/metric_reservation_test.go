package peerpolicy

import (
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyMetricReservationAfterHealthyChurn(t *testing.T) {
	tracker, registry := exportTestTracker(t, new(mclock.Simulated))
	active := ""
	tracker.SetClassResolver(func(id string) (bool, bool) { return false, id == active })
	for i := range 1100 {
		active = fmt.Sprintf("healthy-%d", i)
		tracker.Connected(active)
		scrapePeerMetrics(registry)
		active = ""
	}
	active = "abusive"
	tracker.Connected(active)
	tracker.Observe(active, Evidence{Reason: InvalidEncoding})

	require.Contains(t, scrapePeerMetrics(registry), `eth_peerpolicy_peer_risk{peer_id="abusive"} 60`)
	requireMetricReservationIdentity(t, registry, active, true)
	require.Equal(t, int64(60), gauge(t, registry, "peer_abuse_risk")[active])
	require.Equal(t, int64(1), gauge(t, registry, "connected_risk_count")["ordinary"])
	require.Equal(t, int64(60), gauge(t, registry, "connected_risk_sum")["ordinary"])
}

func requireMetricReservationIdentity(t *testing.T, registry metrics.Registry, id string, present bool) {
	t.Helper()

	families := 0
	registry.Each(func(name string, metric interface{}) {
		vector := metric.(*metrics.GaugeVec)
		if vector.Label() == "peer_id" {
			families++
			_, exists := vector.Snapshot()[id]
			require.Equal(t, present, exists, name)
		}
	})
	require.Equal(t, 12, families)
}

func fillGeneralMetricReservation(t *testing.T, tracker *Tracker, registry metrics.Registry) {
	t.Helper()

	for i := range maxPeerMetricIdentities / 2 {
		tracker.Connected(fmt.Sprintf("general-%d", i))
	}
	require.Len(t, gauge(t, registry, "peer_risk"), maxPeerMetricIdentities/2)
}

func TestPeerPolicyMetricReservationRejectsNonAbuse(t *testing.T) {
	for _, status := range []string{"protected", "idle", "request-only"} {
		t.Run(status, func(t *testing.T) {
			clock := new(mclock.Simulated)
			tracker, registry := exportTestTracker(t, clock)
			fillGeneralMetricReservation(t, tracker, registry)
			tracker.SetReady(true)
			tracker.Connected("candidate")
			if status == "protected" {
				tracker.SetExempt("candidate", true)
				tracker.Observe("candidate", Evidence{Reason: InvalidEncoding})
			}
			for range 3 {
				if status == "request-only" {
					tracker.Observe("candidate", Evidence{Activity: ActivityEvent{ServedResponses: 1}})
				}
				clock.Run(time.Minute)
			}
			clock.Run(activityGrace - 3*time.Minute)
			snapshot := tracker.Snapshot("candidate")
			require.Zero(t, snapshot.AbuseRisk)
			if status != "protected" {
				require.Equal(t, status, snapshot.Contribution.Status)
				require.Equal(t, uint64(20), snapshot.Risk)
			}
			requireMetricReservationIdentity(t, registry, "candidate", false)
			require.Equal(t, int64(maxPeerMetricIdentities/2), gauge(t, registry, "peer_risk_export")["admitted"])
			require.Equal(t, int64(1), gauge(t, registry, "peer_risk_export")["omitted"])
			counts := gauge(t, registry, "connected_risk_count")
			require.Equal(t, int64(maxPeerMetricIdentities/2+1), counts["ordinary"]+counts["protected"])
		})
	}
}

func TestPeerPolicyMetricReservationAdmitsNewAbuse(t *testing.T) {
	tracker, registry := exportTestTracker(t, new(mclock.Simulated))
	fillGeneralMetricReservation(t, tracker, registry)
	tracker.Connected("candidate")
	requireMetricReservationIdentity(t, registry, "candidate", false)
	tracker.Observe("candidate", Evidence{Reason: InvalidEncoding})
	requireMetricReservationIdentity(t, registry, "candidate", true)
	require.Equal(t, int64(60), gauge(t, registry, "peer_abuse_risk")["candidate"])
	require.Equal(t, int64(maxPeerMetricIdentities/2+1), gauge(t, registry, "peer_risk_export")["admitted"])
	require.Zero(t, gauge(t, registry, "peer_risk_export")["omitted"])
}

func TestPeerPolicyMetricReservationExistingIdentityUsesOneSlot(t *testing.T) {
	tracker, registry := exportTestTracker(t, new(mclock.Simulated))
	fillGeneralMetricReservation(t, tracker, registry)
	tracker.Observe("general-0", Evidence{Reason: InvalidEncoding})
	require.Equal(t, int64(60), gauge(t, registry, "peer_risk")["general-0"])
	require.Equal(t, int64(maxPeerMetricIdentities/2), gauge(t, registry, "peer_risk_export")["admitted"])
	for i := range maxPeerMetricIdentities / 2 {
		tracker.Observe(fmt.Sprintf("abusive-%d", i), Evidence{Reason: InvalidEncoding})
	}
	require.Len(t, gauge(t, registry, "peer_risk"), maxPeerMetricIdentities)
	require.Equal(t, int64(maxPeerMetricIdentities), gauge(t, registry, "peer_risk_export")["admitted"])
	require.Zero(t, gauge(t, registry, "peer_risk_export")["omitted"])
}

func TestPeerPolicyMetricReservationRetainsAdmittedIdentity(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)
	fillGeneralMetricReservation(t, tracker, registry)
	tracker.Observe("abusive", Evidence{Reason: InvalidEncoding})
	require.Equal(t, int64(60), gauge(t, registry, "peer_risk")["abusive"])
	clock.Run(windowWidth * windowCount)
	requireMetricReservationIdentity(t, registry, "abusive", false)
	tracker.Connected("abusive")
	requireMetricReservationIdentity(t, registry, "abusive", true)
	require.Zero(t, gauge(t, registry, "peer_abuse_risk")["abusive"])
	tracker.SetExempt("abusive", true)
	requireMetricReservationIdentity(t, registry, "abusive", true)
	require.Equal(t, int64(1), gauge(t, registry, "peer_protected")["abusive"])
	require.Equal(t, int64(maxPeerMetricIdentities/2+1), gauge(t, registry, "peer_risk_export")["admitted"])
}

func TestPeerPolicyMetricReservationNeverRecycles(t *testing.T) {
	clock := new(mclock.Simulated)
	first, registry := exportTestTracker(t, clock)
	fillGeneralMetricReservation(t, first, registry)
	for i := range maxPeerMetricIdentities / 2 {
		first.Observe(fmt.Sprintf("abusive-%d", i), Evidence{Reason: InvalidEncoding})
	}
	require.Len(t, gauge(t, registry, "peer_risk"), maxPeerMetricIdentities)
	clock.Run(windowWidth * windowCount)
	require.Len(t, gauge(t, registry, "peer_risk"), maxPeerMetricIdentities/2)
	first.Observe("overflow", Evidence{Reason: InvalidEncoding})
	requireMetricReservationIdentity(t, registry, "overflow", false)
	first.UnregisterPeerMetrics()
	second := peerMetricsTestTracker(clock)
	second.metrics.peerIDs = first.metrics.peerIDs
	require.NoError(t, second.RegisterPeerMetrics(registry))
	t.Cleanup(second.UnregisterPeerMetrics)
	second.Observe("overflow", Evidence{Reason: InvalidEncoding})
	requireMetricReservationIdentity(t, registry, "overflow", false)
	second.Connected("general-0")
	second.Connected("abusive-0")
	requireMetricReservationIdentity(t, registry, "general-0", true)
	requireMetricReservationIdentity(t, registry, "abusive-0", true)
	require.Equal(t, int64(maxPeerMetricIdentities), gauge(t, registry, "peer_risk_export")["admitted"])
	require.Equal(t, int64(1), gauge(t, registry, "peer_risk_export")["omitted"])
}
