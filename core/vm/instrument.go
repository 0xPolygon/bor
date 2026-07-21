// Copyright 2026 The go-ethereum Authors
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

// This file is throwaway copy-node instrumentation, not a production
// feature. It measures whether extending bor's existing per-block shared
// caches (EcrecoverCache, Keccak256Cache — see evm.go/instructions.go) to
// more precompiles and opcodes would pay off, following the design geth
// PR #35388 (precompile result caching via the prefetcher) proposes for
// upstream geth. It never stores or returns a cached result, so it cannot
// change consensus-critical output — it only counts and times calls.
//
// Content-keyed, not index-keyed: the "would this hit a cache" check is
// keyed by a hash of the call's actual inputs, not by transaction index or
// call order. That is deliberate — during block *building* the prefetcher
// runs mempool transactions in an order unrelated to the block that is
// eventually sealed, so an index-keyed simulation would misreport the hit
// rate that a real cache would see. Content-keying gives the same answer
// regardless of call order, which is the only thing that's actually true
// both for block building and block import.

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

// maxObservedCallInput bounds the size of data hashed into an observation
// key. Precompiles/opcodes fed larger-than-this input are one-off enough
// that a real cache would likely exclude them too (see geth PR #35388's
// own 8KB bound); we skip observing them so this instrumentation doesn't
// itself become the CPU cost we're trying to measure around.
const maxObservedCallInput = 8192

// callStats accumulates noisy per-label call data for one block. All
// fields are atomics so concurrent goroutines (throwaway prefetch, serial
// processor, parallel/BlockSTM processor) can update them without a lock.
type callStats struct {
	calls     atomic.Int64
	wouldHit  atomic.Int64
	nanos     atomic.Int64
	inputSum  atomic.Int64
	outputSum atomic.Int64
}

// CallObserver tracks, for a single block, whether a repeated
// (label, content) pair would have hit a shared result cache, and how
// expensive each call actually was. One instance is shared across the
// throwaway prefetch pass, the serial processor, and the parallel
// (BlockSTM) processor for that block, so the reported hit rate answers
// "what would a cache shared across all three paths see" — the real
// design question — rather than today's partial wiring (only the
// prefetcher and the parallel processor share EcrecoverCache/
// Keccak256Cache; the serial processor does not).
//
// Caveat: because the serial and parallel processors race each other and
// only one result is kept, the raw call counts/nanos summed here include
// whichever processor lost the race. That inflates the "total time spent"
// figures relative to true per-block wall-clock cost — use the hit-rate
// numbers as the primary signal, and cross-check timing against a
// wall-clock/pprof profile of the same replay run rather than this
// summed total.
type CallObserver struct {
	seen sync.Map // key: common.Hash, value: struct{}

	mu    sync.Mutex
	stats map[string]*callStats // key: label (precompile address hex, or opcode name)
}

// NewCallObserver constructs an observer for a single block.
func NewCallObserver() *CallObserver {
	return &CallObserver{stats: make(map[string]*callStats)}
}

func (o *CallObserver) statsFor(label string) *callStats {
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.stats[label]
	if !ok {
		s = &callStats{}
		o.stats[label] = s
	}
	return s
}

// Observe records one call under label (e.g. a precompile address or an
// opcode name), keyed by a content hash of its inputs. dur is the actual
// wall-clock time the call took; inputLen/outputLen are recorded to help
// size a real cache's entry bounds later. Safe to call on a nil receiver.
func (o *CallObserver) Observe(label string, key common.Hash, dur time.Duration, inputLen, outputLen int) {
	if o == nil {
		return
	}
	s := o.statsFor(label)
	s.calls.Add(1)
	s.nanos.Add(dur.Nanoseconds())
	s.inputSum.Add(int64(inputLen))
	s.outputSum.Add(int64(outputLen))

	if _, hit := o.seen.LoadOrStore(key, struct{}{}); hit {
		s.wouldHit.Add(1)
	}
}

// ObserveKey derives a content key for an observation from arbitrary byte
// slices (e.g. address+input for a precompile, or an opcode's operand
// bytes). Not a consensus hash — sha256 is picked purely for hardware
// acceleration, matching geth PR #35388's own key derivation.
func ObserveKey(parts ...[]byte) common.Hash {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	var out common.Hash
	h.Sum(out[:0])
	return out
}

// LogSummary emits one noisy Info-level log line per observed label, plus
// Datadog-scrapeable metrics (hit-rate gauge, call-count meter, timing
// histogram), intended to run once after every block on the copy-node
// replay so a full per-block time series is available, not just an
// end-of-run aggregate.
func (o *CallObserver) LogSummary(blockNumber uint64) {
	if o == nil {
		return
	}
	o.mu.Lock()
	labels := make([]string, 0, len(o.stats))
	for l := range o.stats {
		labels = append(labels, l)
	}
	o.mu.Unlock()
	sort.Strings(labels)

	for _, label := range labels {
		s := o.stats[label]
		calls := s.calls.Load()
		if calls == 0 {
			continue
		}
		hits := s.wouldHit.Load()
		nanos := s.nanos.Load()

		log.Info("instrument: call stats",
			"block", blockNumber,
			"label", label,
			"calls", calls,
			"wouldHitRate", fmt.Sprintf("%.4f", float64(hits)/float64(calls)),
			"avgNanos", nanos/calls,
			"totalMicros", nanos/1000,
			"avgInputBytes", s.inputSum.Load()/calls,
			"avgOutputBytes", s.outputSum.Load()/calls,
		)

		prefix := "chain/instrument/" + label
		metrics.GetOrRegisterGaugeFloat64(prefix+"/hitrate", nil).Update(float64(hits) / float64(calls))
		metrics.GetOrRegisterMeter(prefix+"/calls", nil).Mark(calls)
		metrics.GetOrRegisterTimer(prefix+"/duration", nil).Update(time.Duration(nanos / calls))
	}
}
