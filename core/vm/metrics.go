// Copyright 2025 The go-ethereum Authors
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

package vm

import "github.com/ethereum/go-ethereum/metrics"

// Per-opcode observability metrics. All durations are in nanoseconds.
//
// Duration histograms time the full opcode body — for SLOAD this includes
// the StateDB.GetState call which traverses originStorage, pendingStorage,
// diff layers, disk-layer write buffer, clean state cache, and (on a miss)
// a Pebble snapshot read. For SSTORE it includes the GetState call used
// for gas metering plus the dirty-storage write.
//
// Cold/warm meters are incremented in the EIP-2929 gas functions (that's
// the only point where we can distinguish — by the time the opcode body
// runs, the access list has already been updated).
var (
	sloadDurationHist  = metrics.NewRegisteredHistogram("vm/sload/duration", nil, metrics.NewExpDecaySample(1028, 0.015))
	sstoreDurationHist = metrics.NewRegisteredHistogram("vm/sstore/duration", nil, metrics.NewExpDecaySample(1028, 0.015))

	sloadColdMeter  = metrics.NewRegisteredMeter("vm/sload/cold", nil)
	sloadWarmMeter  = metrics.NewRegisteredMeter("vm/sload/warm", nil)
	sstoreColdMeter = metrics.NewRegisteredMeter("vm/sstore/cold", nil)
	sstoreWarmMeter = metrics.NewRegisteredMeter("vm/sstore/warm", nil)
)
