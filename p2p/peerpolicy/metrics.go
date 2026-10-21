// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/metrics"
)

type observationMetrics struct {
	peerRegistry metrics.Registry
	peerIDs      *peerMetricIdentities
	perPeer      bool
	reasons      [reasonCount]*metrics.Counter
	windows      [reasonCount]*metrics.Counter
	items        [familyCount]*metrics.Counter
	bytes        [familyCount]*metrics.Counter
	evictions    *metrics.Counter
	throttle     *metrics.Counter
	jail         *metrics.Counter
	risk         metrics.Histogram
}

func newObservationMetrics() *observationMetrics {
	m := &observationMetrics{peerIDs: &processPeerMetricIdentities}
	for r := InvalidEncoding; r < reasonCount; r++ {
		m.reasons[r] = metrics.GetOrRegisterCounter("eth/peerpolicy/events/"+strings.ReplaceAll(r.String(), "-", "_"), nil)
		m.windows[r] = metrics.GetOrRegisterCounter("eth/peerpolicy/windows/"+strings.ReplaceAll(r.String(), "-", "_"), nil)
	}
	for f, name := range []string{"other", "block_announcements", "transaction_announcements", "transactions", "blocks", "requests", "body_replies", "transaction_requests"} {
		m.items[f] = metrics.GetOrRegisterCounter("eth/peerpolicy/items/"+name, nil)
		m.bytes[f] = metrics.GetOrRegisterCounter("eth/peerpolicy/bytes/"+name, nil)
	}
	m.evictions = metrics.GetOrRegisterCounter("eth/peerpolicy/evictions", nil)
	m.throttle = metrics.GetOrRegisterCounter("eth/peerpolicy/would_throttle", nil)
	m.jail = metrics.GetOrRegisterCounter("eth/peerpolicy/would_jail", nil)
	m.risk = metrics.GetOrRegisterHistogram("eth/peerpolicy/risk", nil, metrics.NewUniformSample(1028))
	return m
}

func (m *observationMetrics) traffic(e Evidence) {
	m.items[e.Family].Inc(int64(min(e.Items, 1<<63-1)))
	m.bytes[e.Family].Inc(int64(min(e.Bytes, 1<<63-1)))
}

func (m *observationMetrics) transition(action string) {
	switch action {
	case "throttle":
		m.throttle.Inc(1)
	case "jail":
		m.jail.Inc(1)
	}
}

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
	risk         map[string]int64
	abuseRisk    map[string]int64
	behaviorRisk map[string]int64
	connected    map[string]int64
	protected    map[string]int64
	status       map[string]int64
	activity     map[string]ActivityEvent
	classRisk    map[string]int64
	classCount   map[string]int64
	statusCount  map[string]int64
	omitted      int64
	admitted     int64
}

type peerMetricFamily struct {
	name   string
	label  string
	source func(peerMetricSample) map[string]int64
}

func (t *Tracker) families(perPeer bool) []peerMetricFamily {
	families := []peerMetricFamily{
		{"eth/peerpolicy/connected_risk_sum", "class", func(s peerMetricSample) map[string]int64 { return s.classRisk }},
		{"eth/peerpolicy/connected_risk_count", "class", func(s peerMetricSample) map[string]int64 { return s.classCount }},
		{"eth/peerpolicy/connected_contribution_status", "status", func(s peerMetricSample) map[string]int64 { return s.statusCount }},
	}
	if !perPeer {
		return families
	}
	activity := func(pick func(ActivityEvent) uint64) func(peerMetricSample) map[string]int64 {
		return func(sample peerMetricSample) map[string]int64 {
			values := make(map[string]int64, len(sample.activity))
			for id, event := range sample.activity {
				values[id] = saturate(pick(event))
			}

			return values
		}
	}

	return append(families, []peerMetricFamily{
		{peerRiskMetric, "peer_id", func(s peerMetricSample) map[string]int64 { return s.risk }},
		{"eth/peerpolicy/peer_abuse_risk", "peer_id", func(s peerMetricSample) map[string]int64 { return s.abuseRisk }},
		{"eth/peerpolicy/peer_behavior_risk", "peer_id", func(s peerMetricSample) map[string]int64 { return s.behaviorRisk }},
		{"eth/peerpolicy/peer_connected", "peer_id", func(s peerMetricSample) map[string]int64 { return s.connected }},
		{"eth/peerpolicy/peer_protected", "peer_id", func(s peerMetricSample) map[string]int64 { return s.protected }},
		{"eth/peerpolicy/peer_contribution_status", "peer_id", func(s peerMetricSample) map[string]int64 { return s.status }},
		{"eth/peerpolicy/peer_received_messages", "peer_id", activity(func(e ActivityEvent) uint64 { return e.ReceivedMessages })},
		{"eth/peerpolicy/peer_received_bytes", "peer_id", activity(func(e ActivityEvent) uint64 { return e.ReceivedBytes })},
		{"eth/peerpolicy/peer_served_responses", "peer_id", activity(func(e ActivityEvent) uint64 { return e.ServedResponses })},
		{"eth/peerpolicy/peer_served_bytes", "peer_id", activity(func(e ActivityEvent) uint64 { return e.ServedBytes })},
		{"eth/peerpolicy/peer_accepted_transactions", "peer_id", activity(func(e ActivityEvent) uint64 { return e.AcceptedTransactions })},
		{"eth/peerpolicy/peer_unclassified_messages", "peer_id", activity(func(e ActivityEvent) uint64 { return e.UnclassifiedMessages })},
		{peerRiskExportMetric, "state", peerRiskExport},
	}...)
}

