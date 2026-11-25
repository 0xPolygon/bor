package core

import (
	"encoding/binary"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	cmath "github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

var (
	bpsDenominator = big.NewInt(10_000)

	// keccak256("PaymentLaneDiscount(address,uint256,uint256,uint256,uint256)")
	paymentLaneLogSig = common.HexToHash("0x0b5685dffe5490f24515c0abaaf545f7393b5308690bfe2029a17419c97a2f06")
)

// paymentLaneCut is what the payment lane does not charge a transaction.
// The amounts are always non-nil so callers can subtract them
// unconditionally, and read-only: without a discount they share
// common.Big0, so no transaction pays an allocation for the lane.
type paymentLaneCut struct {
	gas      uint64 // payment gas, priced at the discount
	capacity uint64 // gas the tx counts toward the block's payment share
	baseCut  *big.Int
	tipCut   *big.Int
	refund   *big.Int // baseCut + tipCut
}

func (c paymentLaneCut) annotate(res *ExecutionResult) *ExecutionResult {
	res.PaymentLaneGas = c.gas
	res.PaymentLaneCapacityGas = c.capacity
	res.PaymentLaneRefund = c.refund

	return res
}

// paymentLaneDiscount returns how much of the base fee and of the tip is not
// charged for the transaction's payment gas. Gas used is unchanged; only the
// price of the payment share of it is.
func (st *stateTransition) paymentLaneDiscount(vmerr error, effectiveTip *big.Int) paymentLaneCut {
	cut := paymentLaneCut{baseCut: common.Big0, tipCut: common.Big0, refund: common.Big0}

	cut.gas = st.paymentLaneGasUsed(vmerr)
	cut.capacity = st.paymentLaneCapacity(cut.gas)

	scale := st.evm.Context.PaymentLaneScaleBps
	if cut.gas == 0 || scale == 0 || st.evm.Context.BaseFee == nil || effectiveTip.Sign() < 0 {
		return cut
	}

	gas := new(big.Int).SetUint64(cut.gas)

	// Per-gas cuts are floored, then scaled and floored again, before
	// multiplying by gas, so the result does not depend on how the gas is
	// split across frames.
	lane := st.evm.PaymentLane()
	cut.baseCut = scaledPerGasCut(baseFeeCutPerGas(st.evm.Context.BaseFee, lane), scale, gas)
	cut.tipCut = scaledPerGasCut(tipCutPerGas(effectiveTip, lane), scale, gas)
	cut.refund = new(big.Int).Add(cut.baseCut, cut.tipCut)

	return cut
}

// settlePaymentLane prices the payment share and applies it to the nominal
// tip and burnt base fee before those amounts are credited. The burn credit
// stays here, ahead of the refund, so the journal order matches the inline
// settlement this replaced. Upstream fee settlement in execute only has to
// call this and annotate the result.
func (st *stateTransition) settlePaymentLane(vmerr error, effectiveTip, amount, burnAmount *big.Int, burntContract common.Address) paymentLaneCut {
	cut := st.paymentLaneDiscount(vmerr, effectiveTip)
	amount.Sub(amount, cut.tipCut)

	if burnAmount != nil {
		burnAmount.Sub(burnAmount, cut.baseCut)
		if !st.noFeeBurnAndTip {
			st.state.AddBalance(burntContract, cmath.BigIntToUint256Int(burnAmount), tracing.BalanceChangeTransfer)
		}
	}

	st.refundPaymentLane(cut)

	return cut
}

func bps(v *big.Int, b uint64) *big.Int {
	out := new(big.Int).Mul(v, new(big.Int).SetUint64(b))
	return out.Div(out, bpsDenominator)
}

func baseFeeCutPerGas(baseFee *big.Int, lane *params.PaymentLaneConfig) *big.Int {
	return bps(baseFee, lane.BaseFeeDiscountBps)
}

// tipCutPerGas applies the tip discount, then the cap on what is left.
func tipCutPerGas(tip *big.Int, lane *params.PaymentLaneConfig) *big.Int {
	charged := new(big.Int).Sub(tip, bps(tip, lane.TipDiscountBps))
	if lane.TipCap != nil && charged.Cmp(lane.TipCap) > 0 {
		charged.Set(lane.TipCap)
	}

	return charged.Sub(tip, charged)
}

func scaledPerGasCut(perGas *big.Int, scale uint64, gas *big.Int) *big.Int {
	out := bps(perGas, scale)

	return out.Mul(out, gas)
}

// refundPaymentLane credits the sender and records the discount in a log, so
// indexers can reconcile the fee without re-executing: receipts still carry
// the nominal effectiveGasPrice, which is derived from the tx alone. The
// refund is part of the transaction's own state changes, so it is applied
// even when burn and tip are deferred to the caller (serial and BlockSTM).
//
// With a non-payment gas cap configured, every payment is logged, discounted
// or not, because block validity is then checked from the receipts.
func (st *stateTransition) refundPaymentLane(cut paymentLaneCut) {
	if cut.refund.Sign() > 0 {
		st.state.AddBalance(st.msg.From, cmath.BigIntToUint256Int(cut.refund), tracing.BalanceIncreaseGasReturn)
	}

	lane := st.evm.PaymentLane()
	logAll := lane != nil && lane.NonPaymentGasCapPercentage > 0
	if cut.capacity == 0 || (cut.refund.Sign() == 0 && !logAll) {
		return
	}

	data := make([]byte, 128)
	binary.BigEndian.PutUint64(data[24:32], cut.gas)
	cut.baseCut.FillBytes(data[32:64])
	cut.tipCut.FillBytes(data[64:96])
	binary.BigEndian.PutUint64(data[120:128], cut.capacity)

	st.state.AddLog(&types.Log{
		Address: feeAddress,
		Topics:  []common.Hash{paymentLaneLogSig, st.msg.From.Hash()},
		Data:    data,
	})
}

// paymentLaneGasUsed is the payment gas of the transaction, independent of
// the discount scale: it also feeds the per-block payment capacity check.
// A failed transaction made no payment; frames already dropped their credits
// on revert, but a failure after the last frame (e.g. EIP-7623 floor
// accounting) must not leave a credit behind either. When the transaction
// itself is a payment call, its base cost and the calldata of the payment
// are payment gas too; trailing calldata, access lists and authorizations
// are not. Refunds are credited after the frames ran, so the credit is
// capped at the final gas used.
func (st *stateTransition) paymentLaneGasUsed(vmerr error) uint64 {
	if vmerr != nil {
		return 0
	}

	laneGas, txCallLen := st.evm.PaymentLaneGas()
	if txCallLen > 0 {
		// The same intrinsic gas the transaction paid for that calldata,
		// without access list or authorizations.
		intrinsic, err := IntrinsicGas(st.msg.Data[:txCallLen], nil, nil, false, true, true, false)
		if err == nil {
			laneGas += intrinsic
		}
	}

	return min(laneGas, st.gasUsed())
}

// PaymentLaneAttemptGas is the most gas a transaction from `from` to `to`
// with data counts toward the payment share of a block when it is a direct
// payment attempt, 0 otherwise: the base cost and canonical calldata of the
// payment plus the bound of its frame credit. Access lists, authorizations,
// trailing calldata and gas burnt beyond the frame bound (e.g. in a
// signature callback) are not payment gas.
func PaymentLaneAttemptGas(lane *params.PaymentLaneConfig, from common.Address, to *common.Address, data []byte) uint64 {
	if to == nil {
		return 0
	}

	callLen, maxGas := vm.DirectPaymentCall(lane, from, *to, data)
	if callLen == 0 {
		return 0
	}

	intrinsic, err := IntrinsicGas(data[:callLen], nil, nil, false, true, true, false)
	if err != nil {
		return 0
	}

	return intrinsic + maxGas
}

// paymentLaneCapacity is the gas the transaction counts toward the payment
// share of the block under the non-payment cap. A direct payment attempt
// counts whether or not it ends up discounted, up to PaymentLaneAttemptGas:
// a payment that fails or moves nothing still pays full price and uses the
// payment share, instead of being refused for free by the block builder.
func (st *stateTransition) paymentLaneCapacity(laneGas uint64) uint64 {
	if bound := PaymentLaneAttemptGas(st.evm.PaymentLane(), st.msg.From, st.msg.To, st.msg.Data); bound > 0 {
		return min(st.gasUsed(), bound)
	}

	return laneGas
}

// PaymentLaneDiscountFor returns what a transaction with laneGas payment gas
// is not charged at the given base fee, effective tip and discount scale. It
// is the same arithmetic the state transition applies, for fee quotes.
func PaymentLaneDiscountFor(lane *params.PaymentLaneConfig, baseFee, effectiveTip *big.Int, scale, laneGas uint64) *big.Int {
	if lane == nil || baseFee == nil || effectiveTip.Sign() < 0 {
		return new(big.Int)
	}

	gas := new(big.Int).SetUint64(laneGas)
	discount := scaledPerGasCut(baseFeeCutPerGas(baseFee, lane), scale, gas)

	return discount.Add(discount, scaledPerGasCut(tipCutPerGas(effectiveTip, lane), scale, gas))
}
