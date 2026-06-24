package vm

import (
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
)

// ladderShadowRec carries a pending shadow comparison across loop iterations
// within a single Run frame (the block runs as straight-line code after entry).
type ladderShadowRec struct {
	pending    bool
	endPC      uint64
	rDepth     int           // expected result depth-from-top at endPC
	gasAtStart uint64        // contract.Gas at block entry
	gasStatic  uint64        // analyzer's static gas for the block
	native     uint256.Int   // native result computed at entry
}

// tryNativeLadder is called at every JUMPDEST when the feature is on and no tracer
// is attached. In active mode it may substitute the whole block (returns true,
// having advanced *pc past it). In shadow mode it records a pending comparison and
// returns false so the interpreter runs the block normally. Any non-match or
// insufficient gas returns false (the interpreter runs unchanged).
func (evm *EVM) tryNativeLadder(contract *Contract, stack *Stack, pc *uint64, lsh *ladderShadowRec) bool {
	table := nativectf.LadderTableFor(contract.CodeHash, contract.Code)
	if len(table) == 0 {
		return false
	}
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
	native := *nativectf.ModSqrtCandidate(&x)

	if evm.Config.NativeCTF == NativeCTFShadow {
		*lsh = ladderShadowRec{
			pending:    true,
			endPC:      meta.EndPC,
			rDepth:     rDepth,
			gasAtStart: contract.Gas,
			gasStatic:  meta.GasStatic,
			native:     native,
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
		return
	}
	ladderShadowMismatch.Inc(1)
	log.Warn("nativectf ladder shadow MISMATCH",
		"endPC", lsh.endPC,
		"native", lsh.native.Hex(), "interp", got.Hex(),
		"gasStatic", lsh.gasStatic, "gasUsed", gasUsed)
}
