// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.

package tracers

import (
	"bytes"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
)

func TestVMTraceStackArg(t *testing.T) {
	t.Parallel()

	stack := []uint256.Int{*uint256.NewInt(10), *uint256.NewInt(20), *uint256.NewInt(30)}

	tests := []struct {
		name   string
		stack  []uint256.Int
		n      int
		want   uint64
		wantOk bool
	}{
		{name: "top", stack: stack, n: 1, want: 30, wantOk: true},
		{name: "second", stack: stack, n: 2, want: 20, wantOk: true},
		{name: "bottom", stack: stack, n: 3, want: 10, wantOk: true},
		{name: "out of range", stack: stack, n: 4, wantOk: false},
		{name: "empty stack", stack: nil, n: 1, wantOk: false},
		{name: "over uint64", stack: []uint256.Int{*new(uint256.Int).Lsh(uint256.NewInt(1), 64)}, n: 1, wantOk: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := vmTraceStackArg(tc.stack, tc.n)
			if ok != tc.wantOk {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOk)
			}
			if ok && got != tc.want {
				t.Errorf("value = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestVMTraceMemRegion(t *testing.T) {
	t.Parallel()

	// Build a bottom-first stack whose top-N operands are the given values
	// (values[0] is the top of the stack).
	stackOf := func(topFirst ...uint64) []uint256.Int {
		s := make([]uint256.Int, 0, len(topFirst))
		for i := len(topFirst) - 1; i >= 0; i-- {
			s = append(s, *uint256.NewInt(topFirst[i]))
		}
		return s
	}

	tests := []struct {
		name       string
		op         vm.OpCode
		stack      []uint256.Int
		wantOff    uint64
		wantSize   uint64
		wantWrites bool
	}{
		{name: "MSTORE 32-byte word", op: vm.MSTORE, stack: stackOf(0x40, 7), wantOff: 0x40, wantSize: 32, wantWrites: true},
		{name: "MLOAD reports touched region", op: vm.MLOAD, stack: stackOf(0x20), wantOff: 0x20, wantSize: 32, wantWrites: true},
		{name: "MSTORE8 single byte", op: vm.MSTORE8, stack: stackOf(0x1f, 0xff), wantOff: 0x1f, wantSize: 1, wantWrites: true},
		{name: "CALLDATACOPY destOff/len", op: vm.CALLDATACOPY, stack: stackOf(0x80, 0x10, 0x33), wantOff: 0x80, wantSize: 0x33, wantWrites: true},
		{name: "CODECOPY destOff/len", op: vm.CODECOPY, stack: stackOf(0x00, 0x04, 0x20), wantOff: 0x00, wantSize: 0x20, wantWrites: true},
		{name: "RETURNDATACOPY destOff/len", op: vm.RETURNDATACOPY, stack: stackOf(0x60, 0x00, 0x40), wantOff: 0x60, wantSize: 0x40, wantWrites: true},
		{name: "EXTCODECOPY addr/destOff/srcOff/len", op: vm.EXTCODECOPY, stack: stackOf(0xaa, 0x100, 0x00, 0x08), wantOff: 0x100, wantSize: 0x08, wantWrites: true},
		{name: "CALL retOff/retLen", op: vm.CALL, stack: stackOf(5000, 0xbb, 0, 0x20, 0x04, 0x200, 0x40), wantOff: 0x200, wantSize: 0x40, wantWrites: true},
		{name: "CALLCODE retOff/retLen", op: vm.CALLCODE, stack: stackOf(5000, 0xbb, 0, 0x20, 0x04, 0x180, 0x20), wantOff: 0x180, wantSize: 0x20, wantWrites: true},
		{name: "DELEGATECALL retOff/retLen", op: vm.DELEGATECALL, stack: stackOf(5000, 0xbb, 0x20, 0x04, 0x240, 0x10), wantOff: 0x240, wantSize: 0x10, wantWrites: true},
		{name: "STATICCALL retOff/retLen", op: vm.STATICCALL, stack: stackOf(5000, 0xbb, 0x20, 0x04, 0x2c0, 0x08), wantOff: 0x2c0, wantSize: 0x08, wantWrites: true},
		{name: "zero length copy omitted", op: vm.CALLDATACOPY, stack: stackOf(0x80, 0x10, 0x00), wantWrites: false},
		{name: "zero length call ret omitted", op: vm.CALL, stack: stackOf(5000, 0xbb, 0, 0x20, 0x04, 0x200, 0x00), wantWrites: false},
		{name: "non-writing opcode", op: vm.ADD, stack: stackOf(1, 2), wantWrites: false},
		// MCOPY must stay excluded: erigon's OeTracer records no mem region for it.
		{name: "MCOPY excluded", op: vm.MCOPY, stack: stackOf(0x40, 0x00, 0x20), wantWrites: false},
		{name: "short stack MSTORE", op: vm.MSTORE, stack: nil, wantWrites: false},
		{name: "short stack CALL", op: vm.CALL, stack: stackOf(5000, 0xbb), wantWrites: false},
		{name: "offset over uint64", op: vm.MSTORE, stack: []uint256.Int{*new(uint256.Int).Lsh(uint256.NewInt(1), 80)}, wantWrites: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			off, size, writes := vmTraceMemRegion(tc.op, tc.stack)
			if writes != tc.wantWrites {
				t.Fatalf("writes = %v, want %v", writes, tc.wantWrites)
			}
			if !writes {
				if off != 0 || size != 0 {
					t.Errorf("non-writing op returned off=%d size=%d, want 0,0", off, size)
				}
				return
			}
			if off != tc.wantOff || size != tc.wantSize {
				t.Errorf("region = [%#x, +%#x), want [%#x, +%#x)", off, size, tc.wantOff, tc.wantSize)
			}
		})
	}
}

func TestVMTraceOpPushCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		op   vm.OpCode
		want int
	}{
		{vm.ADD, 1},
		{vm.KECCAK256, 1},
		{vm.BALANCE, 1},
		{vm.BASEFEE, 1},
		{vm.MLOAD, 1},
		{vm.SLOAD, 1},
		{vm.CALL, 1},
		{vm.CREATE2, 1},
		{vm.STOP, 0},
		{vm.MSTORE, 0},
		{vm.SSTORE, 0},
		{vm.RETURN, 0},
		{vm.CALLDATACOPY, 0},
		{vm.PUSH0, 1},
		{vm.PUSH1, 1},
		{vm.PUSH32, 1},
		// Parity reports ret = n+1 items for DUPn/SWAPn, not the net growth.
		{vm.DUP1, 2},
		{vm.DUP16, 17},
		{vm.SWAP1, 2},
		{vm.SWAP16, 17},
		{vm.LOG0, 0},
		{vm.LOG4, 0},
	}
	for _, tc := range tests {
		if got := vmTraceOpPushCount(tc.op); got != tc.want {
			t.Errorf("vmTraceOpPushCount(%s) = %d, want %d", tc.op, got, tc.want)
		}
	}
}

