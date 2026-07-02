package vm

import (
	"time"

	"github.com/ethereum/go-ethereum/core/vm/nativectf"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/holiman/uint256"
)

// Native BN254 ladder superinstruction (mid-execution, internal getCollectionId).
// Recognizes the universal jump-free square-and-multiply ladder block and either
// validates it (shadow) or substitutes a native result (active), gas-and-result
// exact. See investigations design-2026-06-24-native-gci-ladder-superinstruction.

var (
	// ladderShadowMatch / ladderShadowMismatch: shadow-mode agreement counters.
	// A nonzero mismatch is a hard stop signal — never go active until it is zero.
	ladderShadowMatch    = metrics.NewRegisteredCounter("vm/nativectf/ladder/match", nil)
	ladderShadowMismatch = metrics.NewRegisteredCounter("vm/nativectf/ladder/mismatch", nil)
	// ladderActive: active-mode substitution count.
	ladderActive = metrics.NewRegisteredCounter("vm/nativectf/ladder/active", nil)
	// Performance comparison (shadow mode): cumulative wall time the interpreter
	// spends executing recognized ladder blocks vs the native compute for the same
	// blocks. interp_ns/native_ns is the per-ladder speedup; (interp_ns - native_ns)
	// is the execution time active mode would save. Wall-clock under parallel
	// execution is approximate (scheduling jitter) but directional over millions.
	ladderInterpNs = metrics.NewRegisteredCounter("vm/nativectf/ladder/interp_ns", nil)
	ladderNativeNs = metrics.NewRegisteredCounter("vm/nativectf/ladder/native_ns", nil)
	// ladderTotalInterpNs: cumulative interpreter wall time across all outermost
	// (depth==1) EVM frames, measured on the SAME per-goroutine-wall basis as
	// interp_ns. It is the denominator for the overall-gain estimate:
	//   ladder share of EVM execution = interp_ns / total_interp_ns
	//   EVM-exec time native eliminates = (interp_ns - native_ns) / total_interp_ns
	// Only the depth==1 frame is timed, so nested CALLs are counted exactly once
	// (a child frame's wall time is already inside its parent's, never re-added).
	ladderTotalInterpNs = metrics.NewRegisteredCounter("vm/nativectf/ladder/total_interp_ns", nil)
)

// LadderMetrics is a snapshot of the shadow-mode ladder instrumentation counters,
// exported so an offline re-exec harness can read them without touching the metrics
// registry by string name. interp_ns/total_interp_ns is the ladder's share of EVM
// interpreter execution time; under strictly serial re-execution wall-clock ~= CPU
// time, so the ratio is meaningful (unlike the original parallel-exec measurement).
type LadderMetrics struct {
	Match, Mismatch, Active     int64
	InterpNs, NativeNs, TotalNs int64
}

// LadderMetricsSnapshot returns the current cumulative ladder instrumentation counters.
func LadderMetricsSnapshot() LadderMetrics {
	return LadderMetrics{
		Match:    ladderShadowMatch.Snapshot().Count(),
		Mismatch: ladderShadowMismatch.Snapshot().Count(),
		Active:   ladderActive.Snapshot().Count(),
		InterpNs: ladderInterpNs.Snapshot().Count(),
		NativeNs: ladderNativeNs.Snapshot().Count(),
		TotalNs:  ladderTotalInterpNs.Snapshot().Count(),
	}
}

// ladderShadowRec carries a pending shadow comparison across loop iterations
// within a single Run frame (the block runs as straight-line code after entry).
type ladderShadowRec struct {
	pending    bool
	endPC      uint64
	rDepth     int         // expected result depth-from-top at endPC
	gasAtStart uint64      // contract.Gas at block entry
	gasStatic  uint64      // analyzer's static gas for the block
	native     uint256.Int // native result computed at entry
	nativeNs   int64       // wall time the native compute took
	entryTime  time.Time   // wall clock when the interpreter began the block
}

