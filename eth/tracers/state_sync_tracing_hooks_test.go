// Copyright 2025 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package tracers

import (
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// recordingHooks accumulates every hook invocation into a string slice for
// order-sensitive assertions. Only hooks exercised by the wrapper tests are
// implemented; tests pick the subset they need via the returned *tracing.Hooks.
type recordingHooks struct {
	events []string
}

func (r *recordingHooks) hooks() *tracing.Hooks {
	return &tracing.Hooks{
		OnTxStart:       r.onTxStart,
		OnTxEnd:         r.onTxEnd,
		OnEnter:         r.onEnter,
		OnExit:          r.onExit,
		OnOpcode:        r.onOpcode,
		OnFault:         r.onFault,
		OnGasChange:     r.onGasChange,
		OnBalanceChange: r.onBalanceChange,
		OnNonceChange:   r.onNonceChange,
		OnCodeChange:    r.onCodeChange,
		OnStorageChange: r.onStorageChange,
		OnLog:           r.onLog,
	}
}

func (r *recordingHooks) onTxStart(env *tracing.VMContext, tx *types.Transaction, from common.Address) {
	r.events = append(r.events, fmt.Sprintf("OnTxStart(type=%d,from=%s)", tx.Type(), from.Hex()))
}

func (r *recordingHooks) onTxEnd(receipt *types.Receipt, err error) {
	errStr := "nil"
	if err != nil {
		errStr = err.Error()
	}
	r.events = append(r.events, fmt.Sprintf("OnTxEnd(err=%s)", errStr))
}

func (r *recordingHooks) onEnter(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	r.events = append(r.events, fmt.Sprintf("OnEnter(depth=%d,typ=0x%02x,from=%s,to=%s)", depth, typ, from.Hex(), to.Hex()))
}

func (r *recordingHooks) onExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	errStr := "nil"
	if err != nil {
		errStr = err.Error()
	}
	r.events = append(r.events, fmt.Sprintf("OnExit(depth=%d,err=%s,reverted=%v)", depth, errStr, reverted))
}

func (r *recordingHooks) onOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	r.events = append(r.events, fmt.Sprintf("OnOpcode(depth=%d,op=0x%02x)", depth, op))
}

func (r *recordingHooks) onFault(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, depth int, err error) {
	r.events = append(r.events, fmt.Sprintf("OnFault(depth=%d,op=0x%02x)", depth, op))
}

func (r *recordingHooks) onGasChange(old, new uint64, reason tracing.GasChangeReason) {
	r.events = append(r.events, fmt.Sprintf("OnGasChange(old=%d,new=%d)", old, new))
}

func (r *recordingHooks) onBalanceChange(addr common.Address, prev, new *big.Int, reason tracing.BalanceChangeReason) {
	r.events = append(r.events, fmt.Sprintf("OnBalanceChange(addr=%s)", addr.Hex()))
}

func (r *recordingHooks) onNonceChange(addr common.Address, prev, new uint64) {
	r.events = append(r.events, fmt.Sprintf("OnNonceChange(addr=%s,new=%d)", addr.Hex(), new))
}

func (r *recordingHooks) onCodeChange(addr common.Address, prevCodeHash common.Hash, prevCode []byte, codeHash common.Hash, code []byte) {
	r.events = append(r.events, fmt.Sprintf("OnCodeChange(addr=%s)", addr.Hex()))
}

func (r *recordingHooks) onStorageChange(addr common.Address, slot common.Hash, prev, new common.Hash) {
	r.events = append(r.events, fmt.Sprintf("OnStorageChange(addr=%s,slot=%s)", addr.Hex(), slot.Hex()))
}

func (r *recordingHooks) onLog(log *types.Log) {
	r.events = append(r.events, fmt.Sprintf("OnLog(addr=%s)", log.Address.Hex()))
}

// makeStateSyncTx constructs a minimal StateSyncTx for hook tests. The events
// and addresses are placeholders; the wrapper only inspects tx.Type().
func makeStateSyncTx() *types.Transaction {
	return types.NewTx(&types.StateSyncTx{
		StateSyncData: []*types.StateSyncData{{
			ID:       1,
			Contract: common.HexToAddress("0x1111111111111111111111111111111111111111"),
		}},
	})
}

