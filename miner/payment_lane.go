package miner

import (
	"errors"
	"fmt"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
)

// paymentLaneMaxRefusedPercentage bounds, as a share of the gas limit, the
// gas a block build may spend executing transactions the payment lane cap
// then refuses. Refused transactions pay nothing, so without a bound a flood
// of them would make every build re-execute work for free. Direct payment
// calls are never refused while their gas limit is within what they can
// count toward the payment share.
const paymentLaneMaxRefusedPercentage = 10

var (
	// paymentLaneRefusedCounter counts txs left out to respect the cap.
	paymentLaneRefusedCounter = metrics.NewRegisteredCounter("miner/paymentlane/refused", nil)
	// paymentLaneRefusedGasCounter counts gas executed by refused txs.
	paymentLaneRefusedGasCounter = metrics.NewRegisteredCounter("miner/paymentlane/refusedgas", nil)
)

// nonPaymentBudget returns the non-payment gas tx may use under the payment
// lane cap, nil when it cannot exceed it: a direct payment call counts up to
// core.PaymentLaneAttemptGas toward the payment share, so only the gas it
// may use beyond that needs room. Otherwise the budget is enforced by the
// transaction's own execution (core.ApplyTransaction rejects it before the
// state is finalised), so nothing runs twice and the build interrupt
// applies; a tx only runs if a refusal would still fit the build's
// allowance, so the work a build can spend on refused txs is bounded. A
// direct payment call runs even with no budget left: one with a generous gas
// limit uses no budget unless it burns more than a payment.
func (w *worker) nonPaymentBudget(env *environment, tx *types.Transaction) (*uint64, error) {
	limit, ok := core.NonPaymentGasCap(w.chainConfig, env.header)
	if !ok {
		return nil, nil
	}

	nonPayment := core.NonPaymentGas(env.header.GasUsed, env.paymentLaneGas)
	left := limit - min(nonPayment, limit)

	bound := w.paymentLaneAttemptGas(env, tx)
	if tx.Gas() <= left+bound {
		return nil, nil
	}

	if bound == 0 && left < params.TxGas || env.paymentLaneRefusedGas+tx.Gas() > env.header.GasLimit/100*paymentLaneMaxRefusedPercentage {
		paymentLaneRefusedCounter.Inc(1)
		return nil, fmt.Errorf("%w: budget exhausted", core.ErrNonPaymentGasCap)
	}

	return &left, nil
}

// refusedByNonPaymentCap accounts for a tx the cap refused after executing
// it, with the gas its execution took.
func (w *worker) refusedByNonPaymentCap(env *environment, err error, gasUsed uint64) {
	if !errors.Is(err, core.ErrNonPaymentGasCap) {
		return
	}

	env.paymentLaneRefusedGas += gasUsed
	paymentLaneRefusedCounter.Inc(1)
	paymentLaneRefusedGasCounter.Inc(int64(gasUsed))
}

func (w *worker) paymentLaneAttemptGas(env *environment, tx *types.Transaction) uint64 {
	from, err := types.Sender(env.signer, tx)
	if err != nil {
		return 0
	}

	return core.PaymentLaneAttemptGas(w.chainConfig.Bor.PaymentLaneAt(env.header.Number), from, tx.To(), tx.Data())
}

// recordPaymentLaneGas tracks the payment share of a committed tx, for the
// non-payment budget of the next ones.
func (w *worker) recordPaymentLaneGas(env *environment, receipt *types.Receipt) {
	if _, ok := core.NonPaymentGasCap(w.chainConfig, env.header); ok {
		env.paymentLaneGas += core.PaymentLaneCapacityFromLogs(receipt.Logs)
	}
}
