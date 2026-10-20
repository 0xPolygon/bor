package peerpolicy

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
)

func exportTestTracker(t *testing.T, clock mclock.Clock) (*Tracker, metrics.Registry) {
	t.Helper()

	tracker := peerMetricsTestTracker(clock)
	registry := metrics.NewRegistry()
	require.NoError(t, tracker.RegisterPeerMetrics(registry))
	t.Cleanup(tracker.UnregisterPeerMetrics)

	return tracker, registry
}

func gauge(t *testing.T, registry metrics.Registry, name string) map[string]int64 {
	t.Helper()

	value := registry.Get("eth/peerpolicy/" + name)
	require.NotNil(t, value, name)

	return value.(*metrics.GaugeVec).Snapshot()
}

func TestPeerPolicyExportsConnectedZeroRisk(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)
	tracker.SetReady(true)

	protected := map[string]bool{"trusted": true}
	tracker.SetClassResolver(func(id string) (bool, bool) { return protected[id], id != "gone" })

	for _, id := range []string{"quiet", "trusted", "busy"} {
		tracker.Connected(id)
	}

	tracker.Observe("busy", Evidence{Reason: InvalidBlock, Activity: ActivityEvent{ReceivedMessages: 4, ReceivedBytes: 900}})

	require.Equal(t, map[string]int64{"quiet": 0, "trusted": 0, "busy": 60}, gauge(t, registry, "peer_risk"))
	require.Equal(t, map[string]int64{"quiet": 1, "trusted": 1, "busy": 1}, gauge(t, registry, "peer_connected"))
	require.Equal(t, map[string]int64{"quiet": 0, "trusted": 1, "busy": 0}, gauge(t, registry, "peer_protected"))
	require.Equal(t, int64(4), gauge(t, registry, "peer_received_messages")["busy"])
	require.Equal(t, int64(900), gauge(t, registry, "peer_received_bytes")["busy"])

	require.Equal(t, map[string]int64{"ordinary": 60, "protected": 0}, gauge(t, registry, "connected_risk_sum"))
	require.Equal(t, map[string]int64{"ordinary": 2, "protected": 1}, gauge(t, registry, "connected_risk_count"))

	tracker.Connected("gone")
	tracker.Observe("gone", Evidence{Reason: InvalidBlock})
	require.Equal(t, int64(60), gauge(t, registry, "peer_risk")["gone"])
	require.Equal(t, int64(0), gauge(t, registry, "peer_connected")["gone"])
	require.Equal(t, int64(3), gauge(t, registry, "connected_risk_count")["ordinary"]+gauge(t, registry, "connected_risk_count")["protected"])

	clock.Run(windowWidth * windowCount)
	require.NotContains(t, gauge(t, registry, "peer_risk"), "gone", "a disconnected zero must not persist")
	require.Contains(t, gauge(t, registry, "peer_risk"), "quiet", "a connected zero must persist")
}

func TestPeerPolicyExportSkipsDiagnosticRecords(t *testing.T) {
	tracker, registry := exportTestTracker(t, new(mclock.Simulated))
	tracker.Observe("diagnostic-only", Evidence{Reason: FetcherDrop})

	require.Empty(t, gauge(t, registry, "peer_risk"))
	require.Equal(t, map[string]int64{"ordinary": 0, "protected": 0}, gauge(t, registry, "connected_risk_count"))
	require.Equal(t, uint64(0), tracker.Snapshot("diagnostic-only").Risk)
}

func TestPeerPolicyExportsContributionStatus(t *testing.T) {
	require.Equal(t, []string{"unknown", "warming-up", "contributing", "request-only", "idle"}, contributionStatuses)

	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)

	tracker.Connected("peer")
	require.Equal(t, int64(0), gauge(t, registry, "peer_contribution_status")["peer"], "not ready is unknown")
	require.Equal(t, int64(1), gauge(t, registry, "connected_contribution_status")["unknown"])

	tracker.SetReady(true)
	tracker.Connected("peer")
	require.Equal(t, int64(1), gauge(t, registry, "peer_contribution_status")["peer"], "inside the grace period is warming-up")

	clock.Run(activityGrace + activityWidth)
	require.Equal(t, int64(4), gauge(t, registry, "peer_contribution_status")["peer"], "no traffic after grace is idle")
	require.Equal(t, int64(1), gauge(t, registry, "connected_contribution_status")["idle"])

	tracker.Observe("peer", Evidence{Activity: ActivityEvent{AcceptedTransactions: 1}})
	require.Equal(t, int64(2), gauge(t, registry, "peer_contribution_status")["peer"])
	require.Equal(t, int64(1), gauge(t, registry, "peer_accepted_transactions")["peer"])

	counts := gauge(t, registry, "connected_contribution_status")
	require.Len(t, counts, len(contributionStatuses), "every status is a finite label")
	require.Equal(t, int64(1), counts["contributing"])
	require.Equal(t, int64(0), counts["idle"])
}

