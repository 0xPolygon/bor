// Code generator for interpreter_dispatch.go — the EVM switch dispatch.
// Adapted from GEVM's gen/main.go (github.com/Giulio2002/gevm).
//
// GEVM reads opXXX function bodies from inst_*.go files and emits Run() methods
// with gas counter accumulation, stack checks, fork gates, and inlined bodies.
// It is parameterised by a `tracing` flag to produce both Run() and RunWithTracing().
//
// Bor adaptations from GEVM:
//   - Inline bodies in opDef.body instead of AST-parsed (incompatible function signatures)
//   - No fork gates (Bor's jumpTable handles fork activation)
//   - No Host interface / needsHost / needsOp (Bor accesses state directly)
//   - No tracing path / RunWithTracing / DebugGasTable (existing Run() handles tracing)
//   - No LOG emission (dynamic gas; handled by jumpTable fallback)
//   - Inlines hot opcodes; rest use jumpTable fallback
//   - Different gas constants (spec.GasVerylow → GasFastestStep, etc.)
//   - Return-based errors instead of Halt methods
//   - Different variable names (gas.remaining→contract.Gas, gasCounter→gasAccum)
//   - Different function signature (method on *EVM)
//
// Usage: go generate ./core/vm/
package main

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
)

// ---------------------------------------------------------------------------
// Opcode definition types
// ---------------------------------------------------------------------------

type gasMode int

const (
	modeAccumulate gasMode = iota // gas counter accumulated, flushed at control-flow boundaries and on error
	modeFlush                     // gas counter flushed before body
)

type shape int

const (
	shapeBinaryOp  shape = iota // pop 2, push 1: s.top < 2, s.top--
	shapeUnaryOp                // pop 1, push 1 in-place: s.top == 0
	shapeTernaryOp              // pop 3, push 1: s.top < 3, s.top -= 2
	shapePushVal                // push 1: s.top >= StackLimit, body does s.top++
	shapePop1                   // pop 1: s.top == 0, body does s.top--
	shapeCustom                 // body handles everything
)

type opDef struct {
	name     string  // "ADD", "SSTORE"
	gas      string  // "GasFastestStep", "params.JumpdestGas", "" (no static gas)
	mode     gasMode // Accumulate or Flush
	shape    shape
	funcName string // reference to opXXX in instructions.go
	body     string // inline Go code
}

// ---------------------------------------------------------------------------
// Long opcode bodies — extracted for readability
// ---------------------------------------------------------------------------

const bodyJump = `if stack.top < 1 {
return nil, &ErrStackUnderflow{stackLen: stack.top, required: 1}
}
stack.top--
pos := stack.data[stack.top]
if evm.abort.Load() {
return nil, errStopToken
}
if !contract.validJumpdest(&pos) {
return nil, ErrInvalidJump
}
pc = pos.Uint64()
continue`

const bodyJumpi = `if stack.top < 2 {
return nil, &ErrStackUnderflow{stackLen: stack.top, required: 2}
}
stack.top -= 2
pos := stack.data[stack.top+1]
cond := stack.data[stack.top]
if evm.abort.Load() {
return nil, errStopToken
}
if !cond.IsZero() {
if !contract.validJumpdest(&pos) {
return nil, ErrInvalidJump
}
pc = pos.Uint64()
continue
}`

const bodyPush1 = `pc++
if pc < codeLen {
stack.data[stack.top].SetUint64(uint64(code[pc]))
} else {
stack.data[stack.top].Clear()
}
stack.top++`

const bodyPush2 = `if pc+2 < codeLen {
stack.data[stack.top].SetBytes2(code[pc+1 : pc+3])
} else if pc+1 < codeLen {
stack.data[stack.top].SetUint64(uint64(code[pc+1]) << 8)
} else {
stack.data[stack.top].Clear()
}
pc += 2
stack.top++`

const bodyPush3 = `start := min(codeLen, pc+1)
end := min(codeLen, pc+4)
stack.data[stack.top].SetBytes(code[start:end])
if missing := 3 - int(end-start); missing > 0 {
stack.data[stack.top].Lsh(&stack.data[stack.top], uint(8*missing))
}
pc += 3
stack.top++`

