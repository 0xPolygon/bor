package peerpolicy

import (
	"fmt"

	"github.com/ethereum/go-ethereum/metrics"
)

const peerRiskMetric = "eth/peerpolicy/peer_risk"

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
	t.metrics.peerRegistry = registry
	return nil
}

func (t *Tracker) UnregisterPeerMetrics() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.metrics.peerRegistry != nil {
		t.metrics.peerRegistry.Unregister(peerRiskMetric)
		t.metrics.peerRegistry = nil
	}
}

func (t *Tracker) peerRisks() map[string]int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	tick := int64(t.clock.Now()) / int64(windowWidth)
	values := make(map[string]int64)
	// Peek avoids treating a scrape as peer activity and changing LRU eviction.
	for _, id := range t.peers.Keys() {
		peer, _ := t.peers.Peek(id)
		if risk := peer.risk(tick, nil); !peer.exempt && risk > 0 {
			values[id] = int64(risk)
		}
	}
	return values
}
