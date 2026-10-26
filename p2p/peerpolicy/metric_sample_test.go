package peerpolicy

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/stretchr/testify/require"
)

func TestPeerPolicySingleMetricSample(t *testing.T) {
	for _, encoding := range []string{"prometheus", "json"} {
		t.Run(encoding, func(t *testing.T) {
			clock := new(mclock.Simulated)
			tracker, registry := exportTestTracker(t, clock)
			tracker.SetReady(true)
			tracker.Connected("peer")
			clock.Run(10 * time.Minute)
			tracker.Observe("peer", Evidence{Reason: InvalidBlock})
			calls := 0
			tracker.SetClassResolver(func(string) (bool, bool) {
				calls++
				return false, true
			})
			for i := 1; i <= 2; i++ {
				if encoding == "prometheus" {
					require.Contains(t, scrapePeerMetrics(registry), `eth_peerpolicy_peer_risk{peer_id="peer"} 60`)
				} else {
					data, err := json.Marshal(registry)
					require.NoError(t, err)
					require.Contains(t, string(data), `"values":{"peer":60}`)
				}
				require.Equal(t, i, calls, "each scrape must collect exactly once")
			}
		})
	}
}

func TestPeerPolicyMetricSampleConsistency(t *testing.T) {
	clock := new(mclock.Simulated)
	tracker, registry := exportTestTracker(t, clock)
	tracker.SetReady(true)
	tracker.Connected("peer")
	clock.Run(10 * time.Minute)
	registry.Each(func(name string, metric interface{}) {
		values := metric.(*metrics.GaugeVec).Snapshot()
		switch name {
		case peerRiskMetric, "eth/peerpolicy/peer_behavior_risk":
			require.Equal(t, int64(20), values["peer"])
		case "eth/peerpolicy/peer_abuse_risk":
			require.Zero(t, values["peer"])
		}
		tracker.Observe("peer", Evidence{Reason: InvalidBlock})
	})
	require.Equal(t, uint64(60), tracker.Snapshot("peer").Risk)
	require.Equal(t, int64(60), registry.Get(peerRiskMetric).(*metrics.GaugeVec).Snapshot()["peer"])
}