// TestParityVMTracerInterrupt asserts Stop halts recording: hooks become
// no-ops and GetResult surfaces the stop reason.
func TestParityVMTracerInterrupt(t *testing.T) {
	t.Parallel()

	tr := &parityVMTracer{}
	tr.OnEnter(0, byte(vm.CALL), [20]byte{}, [20]byte{}, nil, 0, nil)
	if tr.root == nil || len(tr.stack) != 1 {
		t.Fatalf("root frame not established: root=%v stack=%d", tr.root, len(tr.stack))
	}

	stopErr := errors.New("interrupted")
	tr.Stop(stopErr)

	// Post-stop hook invocations must not record anything.
	tr.OnOpcode(0, byte(vm.PUSH1), 100, 3, nil, nil, 0, nil)
	tr.OnEnter(1, byte(vm.CALL), [20]byte{}, [20]byte{}, nil, 0, nil)
	tr.OnExit(0, nil, 0, nil, false)
	if got := len(tr.root.Ops); got != 0 {
		t.Errorf("ops recorded after Stop: %d", got)
	}
	if got := len(tr.stack); got != 1 {
		t.Errorf("frame pushed after Stop: stack=%d", got)
	}

	if _, err := tr.GetResult(); !errors.Is(err, stopErr) {
		t.Errorf("GetResult error = %v, want %v", err, stopErr)
	}
}