func TestPeerPolicyExportAggregatesSurviveIdentityCap(t *testing.T) {
	tracker, registry := exportTestTracker(t, new(mclock.Simulated))
	tracker.SetReady(true)

	const spent = 16
	for i := range spent {
		tracker.Connected(fmt.Sprintf("a-%d", i))
	}

	require.Len(t, gauge(t, registry, "peer_risk"), spent)

	for i := range maxPeerMetricIdentities {
		tracker.Connected(fmt.Sprintf("b-%d", i))
	}

	require.Len(t, gauge(t, registry, "peer_risk"), maxPeerMetricIdentities-spent)
	require.Len(t, gauge(t, registry, "peer_connected"), maxPeerMetricIdentities-spent, "families share one admission set")

	counts := gauge(t, registry, "connected_risk_count")
	require.Equal(t, int64(maxPeers), counts["ordinary"]+counts["protected"], "aggregates cover the whole connected population")

	var statuses int64
	for _, count := range gauge(t, registry, "connected_contribution_status") {
		statuses += count
	}

	require.Equal(t, int64(maxPeers), statuses, "status counts cover peers the cap could not label")

	coverage := gauge(t, registry, "peer_risk_export")
	require.Equal(t, int64(maxPeerMetricIdentities), coverage["limit"])
	require.Equal(t, int64(maxPeerMetricIdentities), coverage["admitted"])
	require.Equal(t, int64(maxPeerMetricIdentities-spent), coverage["exported"])
	require.Equal(t, int64(spent), coverage["omitted"], "omitted must count connected zero-risk identities too")
}

func TestPeerPolicyExportSaturatesActivityCounters(t *testing.T) {
	tracker, registry := exportTestTracker(t, new(mclock.Simulated))
	tracker.Connected("peer")

	for range 2 {
		tracker.Observe("peer", Evidence{Activity: ActivityEvent{ReceivedBytes: math.MaxUint64}})
	}

	require.Equal(t, int64(math.MaxInt64), gauge(t, registry, "peer_received_bytes")["peer"])
}

func TestPeerPolicyExportDoesNotRefreshEviction(t *testing.T) {
	tracker, registry := exportTestTracker(t, new(mclock.Simulated))
	for i := range maxPeers {
		tracker.Connected(fmt.Sprint(i))
	}

	for range 3 {
		require.NotEmpty(t, gauge(t, registry, "peer_connected"))
	}

	tracker.Connected("new")
	require.NotContains(t, gauge(t, registry, "peer_connected"), "0", "scrapes changed the eviction order")
}

func TestPeerPolicyExportRegistrationIsAllOrNothing(t *testing.T) {
	registry := metrics.NewRegistry()
	other := metrics.NewGauge()
	require.NoError(t, registry.Register("eth/peerpolicy/connected_risk_sum", other))

	tracker := peerMetricsTestTracker(new(mclock.Simulated))
	require.ErrorIs(t, tracker.RegisterPeerMetrics(registry), metrics.ErrDuplicateMetric)
	require.Same(t, other, registry.Get("eth/peerpolicy/connected_risk_sum"), "an unrelated collector was removed")

	for _, family := range tracker.families() {
		if family.name != "eth/peerpolicy/connected_risk_sum" {
			require.Nil(t, registry.Get(family.name), family.name)
		}
	}

	registry.Unregister("eth/peerpolicy/connected_risk_sum")
	require.NoError(t, tracker.RegisterPeerMetrics(registry))
	tracker.UnregisterPeerMetrics()

	for _, family := range tracker.families() {
		require.Nil(t, registry.Get(family.name), family.name)
	}
}

