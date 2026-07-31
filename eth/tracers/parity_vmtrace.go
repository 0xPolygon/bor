// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.

package tracers

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// vmTraceName is the registered tracer name backing the Parity/OpenEthereum
// "vmTrace" trace type.
const vmTraceName = "parityVmTracer"

func init() {
	DefaultDirectory.Register(vmTraceName, newParityVMTracer, false)
}

// vmTraceFrame is the Parity vmTrace object for a single call frame:
//
//	{"code": "0x<executed bytecode>", "ops": [op...]}
type vmTraceFrame struct {
	Code hexutil.Bytes `json:"code"`
	Ops  []*vmTraceOp  `json:"ops"`
}

// vmTraceOp is a single executed opcode entry within a frame.
type vmTraceOp struct {
	PC   uint64        `json:"pc"`
	Cost uint64        `json:"cost"`
	Op   string        `json:"op"`
	Idx  string        `json:"idx"`
	Ex   *vmTraceEx    `json:"ex"`
	Sub  *vmTraceFrame `json:"sub"`
}

// vmTraceEx captures the post-execution effect of an op: memory written, values
// pushed, storage written and gas remaining.
type vmTraceEx struct {
	Mem   *vmTraceMem `json:"mem"`
	Push  []string    `json:"push"`
	Store *vmTraceSto `json:"store"`
	Used  uint64      `json:"used"`
}

// vmTraceMem is a contiguous memory write: offset + written bytes.
type vmTraceMem struct {
	Off  int           `json:"off"`
	Data hexutil.Bytes `json:"data"`
}

// vmTraceSto is an SSTORE: the key/value written, as minimal hex quantities.
type vmTraceSto struct {
	Key string `json:"key"`
	Val string `json:"val"`
}

// vmTraceMemStackDepth is the deepest pre-op stack operand any mem-region spec
// reads (CALL/CALLCODE retLen is the 7th item from the top); see vmTraceMemSpecs.
const vmTraceMemStackDepth = 7

// vmTraceStackTail returns a copy of at most the top n stack items, preserving
// the bottom-first order. Top-relative operand positions (vmTraceStackArg) are
// unaffected because only the unread deeper items are dropped.
func vmTraceStackTail(stack []uint256.Int, n int) []uint256.Int {
	if len(stack) > n {
		stack = stack[len(stack)-n:]
	}
	return append([]uint256.Int{}, stack...)
}

// vmTracePending holds the still-open op of a frame whose post-op effects
// (push/mem) can only be observed at the NEXT OnOpcode (look-ahead).
type vmTracePending struct {
	op       *vmTraceOp
	opcode   vm.OpCode // the executed opcode (for push count / mem region)
	gasStart uint64
	// preStack is the top vmTraceMemStackDepth items of the pre-op stack
	// (bottom-first), enough for every mem-region operand without copying the
	// whole stack on each opcode.
	preStack []uint256.Int
	// retOff/retLen are the declared return-data region of a call op opened by this
	// op (retOff/retSize operands). erigon reports the FULL declared region from
	// post-call memory, zero-padded — not capped to the actual returned length.
}

// vmTraceState is the per-frame bookkeeping kept on a stack mirroring the EVM
// call stack.
type vmTraceState struct {
	frame   *vmTraceFrame
	prefix  string          // idx prefix for ops in this frame ("0", "0-160", ...)
	next    int             // next op index within this frame
	pending *vmTracePending // op awaiting finalization at the next step / exit
}

// parityVMTracer implements the Parity/OpenEthereum vmTrace (opcode-level trace).
type parityVMTracer struct {
	statedb    tracing.StateDB
	root       *vmTraceFrame
	stack      []*vmTraceState // index 0 == root frame
	rootPrefix string          // idx prefix of the root frame (tx index, or "" for trace_call)
	interrupt  atomic.Bool
	reason     error
}

