package tracing

import "sync/atomic"

// ExecSegments accumulates coarse per-segment wall time of transaction
// execution (lab instrumentation). Attach one instance per block/build via
// vm.Config.Segments; nil (the default) disables all recording. The derived
// remainder ApplyNs - PrecheckNs - EVMNs is the refund/fee-settlement tail.
type ExecSegments struct {
	TxN        atomic.Int64 // transactions measured
	MsgConvNs  atomic.Int64 // TransactionToMessage: sender recovery + msg fields
	ApplyNs    atomic.Int64 // ApplyMessage total (precheck + EVM + refunds)
	PrecheckNs atomic.Int64 // nonce/balance checks, buyGas, intrinsic gas, Prepare
	EVMNs      atomic.Int64 // evm.Call / evm.Create only
	FinaliseNs atomic.Int64 // per-tx statedb.Finalise / IntermediateRoot
	ReceiptNs  atomic.Int64 // MakeReceipt: GetLogs + bloom

	// Block-level segments of StateProcessor.Process (import path):
	ProcPrologNs atomic.Int64 // Process entry -> tx loop (EVM setup, system calls)
	ProcLoopNs   atomic.Int64 // whole tx loop wall (per-tx sums + loop overhead)
	ProcFinalNs  atomic.Int64 // engine.Finalize: bor spans + state-sync events
}

// SnapshotUs returns the segment sums in microseconds, keyed for the trace
// records ("tx_n" is a count, not a duration).
func (s *ExecSegments) SnapshotUs() map[string]int64 {
	if s == nil {
		return nil
	}
	return map[string]int64{
		"tx_n":        s.TxN.Load(),
		"msg_conv_us": s.MsgConvNs.Load() / 1e3,
		"apply_us":    s.ApplyNs.Load() / 1e3,
		"precheck_us": s.PrecheckNs.Load() / 1e3,
		"evm_us":      s.EVMNs.Load() / 1e3,
		"finalise_us": s.FinaliseNs.Load() / 1e3,
		"receipt_us":  s.ReceiptNs.Load() / 1e3,

		"proc_prolog_us": s.ProcPrologNs.Load() / 1e3,
		"proc_loop_us":   s.ProcLoopNs.Load() / 1e3,
		"proc_final_us":  s.ProcFinalNs.Load() / 1e3,
	}
}
