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
//  2. Shifts the depth of every subsequent OnEnter / OnExit / OnOpcode / OnFault
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
	wrapped := *inner
	wrapped.OnTxStart = w.OnTxStart
	wrapped.OnTxEnd = w.OnTxEnd
	wrapped.OnEnter = w.OnEnter
	wrapped.OnExit = w.OnExit
	wrapped.OnOpcode = w.OnOpcode
	wrapped.OnFault = w.OnFault
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
}

func (t *stateSyncHooks) OnTxStart(env *tracing.VMContext, tx *types.Transaction, from common.Address) {
	t.active = tx.Type() == types.StateSyncTxType

	// Forward OnTxStart first so the inner tracer initializes its per-tx state
	// (e.g., callTracer resets its call stack) before we emit the synthetic root.
	if t.inner.OnTxStart != nil {
		t.inner.OnTxStart(env, tx, from)
	}

	if t.active && t.inner.OnEnter != nil {
		// Add a synthetic root frame at depth 0 wrapping the state-sync transaction. It uses
		// from = params.BorSystemAddress and to = StateReceiverContract and the gas / value
		// are zero as it carries no real cost. The sender and receiver address are the same
		// address the EVM sees during actual execution and not zero address as it doesn't
		// actually represent what actually was executed.
		t.inner.OnEnter(0, byte(vm.CALL), params.BorSystemAddress, t.stateReceiverAddress, nil, 0, big.NewInt(0))
	}
}

func (t *stateSyncHooks) OnTxEnd(receipt *types.Receipt, err error) {
	if t.active && t.inner.OnExit != nil {
		// Report the total gas used by the state-sync tx on the synthetic root frame.
		// Receipt is nil on error paths (e.g., ApplyStateSyncEvents failed in traceTx);
		// fall back to 0 in that case.
		var gasUsed uint64
		if receipt != nil {
			gasUsed = receipt.GasUsed
		}
		t.inner.OnExit(0, nil, gasUsed, err, err != nil)
	}
	if t.inner.OnTxEnd != nil {
		t.inner.OnTxEnd(receipt, err)
	}
	t.active = false
}

func (t *stateSyncHooks) OnEnter(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	if t.inner.OnEnter == nil {
		return
	}
	if t.active {
		depth++
	}
	t.inner.OnEnter(depth, typ, from, to, input, gas, value)
}

func (t *stateSyncHooks) OnExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	if t.inner.OnExit == nil {
		return
	}
	if t.active {
		depth++
	}
	t.inner.OnExit(depth, output, gasUsed, err, reverted)
}

func (t *stateSyncHooks) OnOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	if t.inner.OnOpcode == nil {
		return
	}
	if t.active {
		depth++
	}
	t.inner.OnOpcode(pc, op, gas, cost, scope, rData, depth, err)
}

func (t *stateSyncHooks) OnFault(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, depth int, err error) {
	if t.inner.OnFault == nil {
		return
	}
	if t.active {
		depth++
	}
	t.inner.OnFault(pc, op, gas, cost, scope, depth, err)
}
