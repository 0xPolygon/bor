//go:build devnet_repro

package vm

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// devnetSlowOpcodeThreshold is the gap above which a single opcode/state
// access is considered a candidate explanation for interrupt overshoot (see
// design doc docs/superpowers/specs/2026-09-03-heavy-contract-slow-block-repro-design.md §8).
const devnetSlowOpcodeThreshold = 5 * time.Millisecond

// SlowOpcodeGap records a single opcode-dispatch gap exceeding
// devnetSlowOpcodeThreshold, tagged with enough identifying information to
// localize it (final review finding I2 — a bare time.Duration couldn't
// answer what §8 exists to answer).
//
// Attribution semantics (deliberate, see devnetTraceOpcodeGapForKey): Op/Pc/
// Contract identify the PREVIOUS instruction executed in this call frame —
// the one whose execution actually consumed the wall-clock time between
// this call and the last one for the same frame key — not the instruction
// about to run next. The gap measured at the top of the loop is the time
// between "finished running the previous opcode's execute()" and "about to
// run the next one", so the previous opcode is what was actually running
// during that window. For a CALL/DELEGATECALL-class opcode this attributes
// the whole nested-call gap (including anything slow several frames deeper)
// to the CALL itself in the CALLER's frame, not to whatever ran deepest —
// full call-stack attribution is an explicit non-goal of this fix (the final
// review asked only for enough info to point a human at the right call
// frame to dig into next, not a call-stack breakdown).
type SlowOpcodeGap struct {
	Duration time.Duration
	Op       OpCode
	Pc       uint64
	Contract common.Address
}

// devnetOpcodeGapEntry is the per-call-frame bookkeeping record: the
// timestamp of the most recent devnetTraceOpcodeGapForKey call for this
// frame key, plus the opcode/pc/contract that call observed (i.e. the
// instruction that was about to run at that point, which becomes "the
// previous instruction" by the time the NEXT call for the same key fires).
type devnetOpcodeGapEntry struct {
	At       time.Time
	Op       OpCode
	Pc       uint64
	Contract common.Address
}

var (
	devnetLastOpcodeAt sync.Map // map[*uint64]devnetOpcodeGapEntry, keyed per call frame

	devnetSlowGapsMu sync.Mutex
	devnetSlowGaps   []SlowOpcodeGap
)

// devnetTraceOpcodeGapForKey records the elapsed time since the previous call
// with the same key (a per-call-frame identity, see interpreter.go/
// interpreter_dispatch.go call sites), and appends it to the slow-gap log if
// it exceeds devnetSlowOpcodeThreshold. The first call for a given key only
// seeds the timestamp/opcode/pc/contract.
//
// op, pc and contractAddr describe the opcode about to execute at the
// current call site (not yet run) — see SlowOpcodeGap's doc comment for why
// a recorded gap ends up attributed to the PREVIOUS call's op/pc/contract,
// not this call's.
func devnetTraceOpcodeGapForKey(key *uint64, op OpCode, pc uint64, contractAddr common.Address) {
	now := time.Now()
	entry := devnetOpcodeGapEntry{At: now, Op: op, Pc: pc, Contract: contractAddr}

	prevAny, ok := devnetLastOpcodeAt.Swap(key, entry)
	if !ok {
		return
	}

	prev := prevAny.(devnetOpcodeGapEntry)
	gap := now.Sub(prev.At)

	if gap >= devnetSlowOpcodeThreshold {
		devnetSlowGapsMu.Lock()
		devnetSlowGaps = append(devnetSlowGaps, SlowOpcodeGap{
			Duration: gap,
			Op:       prev.Op,
			Pc:       prev.Pc,
			Contract: prev.Contract,
		})
		devnetSlowGapsMu.Unlock()
	}
}

// drainSlowOpcodeGaps returns and clears all recorded slow gaps. Intended to
// be polled by the debug RPC layer (Task 4) or a periodic logger, not by
// production code.
func drainSlowOpcodeGaps() []SlowOpcodeGap {
	devnetSlowGapsMu.Lock()
	defer devnetSlowGapsMu.Unlock()

	gaps := devnetSlowGaps
	devnetSlowGaps = nil

	return gaps
}

// DrainSlowOpcodeGaps is the exported form of drainSlowOpcodeGaps, for callers
// outside package vm (the debug RPC layer, eth/api_debug_repro.go).
func DrainSlowOpcodeGaps() []SlowOpcodeGap {
	return drainSlowOpcodeGaps()
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
