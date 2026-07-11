// evm2go transpiles EVM runtime bytecode into a Go function that executes
// the contract inside bor's core/vm, dispatched by code hash.
//
// Design (prototype):
//   - The bytecode is split into basic blocks (leaders: pc 0, JUMPDESTs, and
//     the instruction after any terminator/JUMP/JUMPI).
//   - Constant-gas "compute/stack/control" opcodes are inlined as direct
//     uint256 operations on the interpreter stack array with block-relative
//     constant indices — no per-op dispatch, no per-op gas or stack checks.
//   - Gas for inlined opcodes is accumulated per straight-line segment and
//     charged exactly before any point that can observe gas (aotStep calls,
//     GAS opcode) and at block exits, which is outcome-equivalent to per-op
//     charging (straight-line code; OOG consumes the whole frame either way).
//   - Stack requirements are validated once per block (min entry height and
//     max growth), outcome-equivalent to per-op validation.
//   - Stateful and dynamic-gas opcodes (SLOAD, SSTORE, CALL*, KECCAK256,
//     MLOAD/MSTORE, LOG*, RETURN, REVERT, ...) run through vm.aotStep, which
//     replicates the interpreter loop body via the live jump table — their
//     gas and semantics are bit-identical by construction.
//   - Intra-block constant tracking (PUSH/DUP/SWAP propagation) resolves
//     static JUMP/JUMPI targets into direct gotos and constant-folds MULMOD
//     moduli into precomputed reciprocals (uint256.MulModWithReciprocal).
//
// Usage:
//
//	evm2go -hex path/to/runtime.hex -name CTFExchangeV2 -out core/vm/aot_gen_ctfexchangev2.go
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// ---- opcode metadata -------------------------------------------------------

type opInfo struct {
	name string
	gas  uint64 // constant gas (Cancun rules); only meaningful for inlined ops
	pops int
	push int
	mode opMode
}

type opMode int

const (
	modeInline opMode = iota // emitted as direct Go code
	modeStep                 // routed through vm.aotStep
	modeTerm                 // terminator routed through aotStep (returns)
)

const (
	opSTOP     = 0x00
	opJUMP     = 0x56
	opJUMPI    = 0x57
	opPC       = 0x58
	opMSIZE    = 0x59
	opGAS      = 0x5A
	opJUMPDEST = 0x5B
	opPUSH0    = 0x5F
	opPUSH1    = 0x60
	opPUSH32   = 0x7F
	opDUP1     = 0x80
	opDUP16    = 0x8F
	opSWAP1    = 0x90
	opSWAP16   = 0x9F
	opMULMOD   = 0x09
)

var ops [256]opInfo