// RegisterPeerMetrics registers collectors for the node's lifetime.
func (t *Tracker) RegisterPeerMetrics(registry metrics.Registry, perPeer bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.metrics.peerRegistry != nil {
		return fmt.Errorf("peer reputation gauges already registered")
	}

	families := t.families(perPeer)
	group := metrics.NewGaugeVecGroup(func() map[string]map[string]int64 {
		sample := t.collect(perPeer)
		values := make(map[string]map[string]int64, len(families))
		for _, family := range families {
			values[family.name] = family.source(sample)
		}
		return values
	})
	for i, family := range families {
		if err := registry.Register(family.name, group.Gauge(family.name, family.label)); err != nil {
			for _, registered := range families[:i] {
				registry.Unregister(registered.name)
			}

			return fmt.Errorf("register %s: %w", family.name, err)
		}
	}

	t.metrics.peerRegistry = registry
	t.metrics.perPeer = perPeer

	return nil
}

func (t *Tracker) UnregisterPeerMetrics() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.metrics.peerRegistry != nil {
		for _, family := range t.families(t.metrics.perPeer) {
			t.metrics.peerRegistry.Unregister(family.name)
		}

		t.metrics.peerRegistry = nil
	}
}

func (t *Tracker) peerRisks() map[string]int64 { return t.collect(true).risk }

func (t *Tracker) peerRiskExport() map[string]int64 { return peerRiskExport(t.collect(true)) }

func peerRiskExport(sample peerMetricSample) map[string]int64 {
	return map[string]int64{
		"limit": maxPeerMetricIdentities, "admitted": sample.admitted,
		"exported": int64(len(sample.risk)), "omitted": sample.omitted,
	}
}

func (t *Tracker) collect(perPeer bool) peerMetricSample {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()
	sample := peerMetricSample{
		classRisk:   map[string]int64{"ordinary": 0, "protected": 0},
		classCount:  map[string]int64{"ordinary": 0, "protected": 0},
		statusCount: make(map[string]int64, len(contributionStatuses)),
	}
	if perPeer {
		sample.risk, sample.connected = make(map[string]int64), make(map[string]int64)
		sample.abuseRisk, sample.behaviorRisk = make(map[string]int64), make(map[string]int64)
		sample.protected, sample.status = make(map[string]int64), make(map[string]int64)
		sample.activity = make(map[string]ActivityEvent)
	}

	for _, status := range contributionStatuses {
		sample.statusCount[status] = 0
	}

	// Peek avoids treating a scrape as peer activity and changing LRU eviction.
	for _, id := range t.peers.Keys() {
		peer, _ := t.peers.Peek(id)
		t.samplePeer(&sample, id, peer, now)
	}
	if !perPeer {
		return sample
	}

	// Population totals include identities omitted by the lifetime label budget.
	sample.omitted, sample.admitted = t.metrics.peerIDs.filter(sample.risk, sample.abuseRisk)
	for _, values := range []map[string]int64{sample.abuseRisk, sample.behaviorRisk, sample.connected, sample.protected, sample.status} {
		retain(values, sample.risk)
	}

	retain(sample.activity, sample.risk)

	return sample
}

func (t *Tracker) samplePeer(sample *peerMetricSample, id string, peer *peerRecord, now mclock.AbsTime) {
	snapshot, connected := t.snapshotPeer(id, peer, now, nil)
	contribution := snapshot.Contribution
	if !connected && (contribution.Protected || snapshot.AbuseRisk == 0) {
		return
	}

	if sample.risk != nil {
		sample.risk[id] = int64(snapshot.Risk)
		sample.abuseRisk[id] = int64(snapshot.AbuseRisk)
		sample.behaviorRisk[id] = int64(contribution.Risk)
		sample.connected[id] = flag(connected)
		sample.protected[id] = flag(contribution.Protected)
		sample.status[id] = contributionStatusCode(contribution.Status)
		sample.activity[id] = contribution.ActivityEvent
	}

	if connected {
		class := "ordinary"
		if contribution.Protected {
			class = "protected"
		}
		sample.classRisk[class] += int64(snapshot.Risk)
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

const (
	maxPeerMetricIdentities        = 1024
	maxGeneralPeerMetricIdentities = maxPeerMetricIdentities / 2
)

// Keep admissions for the process lifetime: expiry, eviction and collector
// re-registration must not allow remote identity churn to mint new series.
var processPeerMetricIdentities peerMetricIdentities

type peerMetricIdentities struct {
	mu  sync.Mutex
	ids map[string]struct{}
}

func (p *peerMetricIdentities) filter(values, abuseRisk map[string]int64) (omitted, admitted int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ids == nil {
		p.ids = make(map[string]struct{})
	}
	for id := range values {
		if _, exists := p.ids[id]; exists {
			continue
		}
		// Reserve half the lifetime budget for abuse evidence, not connection churn.
		limit := maxGeneralPeerMetricIdentities
		if abuseRisk[id] > 0 {
			limit = maxPeerMetricIdentities
		}
		if len(p.ids) >= limit {
			delete(values, id)
			omitted++
			continue
		}
		p.ids[id] = struct{}{}
	}
	return omitted, int64(len(p.ids))
}