// vmTraceJoinIdx builds an op idx from a frame prefix and op index. An empty
// prefix (trace_call's root) yields just the index ("0","1",...); otherwise
// "<prefix>-<i>" (e.g. "25-0" for tx index 25, "25-3-0" for a subcall).
func vmTraceJoinIdx(prefix string, i int) string {
	if prefix == "" {
		return strconv.Itoa(i)
	}
	return prefix + "-" + strconv.Itoa(i)
}

// newParityVMTracer constructs the vmTrace tracer. The root idx prefix is the
// transaction's index within its block (replay methods); for trace_call there is
// no transaction, so the prefix is empty.
func newParityVMTracer(ctx *Context, _ json.RawMessage, _ *params.ChainConfig) (*Tracer, error) {
	rootPrefix := ""
	if ctx != nil && ctx.TxHash != (common.Hash{}) {
		rootPrefix = strconv.Itoa(ctx.TxIndex)
	}
	t := &parityVMTracer{rootPrefix: rootPrefix}
	return &Tracer{
		Hooks: &tracing.Hooks{
			OnTxStart: t.OnTxStart,
			OnTxEnd:   t.OnTxEnd,
			OnEnter:   t.OnEnter,
			OnExit:    t.OnExit,
			OnOpcode:  t.OnOpcode,
		},
		GetResult: t.GetResult,
		Stop:      t.Stop,
	}, nil
}

// OnTxStart captures the state DB used to resolve frame code for call frames.
func (t *parityVMTracer) OnTxStart(env *tracing.VMContext, _ *types.Transaction, _ common.Address) {
	t.statedb = env.StateDB
}

func (t *parityVMTracer) OnTxEnd(_ *types.Receipt, _ error) {
	// no-op: parityVMTracer captures per-op state; receipt data is unused
}

// OnEnter is invoked when a new message (call/create) starts. The root frame is
// established at depth 0; deeper frames are pushed as the .sub of the parent's
// current pending op.
func (t *parityVMTracer) OnEnter(depth int, typ byte, _ common.Address, to common.Address, input []byte, _ uint64, _ *big.Int) {
	if t.interrupt.Load() {
		return
	}
	op := vm.OpCode(typ)

	frame := &vmTraceFrame{Code: hexutil.Bytes{}, Ops: []*vmTraceOp{}}
	switch op {
	case vm.CREATE, vm.CREATE2:
		frame.Code = append(hexutil.Bytes{}, input...)
	case vm.SELFDESTRUCT:
		// A SELFDESTRUCT enters a frame only to transfer the balance to the
		// beneficiary; no code executes there, so erigon reports empty code.
		// Leave frame.Code empty (do NOT resolve the beneficiary's code).
	default:
		if t.statedb != nil {
			frame.Code = append(hexutil.Bytes{}, t.statedb.GetCode(to)...)
		}
	}

	if depth == 0 || len(t.stack) == 0 {
		t.root = frame
		t.stack = []*vmTraceState{{frame: frame, prefix: t.rootPrefix}}
		return
	}

	parent := t.stack[len(t.stack)-1]
	prefix := t.rootPrefix
	if parent.pending != nil {
		// Attach the sub-frame to the call op that opened it.
		parent.pending.op.Sub = frame
		prefix = parent.pending.op.Idx
	}
	t.stack = append(t.stack, &vmTraceState{frame: frame, prefix: prefix})
}

// OnExit pops the current frame, finalizing its last pending op (no scope is
// available, so push/mem are empty — acceptable for terminal STOP/RETURN/REVERT).
func (t *parityVMTracer) OnExit(_ int, _ []byte, _ uint64, _ error, _ bool) {
	if t.interrupt.Load() {
		return
	}
	if len(t.stack) == 0 {
		return
	}
	cur := t.stack[len(t.stack)-1]
	if cur.pending != nil {
		t.finalizeNoScope(cur.pending)
		cur.pending = nil
	}
	t.stack = t.stack[:len(t.stack)-1]
}

