package core

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// ErrNonPaymentGasCap is returned when non-payment transactions use more gas
// in a block than the payment lane allows.
var ErrNonPaymentGasCap = errors.New("non-payment gas above payment lane cap")

// NonPaymentGas returns the gas not counted toward the payment share.
func NonPaymentGas(used, payment uint64) uint64 {
	return used - min(payment, used)
}

// PaymentLaneCapacityFromLogs sums the gas PaymentLaneDiscount logs count
// toward the block's payment share. Logs are covered by the receipts root,
// so the sum is consensus data, and validators recompute them from their own
// execution. Only code running at the fee address could emit one, and the
// genesis contract there never emits this topic; replacing that code is
// itself a hardfork.
func PaymentLaneCapacityFromLogs(logs []*types.Log) uint64 {
	var gas uint64

	for _, l := range logs {
		if isPaymentLaneLog(l) {
			gas += laneLogWord(l.Data, 3)
		}
	}

	return gas
}

// laneLogWord reads word i of a PaymentLaneDiscount log as a uint64: the gas
// words are written from uint64s, so the high bytes are always zero.
func laneLogWord(data []byte, i int) uint64 {
	return binary.BigEndian.Uint64(data[i*32+24 : (i+1)*32])
}

// NonPaymentGasCap returns the non-payment gas budget of a block, if the
// payment lane caps it at that height.
func NonPaymentGasCap(config *params.ChainConfig, header *types.Header) (uint64, bool) {
	if config == nil || config.Bor == nil {
		return 0, false
	}

	lane := config.Bor.PaymentLaneAt(header.Number)
	if lane == nil {
		return 0, false
	}

	return lane.NonPaymentGasCap(header.GasLimit)
}

// checkNonPaymentBudget enforces the block builder's non-payment budget. It
// runs before the transaction's state is finalised, so the builder can still
// revert it: one execution decides admission.
func checkNonPaymentBudget(evm *vm.EVM, result *ExecutionResult) error {
	budget := evm.Config.NonPaymentGasBudget
	if budget == nil {
		return nil
	}

	if used := NonPaymentGas(result.UsedGas, result.PaymentLaneCapacityGas); used > *budget {
		return fmt.Errorf("%w: %d > %d", ErrNonPaymentGasCap, used, *budget)
	}

	return nil
}

// ValidatePaymentLaneCapacity checks that non-payment gas stays within the
// payment lane cap, so payments always keep their share of the block.
func ValidatePaymentLaneCapacity(config *params.ChainConfig, header *types.Header, receipts types.Receipts) error {
	limit, ok := NonPaymentGasCap(config, header)
	if !ok {
		return nil
	}

	var payment uint64
	for _, r := range receipts {
		payment += PaymentLaneCapacityFromLogs(r.Logs)
	}

	if nonPayment := NonPaymentGas(header.GasUsed, payment); nonPayment > limit {
		return fmt.Errorf("%w: %d > %d", ErrNonPaymentGasCap, nonPayment, limit)
	}

	return nil
}

// PaymentLaneReceipt is the payment lane part of a transaction's fee, read
// back from its PaymentLaneDiscount log.
type PaymentLaneReceipt struct {
	Gas     uint64
	BaseCut *big.Int
	TipCut  *big.Int
}

// PaymentLaneFromLogs returns the lane record of a transaction, if it made a
// payment. A direct payment attempt that failed or moved nothing is logged
// with zero payment gas only for its share of the block, and has none.
func PaymentLaneFromLogs(logs []*types.Log) (PaymentLaneReceipt, bool) {
	for _, l := range logs {
		if isPaymentLaneLog(l) && laneLogWord(l.Data, 0) > 0 {
			return PaymentLaneReceipt{
				Gas:     laneLogWord(l.Data, 0),
				BaseCut: new(big.Int).SetBytes(l.Data[32:64]),
				TipCut:  new(big.Int).SetBytes(l.Data[64:96]),
			}, true
		}
	}

	return PaymentLaneReceipt{}, false
}

// isPaymentLaneLog matches the PaymentLaneDiscount log. Receipt log slices
// can hold nil entries (e.g. preconfirmation receipts served over RPC).
func isPaymentLaneLog(l *types.Log) bool {
	return l != nil && l.Address == feeAddress && len(l.Topics) > 0 && l.Topics[0] == paymentLaneLogSig && len(l.Data) == 128
}