const bodyPush4 = `start := min(codeLen, pc+1)
end := min(codeLen, pc+5)
stack.data[stack.top].SetBytes(code[start:end])
if missing := 4 - int(end-start); missing > 0 {
stack.data[stack.top].Lsh(&stack.data[stack.top], uint(8*missing))
}
pc += 4
stack.top++`

const bodySar = `shift := stack.data[stack.top]
value := &stack.data[stack.top-1]
if shift.GtUint64(256) {
if value.Sign() >= 0 { value.Clear() } else { value.SetAllOne() }
} else { value.SRsh(value, uint(shift.Uint64())) }`

// ---------------------------------------------------------------------------
// Opcode table — single source of truth for code generation
// ---------------------------------------------------------------------------
// Only hot-path opcodes are listed. All others fall through to the jumpTable
// in the default case.
//
// Gas constant mapping from GEVM:
//   spec.GasBase    (2) → GasQuickStep
//   spec.GasVerylow (3) → GasFastestStep
//   spec.GasLow     (5) → GasFastStep
//   spec.GasMid     (8) → GasMidStep
//   spec.GasHigh   (10) → GasSlowStep
//   spec.GasJumpdest(1) → params.JumpdestGas

var opcodes = []opDef{
	// === Arithmetic (0x00-0x0B) ===
	{name: "STOP", mode: modeFlush, shape: shapeCustom, funcName: "opStop",
		body: `return nil, errStopToken`},
	{name: "ADD", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opAdd",
		body: "y := &stack.data[stack.top-1]\ny.Add(&stack.data[stack.top], y)"},
	{name: "MUL", gas: "GasFastStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opMul",
		body: "y := &stack.data[stack.top-1]\ny.Mul(&stack.data[stack.top], y)"},
	{name: "SUB", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opSub",
		body: "y := &stack.data[stack.top-1]\ny.Sub(&stack.data[stack.top], y)"},
	{name: "DIV", gas: "GasFastStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opDiv",
		body: "y := &stack.data[stack.top-1]\ny.Div(&stack.data[stack.top], y)"},
	{name: "SDIV", gas: "GasFastStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opSdiv",
		body: "y := &stack.data[stack.top-1]\ny.SDiv(&stack.data[stack.top], y)"},
	{name: "MOD", gas: "GasFastStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opMod",
		body: "y := &stack.data[stack.top-1]\ny.Mod(&stack.data[stack.top], y)"},
	{name: "SMOD", gas: "GasFastStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opSmod",
		body: "y := &stack.data[stack.top-1]\ny.SMod(&stack.data[stack.top], y)"},
	{name: "ADDMOD", gas: "GasMidStep", mode: modeAccumulate, shape: shapeTernaryOp, funcName: "opAddmod",
		body: "x := stack.data[stack.top+1]\ny := stack.data[stack.top]\nz := &stack.data[stack.top-1]\nz.AddMod(&x, &y, z)"},
	{name: "MULMOD", gas: "GasMidStep", mode: modeAccumulate, shape: shapeTernaryOp, funcName: "opMulmod",
		body: "x := stack.data[stack.top+1]\ny := stack.data[stack.top]\nz := &stack.data[stack.top-1]\nz.MulMod(&x, &y, z)"},
	{name: "SIGNEXTEND", gas: "GasFastStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opSignextend",
		body: "back := stack.data[stack.top]\nnum := &stack.data[stack.top-1]\nnum.ExtendSign(num, &back)"},

	// === Comparison & Bitwise (0x10-0x1D) ===
	{name: "LT", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opLt",
		body: "x := stack.data[stack.top]\ny := &stack.data[stack.top-1]\nif x.Lt(y) { y.SetOne() } else { y.Clear() }"},
	{name: "GT", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opGt",
		body: "x := stack.data[stack.top]\ny := &stack.data[stack.top-1]\nif x.Gt(y) { y.SetOne() } else { y.Clear() }"},
	{name: "SLT", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opSlt",
		body: "x := stack.data[stack.top]\ny := &stack.data[stack.top-1]\nif x.Slt(y) { y.SetOne() } else { y.Clear() }"},
	{name: "SGT", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opSgt",
		body: "x := stack.data[stack.top]\ny := &stack.data[stack.top-1]\nif x.Sgt(y) { y.SetOne() } else { y.Clear() }"},
	{name: "EQ", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opEq",
		body: "x := stack.data[stack.top]\ny := &stack.data[stack.top-1]\nif x.Eq(y) { y.SetOne() } else { y.Clear() }"},
	{name: "ISZERO", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeUnaryOp, funcName: "opIszero",
		body: "x := &stack.data[stack.top-1]\nif x.IsZero() { x.SetOne() } else { x.Clear() }"},
	{name: "AND", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opAnd",
		body: "y := &stack.data[stack.top-1]\ny.And(&stack.data[stack.top], y)"},
	{name: "OR", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opOr",
		body: "y := &stack.data[stack.top-1]\ny.Or(&stack.data[stack.top], y)"},
	{name: "XOR", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opXor",
		body: "y := &stack.data[stack.top-1]\ny.Xor(&stack.data[stack.top], y)"},
	{name: "NOT", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeUnaryOp, funcName: "opNot",
		body: "x := &stack.data[stack.top-1]\nx.Not(x)"},
	{name: "BYTE", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opByte",
		body: "th := stack.data[stack.top]\nval := &stack.data[stack.top-1]\nval.Byte(&th)"},
	{name: "SHL", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opShl",
		body: "shift := stack.data[stack.top]\nvalue := &stack.data[stack.top-1]\nif shift.LtUint64(256) { value.Lsh(value, uint(shift.Uint64())) } else { value.Clear() }"},
	{name: "SHR", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opShr",
		body: "shift := stack.data[stack.top]\nvalue := &stack.data[stack.top-1]\nif shift.LtUint64(256) { value.Rsh(value, uint(shift.Uint64())) } else { value.Clear() }"},
	{name: "SAR", gas: "GasFastestStep", mode: modeAccumulate, shape: shapeBinaryOp, funcName: "opSar",
		body: bodySar},

	// === Stack/Memory/Storage (0x50-0x5E) ===
	{name: "POP", gas: "GasQuickStep", mode: modeAccumulate, shape: shapePop1, funcName: "opPop",
		body: `stack.top--`},
	{name: "JUMP", gas: "GasMidStep", mode: modeFlush, shape: shapeCustom, funcName: "opJump",
		body: bodyJump},
	{name: "JUMPI", gas: "GasSlowStep", mode: modeFlush, shape: shapeCustom, funcName: "opJumpi",
		body: bodyJumpi},
	{name: "PC", gas: "GasQuickStep", mode: modeAccumulate, shape: shapePushVal, funcName: "opPc",
		body: "stack.data[stack.top].SetUint64(pc)\nstack.top++"},
	{name: "MSIZE", gas: "GasQuickStep", mode: modeAccumulate, shape: shapePushVal, funcName: "opMsize",
		body: "stack.data[stack.top].SetUint64(uint64(mem.Len()))\nstack.top++"},
	{name: "JUMPDEST", gas: "params.JumpdestGas", mode: modeFlush, shape: shapeCustom}, // no body

	// === INVALID (0xFE) ===
	{name: "INVALID", mode: modeFlush, shape: shapeCustom, funcName: "opInvalid",
		body: `return nil, &ErrInvalidOpCode{opcode: INVALID}`},
}