func TestParityVMTracerDefensivePaths(t *testing.T) {
	t.Parallel()

	tr := &parityVMTracer{}
	// Hooks before a root frame exists must be harmless.
	tr.OnOpcode(0, byte(vm.STOP), 0, 0, nil, nil, 0, nil)

	createCode := []byte{byte(vm.PUSH0), byte(vm.STOP)}
	tr.OnEnter(0, byte(vm.CREATE), common.Address{}, common.Address{}, createCode, 0, nil)
	if !bytes.Equal(tr.root.Code, createCode) {
		t.Errorf("create frame code = %x, want %x", tr.root.Code, createCode)
	}

	pending := &vmTracePending{op: &vmTraceOp{Cost: 3}, gasStart: 10}
	tr.finalizeNoScope(pending)
	if pending.op.Ex == nil || pending.op.Ex.Used != 7 {
		t.Errorf("finalized op = %+v, want used=7", pending.op.Ex)
	}

	empty := &parityVMTracer{}
	raw, err := empty.GetResult()
	if err != nil {
		t.Fatalf("empty result: %v", err)
	}
	if string(raw) != `{"code":"0x","ops":[]}` {
		t.Errorf("empty result = %s", raw)
	}
}

// TestParityVMTracerOpIndexing asserts sequential idx assignment and the
// look-ahead ex.used finalization (used = gas remaining after the op).
func TestParityVMTracerOpIndexing(t *testing.T) {
	t.Parallel()

	tr := &parityVMTracer{}
	tr.OnEnter(0, byte(vm.CALL), [20]byte{}, [20]byte{}, nil, 0, nil)

	scope := stubOpContext{}
	tr.OnOpcode(0, byte(vm.PUSH1), 100, 3, scope, nil, 0, nil)
	tr.OnOpcode(2, byte(vm.PUSH1), 97, 3, scope, nil, 0, nil)
	tr.OnOpcode(4, byte(vm.STOP), 94, 0, scope, nil, 0, nil)

	ops := tr.root.Ops
	if len(ops) != 3 {
		t.Fatalf("recorded %d ops, want 3", len(ops))
	}
	for i, wantIdx := range []string{"0", "1", "2"} {
		if ops[i].Idx != wantIdx {
			t.Errorf("ops[%d].Idx = %q, want %q", i, ops[i].Idx, wantIdx)
		}
	}
	// Finalized via look-ahead: used = gas at the NEXT opcode.
	if ops[0].Ex.Used != 97 {
		t.Errorf("ops[0].ex.used = %d, want 97", ops[0].Ex.Used)
	}
	if ops[1].Ex.Used != 94 {
		t.Errorf("ops[1].ex.used = %d, want 94", ops[1].Ex.Used)
	}
}

// stubOpContext is a minimal tracing.OpContext for driving OnOpcode directly.
type stubOpContext struct {
	stack []uint256.Int
	mem   []byte
}

func (s stubOpContext) MemoryData() []byte       { return s.mem }
func (s stubOpContext) StackData() []uint256.Int { return s.stack }
func (stubOpContext) Caller() common.Address     { return common.Address{} }
func (stubOpContext) Address() common.Address    { return common.Address{} }
func (stubOpContext) CallValue() *uint256.Int    { return uint256.NewInt(0) }
func (stubOpContext) CallInput() []byte          { return nil }
func (stubOpContext) ContractCode() []byte       { return nil }