// tryNativeLadder is called at every JUMPDEST when the feature is on and no tracer
// is attached. In active mode it may substitute the whole block (returns true,
// having advanced *pc past it). In shadow mode it records a pending comparison and
// returns false so the interpreter runs the block normally. Any non-match or
// insufficient gas returns false (the interpreter runs unchanged).
func (evm *EVM) tryNativeLadder(contract *Contract, table map[uint64]nativectf.LadderMeta, stack *Stack, pc *uint64, lsh *ladderShadowRec) bool {
	meta, ok := table[*pc]
	if !ok || !meta.Pure { // Phase 1: pure (side-effect-free, static-gas) blocks only
		return false
	}
	if stack.len() <= meta.BaseDepth || stack.len() < meta.In {
		return false // defensive: malformed frame, let the interpreter handle it
	}
	rDepth, ok := meta.ResultDepthFromTop()
	if !ok {
		return false
	}

	x := *stack.Back(meta.BaseDepth) // copy
	t0 := time.Now()
	native := *nativectf.ModSqrtCandidate(&x)
	nativeNs := time.Since(t0).Nanoseconds()

	if evm.Config.NativeCTF == NativeCTFShadow {
		*lsh = ladderShadowRec{
			pending:    true,
			endPC:      meta.EndPC,
			rDepth:     rDepth,
			gasAtStart: contract.Gas,
			gasStatic:  meta.GasStatic,
			native:     native,
			nativeNs:   nativeNs,
			entryTime:  time.Now(), // interpreter starts the block now
		}
		return false // let the interpreter execute the block; finalize at endPC
	}

	// Active substitution.
	if contract.Gas < meta.GasStatic {
		return false // interpreter will OOG at the identical opcode
	}
	// Save the top `In` entry values, then rebuild the output stack per meta.Out.
	saved := make([]uint256.Int, meta.In)
	for d := 0; d < meta.In; d++ {
		saved[d] = *stack.Back(d)
	}
	for i := 0; i < meta.In; i++ {
		stack.pop()
	}
	for _, o := range meta.Out { // bottom-to-top
		switch o.Kind {
		case nativectf.OutEntry:
			v := saved[o.Depth]
			stack.push(&v)
		case nativectf.OutResult:
			r := native
			stack.push(&r)
		case nativectf.OutConst:
			c := *o.Const
			stack.push(&c)
		}
	}
	contract.Gas -= meta.GasStatic
	*pc = meta.EndPC
	ladderActive.Inc(1)
	return true
}

// finalizeLadderShadow runs at endPC: compares the interpreter's realized result
// and gas for the block against the native prediction, and records the verdict.
func (evm *EVM) finalizeLadderShadow(contract *Contract, stack *Stack, lsh *ladderShadowRec) {
	lsh.pending = false
	gasUsed := lsh.gasAtStart - contract.Gas
	if stack.len() <= lsh.rDepth {
		ladderShadowMismatch.Inc(1)
		log.Warn("nativectf ladder shadow: stack too short at endPC", "endPC", lsh.endPC, "rDepth", lsh.rDepth, "len", stack.len())
		return
	}
	got := stack.Back(lsh.rDepth)
	if got.Eq(&lsh.native) && gasUsed == lsh.gasStatic {
		ladderShadowMatch.Inc(1)
		// Record the interpreter-vs-native timing for the performance comparison.
		ladderInterpNs.Inc(time.Since(lsh.entryTime).Nanoseconds())
		ladderNativeNs.Inc(lsh.nativeNs)
		return
	}
	ladderShadowMismatch.Inc(1)
	log.Warn("nativectf ladder shadow MISMATCH",
		"endPC", lsh.endPC,
		"native", lsh.native.Hex(), "interp", got.Hex(),
		"gasStatic", lsh.gasStatic, "gasUsed", gasUsed)
}