func TestPeerPolicyExportReachesBothEncodings(t *testing.T) {
	tracker, registry := exportTestTracker(t, new(mclock.Simulated))
	tracker.SetReady(true)
	tracker.Connected("peer")
	tracker.Observe("peer", Evidence{Activity: ActivityEvent{ServedResponses: 2, ServedBytes: 64}})

	scrape := scrapePeerMetrics(registry)
	require.Contains(t, scrape, `eth_peerpolicy_peer_risk{peer_id="peer"} 0`)
	require.Contains(t, scrape, `eth_peerpolicy_peer_served_responses{peer_id="peer"} 2`)
	require.Contains(t, scrape, `eth_peerpolicy_peer_served_bytes{peer_id="peer"} 64`)
	require.Contains(t, scrape, `eth_peerpolicy_connected_risk_count{class="ordinary"} 1`)

	encoded, err := json.Marshal(registry)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"eth/peerpolicy/peer_served_responses"`)
	require.Contains(t, string(encoded), `"label":"peer_id"`)
}

func TestPeerPolicyExportConcurrentWithTraffic(t *testing.T) {
	tracker, registry := exportTestTracker(t, new(mclock.Simulated))
	tracker.SetReady(true)

	var trusted atomic.Bool
	tracker.SetClassResolver(func(string) (bool, bool) { return trusted.Load(), true })

	var wg sync.WaitGroup
	for worker := range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range 100 {
				switch worker {
				case 0:
					tracker.Connected(fmt.Sprint(i % 8))
				case 1:
					tracker.Observe(fmt.Sprint(i%8), Evidence{Reason: InvalidBlock, Activity: ActivityEvent{ReceivedMessages: 1}})
				case 2:
					trusted.Store(i%2 == 0)
				case 3:
					require.NotEmpty(t, strings.TrimSpace(scrapePeerMetrics(registry)))
				}
			}
		}()
	}

	wg.Wait()

	counts := gauge(t, registry, "connected_risk_count")
	require.Equal(t, int64(8), counts["ordinary"]+counts["protected"])
}

func TestPeerPolicyExportFamiliesRetainLifetimeBudget(t *testing.T) {
	clock := new(mclock.Simulated)
	first, registry := exportTestTracker(t, clock)
	for i := range maxPeerMetricIdentities {
		first.Connected(fmt.Sprint(i))
	}
	require.Len(t, gauge(t, registry, "peer_received_messages"), maxPeerMetricIdentities)
	clock.Run(activityCount * activityWidth)
	first.UnregisterPeerMetrics()

	second := New(clock)
	require.Same(t, &processPeerMetricIdentities, second.metrics.peerIDs)
	second.metrics.peerIDs = first.metrics.peerIDs
	require.NoError(t, second.RegisterPeerMetrics(registry))
	t.Cleanup(second.UnregisterPeerMetrics)
	second.Connected("replacement")
	second.Observe("replacement", Evidence{Reason: InvalidBlock})
	second.Connected("clean-replacement")
	for _, family := range second.families() {
		if family.label == "peer_id" {
			require.Empty(t, registry.Get(family.name).(*metrics.GaugeVec).Snapshot(), family.name)
		}
	}
	require.Equal(t, int64(2), gauge(t, registry, "peer_risk_export")["omitted"])
	require.Equal(t, int64(2), gauge(t, registry, "connected_risk_count")["ordinary"])
	require.Equal(t, int64(60), gauge(t, registry, "connected_risk_sum")["ordinary"])
	require.NotContains(t, scrapePeerMetrics(registry), `peer_id="replacement"`)
	encoded, err := json.Marshal(registry)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "replacement")
}

func TestPeerPolicyExportDisconnectAndReconnect(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)
	connected, lookups := true, 0
	tracker.SetClassResolver(func(string) (bool, bool) {
		lookups++
		return false, connected
	})
	tracker.SetReady(true)
	tracker.Connected("peer")
	clock.Run(activityGrace + activityWidth)
	tracker.Observe("peer", Evidence{Reason: InvalidBlock, Activity: ActivityEvent{AcceptedTransactions: 1}})
	require.Equal(t, int64(2), gauge(t, registry, "peer_contribution_status")["peer"])

	connected = false
	lookups = 0
	sample := tracker.collect()
	require.Equal(t, 1, lookups, "class and connectivity must use the same lookup")
	require.Equal(t, int64(0), sample.connected["peer"])
	require.Equal(t, int64(0), sample.status["peer"])
	require.Equal(t, int64(60), sample.risk["peer"])
	require.Zero(t, sample.classCount["ordinary"])

	connected = true
	tracker.Connected("peer")
	sample = tracker.collect()
	require.Equal(t, int64(1), sample.connected["peer"])
	require.Equal(t, int64(1), sample.status["peer"])
	require.Equal(t, int64(60), sample.risk["peer"])
	require.Zero(t, sample.activity["peer"].AcceptedTransactions)
}