// pushOps are handled separately from the main table with specialised inlining.
var pushOps = []opDef{
	{name: "PUSH0", gas: "GasQuickStep", funcName: "opPush0",
		body: "stack.data[stack.top].Clear()\nstack.top++"},
	{name: "PUSH1", gas: "GasFastestStep", funcName: "opPush1", body: bodyPush1},
	{name: "PUSH2", gas: "GasFastestStep", funcName: "opPush2", body: bodyPush2},
	{name: "PUSH3", gas: "GasFastestStep", funcName: "opPush3", body: bodyPush3},
	{name: "PUSH4", gas: "GasFastestStep", funcName: "opPush4", body: bodyPush4},
}

// ---------------------------------------------------------------------------
// Code emission
// ---------------------------------------------------------------------------

type emitter struct {
	buf *bytes.Buffer
}

func (e *emitter) p(format string, args ...any) {
	fmt.Fprintf(e.buf, format, args...)
}

// emitGas emits gas charging for a single opcode.
// In accumulator mode: gasAccum += expr.
func (e *emitter) emitGas(gasExpr string) {
	e.p("gasAccum += %s\n", gasExpr)
}

// emitFlush emits the gasAccum flush check + deduction.
func (e *emitter) emitFlush() {
	e.p("if contract.Gas < gasAccum {\n")
	e.p("return nil, ErrOutOfGas\n")
	e.p("}\n")
	e.p("contract.Gas -= gasAccum\n")
	e.p("gasAccum = 0\n")
}

