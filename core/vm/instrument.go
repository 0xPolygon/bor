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

// callStats accumulates noisy per-(label, path) call data for one block.
// The atomic fields let concurrent goroutines (throwaway prefetch, serial
// processor, parallel/BlockSTM processor, and within BlockSTM every worker/
// incarnation) update them without a lock. nanosHist/inputHist are safe for
// concurrent Update from multiple goroutines too — the underlying Sample
// implementation holds its own mutex.
type callStats struct {
	label string // original label, e.g. a precompile address hex or opcode name
	path  string // "prefetch" / "serial" / "parallel" / "" if untagged

	calls     atomic.Int64
	wouldHit  atomic.Int64
	nanos     atomic.Int64
	inputSum  atomic.Int64
	outputSum atomic.Int64

	// retries counts calls made by a BlockSTM incarnation > 0, i.e. a
	// transaction re-executed after a conflict-driven abort. Always 0
	// outside the parallel path (incarnation is meaningless there).
	retries atomic.Int64

	nanosHist metrics.Histogram // real per-call duration, for percentiles
	inputHist metrics.Histogram // real per-call input size, for percentiles
}

func newCallStats(label, path string) *callStats {
	return &callStats{
		label:     label,
		path:      path,
		nanosHist: metrics.NewHistogram(metrics.NewUniformSample(1028)),
		inputHist: metrics.NewHistogram(metrics.NewUniformSample(1028)),
	}
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
// Stats are bucketed per (label, path) — see Observe's path parameter —
// so a block's log makes it possible to tell how much of a label's would-
// hit rate is already realized by today's prefetch↔parallel sharing versus
// what would only materialize if the serial processor got the real caches
// too.
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
	stats map[string]*callStats // key: label + "|" + path
}

// NewCallObserver constructs an observer for a single block.
func NewCallObserver() *CallObserver {
	return &CallObserver{stats: make(map[string]*callStats)}
}

func (o *CallObserver) statsFor(label, path string) *callStats {
	key := label + "|" + path
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.stats[key]
	if !ok {
		s = newCallStats(label, path)
		o.stats[key] = s
	}
	return s
}

// Observe records one call under label (e.g. a precompile address or an
// opcode name), keyed by a content hash of its inputs. dur is the actual
// wall-clock time the call took; inputLen/outputLen are recorded to help
// size a real cache's entry bounds later.
//
// path identifies which of the three racing execution paths made this
// call ("prefetch" / "serial" / "parallel") — set from vm.Config.
// ObservePath at each call site, so a block's log can tell how much of
// the reported hit rate is already realized by today's prefetch↔parallel
// sharing versus what serial is currently missing out on.
//
// incarnation is the BlockSTM incarnation number for this call (0 for the
// first attempt at a transaction; >0 means it's a conflict-driven
// re-execution). Meaningless outside the parallel path — pass 0 there.
//
// Safe to call on a nil receiver.
func (o *CallObserver) Observe(label, path string, incarnation int, key common.Hash, dur time.Duration, inputLen, outputLen int) {
	if o == nil {
		return
	}
	s := o.statsFor(label, path)
	s.calls.Add(1)
	s.nanos.Add(dur.Nanoseconds())
	s.inputSum.Add(int64(inputLen))
	s.outputSum.Add(int64(outputLen))
	s.nanosHist.Update(dur.Nanoseconds())
	s.inputHist.Update(int64(inputLen))
	if incarnation > 0 {
		s.retries.Add(1)
	}

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
	keys := make([]string, 0, len(o.stats))
	for k := range o.stats {
		keys = append(keys, k)
	}
	o.mu.Unlock()
	sort.Strings(keys)

	for _, key := range keys {
		s := o.stats[key]
		calls := s.calls.Load()
		if calls == 0 {
			continue
		}
		hits := s.wouldHit.Load()
		nanos := s.nanos.Load()
		retries := s.retries.Load()

		nanosP := s.nanosHist.Snapshot().Percentiles([]float64{0.5, 0.9, 0.99})
		inputP := s.inputHist.Snapshot().Percentiles([]float64{0.5, 0.9, 0.99})

		log.Info("instrument: call stats",
			"block", blockNumber,
			"label", s.label,
			"path", s.path,
			"calls", calls,
			"wouldHitRate", fmt.Sprintf("%.4f", float64(hits)/float64(calls)),
			"retryRate", fmt.Sprintf("%.4f", float64(retries)/float64(calls)),
			"avgNanos", nanos/calls,
			"p50Nanos", int64(nanosP[0]),
			"p90Nanos", int64(nanosP[1]),
			"p99Nanos", int64(nanosP[2]),
			"totalMicros", nanos/1000,
			"avgInputBytes", s.inputSum.Load()/calls,
			"p50InputBytes", int64(inputP[0]),
			"p90InputBytes", int64(inputP[1]),
			"p99InputBytes", int64(inputP[2]),
			"avgOutputBytes", s.outputSum.Load()/calls,
		)

		prefix := "chain/instrument/" + key
		metrics.GetOrRegisterGaugeFloat64(prefix+"/hitrate", nil).Update(float64(hits) / float64(calls))
		metrics.GetOrRegisterGaugeFloat64(prefix+"/retryrate", nil).Update(float64(retries) / float64(calls))
		metrics.GetOrRegisterMeter(prefix+"/calls", nil).Mark(calls)
		metrics.GetOrRegisterTimer(prefix+"/duration", nil).Update(time.Duration(nanos / calls))
	}
}