// OnOpcode records a new op for the current frame and, via look-ahead, finalizes
// the previous pending op of that frame using the current (post-previous-op) scope.
func (t *parityVMTracer) OnOpcode(pc uint64, opcode byte, gas, cost uint64, scope tracing.OpContext, _ []byte, _ int, err error) {
	if t.interrupt.Load() {
		return
	}
	if len(t.stack) == 0 {
		return
	}
	cur := t.stack[len(t.stack)-1]

	// Finalize the previous op now that the scope reflects its result. This op's
	// gas == the gas remaining after the previous op (including gas returned by a
	// sub-call), which is exactly Parity's ex.used for that previous op.
	if cur.pending != nil {
		t.finalizeWithScope(cur.pending, scope, gas)
		cur.pending = nil
	}
	// A pre-execution fault belongs to the current opcode. The previous opcode
	// has now been finalized from this post-previous-op scope, but the failing
	// opcode itself must not be recorded as executed.
	if err != nil {
		return
	}

	op := vm.OpCode(opcode)
	entry := &vmTraceOp{
		PC:   pc,
		Cost: cost,
		Op:   op.String(),
		Idx:  vmTraceJoinIdx(cur.prefix, cur.next),
	}
	cur.next++
	cur.frame.Ops = append(cur.frame.Ops, entry)

	// SSTORE store value is available now (operands still on the stack).
	var store *vmTraceSto
	if op == vm.SSTORE {
		data := scope.StackData()
		if n := len(data); n >= 2 {
			store = &vmTraceSto{
				Key: hexutil.EncodeBig(data[n-1].ToBig()),
				Val: hexutil.EncodeBig(data[n-2].ToBig()),
			}
		}
	}

	// Pre-op snapshot: opcode (push count / mem region) + a copy of the stack
	// (memory-write operands). push/mem are finalized at the next OnOpcode.
	pre := &vmTracePending{
		op:       entry,
		opcode:   op,
		gasStart: gas,
		preStack: vmTraceStackTail(scope.StackData(), vmTraceMemStackDepth),
	}
	entry.Ex = &vmTraceEx{Push: []string{}, Used: gas - cost}
	if store != nil {
		entry.Ex.Store = store
	}
	cur.pending = pre
}

// finalizeWithScope completes an op's ex (push, mem, used) using the op's known
// stack-push count and memory-write region read against the post-op scope, and
// nextGas = the gas remaining after the op (this is Parity's ex.used, correct
// for calls where the callee returns leftover gas).
func (t *parityVMTracer) finalizeWithScope(p *vmTracePending, scope tracing.OpContext, nextGas uint64) {
	p.op.Ex.Used = nextGas
	// push = the op's pushed value(s), i.e. the top N items of the post-op stack.
	if n := vmTraceOpPushCount(p.opcode); n > 0 {
		curStack := scope.StackData()
		if n > len(curStack) {
			n = len(curStack)
		}
		push := make([]string, 0, n)
		for i := len(curStack) - n; i < len(curStack); i++ {
			push = append(push, hexutil.EncodeBig(curStack[i].ToBig()))
		}
		p.op.Ex.Push = push
	}
	// mem = the region the op touched, read from post-op memory. erigon reports the
	// FULL declared region (MSTORE/MLOAD = 32, COPY/CALL = declared length) and
	// zero-pads when memory is shorter — it does NOT cap a call to the actually
	// returned byte count. Mirror that with a zero-padded copy.
	if off, size, writes := vmTraceMemRegion(p.opcode, p.preStack); writes {
		mem := scope.MemoryData()
		data := make([]byte, size)
		if off < uint64(len(mem)) {
			end := off + size
			if end > uint64(len(mem)) {
				end = uint64(len(mem))
			}
			copy(data, mem[off:end])
		}
		p.op.Ex.Mem = &vmTraceMem{Off: int(off), Data: data}
	}
}

