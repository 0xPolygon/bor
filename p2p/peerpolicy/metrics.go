// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
// The go-ethereum library is distributed under the GNU Lesser General Public License v3.0.

package peerpolicy

import (
	"strings"

	"github.com/ethereum/go-ethereum/metrics"
)

type observationMetrics struct {
	reasons   [reasonCount]*metrics.Counter
	windows   [reasonCount]*metrics.Counter
	items     [familyCount]*metrics.Counter
	bytes     [familyCount]*metrics.Counter
	evictions *metrics.Counter
	throttle  *metrics.Counter
	jail      *metrics.Counter
	risk      metrics.Histogram
}

func newObservationMetrics() *observationMetrics {
	m := &observationMetrics{}
	for r := InvalidEncoding; r < reasonCount; r++ {
		m.reasons[r] = metrics.GetOrRegisterCounter("eth/peerpolicy/events/"+strings.ReplaceAll(r.String(), "-", "_"), nil)
		m.windows[r] = metrics.GetOrRegisterCounter("eth/peerpolicy/windows/"+strings.ReplaceAll(r.String(), "-", "_"), nil)
	}
	for f, name := range []string{"other", "block_announcements", "transaction_announcements", "transactions", "blocks", "requests", "body_replies"} {
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