func init() {
	for i := range ops {
		ops[i] = opInfo{name: fmt.Sprintf("op0x%02X", i), mode: modeStep}
	}
	set := func(code int, name string, gas uint64, pops, push int, mode opMode) {
		ops[code] = opInfo{name: name, gas: gas, pops: pops, push: push, mode: mode}
	}
	// Inlined arithmetic / logic (constant gas).
	set(0x01, "ADD", 3, 2, 1, modeInline)
	set(0x02, "MUL", 5, 2, 1, modeInline)
	set(0x03, "SUB", 3, 2, 1, modeInline)
	set(0x04, "DIV", 5, 2, 1, modeInline)
	set(0x05, "SDIV", 5, 2, 1, modeInline)
	set(0x06, "MOD", 5, 2, 1, modeInline)
	set(0x07, "SMOD", 5, 2, 1, modeInline)
	set(0x08, "ADDMOD", 8, 3, 1, modeInline)
	set(0x09, "MULMOD", 8, 3, 1, modeInline)
	set(0x0B, "SIGNEXTEND", 5, 2, 1, modeInline)
	set(0x10, "LT", 3, 2, 1, modeInline)
	set(0x11, "GT", 3, 2, 1, modeInline)
	set(0x12, "SLT", 3, 2, 1, modeInline)
	set(0x13, "SGT", 3, 2, 1, modeInline)
	set(0x14, "EQ", 3, 2, 1, modeInline)
	set(0x15, "ISZERO", 3, 1, 1, modeInline)
	set(0x16, "AND", 3, 2, 1, modeInline)
	set(0x17, "OR", 3, 2, 1, modeInline)
	set(0x18, "XOR", 3, 2, 1, modeInline)
	set(0x19, "NOT", 3, 1, 1, modeInline)
	set(0x1A, "BYTE", 3, 2, 1, modeInline)
	set(0x1B, "SHL", 3, 2, 1, modeInline)
	set(0x1C, "SHR", 3, 2, 1, modeInline)
	set(0x1D, "SAR", 3, 2, 1, modeInline)
	// Frame-local context (constant gas, no state access).
	set(0x30, "ADDRESS", 2, 0, 1, modeInline)
	set(0x33, "CALLER", 2, 0, 1, modeInline)
	set(0x34, "CALLVALUE", 2, 0, 1, modeInline)
	set(0x35, "CALLDATALOAD", 3, 1, 1, modeInline)
	set(0x36, "CALLDATASIZE", 2, 0, 1, modeInline)
	set(0x38, "CODESIZE", 2, 0, 1, modeInline)
	set(0x3D, "RETURNDATASIZE", 2, 0, 1, modeInline)
	// Stack / control.
	set(0x50, "POP", 2, 1, 0, modeInline)
	set(opJUMP, "JUMP", 8, 1, 0, modeInline)
	set(opJUMPI, "JUMPI", 10, 2, 0, modeInline)
	set(opPC, "PC", 2, 0, 1, modeInline)
	set(opMSIZE, "MSIZE", 2, 0, 1, modeInline)
	set(opGAS, "GAS", 2, 0, 1, modeInline)
	set(opJUMPDEST, "JUMPDEST", 1, 0, 0, modeInline)
	set(opSTOP, "STOP", 0, 0, 0, modeInline)
	set(opPUSH0, "PUSH0", 2, 0, 1, modeInline)
	for i := 0; i < 32; i++ {
		set(opPUSH1+i, fmt.Sprintf("PUSH%d", i+1), 3, 0, 1, modeInline)
	}
	for i := 0; i < 16; i++ {
		set(opDUP1+i, fmt.Sprintf("DUP%d", i+1), 3, i+1, i+2, modeInline)
		set(opSWAP1+i, fmt.Sprintf("SWAP%d", i+1), 3, i+2, i+2, modeInline)
	}
	// Stepped opcodes: net stack effect must still be tracked statically.
	step := func(code int, name string, pops, push int) {
		set(code, name, 0, pops, push, modeStep)
	}
	step(0x0A, "EXP", 2, 1)
	step(0x20, "KECCAK256", 2, 1)
	step(0x31, "BALANCE", 1, 1)
	step(0x32, "ORIGIN", 0, 1)
	step(0x37, "CALLDATACOPY", 3, 0)
	step(0x39, "CODECOPY", 3, 0)
	step(0x3A, "GASPRICE", 0, 1)
	step(0x3B, "EXTCODESIZE", 1, 1)
	step(0x3C, "EXTCODECOPY", 4, 0)
	step(0x3E, "RETURNDATACOPY", 3, 0)
	step(0x3F, "EXTCODEHASH", 1, 1)
	step(0x40, "BLOCKHASH", 1, 1)
	step(0x41, "COINBASE", 0, 1)
	step(0x42, "TIMESTAMP", 0, 1)
	step(0x43, "NUMBER", 0, 1)
	step(0x44, "PREVRANDAO", 0, 1)
	step(0x45, "GASLIMIT", 0, 1)
	step(0x46, "CHAINID", 0, 1)
	step(0x47, "SELFBALANCE", 0, 1)
	step(0x48, "BASEFEE", 0, 1)
	step(0x49, "BLOBHASH", 1, 1)
	step(0x4A, "BLOBBASEFEE", 0, 1)
	step(0x51, "MLOAD", 1, 1)
	step(0x52, "MSTORE", 2, 0)
	step(0x53, "MSTORE8", 2, 0)
	step(0x54, "SLOAD", 1, 1)
	step(0x55, "SSTORE", 2, 0)
	step(0x5C, "TLOAD", 1, 1)
	step(0x5D, "TSTORE", 2, 0)
	step(0x5E, "MCOPY", 3, 0)
	for i := 0; i <= 4; i++ {
		step(0xA0+i, fmt.Sprintf("LOG%d", i), 2+i, 0)
	}
	step(0xF0, "CREATE", 3, 1)
	step(0xF1, "CALL", 7, 1)
	step(0xF2, "CALLCODE", 7, 1)
	step(0xF4, "DELEGATECALL", 6, 1)
	step(0xF5, "CREATE2", 4, 1)
	step(0xFA, "STATICCALL", 6, 1)
	// Terminators through aotStep.
	term := func(code int, name string, pops int) {
		set(code, name, 0, pops, 0, modeTerm)
	}
	term(0xF3, "RETURN", 2)
	term(0xFD, "REVERT", 2)
	term(0xFE, "INVALID", 0)
	term(0xFF, "SELFDESTRUCT", 1)
}

// ---- instruction decoding --------------------------------------------------

type instr struct {
	pc   int
	code byte
	imm  []byte // PUSH immediate
}

func decode(bytecode []byte) []instr {
	var out []instr
	for pc := 0; pc < len(bytecode); {
		op := bytecode[pc]
		in := instr{pc: pc, code: op}
		if op >= opPUSH1 && op <= opPUSH32 {
			n := int(op-opPUSH1) + 1
			end := pc + 1 + n
			if end > len(bytecode) {
				end = len(bytecode)
			}
			// Implicit zero padding past end of code, per EVM semantics.
			imm := make([]byte, n)
			copy(imm, bytecode[pc+1:end])
			in.imm = imm
			pc += 1 + n
		} else {
			pc++
		}
		out = append(out, in)
	}
	return out
}

func jumpdests(bytecode []byte) map[int]bool {
	dests := make(map[int]bool)
	for pc := 0; pc < len(bytecode); {
		op := bytecode[pc]
		if op == opJUMPDEST {
			dests[pc] = true
		}
		if op >= opPUSH1 && op <= opPUSH32 {
			pc += int(op-opPUSH1) + 2
		} else {
			pc++
		}
	}
	return dests
}

// ---- basic blocks ----------------------------------------------------------

type block struct {
	start  int // pc of first instruction
	instrs []instr
	// analysis results
	minDelta int // most negative running stack delta (entry requirement = -minDelta)
	maxDelta int // most positive running stack delta (overflow check)
}

