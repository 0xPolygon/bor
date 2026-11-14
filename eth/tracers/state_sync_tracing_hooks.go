package tracers

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// WrapStateSyncHooks wraps the underlying tracer's hooks to handle state-sync
// transactions. Because state-sync events are applied over state as independent
// EVM calls, the canonical tracer wouldn't be accurate (and panic most probably)
// as it expects a single top-level call per transaction. Using canonical tracer
// for state-sync transactions would produce multiple top-level calls at depth 0
// for each event.
//
// The wrapper:
//  1. On OnTxStart, if the tx is a StateSyncTx, emits a synthetic top-level CALL
//     at depth 0 from BorSystemAddress to stateReceiverAddress. The synthetic
//     root opens here, before the first real OnEnter fires.
//  2. Shifts the depth of every subsequent enter / exit / opcode / fault event
//     by +1 so the N real top-level calls appear as children of the synthetic root.
//  3. On OnTxEnd, emits a matching synthetic OnExit at depth 0 to close the root.
//
// For non state-sync transactions, every hook is forwarded unchanged (passthrough).
// State-change hooks (OnLog, OnBalanceChange, OnNonceChange, OnCodeChange,
// OnStorageChange) and OnGasChange carry no depth and are always forwarded
// unchanged regardless of tx type.
//
// The wrapper carries internal state (a single boolean `active`) bracketed by
// OnTxStart/OnTxEnd. The caller must ensure OnTxEnd is fired even on error
// paths — otherwise a synthetic root frame leaks in the inner tracer.
//
// The wrapper instance is not safe for concurrent OnTxStart invocations from
// multiple goroutines. Callers using the live tracer should ensure that transactions
// are processed serially.
func WrapStateSyncHooks(inner *tracing.Hooks, stateReceiverAddress common.Address) *tracing.Hooks {
	w := &stateSyncHooks{inner: inner, stateReceiverAddress: stateReceiverAddress}

	// Copy the inner tracer's hooks and override only the depth-bearing ones; this
	// preserves passthrough hooks (OnLog, OnBalanceChange, ...) we don't wrap.
	//
	// The depth-bearing hooks are exposed in their V2 form only and forwarded with
	// the inner Emit helpers, which prefer the inner V2 hook and fall back to V1.
	// This keeps tracers that set only one of the two forms (e.g. muxTracer, which
	// exposes only V2) receiving every frame.
	wrapped := *inner
	wrapped.OnTxStart = w.OnTxStart
	wrapped.OnTxEnd = w.OnTxEnd
	wrapped.OnEnter, wrapped.OnEnterV2 = nil, w.OnEnterV2
	wrapped.OnExit, wrapped.OnExitV2 = nil, w.OnExitV2
	wrapped.OnOpcode, wrapped.OnOpcodeV2 = nil, w.OnOpcodeV2
	wrapped.OnFault, wrapped.OnFaultV2 = nil, w.OnFaultV2
	return &wrapped
}

type stateSyncHooks struct {
	inner                *tracing.Hooks
	stateReceiverAddress common.Address

	// active is true while we are inside a StateSyncTx's OnTxStart/OnTxEnd window.
	// OnEnter has no transaction reference, so we cache the decision once at
	// OnTxStart (where tx.Type() is available) and read it from all depth-bearing
	// hooks. Reset to false in OnTxEnd.
	active bool

	// entryGas is the per-frame entry budget, to derive the V1 gasUsed on exit.
	entryGas []tracing.Gas
}

func (t *stateSyncHooks) OnTxStart(env *tracing.VMContext, tx *types.Transaction, from common.Address) {
	t.active = tx.Type() == types.StateSyncTxType

	// Forward OnTxStart first so the inner tracer initializes its per-tx state
	// (e.g., callTracer resets its call stack) before we emit the synthetic root.
	if t.inner.OnTxStart != nil {
		t.inner.OnTxStart(env, tx, from)
	}

	if t.active {
		// Add a synthetic root frame at depth 0 wrapping the state-sync transaction. It uses
		// from = params.BorSystemAddress and to = StateReceiverContract and the gas / value
		// are zero as it carries no real cost. The sender and receiver address are the same
		// address the EVM sees during actual execution and not zero address as it doesn't
		// actually represent what actually was executed.
		t.inner.EmitEnter(0, byte(vm.CALL), params.BorSystemAddress, t.stateReceiverAddress, nil, tracing.Gas{}, big.NewInt(0))
	}
}

func (t *stateSyncHooks) OnTxEnd(receipt *types.Receipt, err error) {
	if t.active {
		// Report the total gas used by the state-sync tx on the synthetic root frame.
		// A V1 exit receives it as gasUsed; a V2 exit carries only the leftover gas,
		// which is zero for this budgetless frame.
		// Receipt is nil on error paths (e.g., ApplyStateSyncEvents failed in traceTx);
		// fall back to 0 in that case.
		var gasUsed uint64
		if receipt != nil {
			gasUsed = receipt.GasUsed
		}
		t.inner.EmitExit(0, nil, tracing.Gas{Execution: gasUsed}, tracing.Gas{}, err, err != nil)
	}
	if t.inner.OnTxEnd != nil {
		t.inner.OnTxEnd(receipt, err)
	}
	t.active = false
}

func (t *stateSyncHooks) OnEnterV2(depth int, typ byte, from common.Address, to common.Address, input []byte, gas tracing.Gas, value *big.Int) {
	if t.active {
		depth++
	}
	t.entryGas = append(t.entryGas, gas)
	t.inner.EmitEnter(depth, typ, from, to, input, gas, value)
}

func (t *stateSyncHooks) OnExitV2(depth int, output []byte, gasLeft tracing.Gas, err error, reverted bool) {
	if t.active {
		depth++
	}
	// The inner V1 hook derives gasUsed from the entry budget, which the V2 exit
	// doesn't carry; the stack below restores it.
	var entry tracing.Gas
	if n := len(t.entryGas); n > 0 {
		entry, t.entryGas = t.entryGas[n-1], t.entryGas[:n-1]
	}
	t.inner.EmitExit(depth, output, entry, gasLeft, err, reverted)
}

func (t *stateSyncHooks) OnOpcodeV2(pc uint64, op byte, gas, cost tracing.Gas, scope tracing.OpContext, rData []byte, depth int, err error) {
	if t.active {
		depth++
	}
	t.inner.EmitOpcode(pc, op, gas, cost, scope, rData, depth, err)
}

func (t *stateSyncHooks) OnFaultV2(pc uint64, op byte, gas, cost tracing.Gas, scope tracing.OpContext, depth int, err error) {
	if t.active {
		depth++
	}
	t.inner.EmitFault(pc, op, gas, cost, scope, depth, err)
}