// TestParityVMTracerMemAndPushCapture drives an MSTORE through the look-ahead
// finalization and asserts the recorded push and zero-padded memory region.
func TestParityVMTracerMemAndPushCapture(t *testing.T) {
	t.Parallel()

	tr := &parityVMTracer{}
	tr.OnEnter(0, byte(vm.CALL), common.Address{}, common.Address{}, nil, 0, nil)

	// PUSH1 0x2a; PUSH1 0x00; MSTORE; STOP. Each op's push/mem is finalized when
	// the NEXT opcode is observed (look-ahead), reading the then-current scope.
	tr.OnOpcode(0, byte(vm.PUSH1), 100, 3, stubOpContext{}, nil, 0, nil)
	// Observing this op finalizes PUSH1 0x2a: push = top of the current stack.
	afterPushVal := stubOpContext{stack: []uint256.Int{*uint256.NewInt(0x2a)}}
	tr.OnOpcode(2, byte(vm.PUSH1), 97, 3, afterPushVal, nil, 0, nil)
	// Pre-MSTORE stack (bottom-first): [value=0x2a, offset=0]; finalizes PUSH1 0x00.
	mstoreScope := stubOpContext{stack: []uint256.Int{*uint256.NewInt(0x2a), *uint256.NewInt(0)}}
	tr.OnOpcode(4, byte(vm.MSTORE), 94, 6, mstoreScope, nil, 0, nil)
	// Next op observes post-MSTORE memory; MSTORE writes 32 bytes at offset 0,
	// but expose only 8 bytes to exercise the zero-padding path.
	postMem := stubOpContext{mem: []byte{0, 0, 0, 0, 0, 0, 0, 0x99}}
	tr.OnOpcode(5, byte(vm.STOP), 88, 0, postMem, nil, 0, nil)

	ops := tr.root.Ops
	if len(ops) != 4 {
		t.Fatalf("recorded %d ops, want 4", len(ops))
	}
	if len(ops[0].Ex.Push) != 1 || ops[0].Ex.Push[0] != "0x2a" {
		t.Errorf("PUSH1 0x2a push = %v, want [0x2a]", ops[0].Ex.Push)
	}
	if len(ops[1].Ex.Push) != 1 || ops[1].Ex.Push[0] != "0x0" {
		t.Errorf("PUSH1 0x00 push = %v, want [0x0]", ops[1].Ex.Push)
	}
	mem := ops[2].Ex.Mem
	if mem == nil {
		t.Fatal("MSTORE recorded no mem region")
	}
	if mem.Off != 0 || len(mem.Data) != 32 {
		t.Fatalf("MSTORE mem = off %d len %d, want off 0 len 32", mem.Off, len(mem.Data))
	}
	// First 8 bytes copied from (short) memory, rest zero-padded.
	if mem.Data[7] != 0x99 || mem.Data[31] != 0 {
		t.Errorf("MSTORE mem data = %x, want byte 7 = 0x99 and zero padding", mem.Data)
	}
}

func TestParityVMTracerFinalizesPendingOpBeforeFault(t *testing.T) {
	t.Parallel()

	tr := &parityVMTracer{}
	tr.OnEnter(0, byte(vm.CALL), common.Address{}, common.Address{}, nil, 0, nil)

	// Record MSTORE with its pre-op operands, then deliver a pre-execution fault
	// for the following opcode. The fault callback's scope is post-MSTORE and
	// must still finalize MSTORE's memory region.
	preMstore := stubOpContext{stack: []uint256.Int{*uint256.NewInt(0x2a), *uint256.NewInt(0)}}
	tr.OnOpcode(0, byte(vm.MSTORE), 100, 6, preMstore, nil, 0, nil)
	postMstore := stubOpContext{mem: make([]byte, 32)}
	postMstore.mem[31] = 0x2a
	tr.OnOpcode(1, byte(vm.POP), 94, 0, postMstore, nil, 0, &vm.ErrStackUnderflow{})

	if len(tr.root.Ops) != 1 {
		t.Fatalf("recorded %d ops, want only the successful MSTORE", len(tr.root.Ops))
	}
	mem := tr.root.Ops[0].Ex.Mem
	if mem == nil || mem.Off != 0 || len(mem.Data) != 32 || mem.Data[31] != 0x2a {
		t.Fatalf("MSTORE memory was not finalized before fault: %+v", mem)
	}
}

