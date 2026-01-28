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

// Contains the metrics collected by the fetcher and witness manager.

package fetcher

import (
	"github.com/ethereum/go-ethereum/metrics"
)

var (
	// Witness verification metrics
	witnessVerifyCheckMeter       = metrics.NewRegisteredMeter("eth/fetcher/witness/verify/check", nil)
	witnessVerifySuccessMeter     = metrics.NewRegisteredMeter("eth/fetcher/witness/verify/success", nil)
	witnessVerifyFailureMeter     = metrics.NewRegisteredMeter("eth/fetcher/witness/verify/failure", nil)
	witnessVerifyDropMeter        = metrics.NewRegisteredMeter("eth/fetcher/witness/verify/drop", nil)
	witnessVerifyJailMeter        = metrics.NewRegisteredMeter("eth/fetcher/witness/verify/jail", nil)
	witnessVerifyPeersInsuffMeter = metrics.NewRegisteredMeter("eth/fetcher/witness/verify/peers/insufficient", nil)
	witnessVerifyNoConsensusMeter = metrics.NewRegisteredMeter("eth/fetcher/witness/verify/consensus/none", nil)

	// Witness page count metrics
	witnessPageCountBelowThresholdMeter = metrics.NewRegisteredMeter("eth/fetcher/witness/pagecount/below_threshold", nil)
	witnessPageCountAboveThresholdMeter = metrics.NewRegisteredMeter("eth/fetcher/witness/pagecount/above_threshold", nil)

	// Witness threshold calculation metrics
	witnessThresholdGauge = metrics.NewRegisteredGauge("eth/fetcher/witness/threshold/current", nil)
)
