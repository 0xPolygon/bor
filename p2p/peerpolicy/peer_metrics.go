package peerpolicy

import (
	"fmt"

	"github.com/ethereum/go-ethereum/metrics"
)

const (
	peerRiskMetric       = "eth/peerpolicy/peer_risk"
	peerRiskExportMetric = "eth/peerpolicy/peer_risk_export"
)

// RegisterPeerMetrics exposes retained non-exempt identities with positive risk.
// Registration belongs to the node lifecycle rather than the evidence hot path.
func (t *Tracker) RegisterPeerMetrics(registry metrics.Registry) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.metrics.peerRegistry != nil {
		return fmt.Errorf("peer reputation gauges already registered")
	}
	if err := registry.Register(peerRiskMetric, metrics.NewGaugeVec("peer_id", t.peerRisks)); err != nil {
		return fmt.Errorf("register peer reputation gauges: %w", err)
	}
	if err := registry.Register(peerRiskExportMetric, metrics.NewGaugeVec("state", t.peerRiskExport)); err != nil {
		registry.Unregister(peerRiskMetric)
		return fmt.Errorf("register peer reputation export coverage: %w", err)
	}
	t.metrics.peerRegistry = registry
	return nil
}

func (t *Tracker) UnregisterPeerMetrics() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.metrics.peerRegistry != nil {
		t.metrics.peerRegistry.Unregister(peerRiskMetric)
		t.metrics.peerRegistry.Unregister(peerRiskExportMetric)
		t.metrics.peerRegistry = nil
	}
}

func (t *Tracker) peerRisks() map[string]int64 {
	values, _, _ := t.collectPeerRisks()
	return values
}

func (t *Tracker) peerRiskExport() map[string]int64 {
	values, omitted, admitted := t.collectPeerRisks()
	return map[string]int64{
		"limit": maxPeerMetricIdentities, "admitted": admitted,
		"exported": int64(len(values)), "omitted": omitted,
	}
}

func (t *Tracker) collectPeerRisks() (map[string]int64, int64, int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tick := int64(t.clock.Now()) / int64(windowWidth)
	values := make(map[string]int64)
	// Peek avoids treating a scrape as peer activity and changing LRU eviction.
	for _, id := range t.peers.Keys() {
		peer, _ := t.peers.Peek(id)
		peer.setExempt(t.connectionClass(id, peer.exempt))
		if risk := peer.risk(tick, nil); !peer.exempt && risk > 0 {
			values[id] = int64(risk)
		}
	}
	omitted, admitted := t.metrics.peerIDs.filter(values)
	return values, omitted, admitted
}