func splitBlocks(instrs []instr, dests map[int]bool) []*block {
	leaders := map[int]bool{0: true}
	for i, in := range instrs {
		if dests[in.pc] {
			leaders[in.pc] = true
		}
		switch in.code {
		case opJUMP, opJUMPI, opSTOP, 0xF3, 0xFD, 0xFE, 0xFF:
			if i+1 < len(instrs) {
				leaders[instrs[i+1].pc] = true
			}
		}
	}
	var blocks []*block
	var cur *block
	for _, in := range instrs {
		if leaders[in.pc] {
			cur = &block{start: in.pc}
			blocks = append(blocks, cur)
		}
		if cur == nil { // unreachable prelude (shouldn't happen: pc 0 is a leader)
			cur = &block{start: in.pc}
			blocks = append(blocks, cur)
		}
		cur.instrs = append(cur.instrs, in)
	}
	return blocks
}

func analyze(b *block) {
	b.minDelta, b.maxDelta = 0, 0
	running := 0
	for _, in := range b.instrs {
		o := ops[in.code]
		// Operands are consumed first (underflow point), then results pushed.
		if low := running - o.pops; low < b.minDelta {
			b.minDelta = low
		}
		running += o.push - o.pops
		if running > b.maxDelta {
			b.maxDelta = running
		}
	}
}

// ---- code generation -------------------------------------------------------

type genCtx struct {
	sb        strings.Builder
	name      string
	consts    map[string]string // hex value -> var name
	recips    map[string]bool   // const var names needing reciprocals
	constSeq  int
	bytecode  []byte
	dests     map[int]bool
	blockSet  map[int]bool // pcs that start blocks (for goto targets)
	unhandled map[string]int
	// inlinedStateful counts stateful sites emitted as direct helper calls
	// (codegen v2) instead of aotStep.
	inlinedStateful map[string]int
}

func (g *genCtx) constName(v *uint256.Int) string {
	hex := v.Hex()
	if n, ok := g.consts[hex]; ok {
		return n
	}
	n := fmt.Sprintf("aotC%s_%d", g.name, g.constSeq)
	g.constSeq++
	g.consts[hex] = n
	return n
}

func (g *genCtx) w(format string, args ...any) {
	fmt.Fprintf(&g.sb, format, args...)
	g.sb.WriteByte('\n')
}

// slotVal is the generation-time knowledge about one stack slot: a known
// constant value, and whether it is still pending (known but not yet written
// to the physical stack array — lazy PUSH materialization).
type slotVal struct {
	v       *uint256.Int
	pending bool
}

// constTracker tracks generation-time-known stack values within a block.
// Keys are running-delta slot positions (relative to block entry height).
// Invariant: a slot absent from the map is unknown AND physical (the runtime
// value is present in the stack array). A pending slot's physical cell holds
// garbage and must be materialized before any runtime consumer reads it.
type constTracker struct {
	known map[int]slotVal
}

func newConstTracker() *constTracker { return &constTracker{known: make(map[int]slotVal)} }
func (c *constTracker) get(slot int) *uint256.Int {
	return c.known[slot].v
}
func (c *constTracker) pendingAt(slot int) bool {
	return c.known[slot].pending
}

// set records a known, already-materialized value (nil clears the slot).
func (c *constTracker) set(slot int, v *uint256.Int) {
	if v == nil {
		delete(c.known, slot)
	} else {
		c.known[slot] = slotVal{v: v}
	}
}

// setPending records a known value that has NOT been written to the stack.
func (c *constTracker) setPending(slot int, v *uint256.Int) {
	c.known[slot] = slotVal{v: v, pending: true}
}

// setState installs a full slot state (deleting the unknown/physical state
// to keep the map minimal).
func (c *constTracker) setState(slot int, sv slotVal) {
	if sv.v == nil && !sv.pending {
		delete(c.known, slot)
	} else {
		c.known[slot] = sv
	}
}

// foldOp computes the result of a pure inlined op whose operands are all
// generation-time constants, mirroring the EVM (and the emitted Go) exactly.
// x is the top operand, y the second, z the third. Returns nil if the op is
// not foldable.
func foldOp(code byte, x, y, z *uint256.Int) *uint256.Int {
	r := new(uint256.Int)
	switch ops[code].name {
	case "ADD":
		return r.Add(x, y)
	case "MUL":
		return r.Mul(x, y)
	case "SUB":
		return r.Sub(x, y)
	case "DIV":
		return r.Div(x, y)
	case "SDIV":
		return r.SDiv(x, y)
	case "MOD":
		return r.Mod(x, y)
	case "SMOD":
		return r.SMod(x, y)
	case "ADDMOD":
		return r.AddMod(x, y, z)
	case "MULMOD":
		return r.MulMod(x, y, z)
	case "SIGNEXTEND":
		return r.ExtendSign(y, x)
	case "LT":
		return boolToU256(r, x.Lt(y))
	case "GT":
		return boolToU256(r, x.Gt(y))
	case "SLT":
		return boolToU256(r, x.Slt(y))
	case "SGT":
		return boolToU256(r, x.Sgt(y))
	case "EQ":
		return boolToU256(r, x.Eq(y))
	case "ISZERO":
		return boolToU256(r, x.IsZero())
	case "AND":
		return r.And(x, y)
	case "OR":
		return r.Or(x, y)
	case "XOR":
		return r.Xor(x, y)
	case "NOT":
		return r.Not(x)
	case "BYTE":
		return r.Set(y).Byte(x)
	case "SHL":
		if x.LtUint64(256) {
			return r.Lsh(y, uint(x.Uint64()))
		}
		return r.Clear()
	case "SHR":
		if x.LtUint64(256) {
			return r.Rsh(y, uint(x.Uint64()))
		}
		return r.Clear()
	case "SAR":
		if x.GtUint64(255) {
			if y.Sign() >= 0 {
				return r.Clear()
			}
			return r.SetAllOne()
		}
		return r.SRsh(y, uint(x.Uint64()))
	}
	return nil
}