// emitBody emits the inline body of an opcode.
func (e *emitter) emitBody(op opDef) {
	if op.body != "" {
		e.p("%s\n", op.body)
	}
}

// emitCase emits the complete case for an opcode.
func (e *emitter) emitCase(op opDef) {
	e.p("case %s:\n", op.name)

	if op.gas != "" {
		e.emitGas(op.gas)
	}

	if op.mode == modeFlush {
		e.emitFlushCase(op)
	} else {
		e.emitAccumulateCase(op)
	}
}

// emitFlushCase: flush gasAccum, then body.
func (e *emitter) emitFlushCase(op opDef) {
	e.emitFlush()
	e.emitBody(op)
}

// emitAccumulateCase: accumulate gas, then shape boilerplate.
func (e *emitter) emitAccumulateCase(op opDef) {
	e.emitShapedBody(op)
}

// emitShapedBody emits the stack check + body for shaped opcodes.
func (e *emitter) emitShapedBody(op opDef) {
	switch op.shape {
	case shapeBinaryOp:
		e.p("if stack.top < 2 {\n")
		e.p("return nil, &ErrStackUnderflow{stackLen: stack.top, required: 2}\n")
		e.p("}\n")
		e.p("stack.top--\n")
		e.emitBody(op)

	case shapeUnaryOp:
		e.p("if stack.top < 1 {\n")
		e.p("return nil, &ErrStackUnderflow{stackLen: stack.top, required: 1}\n")
		e.p("}\n")
		e.emitBody(op)

	case shapeTernaryOp:
		e.p("if stack.top < 3 {\n")
		e.p("return nil, &ErrStackUnderflow{stackLen: stack.top, required: 3}\n")
		e.p("}\n")
		e.p("stack.top -= 2\n")
		e.emitBody(op)

	case shapePushVal:
		e.p("if stack.top >= int(params.StackLimit) {\n")
		e.p("return nil, &ErrStackOverflow{stackLen: stack.top, limit: int(params.StackLimit) - 1}\n")
		e.p("}\n")
		e.emitBody(op)

	case shapePop1:
		e.p("if stack.top < 1 {\n")
		e.p("return nil, &ErrStackUnderflow{stackLen: stack.top, required: 1}\n")
		e.p("}\n")
		e.emitBody(op)

	case shapeCustom:
		e.emitBody(op)
	}
}

// emitDup emits a DUP<n> case (n=1..16).
func (e *emitter) emitDup(n int) {
	e.p("case DUP%d:\n", n)
	e.emitGas("GasFastestStep")
	e.p("if stack.top < %d || stack.top >= int(params.StackLimit) {\n", n)
	e.p("if stack.top < %d {\n", n)
	e.p("return nil, &ErrStackUnderflow{stackLen: stack.top, required: %d}\n", n)
	e.p("}\n")
	e.p("return nil, &ErrStackOverflow{stackLen: stack.top, limit: int(params.StackLimit) - 1}\n")
	e.p("}\n")
	e.p("stack.data[stack.top] = stack.data[stack.top-%d]\n", n)
	e.p("stack.top++\n")
}

