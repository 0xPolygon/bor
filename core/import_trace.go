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
	// Per-opcode (top-N + OTHER) and per-executing-contract (top-N + other)
	// splits of the same sampled-block attribution.
	Opcodes   map[string]OpFamStat
	Contracts map[string]OpFamStat
	// Per-contract opcode-family split (top-N contracts) — compute-vs-state
	// profile for native-execution candidate sizing.
	ContractFams map[string]map[string]OpFamStat

	// Read-miss attribution against the block prefetcher (counts and µs):
	//   unpref_*   — miss in a tx the prefetcher never completed
	//   diverged_* — tx was prefetched, but it never touched this key
	//                (state drift sent the prefetch down a different path)
	//   covered_*  — prefetcher touched the key, yet the exec read still
	//                missed (evicted, or warmed too late — the commit race)
	// plus pref_done_n (txs fully prefetched) and pref_touched_n (distinct
	// keys the prefetcher read).
	MissAttrib map[string]int64

	// Validation decomposition from statedb's built-in duration counters (us):
	// account/storage hashes (root computation), updates, and bor consensus
	// time (spans + state-sync inside engine.Finalize).
	ValDetail map[string]int64
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
