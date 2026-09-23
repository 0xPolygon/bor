//go:build devnet_repro

package miner

import (
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// devnetFakeTxGasFeeCap is a fixed fee cap comfortably above any realistic
// mainnet baseFee, used so the spoofed message always clears the
// GasFeeCap >= header.BaseFee pre-check in the state transition (see
// commitFakeTransaction).
var devnetFakeTxGasFeeCap = new(big.Int).Mul(big.NewInt(1000), big.NewInt(1_000_000_000)) // 1000 gwei

// FakeTxSpec describes a devnet-only, signature-bypassed transaction to splice
// into the next block this worker builds. See design doc
// docs/superpowers/specs/2026-09-03-heavy-contract-slow-block-repro-design.md §5.
//
// Field types are go-ethereum's RPC-friendly wire types (hexutil.Bytes,
// hexutil.Big, hexutil.Uint64), not the plain Go types (fixed by final
// review finding C1): the raw types don't decode from the hex/JSON shape the
// debug_stageFakeTx runbook invocation actually sends — encoding/json decodes
// []byte as base64, rejects a "0x..." string into *big.Int, and rejects a
// hex string into uint64. There is no GasPrice field: it was already unused
// by fee calculation (commitFakeTransaction derives its own fee fields, see
// below) and is dropped entirely rather than kept and documented.
type FakeTxSpec struct {
	From     common.Address
	To       common.Address
	Data     hexutil.Bytes
	Value    *hexutil.Big
	GasLimit hexutil.Uint64
}

var (
	fakeTxMu      sync.Mutex
	fakeTxPending *FakeTxSpec
)

// StageFakeTx stages spec to be injected into the next block this worker
// builds. Consumed once: cleared after devnetInjectFakeTx applies it,
// regardless of success or failure.
func (miner *Miner) StageFakeTx(spec FakeTxSpec) {
	fakeTxMu.Lock()
	defer fakeTxMu.Unlock()

	specCopy := spec
	fakeTxPending = &specCopy
}

// takeFakeTx returns and clears the pending spec, or nil if none is staged.
func takeFakeTx() *FakeTxSpec {
	fakeTxMu.Lock()
	defer fakeTxMu.Unlock()

	spec := fakeTxPending
	fakeTxPending = nil

	return spec
}

// ClearPendingFakeTxForTest drops any currently-staged fake tx without
// applying it. fakeTxPending is process-global (package-level), so any test —
// in this package or another that links it, e.g. package eth's debug API
// tests — that calls StageFakeTx without a subsequent devnetInjectFakeTx must
// call this in a t.Cleanup to avoid leaking a staged spec into an unrelated
// worker's next real block build in the same test binary. Exported (rather
// than kept package-private like takeFakeTx) specifically so out-of-package
// tests can reach it; only compiled under the devnet_repro tag, same as the
// rest of this file, so it never reaches a production binary.
func ClearPendingFakeTxForTest() {
	takeFakeTx()
}

// devnetInjectFakeTx applies the currently-staged fake transaction (if any) to
// env, exactly once. Called from buildAndCommitBlock immediately before
// fillTransactions, so the fake tx lands before real pool transactions are
// packed around it.
//
// A deferred recover() here is a safety net for any FUTURE bug of this shape
// inside commitFakeTransaction (I1, final review): the design's whole safety
// argument is that a failed/malformed fake-tx injection must never take down
// the real block build, so any panic reached from this call is logged and
// swallowed rather than allowed to propagate up into the real miner
// goroutine. This is defense in depth, not a substitute for fixing known
// panic causes (e.g. the nil-Value case, handled directly in
// commitFakeTransaction below).
func devnetInjectFakeTx(w *worker, env *environment) {
	devnetInjectStateDiff(env)

	spec := takeFakeTx()
	if spec == nil {
		return
	}

	defer func() {
		if r := recover(); r != nil {
			log.Error("devnet_repro: panic while injecting fake transaction, recovered", "from", spec.From, "to", spec.To, "panic", r)
		}
	}()

	if err := devnetCommitFakeTransaction(env, *spec); err != nil {
		log.Error("devnet_repro: failed to inject fake transaction", "from", spec.From, "to", spec.To, "err", err)
		return
	}

	log.Info("devnet_repro: injected fake transaction", "from", spec.From, "to", spec.To, "number", env.header.Number)
}

// commitFakeTransaction mirrors worker.commitTransaction but bypasses
// signature recovery: it builds a core.Message with spec.From set directly
// and calls core.ApplyTransactionWithEVM instead of core.ApplyTransaction, so
// no ecrecover ever runs against a real signature.
//
// Fee fields are NOT copied flatly from spec.GasPrice: core.ApplyTransactionWithEVM
// pre-checks GasFeeCap >= header.BaseFee (ErrFeeCapTooLow otherwise), and
// env.header.BaseFee is real/non-zero on a live mainnet clone. So GasFeeCap is
// fixed at a value comfortably above any realistic baseFee, GasTipCap is 0,
// and GasPrice is derived the same way core.TransactionToMessage does:
// min(GasTipCap + baseFee, GasFeeCap). See design doc §5.
// devnetCommitFakeTransaction indirects commitFakeTransaction through a
// package-level var so tests can substitute a deliberately panicking
// implementation to exercise devnetInjectFakeTx's recover() safety net
// (final review finding I1) without needing to reconstruct a real internal
// panic trigger — that safety net exists for ANY future bug of this shape,
// not only the nil-Value case commitFakeTransaction itself now guards
// against directly.
var devnetCommitFakeTransaction = commitFakeTransaction

func commitFakeTransaction(env *environment, spec FakeTxSpec) error {
	// I1 (final review): a nil spec.Value (e.g. omitted from the RPC call)
	// otherwise flows through as a nil *big.Int into core.Message.Value, and
	// state_transition.go's buyGas does balanceCheck.Add(balanceCheck,
	// st.msg.Value) — big.Int.Add with a nil operand panics, taking down the
	// whole node from the miner goroutine with no recover below this point
	// (devnetInjectFakeTx's recover() is a backstop, not a substitute for
	// this). Default nil Value to zero explicitly.
	value := new(big.Int)
	if spec.Value != nil {
		value = spec.Value.ToInt()
	}

	// A zero GasLimit is never a legitimate fake-tx request (it can't even
	// cover intrinsic gas) and is rejected rather than allowed to proceed
	// into ApplyTransactionWithEVM with confusing downstream failure modes.
	if spec.GasLimit == 0 {
		return fmt.Errorf("commitFakeTransaction: spec.GasLimit must be non-zero")
	}

	gasLimit := uint64(spec.GasLimit)
	data := []byte(spec.Data)

	// devnetInjectFakeTx runs before commitTransactions in buildAndCommitBlock,
	// so env.gasPool has not been initialized yet on the real call path (it is
	// otherwise lazily created inside commitTransactions — see the matching
	// guard there). Mirror that lazy init here or the very first fake-tx
	// injection on a fresh environment nil-derefs on env.gasPool.Gas() below.
	if env.gasPool == nil {
		env.gasPool = new(core.GasPool).AddGas(env.header.GasLimit)
	}

	nonce := env.state.GetNonce(spec.From)

	gasFeeCap := devnetFakeTxGasFeeCap
	gasTipCap := big.NewInt(0)

	gasPrice := new(big.Int).Set(gasFeeCap)
	if env.header.BaseFee != nil {
		gasPrice = new(big.Int).Add(gasTipCap, env.header.BaseFee)
		if gasPrice.Cmp(gasFeeCap) > 0 {
			gasPrice = gasFeeCap
		}
	}

	msg := &core.Message{
		From:      spec.From,
		To:        &spec.To,
		Nonce:     nonce,
		Value:     value,
		GasLimit:  gasLimit,
		GasPrice:  gasPrice,
		GasFeeCap: gasFeeCap,
		GasTipCap: gasTipCap,
		Data:      data,
	}

	// tx is only used by ApplyTransactionWithEVM for receipt/tx-hash bookkeeping
	// and tracer hooks — its signature is never recovered, so any valid encoding
	// works. An unsigned legacy tx with matching fields is sufficient; its gas
	// price field is cosmetic only and not validated against real economics.
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		To:       &spec.To,
		Value:    value,
		Gas:      gasLimit,
		GasPrice: gasPrice,
		Data:     data,
	})

	// M4 (final review): real commitTransactions calls SetTxContext
	// immediately before every commitTransaction so the state DB attributes
	// any logs emitted during execution to the right tx hash/index.
	// commitFakeTransaction must do the same or the fake tx's logs/bloom get
	// filed under a stale/zero tx hash instead of its own — silently
	// dropping them from its own receipt, which matters for reading back the
	// XEN call's emitted events (the whole point of the experiment).
	env.state.SetTxContext(tx.Hash(), env.tcount)

	snap := env.state.Snapshot()
	gp := env.gasPool.Gas()

	receipt, err := core.ApplyTransactionWithEVM(msg, env.gasPool, env.state, env.header.Number, env.header.Hash(), env.header.Time, tx, &env.header.GasUsed, env.evm)
	if err != nil {
		env.state.RevertToSnapshot(snap)
		env.gasPool.SetGas(gp)

		return fmt.Errorf("commitFakeTransaction: %w", err)
	}

	env.txs = append(env.txs, tx)
	env.receipts = append(env.receipts, receipt)
	env.tcount++
	env.size += tx.Size()

	return nil
}
