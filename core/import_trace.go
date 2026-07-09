package core

import (
	"sync/atomic"

	"github.com/ethereum/go-ethereum/core/state"
)

// Import-trace hook (lab instrumentation). When registered, the block import
// path attaches read-detail collectors to its per-block stats readers and
// reports one ImportTraceData per processed block. The consumer (the miner's
// build tracer) must return quickly — heavy work belongs on its own goroutine.

// ImportTraceData is the per-imported-block measurement payload.
type ImportTraceData struct {
	Number      uint64
	Txs         int
	GasUsed     uint64
	ExecUs      int64 // wall time of the winning processor (incl. state validation)
	ValUs       int64 // state-validation share of the winner
	ParallelWon bool  // true if the BlockSTM processor won the race

	ProcReads     state.ReadDetailStats
	PrefReads     state.ReadDetailStats
	Misses        []state.ReadMissEvent
	MissesDropped int64
	Touched       []uint64 // touched-key hashes (hits+misses) — re-reference ring feed

	// Per-segment exec wall time (tracing.ExecSegments.SnapshotUs keys).
	// Only populated in serial-only mode (parallel processor disabled).
	Segments map[string]int64
	// Opcode-family timing for sampled blocks; wall time is inflated by the
	// tracer on these blocks (OpFamSampled marks them).
	OpFams       map[string]OpFamStat
	OpFamSampled bool
}

var importTraceHook atomic.Pointer[func(ImportTraceData)]

// SetImportTraceHook registers (or clears, with nil) the import trace consumer.
func SetImportTraceHook(h func(ImportTraceData)) {
	if h == nil {
		importTraceHook.Store(nil)
		return
	}
	importTraceHook.Store(&h)
}

func getImportTraceHook() func(ImportTraceData) {
	p := importTraceHook.Load()
	if p == nil {
		return nil
	}
	return *p
}
