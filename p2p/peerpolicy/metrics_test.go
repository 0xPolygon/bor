package peerpolicy

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
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
	e := Evidence{Family: Requests, Items: allowances[Requests].items + 1, Bytes: 1024}
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
	if m.items[Requests].Snapshot().Count()-items != 3*int64(e.Items) || m.bytes[Requests].Snapshot().Count()-bytes != 3072 {
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
		{0, 1, 10, 1},
		{3, 4, 10, 7},
		{3, 7, 10, 10},
		{3, 8, 10, 10},
		{10, 1, 10, 10},
		{9, math.MaxUint64, 10, 10},
	} {
		if got := saturatingAdd(tc.current, tc.delta, tc.limit); got != tc.want {
			t.Fatalf("%+v: got %d", tc, got)
		}
	}
}

func TestPeerPolicyRefusalCounter(t *testing.T) {
	metrics.Enable()
	tracker := New(new(mclock.Simulated))
	counter := tracker.metrics.reasons[ServingRefused]
	before := counter.Snapshot().Count()
	for range 5 {
		tracker.Observe("peer", Evidence{Refused: true})
	}
	if got := counter.Snapshot().Count() - before; got != 5 {
		t.Fatalf("refusals counted %d times, want 5", got)
	}
	assertRisk(t, tracker, "peer", 20, "none")
}

func TestPeerPolicyMetricIdentityLifetime(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker := peerMetricsTestTracker(clock)
	registry := metrics.NewRegistry()
	require.NoError(t, tracker.RegisterPeerMetrics(registry, true))
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
	require.NoError(t, first.RegisterPeerMetrics(registry, true))
	require.Len(t, first.peerRisks(), maxPeerMetricIdentities)
	first.UnregisterPeerMetrics()
	second := New(new(mclock.Simulated))
	require.Same(t, &processPeerMetricIdentities, second.metrics.peerIDs)
	second.metrics.peerIDs = first.metrics.peerIDs // Isolate this test's process budget.
	require.NoError(t, second.RegisterPeerMetrics(registry, true))
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
				omitted, admitted := budget.filter(values, values)
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
	require.ErrorIs(t, tracker.RegisterPeerMetrics(registry, true), metrics.ErrDuplicateMetric)
	tracker.UnregisterPeerMetrics()
	require.Nil(t, registry.Get(peerRiskMetric))
	require.Same(t, other, registry.Get(peerRiskExportMetric))
	registry.Unregister(peerRiskExportMetric)
	require.NoError(t, tracker.RegisterPeerMetrics(registry, true))
	tracker.UnregisterPeerMetrics()
	require.Nil(t, registry.Get(peerRiskMetric))
	require.Nil(t, registry.Get(peerRiskExportMetric))
}