// emitSwap emits a SWAP<n> case (n=1..16).
func (e *emitter) emitSwap(n int) {
	e.p("case SWAP%d:\n", n)
	e.emitGas("GasFastestStep")
	e.p("t := stack.top - 1\n")
	e.p("if t < %d {\n", n)
	e.p("return nil, &ErrStackUnderflow{stackLen: stack.top, required: %d}\n", n+1)
	e.p("}\n")
	e.p("stack.data[t], stack.data[t-%d] = stack.data[t-%d], stack.data[t]\n", n, n)
}

// emitPushGeneric emits the combined PUSH5-PUSH32 case.
func (e *emitter) emitPushGeneric() {
	e.p("case ")
	first := true
	for i := 5; i <= 32; i++ {
		if !first {
			e.p(", ")
		}
		e.p("PUSH%d", i)
		first = false
	}
	e.p(":\n")
	e.emitGas("GasFastestStep")
	e.p("if stack.top >= int(params.StackLimit) {\n")
	e.p("return nil, &ErrStackOverflow{stackLen: stack.top, limit: int(params.StackLimit) - 1}\n")
	e.p("}\n")
	e.p("n := uint64(op - byte(PUSH0))\n")
	e.p("start := min(codeLen, pc+1)\n")
	e.p("end := min(codeLen, pc+1+n)\n")
	e.p("stack.data[stack.top].SetBytes(code[start:end])\n")
	e.p("if missing := int(n) - int(end-start); missing > 0 {\n")
	e.p("stack.data[stack.top].Lsh(&stack.data[stack.top], uint(8*missing))\n")
	e.p("}\n")
	e.p("pc += n\n")
	e.p("stack.top++\n")
}

// emitPush emits a PUSH<n> case with specialized inline code.
func (e *emitter) emitPush(op opDef) {
	e.p("case %s:\n", op.name)
	e.emitGas(op.gas)
	e.p("if stack.top >= int(params.StackLimit) {\n")
	e.p("return nil, &ErrStackOverflow{stackLen: stack.top, limit: int(params.StackLimit) - 1}\n")
	e.p("}\n")
	e.emitBody(op)
}

// emitDefault emits the default case with jumpTable fallback.
// GEVM emits interp.Halt(OpcodeNotFound) here; Bor falls through to the
// jumpTable for all non-inlined opcodes (SLOAD, SSTORE, CALL, LOG, etc.).
func (e *emitter) emitDefault() {
	e.p("default:\n")
	e.emitFlush()
	e.p(`
operation := jumpTable[OpCode(op)]
if operation == nil || operation.undefined {
return nil, &ErrInvalidOpCode{opcode: OpCode(op)}
}

if sLen := stack.len(); sLen < operation.minStack {
return nil, &ErrStackUnderflow{stackLen: sLen, required: operation.minStack}
} else if sLen > operation.maxStack {
return nil, &ErrStackOverflow{stackLen: sLen, limit: operation.maxStack}
}

cost := operation.constantGas
if contract.Gas < cost {
return nil, ErrOutOfGas
}
contract.Gas -= cost

if operation.dynamicGas != nil {
var memorySize uint64
if operation.memorySize != nil {
memSize, overflow := operation.memorySize(stack)
if overflow {
return nil, ErrGasUintOverflow
}
var ovf bool
if memorySize, ovf = math.SafeMul(toWordSize(memSize), 32); ovf {
return nil, ErrGasUintOverflow
}
}
var dynamicCost uint64
dynamicCost, err = operation.dynamicGas(evm, contract, stack, mem, memorySize)
if err != nil {
`)
	e.p("return nil, fmt.Errorf(\"")
	e.buf.WriteString(`%w: %v`)
	e.p("\", ErrOutOfGas, err)\n")
	e.p(`}
if contract.Gas < dynamicCost {
return nil, ErrOutOfGas
}
contract.Gas -= dynamicCost
if memorySize > 0 {
mem.Resize(memorySize)
}
}

ret, err = operation.execute(&pc, evm, callContext)
if err != nil {
if err == errStopToken {
return ret, errStopToken
}
return ret, err
}
`)
}

// ---------------------------------------------------------------------------
// Shared case emission (used by emitRunFunc)
// ---------------------------------------------------------------------------

