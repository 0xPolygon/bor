//go:build devnet_repro

package vm

import (
	"sync"
	"time"
)

// devnetSlowOpcodeThreshold is the gap above which a single opcode/state
// access is considered a candidate explanation for interrupt overshoot (see
// design doc docs/superpowers/specs/2026-09-03-heavy-contract-slow-block-repro-design.md §8).
const devnetSlowOpcodeThreshold = 5 * time.Millisecond

var (
	devnetLastOpcodeAt sync.Map // map[*uint64]time.Time, keyed per call frame

	devnetSlowGapsMu sync.Mutex
	devnetSlowGaps   []time.Duration
)

// devnetTraceOpcodeGapForKey records the elapsed time since the previous call
// with the same key (a per-call-frame identity, see interpreter.go/
// interpreter_dispatch.go call sites), and appends it to the slow-gap log if
// it exceeds devnetSlowOpcodeThreshold. The first call for a given key only
// seeds the timestamp.
func devnetTraceOpcodeGapForKey(key *uint64) {
	now := time.Now()

	prevAny, ok := devnetLastOpcodeAt.Swap(key, now)
	if !ok {
		return
	}

	prev := prevAny.(time.Time)
	gap := now.Sub(prev)

	if gap >= devnetSlowOpcodeThreshold {
		devnetSlowGapsMu.Lock()
		devnetSlowGaps = append(devnetSlowGaps, gap)
		devnetSlowGapsMu.Unlock()
	}
}

// drainSlowOpcodeGaps returns and clears all recorded slow gaps. Intended to
// be polled by the debug RPC layer (Task 4) or a periodic logger, not by
// production code.
func drainSlowOpcodeGaps() []time.Duration {
	devnetSlowGapsMu.Lock()
	defer devnetSlowGapsMu.Unlock()

	gaps := devnetSlowGaps
	devnetSlowGaps = nil

	return gaps
}

// devnetClearOpcodeGapKey removes the bookkeeping entry for a completed call
// frame. Without this, devnetLastOpcodeAt would grow without bound: every
// Run/runSwitch invocation (including deeply nested CALL frames) registers a
// distinct *uint64 key that otherwise lives in the map forever. Call this via
// defer immediately after the frame's pc local is declared, so it runs on
// every return path (including panics/errors) once the frame is done.
func devnetClearOpcodeGapKey(key *uint64) {
	devnetLastOpcodeAt.Delete(key)
}