func boolToU256(r *uint256.Int, b bool) *uint256.Int {
	if b {
		return r.SetOne()
	}
	return r.Clear()
}

// emitBlock emits one basic block and reports whether control can fall
// through past its end (used to skip dead unlabeled successor blocks).
func emitBlock(g *genCtx, b *block, isLast bool) bool {
	// Labels are only legal if referenced: pc 0 (entry goto) and JUMPDEST
	// leaders (dispatch switch). Fallthrough-only leaders get a comment.
	if b.start == 0 || g.dests[b.start] {
		g.w("L%d: // block @%d (%d instrs)", b.start, b.start, len(b.instrs))
	} else {
		g.w("\t// block @%d (%d instrs, fallthrough-only)", b.start, len(b.instrs))
	}
	req := -b.minDelta
	if req > 0 {
		g.w("\tif sp < %d { return nil, &ErrStackUnderflow{stackLen: sp, required: %d} }", req, req)
	}
	if b.maxDelta > 0 {
		g.w("\tif sp+%d > 1024 { return nil, &ErrStackOverflow{stackLen: sp, limit: 1024} }", b.maxDelta)
	}

	tr := newConstTracker()
	delta := 0
	pendingGas := uint64(0)

	flushGas := func() {
		if pendingGas == 0 {
			return
		}
		g.w("\tif contract.Gas < %d { return nil, ErrOutOfGas }", pendingGas)
		g.w("\tcontract.Gas -= %d", pendingGas)
		pendingGas = 0
	}
	// slot returns the Go expression for the stack cell at running-delta d.
	slot := func(d int) string {
		if d >= 0 {
			return fmt.Sprintf("s[sp+%d]", d)
		}
		return fmt.Sprintf("s[sp-%d]", -d)
	}
	// emitPendingWrites writes every pending constant below `top` to its
	// physical stack cell WITHOUT changing tracker state (used inside
	// conditional branches, where the fallthrough path must stay lazy).
	emitPendingWrites := func(top int, indent string) {
		var keys []int
		for d, sv := range tr.known {
			if sv.pending && d < top {
				keys = append(keys, d)
			}
		}
		sort.Ints(keys)
		for _, d := range keys {
			v := tr.known[d].v
			if v.IsUint64() {
				g.w("%s%s.SetUint64(%d)", indent, slot(d), v.Uint64())
			} else {
				g.w("%s%s.Set(&%s)", indent, slot(d), g.constName(v))
			}
		}
	}
	// materializeLive writes and un-pends every pending slot below `top`.
	// Required before any runtime consumer of the physical stack region:
	// generic aotStep sites, block exits, and dynamic dispatch.
	materializeLive := func(top int) {
		emitPendingWrites(top, "\t")
		for d, sv := range tr.known {
			if sv.pending && d < top {
				tr.known[d] = slotVal{v: sv.v}
			}
		}
	}
	// materialize writes and un-pends a single slot (helper-call operands).
	materialize := func(d int) {
		if sv := tr.known[d]; sv.pending {
			if sv.v.IsUint64() {
				g.w("\t%s.SetUint64(%d)", slot(d), sv.v.Uint64())
			} else {
				g.w("\t%s.Set(&%s)", slot(d), g.constName(sv.v))
			}
			tr.known[d] = slotVal{v: sv.v}
		}
	}

	fallthroughEnd := true // whether control can reach the end of the block
	for _, in := range b.instrs {
		o := ops[in.code]
		// Inlined stateful ops (codegen v2): direct helper calls with exact
		// gas semantics instead of the generic aotStep dispatch. Constant gas
		// (fork-independent for these ops) is folded into the segment charge;
		// the helper charges dynamic gas itself.
		if o.mode == modeStep {
			// constOff reports a generation-time-known memory offset for which
			// off+length cannot wrap uint64 (the *C helper precondition).
			constOff := func(d int, length uint64) (uint64, bool) {
				v := tr.get(d)
				if v == nil || !v.IsUint64() || v.Uint64() > ^uint64(0)-length {
					return 0, false
				}
				return v.Uint64(), true
			}
			inlined := true
			switch in.code {
			case 0x51: // MLOAD: pops offset, pushes word, in place.
				pendingGas += 3 // GasFastestStep
				flushGas()
				if off, ok := constOff(delta-1, 32); ok {
					g.w("\tif err = aotMloadC(contract, mem, %d, &%s); err != nil { return nil, err }", off, slot(delta-1))
				} else {
					materialize(delta - 1)
					g.w("\tif err = aotMload(contract, mem, &%s); err != nil { return nil, err }", slot(delta-1))
				}
			case 0x52: // MSTORE: pops offset, value.
				pendingGas += 3
				flushGas()
				materialize(delta - 2)
				if off, ok := constOff(delta-1, 32); ok {
					g.w("\tif err = aotMstoreC(contract, mem, %d, &%s); err != nil { return nil, err }", off, slot(delta-2))
				} else {
					materialize(delta - 1)
					g.w("\tif err = aotMstore(contract, mem, &%s, &%s); err != nil { return nil, err }", slot(delta-1), slot(delta-2))
				}
			case 0x53: // MSTORE8: pops offset, value.
				pendingGas += 3
				flushGas()
				materialize(delta - 1)
				materialize(delta - 2)
				g.w("\tif err = aotMstore8(contract, mem, &%s, &%s); err != nil { return nil, err }", slot(delta-1), slot(delta-2))
			case 0x20: // KECCAK256: pops offset, size; result into size slot.
				pendingGas += 30 // params.Keccak256Gas
				flushGas()
				sizeV := tr.get(delta - 2)
				if off, ok := constOff(delta-1, 0); ok && sizeV != nil && sizeV.IsUint64() && sizeV.Uint64() <= ^uint64(0)-off {
					size := sizeV.Uint64()
					wordGas := (size + 31) / 32 * 6 // toWordSize(size) * params.Keccak256WordGas
					g.w("\tif err = aotKeccak256C(evm, contract, mem, %d, %d, %d, &%s); err != nil { return nil, err }", off, size, wordGas, slot(delta-2))
				} else {
					materialize(delta - 1)
					materialize(delta - 2)
					g.w("\tif err = aotKeccak256(evm, contract, mem, &%s, &%s); err != nil { return nil, err }", slot(delta-1), slot(delta-2))
				}
			case 0x54: // SLOAD: in place on the top slot; gas rule read from jt.
				flushGas()
				materialize(delta - 1)
				g.w("\tstack.top = sp + %d", delta)
				g.w("\tif err = aotSload(evm, contract, scope, jt); err != nil { return nil, err }")
			default:
				inlined = false
			}
			if inlined {
				g.inlinedStateful[o.name]++
				// Invalidate constant knowledge at and above the consumed slots.
				for s := range tr.known {
					if s >= delta-o.pops {
						tr.set(s, nil)
					}
				}
				delta += o.push - o.pops
				continue
			}
		}
		switch o.mode {
		case modeStep, modeTerm:
			flushGas()
			materializeLive(delta)
			g.w("\tstack.top = sp + %d", delta)
			g.w("\tpc = %d", in.pc)
			g.w("\tif res, err = aotStep(evm, contract, scope, jt, &pc); err != nil { return res, err }")
			g.unhandled[o.name]++
			if o.mode == modeTerm {
				// Terminators (RETURN/REVERT/INVALID/SELFDESTRUCT) always
				// return a non-nil error (errStopToken/ErrExecutionReverted/...)
				// from aotStep, so this is unreachable; keep the compiler happy.
				g.w("\treturn res, nil")
				fallthroughEnd = false
				goto doneBlock
			}
			// Invalidate constant knowledge at and above the consumed slots.
			for s := range tr.known {
				if s >= delta-o.pops {
					tr.set(s, nil)
				}
			}
			delta += o.push - o.pops
			continue
		}
		// inlined ops
		pendingGas += o.gas
		switch {
		case in.code == opSTOP:
			// Pending constants die with the frame; the stack is not read
			// after return (no tracers on the AOT path).
			flushGas()
			g.w("\treturn nil, errStopToken")
			fallthroughEnd = false
			goto doneBlock

		case in.code == opJUMPDEST:
			// gas only

		case in.code == opPUSH0:
			// Lazy: recorded in the tracker, written only if a runtime
			// consumer needs the physical slot.
			tr.setPending(delta, uint256.NewInt(0))
			delta++

		case in.code >= opPUSH1 && in.code <= opPUSH32:
			tr.setPending(delta, new(uint256.Int).SetBytes(in.imm))
			delta++

		case in.code >= opDUP1 && in.code <= opDUP16:
			n := int(in.code-opDUP1) + 1
			if src := delta - n; tr.pendingAt(src) {
				tr.setPending(delta, tr.get(src))
			} else {
				g.w("\t%s = %s", slot(delta), slot(delta-n))
				tr.set(delta, tr.get(delta-n))
			}
			delta++

		case in.code >= opSWAP1 && in.code <= opSWAP16:
			n := int(in.code-opSWAP1) + 1
			ai, bi := delta-1, delta-1-n
			sa, sb := tr.known[ai], tr.known[bi]
			switch {
			case sa.pending && sb.pending:
				// Both lazy: swap knowledge, no code.
			case sa.pending:
				// Old top is lazy; old deep value is physical in its cell and
				// must move to the top cell.
				g.w("\t%s = %s", slot(ai), slot(bi))
			case sb.pending:
				// Old deep value is lazy; old top is physical and must move
				// down to the deep cell.
				g.w("\t%s = %s", slot(bi), slot(ai))
			default:
				g.w("\t%s, %s = %s, %s", slot(ai), slot(bi), slot(bi), slot(ai))
			}
			tr.setState(ai, sb)
			tr.setState(bi, sa)

		case in.code == 0x50: // POP
			tr.set(delta-1, nil)
			delta--

		case in.code == opJUMP:
			flushGas()
			dest := tr.get(delta - 1)
			destSlot := slot(delta - 1)
			tr.set(delta-1, nil)
			delta--
			materializeLive(delta)
			// Read the destination BEFORE adjusting sp: destSlot is an
			// expression relative to the block-entry sp.
			if dest != nil {
				if dest.IsUint64() && g.dests[int(dest.Uint64())] {
					// Interrupt is polled on backward edges (any loop must
					// take one, or go through the dispatcher).
					if int(dest.Uint64()) <= in.pc {
						g.w("\tif interrupt.Load() { return nil, ErrInterrupt }")
					}
					if delta != 0 {
						g.w("\tsp += %d", delta)
					}
					g.w("\tgoto L%d", dest.Uint64())
				} else {
					g.w("\treturn nil, ErrInvalidJump")
				}
			} else {
				g.w("\tif !%s.IsUint64() { return nil, ErrInvalidJump }", destSlot)
				g.w("\tpc = %s.Uint64()", destSlot)
				g.w("\tsp += %d", delta)
				g.w("\tstack.top = sp")
				g.w("\tgoto dispatch")
			}
			fallthroughEnd = false
			goto doneBlock

		case in.code == opJUMPI:
			flushGas()
			dest := tr.get(delta - 1)
			condVal := tr.get(delta - 2)
			condSlot := slot(delta - 2)
			destSlot := slot(delta - 1)
			tr.set(delta-1, nil)
			tr.set(delta-2, nil)
			delta -= 2
			if condVal != nil {
				// Branch-folded JUMPI: the condition is a generation-time
				// constant. False: fall through, no code. True: exactly JUMP.
				if condVal.IsZero() {
					break
				}
				materializeLive(delta)
				if dest != nil {
					if dest.IsUint64() && g.dests[int(dest.Uint64())] {
						if int(dest.Uint64()) <= in.pc {
							g.w("\tif interrupt.Load() { return nil, ErrInterrupt }")
						}
						if delta != 0 {
							g.w("\tsp += %d", delta)
						}
						g.w("\tgoto L%d", dest.Uint64())
					} else {
						g.w("\treturn nil, ErrInvalidJump")
					}
				} else {
					g.w("\tif !%s.IsUint64() { return nil, ErrInvalidJump }", destSlot)
					g.w("\tpc = %s.Uint64()", destSlot)
					g.w("\tsp += %d", delta)
					g.w("\tstack.top = sp")
					g.w("\tgoto dispatch")
				}
				fallthroughEnd = false
				goto doneBlock
			}
			g.w("\tif !%s.IsZero() {", condSlot)
			// Pending constants are written only on the taken path; the
			// fallthrough path keeps them lazy (tracker state unchanged).
			emitPendingWrites(delta, "\t\t")
			if dest != nil {
				if dest.IsUint64() && g.dests[int(dest.Uint64())] {
					if int(dest.Uint64()) <= in.pc {
						g.w("\t\tif interrupt.Load() { return nil, ErrInterrupt }")
					}
					if delta != 0 {
						g.w("\t\tsp += %d", delta)
					}
					g.w("\t\tgoto L%d", dest.Uint64())
				} else {
					g.w("\t\treturn nil, ErrInvalidJump")
				}
			} else {
				g.w("\t\tif !%s.IsUint64() { return nil, ErrInvalidJump }", destSlot)
				g.w("\t\tpc = %s.Uint64()", destSlot)
				g.w("\t\tsp += %d", delta)
				g.w("\t\tstack.top = sp")
				g.w("\t\tgoto dispatch")
			}
			g.w("\t}")

		case in.code == opPC:
			tr.setPending(delta, uint256.NewInt(uint64(in.pc)))
			delta++

		case in.code == opMSIZE:
			g.w("\t%s.SetUint64(uint64(mem.Len()))", slot(delta))
			tr.set(delta, nil)
			delta++

		case in.code == opGAS:
			// GAS observes remaining gas: flush including its own cost first.
			flushGas()
			g.w("\t%s.SetUint64(contract.Gas)", slot(delta))
			tr.set(delta, nil)
			delta++

		case in.code == opMULMOD:
			xv, yv, mv := tr.get(delta-1), tr.get(delta-2), tr.get(delta-3)
			if xv != nil && yv != nil && mv != nil {
				// Fully constant: fold at generation time.
				r := foldOp(in.code, xv, yv, mv)
				tr.set(delta-1, nil)
				tr.set(delta-2, nil)
				tr.setState(delta-3, slotVal{v: r, pending: true})
				delta -= 2
				break
			}
			materialize(delta - 1)
			materialize(delta - 2)
			x, y, m := slot(delta-1), slot(delta-2), slot(delta-3)
			if mv != nil && !mv.IsZero() {
				// Constant modulus: precomputed reciprocal; the modulus slot
				// is only written (receiver), never read, so a pending
				// modulus needs no materialization.
				cn := g.constName(mv)
				g.recips[cn] = true
				g.w("\t%s.MulModWithReciprocal(&%s, &%s, &%s, &%sR)", m, x, y, cn, cn)
			} else {
				materialize(delta - 3)
				g.w("\t%s.MulMod(&%s, &%s, &%s)", m, x, y, m)
			}
			tr.set(delta-3, nil)
			tr.set(delta-2, nil)
			tr.set(delta-1, nil)
			delta -= 2

		default:
			// Constant folding: pure op with all operands known.
			if o.pops >= 1 && o.push == 1 {
				xv := tr.get(delta - 1)
				var yv, zv *uint256.Int
				if o.pops >= 2 {
					yv = tr.get(delta - 2)
				}
				if o.pops >= 3 {
					zv = tr.get(delta - 3)
				}
				known := xv != nil && (o.pops < 2 || yv != nil) && (o.pops < 3 || zv != nil)
				if known {
					if r := foldOp(in.code, xv, yv, zv); r != nil {
						for i := delta - o.pops; i < delta; i++ {
							tr.set(i, nil)
						}
						tr.setState(delta-o.pops, slotVal{v: r, pending: true})
						delta += o.push - o.pops
						break
					}
				}
				// Partial fold: shift by a known amount (very common in
				// Solidity masks) avoids materializing the shift operand
				// and the runtime range check.
				if (o.name == "SHL" || o.name == "SHR") && xv != nil && yv == nil {
					y := slot(delta - 2)
					if xv.LtUint64(256) {
						fn := "Lsh"
						if o.name == "SHR" {
							fn = "Rsh"
						}
						g.w("\t%s.%s(&%s, %d)", y, fn, y, xv.Uint64())
					} else {
						g.w("\t%s.Clear()", y)
					}
					tr.set(delta-1, nil)
					tr.set(delta-2, nil)
					delta--
					break
				}
			}
			for i := delta - o.pops; i < delta; i++ {
				materialize(i)
			}
			emitInlineOp(g, in.code, slot, delta, tr)
			delta += o.push - o.pops
		}
	}
	if fallthroughEnd {
		// Fall through to the next block: charge segment, write pending
		// constants, adjust sp.
		flushGas()
		materializeLive(delta)
		if delta != 0 {
			g.w("\tsp += %d", delta)
		}
		if isLast {
			// Running off the end of code == STOP.
			g.w("\treturn nil, errStopToken")
			fallthroughEnd = false
		}
	}
doneBlock:
	g.w("")
	return fallthroughEnd
}

