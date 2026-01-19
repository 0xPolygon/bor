// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package wit

import (
	"github.com/ethereum/go-ethereum/metrics"
)

var (
	// Witness request metrics (client-side)
	witnessRequestsCompact = metrics.NewRegisteredCounter("eth/fetcher/witness/requests/compact", nil)
	witnessRequestsFull    = metrics.NewRegisteredCounter("eth/fetcher/witness/requests/full", nil)

	// Witness served metrics (server-side)
	witnessServedCompact = metrics.NewRegisteredCounter("eth/fetcher/witness/served/compact", nil)
	witnessServedFull    = metrics.NewRegisteredCounter("eth/fetcher/witness/served/full", nil)
)

// MetricsHelper provides helper functions for recording witness metrics.
type MetricsHelper struct{}

// RecordCompactWitnessRequest records a compact witness request (client-side).
func (m *MetricsHelper) RecordCompactWitnessRequest() {
	witnessRequestsCompact.Inc(1)
}

// RecordFullWitnessRequest records a full witness request (client-side).
func (m *MetricsHelper) RecordFullWitnessRequest() {
	witnessRequestsFull.Inc(1)
}

// RecordCompactWitnessServed records when a compact witness is served (server-side).
func (m *MetricsHelper) RecordCompactWitnessServed() {
	witnessServedCompact.Inc(1)
}

// RecordFullWitnessServed records when a full witness is served (server-side).
func (m *MetricsHelper) RecordFullWitnessServed() {
	witnessServedFull.Inc(1)
}

// NewMetricsHelper creates a new metrics helper.
func NewMetricsHelper() *MetricsHelper {
	return &MetricsHelper{}
}