// TestParityVMTracerSubFrame asserts a nested call attaches its frame as the
// opening call op's .sub with the correct idx prefix, and OnExit pops frames
// while finalizing the pending op.
func TestParityVMTracerSubFrame(t *testing.T) {
	t.Parallel()

	tr := &parityVMTracer{}
	tr.OnEnter(0, byte(vm.CALL), common.Address{}, common.Address{}, nil, 0, nil)

	// The CALL op is pending when the sub-frame is entered.
	tr.OnOpcode(0, byte(vm.CALL), 100, 40, stubOpContext{}, nil, 0, nil)
	tr.OnEnter(1, byte(vm.CALL), common.Address{}, common.Address{}, nil, 0, nil)

	if len(tr.stack) != 2 {
		t.Fatalf("stack depth = %d, want 2", len(tr.stack))
	}
	callOp := tr.root.Ops[0]
	if callOp.Sub == nil {
		t.Fatal("sub-frame not attached to the opening CALL op")
	}
	if got := tr.stack[1].prefix; got != callOp.Idx {
		t.Errorf("sub-frame prefix = %q, want the call op idx %q", got, callOp.Idx)
	}

	// An op inside the sub-frame carries the parent op's idx as prefix.
	tr.OnOpcode(0, byte(vm.PUSH1), 50, 3, stubOpContext{}, nil, 1, nil)
	sub := callOp.Sub
	if len(sub.Ops) != 1 || sub.Ops[0].Idx != callOp.Idx+"-0" {
		t.Fatalf("sub ops = %+v, want one op with idx %s-0", sub.Ops, callOp.Idx)
	}

	// Exiting finalizes the pending sub op (no scope: push empty, used = gas-cost).
	tr.OnExit(1, nil, 0, nil, false)
	if len(tr.stack) != 1 {
		t.Errorf("stack depth after sub exit = %d, want 1", len(tr.stack))
	}
	if sub.Ops[0].Ex == nil || sub.Ops[0].Ex.Used != 50-3 {
		t.Errorf("sub op ex.used = %+v, want %d", sub.Ops[0].Ex, 50-3)
	}
	tr.OnExit(0, nil, 0, nil, false)
	if len(tr.stack) != 0 {
		t.Errorf("stack depth after root exit = %d, want 0", len(tr.stack))
	}
	// Defensive: OnExit on an empty stack must not panic.
	tr.OnExit(0, nil, 0, nil, false)
}

func TestVMTraceStackTail(t *testing.T) {
	t.Parallel()

	deep := make([]uint256.Int, 12)
	for i := range deep {
		deep[i] = *uint256.NewInt(uint64(i + 1)) // bottom=1 ... top=12
	}

	t.Run("caps to top n preserving order", func(t *testing.T) {
		t.Parallel()
		got := vmTraceStackTail(deep, vmTraceMemStackDepth)
		if len(got) != vmTraceMemStackDepth {
			t.Fatalf("len = %d, want %d", len(got), vmTraceMemStackDepth)
		}
		// Suffix of the original: bottom of the copy = item 6, top = item 12.
		if got[0].Uint64() != 6 || got[len(got)-1].Uint64() != 12 {
			t.Errorf("tail = [%d..%d], want [6..12]", got[0].Uint64(), got[len(got)-1].Uint64())
		}
		// Top-relative positions must be identical to the uncapped stack.
		for n := 1; n <= vmTraceMemStackDepth; n++ {
			full, _ := vmTraceStackArg(deep, n)
			capped, ok := vmTraceStackArg(got, n)
			if !ok || capped != full {
				t.Errorf("arg %d: capped=%d full=%d", n, capped, full)
			}
		}
	})

	t.Run("short stack copied whole", func(t *testing.T) {
		t.Parallel()
		got := vmTraceStackTail(deep[:3], vmTraceMemStackDepth)
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
	})

	t.Run("copy is independent of the source", func(t *testing.T) {
		t.Parallel()
		src := []uint256.Int{*uint256.NewInt(1)}
		got := vmTraceStackTail(src, vmTraceMemStackDepth)
		src[0] = *uint256.NewInt(99)
		if got[0].Uint64() != 1 {
			t.Errorf("copy aliases the source stack")
		}
	})

	// CALL's retOff/retLen (positions 6 and 7 from the top) must survive the cap
	// even when the pre-op stack is deeper than the copied window.
	t.Run("deep stack CALL mem region intact", func(t *testing.T) {
		t.Parallel()
		// bottom..top: junk x5, then retLen=0x40, retOff=0x200, argsLen, argsOff, value, addr, gas
		stack := []uint256.Int{
			*uint256.NewInt(1), *uint256.NewInt(2), *uint256.NewInt(3), *uint256.NewInt(4), *uint256.NewInt(5),
			*uint256.NewInt(0x40), *uint256.NewInt(0x200), *uint256.NewInt(0x04), *uint256.NewInt(0x20),
			*uint256.NewInt(0), *uint256.NewInt(0xbb), *uint256.NewInt(5000),
		}
		off, size, writes := vmTraceMemRegion(vm.CALL, vmTraceStackTail(stack, vmTraceMemStackDepth))
		if !writes || off != 0x200 || size != 0x40 {
			t.Errorf("region = [%#x, +%#x) writes=%v, want [0x200, +0x40) true", off, size, writes)
		}
	})
}