// emitInlineOp emits the remaining simple inlined operations. delta is the
// running stack delta BEFORE the op executes.
func emitInlineOp(g *genCtx, code byte, slot func(int) string, delta int, tr *constTracker) {
	x := slot(delta - 1) // top
	y := slot(delta - 2)
	z := slot(delta - 3)
	o := ops[code]
	// Conservatively clear constant knowledge for consumed AND produced slots
	// (a pops=0/push=1 op writes slot(delta), which may hold stale knowledge).
	for i := delta - o.pops; i < delta+o.push; i++ {
		tr.set(i, nil)
	}
	switch o.name {
	case "ADD":
		g.w("\t%s.Add(&%s, &%s)", y, x, y)
	case "MUL":
		g.w("\t%s.Mul(&%s, &%s)", y, x, y)
	case "SUB":
		g.w("\t%s.Sub(&%s, &%s)", y, x, y)
	case "DIV":
		g.w("\t%s.Div(&%s, &%s)", y, x, y)
	case "SDIV":
		g.w("\t%s.SDiv(&%s, &%s)", y, x, y)
	case "MOD":
		g.w("\t%s.Mod(&%s, &%s)", y, x, y)
	case "SMOD":
		g.w("\t%s.SMod(&%s, &%s)", y, x, y)
	case "ADDMOD":
		g.w("\t%s.AddMod(&%s, &%s, &%s)", z, x, y, z)
	case "SIGNEXTEND":
		g.w("\t%s.ExtendSign(&%s, &%s)", y, y, x)
	case "NOT":
		g.w("\t%s.Not(&%s)", x, x)
	case "LT":
		g.w("\tif %s.Lt(&%s) { %s.SetOne() } else { %s.Clear() }", x, y, y, y)
	case "GT":
		g.w("\tif %s.Gt(&%s) { %s.SetOne() } else { %s.Clear() }", x, y, y, y)
	case "SLT":
		g.w("\tif %s.Slt(&%s) { %s.SetOne() } else { %s.Clear() }", x, y, y, y)
	case "SGT":
		g.w("\tif %s.Sgt(&%s) { %s.SetOne() } else { %s.Clear() }", x, y, y, y)
	case "EQ":
		g.w("\tif %s.Eq(&%s) { %s.SetOne() } else { %s.Clear() }", x, y, y, y)
	case "ISZERO":
		g.w("\tif %s.IsZero() { %s.SetOne() } else { %s.Clear() }", x, x, x)
	case "AND":
		g.w("\t%s.And(&%s, &%s)", y, x, y)
	case "OR":
		g.w("\t%s.Or(&%s, &%s)", y, x, y)
	case "XOR":
		g.w("\t%s.Xor(&%s, &%s)", y, x, y)
	case "BYTE":
		g.w("\t%s.Byte(&%s)", y, x)
	case "SHL":
		g.w("\tif %s.LtUint64(256) { %s.Lsh(&%s, uint(%s.Uint64())) } else { %s.Clear() }", x, y, y, x, y)
	case "SHR":
		g.w("\tif %s.LtUint64(256) { %s.Rsh(&%s, uint(%s.Uint64())) } else { %s.Clear() }", x, y, y, x, y)
	case "SAR":
		g.w("\tif %s.GtUint64(255) { if %s.Sign() >= 0 { %s.Clear() } else { %s.SetAllOne() } } else { %s.SRsh(&%s, uint(%s.Uint64())) }", x, y, y, y, y, y, x)
	case "ADDRESS":
		g.w("\t%s.SetBytes(contract.Address().Bytes())", slot(delta))
	case "CALLER":
		g.w("\t%s.SetBytes(contract.Caller().Bytes())", slot(delta))
	case "CALLVALUE":
		g.w("\t%s.Set(contract.Value())", slot(delta))
	case "CALLDATALOAD":
		g.w("\tif off, over := %s.Uint64WithOverflow(); !over {", x)
		g.w("\t\t%s.SetBytes(getData(contract.Input, off, 32))", x)
		g.w("\t} else { %s.Clear() }", x)
	case "CALLDATASIZE":
		g.w("\t%s.SetUint64(uint64(len(contract.Input)))", slot(delta))
	case "CODESIZE":
		g.w("\t%s.SetUint64(uint64(len(contract.Code)))", slot(delta))
	case "RETURNDATASIZE":
		g.w("\t%s.SetUint64(uint64(len(evm.returnData)))", slot(delta))
	default:
		panic(fmt.Sprintf("no inline template for %s", o.name))
	}
}

