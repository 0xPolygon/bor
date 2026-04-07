package tracers

import (
	"encoding/json"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// NewStateSyncTxnTracer wraps any tracer to make it compatible with bor state-sync transactions.
//
// State-sync transactions contain multiple independent bridge events, each of which produces its own
// top-level EVM call (OnEnter at depth 0). This breaks tracers like callTracer that expect exactly
// one root call frame per transaction.
//
// The wrapper fixes this by:
//  1. Injecting a synthetic top-level CALL (depth 0) from BorSystemAddress to the StateReceiverContract
//     on the first OnEnter, so there is a single root frame.
//  2. Shifting all real EVM depths by +1, making the actual top-level calls appear as sub-calls.
//  3. Closing the synthetic root frame via OnExit(depth=0) in OnTxEnd.
func NewStateSyncTxnTracer(tracer *Tracer, stateReceiverContractAddress common.Address) *Tracer {
	t := &stateSyncTxnTracer{
		tracer:                       tracer,
		stateReceiverContractAddress: stateReceiverContractAddress,
	}
	return &Tracer{
		Hooks: &tracing.Hooks{
			OnTxStart:       t.OnTxStart,
			OnTxEnd:         t.OnTxEnd,
			OnEnter:         t.OnEnter,
			OnExit:          t.OnExit,
			OnOpcode:        t.OnOpcode,
			OnFault:         t.OnFault,
			OnGasChange:     t.OnGasChange,
			OnBalanceChange: t.OnBalanceChange,
			OnNonceChange:   t.OnNonceChange,
			OnCodeChange:    t.OnCodeChange,
			OnStorageChange: t.OnStorageChange,
			OnLog:           t.OnLog,
		},
		GetResult: t.GetResult,
		Stop:      t.Stop,
	}
}

// stateSyncTxnTracer wraps another tracer and tricks it into seeing a single root call frame
// for state-sync transactions that contain multiple independent EVM calls.
type stateSyncTxnTracer struct {
	tracer                       *Tracer
	stateReceiverContractAddress common.Address
	createdTopLevel              bool
}

func (t *stateSyncTxnTracer) OnTxStart(env *tracing.VMContext, tx *types.Transaction, from common.Address) {
	if t.tracer.OnTxStart != nil {
		t.tracer.OnTxStart(env, tx, from)
	}
}

func (t *stateSyncTxnTracer) OnTxEnd(receipt *types.Receipt, err error) {
	// Close the synthetic top-level call frame before forwarding OnTxEnd,
	// but only if it was actually created (i.e., at least one OnEnter occurred).
	if t.createdTopLevel && t.tracer.OnExit != nil {
		t.tracer.OnExit(0, nil, 0, err, err != nil)
	}

	if t.tracer.OnTxEnd != nil {
		t.tracer.OnTxEnd(receipt, err)
	}
}

func (t *stateSyncTxnTracer) OnEnter(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
	if t.tracer.OnEnter != nil {
		if !t.createdTopLevel {
			// Inject a synthetic root CALL at depth 0 from BorSystemAddress to StateReceiverContract.
			t.tracer.OnEnter(0, byte(vm.CALL), params.BorSystemAddress, t.stateReceiverContractAddress, nil, 0, big.NewInt(0))
			t.createdTopLevel = true
		}

		// Shift all real calls one level deeper so they appear as sub-calls of the synthetic root.
		t.tracer.OnEnter(depth+1, typ, from, to, input, gas, value)
	}
}

func (t *stateSyncTxnTracer) OnExit(depth int, output []byte, gasUsed uint64, err error, reverted bool) {
	if t.tracer.OnExit != nil {
		t.tracer.OnExit(depth+1, output, gasUsed, err, reverted)
	}
}

func (t *stateSyncTxnTracer) OnOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	if t.tracer.OnOpcode != nil {
		t.tracer.OnOpcode(pc, op, gas, cost, scope, rData, depth+1, err)
	}
}

func (t *stateSyncTxnTracer) OnFault(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, depth int, err error) {
	if t.tracer.OnFault != nil {
		t.tracer.OnFault(pc, op, gas, cost, scope, depth+1, err)
	}
}

func (t *stateSyncTxnTracer) OnGasChange(old, new uint64, reason tracing.GasChangeReason) {
	if t.tracer.OnGasChange != nil {
		t.tracer.OnGasChange(old, new, reason)
	}
}

func (t *stateSyncTxnTracer) OnBalanceChange(addr common.Address, prev, new *big.Int, reason tracing.BalanceChangeReason) {
	if t.tracer.OnBalanceChange != nil {
		t.tracer.OnBalanceChange(addr, prev, new, reason)
	}
}

func (t *stateSyncTxnTracer) OnNonceChange(addr common.Address, prev, new uint64) {
	if t.tracer.OnNonceChange != nil {
		t.tracer.OnNonceChange(addr, prev, new)
	}
}

func (t *stateSyncTxnTracer) OnCodeChange(addr common.Address, prevCodeHash common.Hash, prevCode []byte, codeHash common.Hash, code []byte) {
	if t.tracer.OnCodeChange != nil {
		t.tracer.OnCodeChange(addr, prevCodeHash, prevCode, codeHash, code)
	}
}

func (t *stateSyncTxnTracer) OnStorageChange(addr common.Address, slot common.Hash, prev, new common.Hash) {
	if t.tracer.OnStorageChange != nil {
		t.tracer.OnStorageChange(addr, slot, prev, new)
	}
}

func (t *stateSyncTxnTracer) OnLog(log *types.Log) {
	if t.tracer.OnLog != nil {
		t.tracer.OnLog(log)
	}
}

func (t *stateSyncTxnTracer) GetResult() (json.RawMessage, error) {
	if t.tracer.GetResult != nil {
		return t.tracer.GetResult()
	}
	return json.RawMessage{}, nil
}

func (t *stateSyncTxnTracer) Stop(err error) {
	if t.tracer.Stop != nil {
		t.tracer.Stop(err)
	}
}
