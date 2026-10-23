package peerpolicy

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyAggregateMetricsChurn(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := peerMetricsTestTracker(clock)
	registry := metrics.NewRegistry()
	for generation := range 3 {
		require.NoError(t, tracker.RegisterPeerMetrics(registry, false))
		for i := range maxPeers + 1 {
			id := fmt.Sprintf("peer-%d-%d", generation, i)
			tracker.Connected(id)
			tracker.Observe(id, Evidence{Reason: InvalidBlock})
		}
		assertAggregateMetrics(t, registry)
		require.Equal(t, int64(maxPeers), gauge(t, registry, "connected_risk_count")["ordinary"])
		require.Equal(t, int64(maxPeers*60), gauge(t, registry, "connected_risk_sum")["ordinary"])
		require.Nil(t, tracker.metrics.peerIDs.ids, "aggregate scrapes must not initialize identity admissions")
		require.Equal(t, uint64(60), tracker.Snapshot(fmt.Sprintf("peer-%d-%d", generation, maxPeers)).AbuseRisk)
		clock.Run(time.Minute)
		require.Zero(t, gauge(t, registry, "connected_risk_sum")["ordinary"])
		tracker.UnregisterPeerMetrics()
		assertEmptyRegistry(t, registry)
		tracker = peerMetricsTestTracker(clock)
	}
}

func assertAggregateMetrics(t *testing.T, registry metrics.Registry) {
	t.Helper()
	scrape := scrapePeerMetrics(registry)
	require.NotContains(t, scrape, "peer_id=")
	require.NotContains(t, scrape, "eth_peerpolicy_peer_")
	series := 0
	for _, line := range strings.Split(scrape, "\n") {
		if strings.HasPrefix(line, "eth_peerpolicy_") {
			series++
		}
	}
	require.Equal(t, 9, series)
	encoded, err := json.Marshal(registry)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "peer_id")
	require.NotContains(t, string(encoded), "peer-")
}

func assertEmptyRegistry(t *testing.T, registry metrics.Registry) {
	t.Helper()
	registry.Each(func(name string, _ interface{}) { t.Errorf("collector remained: %s", name) })
}

func TestPeerPolicyMetricModesPreserveSnapshots(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := peerMetricsTestTracker(clock)
	tracker.SetReady(true)
	tracker.Connected("peer")
	tracker.Observe("peer", Evidence{Reason: InvalidBlock, Activity: ActivityEvent{AcceptedTransactions: 2, ServedBytes: 100}})
	want := tracker.Snapshot("peer")
	registry := metrics.NewRegistry()
	for _, perPeer := range []bool{false, true, false} {
		require.NoError(t, tracker.RegisterPeerMetrics(registry, perPeer))
		require.Equal(t, int64(60), gauge(t, registry, "connected_risk_sum")["ordinary"])
		require.Equal(t, int64(1), gauge(t, registry, "connected_risk_count")["ordinary"])
		if perPeer {
			require.Equal(t, int64(60), gauge(t, registry, "peer_risk")["peer"])
			require.Equal(t, int64(2), gauge(t, registry, "peer_accepted_transactions")["peer"])
			require.Equal(t, int64(100), gauge(t, registry, "peer_served_bytes")["peer"])
		} else {
			assertAggregateMetrics(t, registry)
		}
		require.Equal(t, want, tracker.Snapshot("peer"))
		tracker.UnregisterPeerMetrics()
		assertEmptyRegistry(t, registry)
	}
}

func TestPeerPolicyAggregateRegistrationRollback(t *testing.T) {
	registry := metrics.NewRegistry()
	name := "eth/peerpolicy/connected_contribution_status"
	other := metrics.NewGauge()
	require.NoError(t, registry.Register(name, other))
	tracker := peerMetricsTestTracker(new(mclock.Simulated))
	require.ErrorIs(t, tracker.RegisterPeerMetrics(registry, false), metrics.ErrDuplicateMetric)
	tracker.UnregisterPeerMetrics()
	require.Same(t, other, registry.Get(name))
	require.Nil(t, registry.Get("eth/peerpolicy/connected_risk_sum"))
	require.Nil(t, registry.Get("eth/peerpolicy/connected_risk_count"))
	registry.Unregister(name)
	require.NoError(t, tracker.RegisterPeerMetrics(registry, false))
	require.Error(t, tracker.RegisterPeerMetrics(metrics.NewRegistry(), true))
	tracker.UnregisterPeerMetrics()
	assertEmptyRegistry(t, registry)
}
