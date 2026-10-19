package peerpolicy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicyMetricIdentityLifetime(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := peerMetricsTestTracker(clock)
	registry := metrics.NewRegistry()
	require.NoError(t, tracker.RegisterPeerMetrics(registry))
	t.Cleanup(tracker.UnregisterPeerMetrics)
	seen := make(map[string]struct{})
	series := regexp.MustCompile(`eth_peerpolicy_peer_risk\{peer_id="([^"]+)"\}`)
	for cohort := range 3 {
		for i := range maxPeerMetricIdentities {
			tracker.Observe(fmt.Sprintf("%d-%d", cohort, i), Evidence{Reason: InvalidBlock})
		}
		values := registry.Get(peerRiskMetric).(*metrics.GaugeVec).Snapshot()
		for _, match := range series.FindAllStringSubmatch(scrapePeerMetrics(registry), -1) {
			seen[match[1]] = struct{}{}
		}
		require.LessOrEqual(t, len(seen), maxPeerMetricIdentities)
		coverage := registry.Get(peerRiskExportMetric).(*metrics.GaugeVec).Snapshot()
		require.Equal(t, int64(maxPeerMetricIdentities), coverage["admitted"])
		require.Equal(t, int64(maxPeerMetricIdentities-len(values)), coverage["omitted"])
		clock.Run(time.Minute)
		require.Empty(t, registry.Get(peerRiskMetric).(*metrics.GaugeVec).Snapshot())
		require.Zero(t, tracker.peerRiskExport()["omitted"])
	}
	tracker.Observe("0-0", Evidence{Reason: InvalidBlock})
	require.Equal(t, int64(60), tracker.peerRisks()["0-0"], "previously admitted identities can reappear")
	tracker.Observe("not-exported", Evidence{Reason: InvalidBlock})
	require.Equal(t, uint64(60), tracker.Snapshot("not-exported").Risk)
	require.NotContains(t, scrapePeerMetrics(registry), `peer_id="not-exported"`)
	encoded, err := json.Marshal(registry)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "not-exported", "JSON must use the same lifetime cap")
}

func TestPeerPolicyMetricIdentityReregistration(t *testing.T) {
	first := peerMetricsTestTracker(new(mclock.Simulated))
	for i := range maxPeerMetricIdentities {
		first.Observe(fmt.Sprint(i), Evidence{Reason: InvalidBlock})
	}
	registry := metrics.NewRegistry()
	require.NoError(t, first.RegisterPeerMetrics(registry))
	require.Len(t, first.peerRisks(), maxPeerMetricIdentities)
	first.UnregisterPeerMetrics()
	second := New(new(mclock.Simulated))
	require.Same(t, &processPeerMetricIdentities, second.metrics.peerIDs)
	second.metrics.peerIDs = first.metrics.peerIDs // Isolate this test's process budget.
	require.NoError(t, second.RegisterPeerMetrics(registry))
	t.Cleanup(second.UnregisterPeerMetrics)
	second.Observe("new", Evidence{Reason: InvalidBlock})
	require.Empty(t, second.peerRisks())
	require.Equal(t, int64(1), second.peerRiskExport()["omitted"])
	require.Equal(t, uint64(60), second.Snapshot("new").Risk)
}

func TestPeerPolicyMetricIdentityConcurrentAdmission(t *testing.T) {
	var budget peerMetricIdentities
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[string]struct{})
	for worker := range 8 {
		wg.Go(func() {
			for batch := range 4 {
				values := make(map[string]int64)
				for i := range 128 {
					values[fmt.Sprintf("%d-%d-%d", worker, batch, i)] = 60
				}
				omitted, admitted := budget.filter(values)
				require.Equal(t, int64(128), int64(len(values))+omitted)
				require.LessOrEqual(t, admitted, int64(maxPeerMetricIdentities))
				mu.Lock()
				for id := range values {
					seen[id] = struct{}{}
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	require.Len(t, seen, maxPeerMetricIdentities)
	require.Len(t, budget.ids, maxPeerMetricIdentities)
}

func TestPeerPolicyMetricIdentityRegistrationRollback(t *testing.T) {
	registry := metrics.NewRegistry()
	other := metrics.NewGauge()
	require.NoError(t, registry.Register(peerRiskExportMetric, other))
	tracker := peerMetricsTestTracker(new(mclock.Simulated))
	require.ErrorIs(t, tracker.RegisterPeerMetrics(registry), metrics.ErrDuplicateMetric)
	tracker.UnregisterPeerMetrics()
	require.Nil(t, registry.Get(peerRiskMetric))
	require.Same(t, other, registry.Get(peerRiskExportMetric))
	registry.Unregister(peerRiskExportMetric)
	require.NoError(t, tracker.RegisterPeerMetrics(registry))
	tracker.UnregisterPeerMetrics()
	require.Nil(t, registry.Get(peerRiskMetric))
	require.Nil(t, registry.Get(peerRiskExportMetric))
}