func (e *emitter) emitAllCases() {
	for _, op := range opcodes {
		e.emitCase(op)
	}
	for _, op := range pushOps {
		op.shape = shapePushVal
		op.mode = modeAccumulate
		e.emitPush(op)
	}
	e.emitPushGeneric()
	for i := 1; i <= 16; i++ {
		e.emitDup(i)
	}
	for i := 1; i <= 16; i++ {
		e.emitSwap(i)
	}
	e.emitDefault()
}

// ---------------------------------------------------------------------------
// runSwitch — fast path with gas accumulator, zero tracing overhead
// ---------------------------------------------------------------------------

func (e *emitter) emitRunFunc() {
	e.p(`// runSwitch is the fast-path EVM interpreter loop using a direct switch
// dispatch for hot opcodes. Inlines hot opcodes directly in the switch body
// (eliminating indirect function calls) and accumulates static gas costs in
// a local variable (eliminating per-opcode heap writes).
//
// This function is only called when no tracer is attached and EIP-4762
// (Verkle) is not active. The existing Run() loop handles those cases.
//
// gasAccum is flushed at control flow points (JUMP, JUMPI, JUMPDEST, STOP,
// INVALID) and the default fallback. Unflushed gasAccum on error is safe:
// non-REVERT errors consume all gas in EVM.Call; REVERT hits the default
// path which flushes first.
//
//nolint:gocognit
func (evm *EVM) runSwitch(
contract *Contract,
stack *Stack,
mem *Memory,
callContext *ScopeContext,
jumpTable *JumpTable,
interrupt *atomic.Bool,
) (ret []byte, err error) {
code := contract.Code
codeLen := uint64(len(code))
var pc uint64
var gasAccum uint64

for {
if interrupt.Load() {
opcodeCommitInterruptCounter.Inc(1)
return nil, ErrInterrupt
}

var op byte
if pc < codeLen {
op = code[pc]
}

switch OpCode(op) {
`)
	e.emitAllCases()
	e.p("}\n")    // switch
	e.p("pc++\n") // increment PC after each opcode
	e.p("}\n")    // for
	e.p("}\n")    // func
}

// ---------------------------------------------------------------------------
// Header
// ---------------------------------------------------------------------------

func (e *emitter) emitHeader() {
	e.p("// Code generated by gen_dispatch; DO NOT EDIT.\n\n")
	e.p("package vm\n\n")
	e.p("import (\n")
	e.p("\t\"fmt\"\n")
	e.p("\t\"sync/atomic\"\n\n")
	e.p("\t\"github.com/ethereum/go-ethereum/common/math\"\n")
	e.p("\t\"github.com/ethereum/go-ethereum/params\"\n")
	e.p("\t\"github.com/holiman/uint256\"\n")
	e.p(")\n\n")
	// Silence unused import warnings
	e.p("// Ensure imports are used.\n")
	e.p("var (\n")
	e.p("\t_ = fmt.Errorf\n")
	e.p("\t_ = math.SafeMul\n")
	e.p("\t_ = params.JumpdestGas\n")
	e.p("\t_ uint256.Int\n")
	e.p(")\n\n")
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	// When invoked via `go generate ./core/vm/`, the working directory is
	// core/vm/ (the package dir). GEVM uses filepath.Dir(os.Getwd()) because
	// its go:generate is in the gen/ subdirectory; ours is in the parent.
	vmDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot get working directory: %v\n", err)
		os.Exit(1)
	}

	var buf bytes.Buffer
	e := &emitter{buf: &buf}
	e.emitHeader()
	e.emitRunFunc()

	// Format with gofmt
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		debugPath := filepath.Join(vmDir, "interpreter_dispatch_debug.go")
		os.WriteFile(debugPath, buf.Bytes(), 0644)
		fmt.Fprintf(os.Stderr, "format error: %v\nwrote unformatted to %s\n", err, debugPath)
		os.Exit(1)
	}

	outPath := filepath.Join(vmDir, "interpreter_dispatch.go")
	if err := os.WriteFile(outPath, formatted, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "cannot write %s: %v\n", outPath, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d bytes)\n", outPath, len(formatted))
}