func main() {
	hexPath := flag.String("hex", "", "path to runtime bytecode hex file (0x-prefixed or raw)")
	name := flag.String("name", "Contract", "Go-identifier-friendly contract name")
	outPath := flag.String("out", "", "output .go file (package vm)")
	flag.Parse()
	if *hexPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: evm2go -hex code.hex -name Name -out core/vm/aot_gen_name.go")
		os.Exit(1)
	}
	raw, err := os.ReadFile(*hexPath)
	if err != nil {
		panic(err)
	}
	hexStr := strings.TrimSpace(string(raw))
	hexStr = strings.TrimPrefix(hexStr, "0x")
	bytecode := common.FromHex(hexStr)
	if len(bytecode) == 0 {
		panic("empty bytecode")
	}
	codeHash := crypto.Keccak256Hash(bytecode)

	instrs := decode(bytecode)
	dests := jumpdests(bytecode)
	blocks := splitBlocks(instrs, dests)
	for _, b := range blocks {
		analyze(b)
	}

	g := &genCtx{
		name:      *name,
		consts:    make(map[string]string),
		recips:    make(map[string]bool),
		bytecode:  bytecode,
		dests:     dests,
		blockSet:        make(map[int]bool),
		unhandled:       make(map[string]int),
		inlinedStateful: make(map[string]int),
	}
	for _, b := range blocks {
		g.blockSet[b.start] = true
	}

	// Emit function body into g.sb first (constants are discovered on the way).
	// Dead blocks — unlabeled (fallthrough-only) with a predecessor that
	// cannot fall through — are skipped entirely: they are unreachable and
	// would trip `go vet` unreachable-code checks.
	prevFall := true
	skipped := 0
	for i, b := range blocks {
		labeled := b.start == 0 || dests[b.start]
		if !labeled && !prevFall {
			prevFall = false
			skipped++
			continue
		}
		prevFall = emitBlock(g, b, i == len(blocks)-1)
	}
	if skipped > 0 {
		fmt.Printf("skipped %d dead blocks\n", skipped)
	}
	body := g.sb.String()

	var out strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&out, f+"\n", a...) }
	w("// Code generated by evm2go. DO NOT EDIT.")
	w("//")
	w("// Contract: %s", *name)
	w("// Code hash: %s", codeHash.Hex())
	w("// Blocks: %d, instructions: %d", len(blocks), len(instrs))
	w("")
	w("package vm")
	w("")
	w("import (")
	w("\t\"sync/atomic\"")
	w("")
	w("\t\"github.com/ethereum/go-ethereum/common\"")
	w("\t\"github.com/holiman/uint256\"")
	w(")")
	w("")
	w("// aotCodeHash%s is the code hash this transpiled body is valid for.", *name)
	w("var aotCodeHash%s = common.HexToHash(%q)", *name, codeHash.Hex())
	w("")
	// constants
	var hexes []string
	for h := range g.consts {
		hexes = append(hexes, h)
	}
	sort.Strings(hexes)
	if len(hexes) > 0 {
		w("var (")
		for _, h := range hexes {
			n := g.consts[h]
			w("\t%s = *uint256.MustFromHex(%q)", n, h)
			if g.recips[n] {
				w("\t%sR = uint256.Reciprocal(&%s)", n, n)
			}
		}
		w(")")
		w("")
	}
	w("func init() { aotRegister(aotCodeHash%s, aotExec%s) }", *name, *name)
	w("")
	w("//nolint:gocognit,maintidx")
	w("func aotExec%s(evm *EVM, contract *Contract, scope *ScopeContext, jt *JumpTable, interrupt *atomic.Bool) ([]byte, error) {", *name)
	w("\tvar (")
	w("\t\tstack = scope.Stack")
	w("\t\ts     = &stack.data")
	w("\t\tmem   = scope.Memory")
	w("\t\tsp    = stack.top")
	w("\t\tpc    uint64")
	w("\t\tres   []byte")
	w("\t\terr   error")
	w("\t)")
	w("\t_ = mem")
	w("\t_ = res")
	w("\t_ = err")
	w("\tgoto L0")
	w("")
	w("dispatch:")
	w("\t// Interrupt is polled here and on backward static edges: any loop")
	w("\t// must take one of the two.")
	w("\tif interrupt.Load() { return nil, ErrInterrupt }")
	w("\tsp = stack.top")
	w("\tswitch pc {")
	var dpcs []int
	for pc := range dests {
		dpcs = append(dpcs, pc)
	}
	sort.Ints(dpcs)
	for _, pc := range dpcs {
		if g.blockSet[pc] {
			w("\tcase %d:", pc)
			w("\t\tgoto L%d", pc)
		}
	}
	w("\tdefault:")
	w("\t\treturn nil, ErrInvalidJump")
	w("\t}")
	w("")
	out.WriteString(body)
	w("}")

	if err := os.WriteFile(*outPath, []byte(out.String()), 0o644); err != nil {
		panic(err)
	}
	// Report step-op usage so coverage is visible.
	var names []string
	for n := range g.unhandled {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Printf("generated %s: %d blocks, %d instrs, codehash %s\n", *outPath, len(blocks), len(instrs), codeHash.Hex())
	fmt.Printf("stepped ops: ")
	for _, n := range names {
		fmt.Printf("%s×%d ", n, g.unhandled[n])
	}
	fmt.Println()
	names = names[:0]
	for n := range g.inlinedStateful {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Printf("inlined stateful ops: ")
	for _, n := range names {
		fmt.Printf("%s×%d ", n, g.inlinedStateful[n])
	}
	fmt.Println()
}
