//go:build devnet_repro

package miner

import (
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
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
type FakeTxSpec struct {
	From     common.Address
	To       common.Address
	Data     []byte
	Value    *big.Int
	GasLimit uint64
	GasPrice *big.Int
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
func devnetInjectFakeTx(w *worker, env *environment) {
	spec := takeFakeTx()
	if spec == nil {
		return
	}

	if err := commitFakeTransaction(env, *spec); err != nil {
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
func commitFakeTransaction(env *environment, spec FakeTxSpec) error {
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
		Value:     spec.Value,
		GasLimit:  spec.GasLimit,
		GasPrice:  gasPrice,
		GasFeeCap: gasFeeCap,
		GasTipCap: gasTipCap,
		Data:      spec.Data,
	}

	// tx is only used by ApplyTransactionWithEVM for receipt/tx-hash bookkeeping
	// and tracer hooks — its signature is never recovered, so any valid encoding
	// works. An unsigned legacy tx with matching fields is sufficient; its gas
	// price field is cosmetic only and not validated against real economics.
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		To:       &spec.To,
		Value:    spec.Value,
		Gas:      spec.GasLimit,
		GasPrice: gasPrice,
		Data:     spec.Data,
	})

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