// makeRegularTx constructs a minimal non-state-sync tx (DynamicFee) for hook tests.
func makeRegularTx() *types.Transaction {
	return types.NewTx(&types.DynamicFeeTx{
		ChainID:   big.NewInt(1),
		Nonce:     0,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(1),
		Gas:       21000,
		Value:     big.NewInt(0),
	})
}

// fireRealEvent simulates one real commitState invocation as the EVM would
// produce it (single CALL at depth=0 → matching OnExit at depth=0).
func fireRealEvent(hooks *tracing.Hooks, from, to common.Address) {
	hooks.OnEnter(0, byte(vm.CALL), from, to, nil, 100000, big.NewInt(0))
	hooks.OnExit(0, nil, 50000, nil, false)
}

// TestWrapStateSyncHooks_StateSyncTx_MultipleEvents verifies the wrapper's main
// purpose: for a StateSyncTx with N real top-level EVM calls, the inner tracer
// sees exactly one synthetic root frame containing N children at depth+1, and
// the root is closed exactly once at OnTxEnd.
func TestWrapStateSyncHooks_StateSyncTx_MultipleEvents(t *testing.T) {
	const numEvents = 3

	rec := &recordingHooks{}
	receiverAddr := common.HexToAddress("0x0000000000000000000000000000000000001001")
	wrapped := WrapStateSyncHooks(rec.hooks(), receiverAddr)

	wrapped.OnTxStart(nil, makeStateSyncTx(), params.BorSystemAddress)
	for i := 0; i < numEvents; i++ {
		fireRealEvent(wrapped, params.BorSystemAddress, receiverAddr)
	}
	wrapped.OnTxEnd(&types.Receipt{}, nil)

	expected := []string{
		fmt.Sprintf("OnTxStart(type=%d,from=%s)", types.StateSyncTxType, params.BorSystemAddress.Hex()),
		// Synthetic root opens at depth 0.
		fmt.Sprintf("OnEnter(depth=0,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), params.BorSystemAddress.Hex(), receiverAddr.Hex()),
	}
	for i := 0; i < numEvents; i++ {
		// Each real commitState OnEnter at depth 0 is shifted to depth 1.
		expected = append(expected,
			fmt.Sprintf("OnEnter(depth=1,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), params.BorSystemAddress.Hex(), receiverAddr.Hex()),
			"OnExit(depth=1,err=nil,reverted=false)",
		)
	}
	// Synthetic root closes at depth 0.
	expected = append(expected,
		"OnExit(depth=0,err=nil,reverted=false)",
		"OnTxEnd(err=nil)",
	)

	assertEvents(t, expected, rec.events)
}

// TestWrapStateSyncHooks_RegularTx_Passthrough verifies the wrapper is a no-op
// for non-state-sync transactions: no synthetic frame, no depth shift.
func TestWrapStateSyncHooks_RegularTx_Passthrough(t *testing.T) {
	rec := &recordingHooks{}
	receiverAddr := common.HexToAddress("0x0000000000000000000000000000000000001001")
	wrapped := WrapStateSyncHooks(rec.hooks(), receiverAddr)

	caller := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	callee := common.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	wrapped.OnTxStart(nil, makeRegularTx(), caller)
	wrapped.OnEnter(0, byte(vm.CALL), caller, callee, nil, 100000, big.NewInt(0))
	wrapped.OnExit(0, nil, 50000, nil, false)
	wrapped.OnTxEnd(&types.Receipt{}, nil)

	expected := []string{
		fmt.Sprintf("OnTxStart(type=%d,from=%s)", types.DynamicFeeTxType, caller.Hex()),
		fmt.Sprintf("OnEnter(depth=0,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), caller.Hex(), callee.Hex()),
		"OnExit(depth=0,err=nil,reverted=false)",
		"OnTxEnd(err=nil)",
	}
	assertEvents(t, expected, rec.events)
}

// TestWrapStateSyncHooks_SyntheticRootGasUsedFromReceipt verifies that the
// synthetic root frame's OnExit reports gasUsed taken from receipt.GasUsed, not
// hard-coded zero. On the error path receipt is nil and gasUsed falls back to 0.
func TestWrapStateSyncHooks_SyntheticRootGasUsedFromReceipt(t *testing.T) {
	type exitCall struct {
		depth   int
		gasUsed uint64
	}

	cases := []struct {
		name    string
		receipt *types.Receipt
		err     error
		// We assert only the depth-0 (synthetic root) OnExit. Children at depth
		// 1+ pass through whatever the caller supplied (50000 below).
		wantRootGasUsed uint64
	}{
		{"happy path — gasUsed flows from receipt", &types.Receipt{GasUsed: 123456}, nil, 123456},
		{"zero gas — receipt with zero is honoured", &types.Receipt{GasUsed: 0}, nil, 0},
		{"error path — nil receipt falls back to 0", nil, errors.New("commitState failed"), 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var exits []exitCall
			inner := &tracing.Hooks{
				OnEnter: func(int, byte, common.Address, common.Address, []byte, uint64, *big.Int) {},
				OnExit: func(depth int, _ []byte, gasUsed uint64, _ error, _ bool) {
					exits = append(exits, exitCall{depth: depth, gasUsed: gasUsed})
				},
			}
			receiverAddr := common.HexToAddress("0x0000000000000000000000000000000000001001")
			wrapped := WrapStateSyncHooks(inner, receiverAddr)

			wrapped.OnTxStart(nil, makeStateSyncTx(), params.BorSystemAddress)
			fireRealEvent(wrapped, params.BorSystemAddress, receiverAddr)
			wrapped.OnTxEnd(tc.receipt, tc.err)

			// Last recorded OnExit is the synthetic root closure at depth 0.
			require.NotEmpty(t, exits)
			root := exits[len(exits)-1]
			require.Equal(t, 0, root.depth, "synthetic root must close at depth 0")
			require.Equal(t, tc.wantRootGasUsed, root.gasUsed)
		})
	}
}

// TestWrapStateSyncHooks_ErrorPath verifies that OnTxEnd called with a non-nil
// error still closes the synthetic root frame with reverted=true.
func TestWrapStateSyncHooks_ErrorPath(t *testing.T) {
	rec := &recordingHooks{}
	receiverAddr := common.HexToAddress("0x0000000000000000000000000000000000001001")
	wrapped := WrapStateSyncHooks(rec.hooks(), receiverAddr)

	wrapped.OnTxStart(nil, makeStateSyncTx(), params.BorSystemAddress)
	fireRealEvent(wrapped, params.BorSystemAddress, receiverAddr)
	wrapped.OnTxEnd(nil, errors.New("commitState failed"))

	expected := []string{
		fmt.Sprintf("OnTxStart(type=%d,from=%s)", types.StateSyncTxType, params.BorSystemAddress.Hex()),
		fmt.Sprintf("OnEnter(depth=0,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), params.BorSystemAddress.Hex(), receiverAddr.Hex()),
		fmt.Sprintf("OnEnter(depth=1,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), params.BorSystemAddress.Hex(), receiverAddr.Hex()),
		"OnExit(depth=1,err=nil,reverted=false)",
		"OnExit(depth=0,err=commitState failed,reverted=true)",
		"OnTxEnd(err=commitState failed)",
	}
	assertEvents(t, expected, rec.events)
}

// TestWrapStateSyncHooks_ResetBetweenTxs verifies that the wrapper's `active`
// flag is reset after OnTxEnd so a subsequent non-state-sync tx is not
// affected by the previous state-sync tx's wrapping (no leaked depth shift,
// no leaked synthetic frame).
func TestWrapStateSyncHooks_ResetBetweenTxs(t *testing.T) {
	rec := &recordingHooks{}
	receiverAddr := common.HexToAddress("0x0000000000000000000000000000000000001001")
	wrapped := WrapStateSyncHooks(rec.hooks(), receiverAddr)

	// Tx 1: state-sync
	wrapped.OnTxStart(nil, makeStateSyncTx(), params.BorSystemAddress)
	fireRealEvent(wrapped, params.BorSystemAddress, receiverAddr)
	wrapped.OnTxEnd(&types.Receipt{}, nil)

	// Tx 2: regular — must be passthrough (no synthetic frame, depths unshifted).
	caller := common.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	callee := common.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	wrapped.OnTxStart(nil, makeRegularTx(), caller)
	wrapped.OnEnter(0, byte(vm.CALL), caller, callee, nil, 100000, big.NewInt(0))
	wrapped.OnExit(0, nil, 50000, nil, false)
	wrapped.OnTxEnd(&types.Receipt{}, nil)

	expected := []string{
		// state-sync tx
		fmt.Sprintf("OnTxStart(type=%d,from=%s)", types.StateSyncTxType, params.BorSystemAddress.Hex()),
		fmt.Sprintf("OnEnter(depth=0,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), params.BorSystemAddress.Hex(), receiverAddr.Hex()),
		fmt.Sprintf("OnEnter(depth=1,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), params.BorSystemAddress.Hex(), receiverAddr.Hex()),
		"OnExit(depth=1,err=nil,reverted=false)",
		"OnExit(depth=0,err=nil,reverted=false)",
		"OnTxEnd(err=nil)",
		// regular tx — depths must not be shifted, no synthetic frame
		fmt.Sprintf("OnTxStart(type=%d,from=%s)", types.DynamicFeeTxType, caller.Hex()),
		fmt.Sprintf("OnEnter(depth=0,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), caller.Hex(), callee.Hex()),
		"OnExit(depth=0,err=nil,reverted=false)",
		"OnTxEnd(err=nil)",
	}
	assertEvents(t, expected, rec.events)
}

// TestWrapStateSyncHooks_NestedCalls verifies that the +1 depth shift applies
// uniformly to all OnEnter/OnExit/OnOpcode/OnFault events inside a state-sync
// tx, not just top-level ones. Real commitState execution will produce nested
// EVM calls at depths 1, 2, 3 etc., and the wrapper must shift them all.
func TestWrapStateSyncHooks_NestedCalls(t *testing.T) {
	rec := &recordingHooks{}
	receiverAddr := common.HexToAddress("0x0000000000000000000000000000000000001001")
	wrapped := WrapStateSyncHooks(rec.hooks(), receiverAddr)

	nested := common.HexToAddress("0xcccccccccccccccccccccccccccccccccccccccc")

	wrapped.OnTxStart(nil, makeStateSyncTx(), params.BorSystemAddress)
	// Simulate: top-level commitState call that internally CALLs another contract.
	wrapped.OnEnter(0, byte(vm.CALL), params.BorSystemAddress, receiverAddr, nil, 100000, big.NewInt(0))
	wrapped.OnEnter(1, byte(vm.CALL), receiverAddr, nested, nil, 50000, big.NewInt(0))
	wrapped.OnOpcode(0, byte(vm.STOP), 50000, 0, nil, nil, 1, nil)
	wrapped.OnFault(0, byte(vm.STOP), 50000, 0, nil, 1, errors.New("oops"))
	wrapped.OnExit(1, nil, 25000, nil, false)
	wrapped.OnExit(0, nil, 50000, nil, false)
	wrapped.OnTxEnd(&types.Receipt{}, nil)

	expected := []string{
		fmt.Sprintf("OnTxStart(type=%d,from=%s)", types.StateSyncTxType, params.BorSystemAddress.Hex()),
		// Synthetic root at depth 0.
		fmt.Sprintf("OnEnter(depth=0,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), params.BorSystemAddress.Hex(), receiverAddr.Hex()),
		// Real OnEnter at original depth 0 → shifted to 1.
		fmt.Sprintf("OnEnter(depth=1,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), params.BorSystemAddress.Hex(), receiverAddr.Hex()),
		// Nested OnEnter at original depth 1 → shifted to 2.
		fmt.Sprintf("OnEnter(depth=2,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), receiverAddr.Hex(), nested.Hex()),
		// Opcode and Fault at original depth 1 → shifted to 2.
		fmt.Sprintf("OnOpcode(depth=2,op=0x%02x)", byte(vm.STOP)),
		fmt.Sprintf("OnFault(depth=2,op=0x%02x)", byte(vm.STOP)),
		// OnExit at original depth 1 → 2.
		"OnExit(depth=2,err=nil,reverted=false)",
		// OnExit at original depth 0 → 1.
		"OnExit(depth=1,err=nil,reverted=false)",
		// Synthetic root close at depth 0.
		"OnExit(depth=0,err=nil,reverted=false)",
		"OnTxEnd(err=nil)",
	}
	assertEvents(t, expected, rec.events)
}

// TestWrapStateSyncHooks_StateChangeHooksPassthrough verifies that state-change
// hooks (OnLog, OnBalanceChange, OnStorageChange, OnNonceChange, OnCodeChange,
// OnGasChange) are identity-forwarded — same arguments, no transformation —
// regardless of whether we're inside a state-sync tx window or not. These
// hooks carry no depth, so the wrapper's depth-shift logic doesn't apply.
func TestWrapStateSyncHooks_StateChangeHooksPassthrough(t *testing.T) {
	rec := &recordingHooks{}
	receiverAddr := common.HexToAddress("0x0000000000000000000000000000000000001001")
	wrapped := WrapStateSyncHooks(rec.hooks(), receiverAddr)
	addr := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	slot := common.HexToHash("0x1234")

	wrapped.OnTxStart(nil, makeStateSyncTx(), params.BorSystemAddress)
	wrapped.OnLog(&types.Log{Address: addr})
	wrapped.OnBalanceChange(addr, big.NewInt(1), big.NewInt(2), tracing.BalanceChangeTransfer)
	wrapped.OnStorageChange(addr, slot, common.Hash{}, common.HexToHash("0x42"))
	wrapped.OnNonceChange(addr, 0, 1)
	wrapped.OnCodeChange(addr, common.Hash{}, nil, common.Hash{}, nil)
	wrapped.OnGasChange(100, 90, tracing.GasChangeUnspecified)
	wrapped.OnTxEnd(&types.Receipt{}, nil)

	expected := []string{
		fmt.Sprintf("OnTxStart(type=%d,from=%s)", types.StateSyncTxType, params.BorSystemAddress.Hex()),
		fmt.Sprintf("OnEnter(depth=0,typ=0x%02x,from=%s,to=%s)", byte(vm.CALL), params.BorSystemAddress.Hex(), receiverAddr.Hex()),
		fmt.Sprintf("OnLog(addr=%s)", addr.Hex()),
		fmt.Sprintf("OnBalanceChange(addr=%s)", addr.Hex()),
		fmt.Sprintf("OnStorageChange(addr=%s,slot=%s)", addr.Hex(), slot.Hex()),
		fmt.Sprintf("OnNonceChange(addr=%s,new=1)", addr.Hex()),
		fmt.Sprintf("OnCodeChange(addr=%s)", addr.Hex()),
		"OnGasChange(old=100,new=90)",
		"OnExit(depth=0,err=nil,reverted=false)",
		"OnTxEnd(err=nil)",
	}
	assertEvents(t, expected, rec.events)
}

// TestWrapStateSyncHooks_NilInnerHooks verifies the wrapper's contract for an
// inner *tracing.Hooks with nil fields.
//
// For hooks the wrapper overrides (OnTxStart, OnTxEnd, OnEnter, OnExit,
// OnOpcode, OnFault), the wrapper's methods nil-guard the inner hook so
// invoking them does not panic. For hooks the wrapper does NOT override
// (state-change hooks, block-lifecycle hooks, etc.), the wrapper's returned
// *tracing.Hooks has those fields exactly mirroring inner — nil if inner is
// nil. Callers (EVM, HookedState) follow the standard tracing.Hooks contract
// of checking `if hooks.OnX != nil` before invocation, so the nil values are
// safe.
func TestWrapStateSyncHooks_NilInnerHooks(t *testing.T) {
	wrapped := WrapStateSyncHooks(&tracing.Hooks{}, common.HexToAddress("0x1001"))

	// Overridden hooks: must not panic even when every inner hook is nil.
	wrapped.OnTxStart(nil, makeStateSyncTx(), params.BorSystemAddress)
	wrapped.OnEnter(0, byte(vm.CALL), common.Address{}, common.Address{}, nil, 0, big.NewInt(0))
	wrapped.OnExit(0, nil, 0, nil, false)
	wrapped.OnOpcode(0, byte(vm.STOP), 0, 0, nil, nil, 0, nil)
	wrapped.OnFault(0, byte(vm.STOP), 0, 0, nil, 0, nil)
	wrapped.OnTxEnd(&types.Receipt{}, nil)

	// Pass-through hooks: must mirror inner's nil-ness. The wrapper does not
	// add a nil-guard for these; callers are expected to nil-check.
	require.Nil(t, wrapped.OnLog, "OnLog should pass through inner's nil")
	require.Nil(t, wrapped.OnBalanceChange, "OnBalanceChange should pass through inner's nil")
	require.Nil(t, wrapped.OnNonceChange, "OnNonceChange should pass through inner's nil")
	require.Nil(t, wrapped.OnCodeChange, "OnCodeChange should pass through inner's nil")
	require.Nil(t, wrapped.OnStorageChange, "OnStorageChange should pass through inner's nil")
	require.Nil(t, wrapped.OnGasChange, "OnGasChange should pass through inner's nil")
	require.Nil(t, wrapped.OnBlockStart, "OnBlockStart should pass through inner's nil")
	require.Nil(t, wrapped.OnBlockEnd, "OnBlockEnd should pass through inner's nil")
	require.Nil(t, wrapped.OnGenesisBlock, "OnGenesisBlock should pass through inner's nil")
	require.Nil(t, wrapped.OnBlockchainInit, "OnBlockchainInit should pass through inner's nil")
	require.Nil(t, wrapped.OnClose, "OnClose should pass through inner's nil")
	require.Nil(t, wrapped.OnSystemCallStart, "OnSystemCallStart should pass through inner's nil")
	require.Nil(t, wrapped.OnSystemCallEnd, "OnSystemCallEnd should pass through inner's nil")
	require.Nil(t, wrapped.OnBlockHashRead, "OnBlockHashRead should pass through inner's nil")
}

// TestWrapStateSyncHooks_PassthroughForwardsAllInnerHooks verifies that the
// wrapper does NOT drop any hooks the inner tracer sets — including block
// lifecycle, system call, and v2 variant hooks. This regression test guards
// against the wrapper accidentally returning a *tracing.Hooks with only a
// subset of inner's fields wired through (which would silently break
// tracers like supply that rely on OnBlockStart/OnGenesisBlock/etc.).
func TestWrapStateSyncHooks_PassthroughForwardsAllInnerHooks(t *testing.T) {
	var (
		blockStartCalled      bool
		blockEndCalled        bool
		genesisCalled         bool
		systemCallStartCalled bool
		systemCallEndCalled   bool
		blockHashReadCalled   bool
		blockchainInitCalled  bool
		closeCalled           bool
	)
	inner := &tracing.Hooks{
		OnBlockStart:      func(tracing.BlockEvent) { blockStartCalled = true },
		OnBlockEnd:        func(error) { blockEndCalled = true },
		OnGenesisBlock:    func(*types.Block, types.GenesisAlloc) { genesisCalled = true },
		OnSystemCallStart: func() { systemCallStartCalled = true },
		OnSystemCallEnd:   func() { systemCallEndCalled = true },
		OnBlockHashRead:   func(uint64, common.Hash) { blockHashReadCalled = true },
		OnBlockchainInit:  func(*params.ChainConfig) { blockchainInitCalled = true },
		OnClose:           func() { closeCalled = true },
	}

	wrapped := WrapStateSyncHooks(inner, common.HexToAddress("0x1001"))

	require.NotNil(t, wrapped.OnBlockStart)
	wrapped.OnBlockStart(tracing.BlockEvent{})
	wrapped.OnBlockEnd(nil)
	wrapped.OnGenesisBlock(nil, nil)
	wrapped.OnSystemCallStart()
	wrapped.OnSystemCallEnd()
	wrapped.OnBlockHashRead(0, common.Hash{})
	wrapped.OnBlockchainInit(nil)
	wrapped.OnClose()

	require.True(t, blockStartCalled, "OnBlockStart was not forwarded")
	require.True(t, blockEndCalled, "OnBlockEnd was not forwarded")
	require.True(t, genesisCalled, "OnGenesisBlock was not forwarded")
	require.True(t, systemCallStartCalled, "OnSystemCallStart was not forwarded")
	require.True(t, systemCallEndCalled, "OnSystemCallEnd was not forwarded")
	require.True(t, blockHashReadCalled, "OnBlockHashRead was not forwarded")
	require.True(t, blockchainInitCalled, "OnBlockchainInit was not forwarded")
	require.True(t, closeCalled, "OnClose was not forwarded")
}

// assertEvents fails the test with a diff-style message if got and want don't match.
func assertEvents(t *testing.T, want, got []string) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("event count mismatch: want %d, got %d\nwant: %#v\ngot:  %#v", len(want), len(got), want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Errorf("event %d mismatch:\n  want: %s\n  got:  %s", i, want[i], got[i])
		}
	}
}