// finalizeNoScope completes the last op of a frame at OnExit, where no scope is
// available. Terminal ops push nothing and write no memory, so leaving push/mem
// empty is correct.
func (t *parityVMTracer) finalizeNoScope(p *vmTracePending) {
	if p.op.Ex == nil {
		p.op.Ex = &vmTraceEx{Push: []string{}, Used: p.gasStart - p.op.Cost}
	}
}

// vmTraceOpPushCount returns how many words the opcode pushes onto the stack
// (the count Parity's vmTrace reports in "push": the items on top of the post-op
// stack). Derived from the core/vm jump-table push counts.
func vmTraceOpPushCount(op vm.OpCode) int {
	switch op {
	case vm.ADD, vm.MUL, vm.SUB, vm.DIV, vm.SDIV, vm.MOD, vm.SMOD,
		vm.ADDMOD, vm.MULMOD, vm.EXP, vm.SIGNEXTEND:
		return 1
	case vm.LT, vm.GT, vm.SLT, vm.SGT, vm.EQ, vm.ISZERO,
		vm.AND, vm.OR, vm.XOR, vm.NOT, vm.BYTE,
		vm.SHL, vm.SHR, vm.SAR:
		return 1
	case vm.KECCAK256:
		return 1
	case vm.ADDRESS, vm.BALANCE, vm.ORIGIN, vm.CALLER, vm.CALLVALUE,
		vm.CALLDATALOAD, vm.CALLDATASIZE, vm.CODESIZE, vm.GASPRICE,
		vm.EXTCODESIZE, vm.RETURNDATASIZE, vm.EXTCODEHASH:
		return 1
	case vm.BLOCKHASH, vm.COINBASE, vm.TIMESTAMP, vm.NUMBER,
		vm.DIFFICULTY, vm.GASLIMIT,
		vm.CHAINID, vm.SELFBALANCE, vm.BASEFEE:
		return 1
	case vm.MLOAD, vm.SLOAD, vm.TLOAD, vm.PC, vm.MSIZE, vm.GAS:
		return 1
	case vm.CREATE, vm.CREATE2, vm.CALL, vm.CALLCODE,
		vm.DELEGATECALL, vm.STATICCALL:
		return 1
	case vm.STOP, vm.POP, vm.MSTORE, vm.MSTORE8, vm.SSTORE, vm.TSTORE,
		vm.JUMP, vm.JUMPI, vm.JUMPDEST, vm.MCOPY,
		vm.RETURN, vm.REVERT, vm.INVALID, vm.SELFDESTRUCT:
		return 0
	case vm.CALLDATACOPY, vm.CODECOPY, vm.EXTCODECOPY, vm.RETURNDATACOPY:
		return 0
	}
	switch {
	case op >= vm.PUSH0 && op <= vm.PUSH32:
		return 1
	// Parity/OpenEthereum reports the top `ret` items, and for DUPn/SWAPn ret = n+1
	// (DUP1 -> 2 copies of the value, SWAP1 -> the 2 swapped values, etc.), not the
	// net stack growth.
	case op >= vm.DUP1 && op <= vm.DUP16:
		return int(op-vm.DUP1) + 2
	case op >= vm.SWAP1 && op <= vm.SWAP16:
		return int(op-vm.SWAP1) + 2
	case op >= vm.LOG0 && op <= vm.LOG4:
		return 0
	}
	return 0
}

// vmTraceMemSpec describes where an opcode's written memory region lives on the
// pre-op stack: offArg/lenArg are 1-based positions from the top holding the
// offset and length operands; fixedLen != 0 means the region length is constant
// (word/byte stores) and lenArg is unused. Stack arg order verified against
// core/vm/instructions.go.
//
// NB: MCOPY is intentionally excluded — erigon's OeTracer does not record a
// mem region for MCOPY (it is absent from its setMem switch), so it must stay
// out here to match.
type vmTraceMemSpec struct {
	offArg   int
	lenArg   int
	fixedLen uint64
}

