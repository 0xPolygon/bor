package peerpolicy

import (
	"fmt"
	"math"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
)

const (
	peerRiskMetric       = "eth/peerpolicy/peer_risk"
	peerRiskExportMetric = "eth/peerpolicy/peer_risk_export"
)

// Status codes are stable: append new statuses without renumbering existing ones.
var contributionStatuses = []string{"unknown", "warming-up", "contributing", "request-only", "idle"}

func contributionStatusCode(status string) int64 {
	for code, name := range contributionStatuses {
		if name == status {
			return int64(code)
		}
	}

	return 0
}

type peerMetricSample struct {
	risk        map[string]int64
	connected   map[string]int64
	protected   map[string]int64
	status      map[string]int64
	activity    map[string]ActivityEvent
	classRisk   map[string]int64
	classCount  map[string]int64
	statusCount map[string]int64
	omitted     int64
	admitted    int64
}

type peerMetricFamily struct {
	name   string
	label  string
	source func() map[string]int64
}

func (t *Tracker) families() []peerMetricFamily {
	activity := func(pick func(ActivityEvent) uint64) func() map[string]int64 {
		return func() map[string]int64 {
			sample := t.collect()
			values := make(map[string]int64, len(sample.activity))
			for id, event := range sample.activity {
				values[id] = saturate(pick(event))
			}

			return values
		}
	}

	return []peerMetricFamily{
		{peerRiskMetric, "peer_id", t.peerRisks},
		{"eth/peerpolicy/peer_connected", "peer_id", func() map[string]int64 { return t.collect().connected }},
		{"eth/peerpolicy/peer_protected", "peer_id", func() map[string]int64 { return t.collect().protected }},
		{"eth/peerpolicy/peer_contribution_status", "peer_id", func() map[string]int64 { return t.collect().status }},
		{"eth/peerpolicy/peer_received_messages", "peer_id", activity(func(e ActivityEvent) uint64 { return e.ReceivedMessages })},
		{"eth/peerpolicy/peer_received_bytes", "peer_id", activity(func(e ActivityEvent) uint64 { return e.ReceivedBytes })},
		{"eth/peerpolicy/peer_served_responses", "peer_id", activity(func(e ActivityEvent) uint64 { return e.ServedResponses })},
		{"eth/peerpolicy/peer_served_bytes", "peer_id", activity(func(e ActivityEvent) uint64 { return e.ServedBytes })},
		{"eth/peerpolicy/peer_accepted_transactions", "peer_id", activity(func(e ActivityEvent) uint64 { return e.AcceptedTransactions })},
		{"eth/peerpolicy/peer_unclassified_messages", "peer_id", activity(func(e ActivityEvent) uint64 { return e.UnclassifiedMessages })},
		{"eth/peerpolicy/connected_risk_sum", "class", func() map[string]int64 { return t.collect().classRisk }},
		{"eth/peerpolicy/connected_risk_count", "class", func() map[string]int64 { return t.collect().classCount }},
		{"eth/peerpolicy/connected_contribution_status", "status", func() map[string]int64 { return t.collect().statusCount }},
		{peerRiskExportMetric, "state", t.peerRiskExport},
	}
}

// RegisterPeerMetrics registers collectors for the node's lifetime.
func (t *Tracker) RegisterPeerMetrics(registry metrics.Registry) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.metrics.peerRegistry != nil {
		return fmt.Errorf("peer reputation gauges already registered")
	}

	families := t.families()
	for i, family := range families {
		if err := registry.Register(family.name, metrics.NewGaugeVec(family.label, family.source)); err != nil {
			for _, registered := range families[:i] {
				registry.Unregister(registered.name)
			}

			return fmt.Errorf("register %s: %w", family.name, err)
		}
	}

	t.metrics.peerRegistry = registry

	return nil
}

func (t *Tracker) UnregisterPeerMetrics() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.metrics.peerRegistry != nil {
		for _, family := range t.families() {
			t.metrics.peerRegistry.Unregister(family.name)
		}

		t.metrics.peerRegistry = nil
	}
}

func (t *Tracker) peerRisks() map[string]int64 { return t.collect().risk }

func (t *Tracker) peerRiskExport() map[string]int64 {
	sample := t.collect()

	return map[string]int64{
		"limit": maxPeerMetricIdentities, "admitted": sample.admitted,
		"exported": int64(len(sample.risk)), "omitted": sample.omitted,
	}
}

func (t *Tracker) collect() peerMetricSample {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()
	sample := peerMetricSample{
		risk: make(map[string]int64), connected: make(map[string]int64),
		protected: make(map[string]int64), status: make(map[string]int64),
		activity:    make(map[string]ActivityEvent),
		classRisk:   map[string]int64{"ordinary": 0, "protected": 0},
		classCount:  map[string]int64{"ordinary": 0, "protected": 0},
		statusCount: make(map[string]int64, len(contributionStatuses)),
	}

	for _, status := range contributionStatuses {
		sample.statusCount[status] = 0
	}

	// Peek avoids treating a scrape as peer activity and changing LRU eviction.
	for _, id := range t.peers.Keys() {
		peer, _ := t.peers.Peek(id)
		t.samplePeer(&sample, id, peer, now)
	}

	// Population totals include identities omitted by the lifetime label budget.
	sample.omitted, sample.admitted = t.metrics.peerIDs.filter(sample.risk)
	for _, values := range []map[string]int64{sample.connected, sample.protected, sample.status} {
		retain(values, sample.risk)
	}

	retain(sample.activity, sample.risk)

	return sample
}

func (t *Tracker) samplePeer(sample *peerMetricSample, id string, peer *peerRecord, now mclock.AbsTime) {
	connected := peer.activity.registered
	if t.classResolver != nil {
		protected, live := t.classResolver(id)
		connected = connected && live
		if live {
			peer.setExempt(protected)
		}
	}
	risk := peer.risk(int64(now)/int64(windowWidth), nil)
	if !connected && (peer.exempt || risk == 0) {
		return
	}

	t.prepareActivity(&peer.activity, now)
	contribution := peer.activity.snapshot(now, t.ready && connected, peer.exempt)
	sample.risk[id] = int64(risk)
	sample.connected[id] = flag(connected)
	sample.protected[id] = flag(peer.exempt)
	sample.status[id] = contributionStatusCode(contribution.Status)
	sample.activity[id] = contribution.ActivityEvent

	if connected {
		class := "ordinary"
		if peer.exempt {
			class = "protected"
		}
		sample.classRisk[class] += int64(risk)
		sample.classCount[class]++
		sample.statusCount[contribution.Status]++
	}
}

func retain[V any](values map[string]V, admitted map[string]int64) {
	for id := range values {
		if _, ok := admitted[id]; !ok {
			delete(values, id)
		}
	}
}

func flag(value bool) int64 {
	if value {
		return 1
	}

	return 0
}

func saturate(value uint64) int64 { return int64(min(value, math.MaxInt64)) }