var vmTraceMemSpecs = map[vm.OpCode]vmTraceMemSpec{
	// MSTORE writes and MLOAD reads a 32-byte word at offset = stack top; Parity
	// reports the touched memory region for both.
	vm.MSTORE:  {offArg: 1, fixedLen: 32},
	vm.MLOAD:   {offArg: 1, fixedLen: 32},
	vm.MSTORE8: {offArg: 1, fixedLen: 1},
	// args: destOff, srcOff, len
	vm.CALLDATACOPY:   {offArg: 1, lenArg: 3},
	vm.CODECOPY:       {offArg: 1, lenArg: 3},
	vm.RETURNDATACOPY: {offArg: 1, lenArg: 3},
	// args: addr, destOff, srcOff, len
	vm.EXTCODECOPY: {offArg: 2, lenArg: 4},
	// args: gas, addr, value, argsOff, argsLen, retOff, retLen
	vm.CALL:     {offArg: 6, lenArg: 7},
	vm.CALLCODE: {offArg: 6, lenArg: 7},
	// args: gas, addr, argsOff, argsLen, retOff, retLen
	vm.DELEGATECALL: {offArg: 5, lenArg: 6},
	vm.STATICCALL:   {offArg: 5, lenArg: 6},
}

// vmTraceStackArg returns the n-th stack operand from the top (1-based) as a
// uint64, with ok=false when absent or out of uint64 range.
func vmTraceStackArg(stack []uint256.Int, n int) (uint64, bool) {
	idx := len(stack) - n
	if idx < 0 {
		return 0, false
	}
	val := stack[idx]
	if !val.IsUint64() {
		return 0, false
	}
	return val.Uint64(), true
}

// vmTraceMemRegion returns the memory region [off, off+size) written by the
// opcode, derived from the pre-op stack (bottom-first, as scope.StackData()).
// writes=false if the opcode doesn't write memory or the length is zero.
func vmTraceMemRegion(op vm.OpCode, stack []uint256.Int) (off uint64, size uint64, writes bool) {
	spec, ok := vmTraceMemSpecs[op]
	if !ok {
		return 0, 0, false
	}
	o, ok := vmTraceStackArg(stack, spec.offArg)
	if !ok {
		return 0, 0, false
	}
	if spec.fixedLen != 0 {
		return o, spec.fixedLen, true
	}
	l, ok := vmTraceStackArg(stack, spec.lenArg)
	if !ok || l == 0 {
		return 0, 0, false
	}
	return o, l, true
}

// GetResult marshals the root frame as the vmTrace object. For a plain value
// transfer to an EOA (no code, no ops) it yields {"code":"0x","ops":[]}.
func (t *parityVMTracer) GetResult() (json.RawMessage, error) {
	frame := t.root
	if frame == nil {
		frame = &vmTraceFrame{Code: hexutil.Bytes{}, Ops: []*vmTraceOp{}}
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("marshal vmTrace: %w", err)
	}
	return raw, t.reason
}

// Stop terminates tracing at the next opportunity.
func (t *parityVMTracer) Stop(err error) {
	t.reason = err
	t.interrupt.Store(true)
}

// parityVMTraceFor executes the message with the vmTrace tracer and returns the
// raw {code, ops} object.
//
// preState MUST be a pre-execution copy of the state (e.g. statedb.Copy()): the
// tracer re-executes the message and advances the state it is given.
func (api *API) parityVMTraceFor(ctx context.Context, in parityExecInput) (json.RawMessage, error) {
	cfg := parityPhaseConfig(vmTraceName, nil, in.config)

	res, _, err := api.traceTx(ctx, in.tx, in.msg, in.txctx, in.vmctx, in.statedb, cfg, nil)
	if err != nil {
		return nil, err
	}

	raw, ok := res.(json.RawMessage)
	if !ok {
		if raw, err = json.Marshal(res); err != nil {
			return nil, fmt.Errorf("marshal vmTrace result: %w", err)
		}
	}
	return raw, nil
}
