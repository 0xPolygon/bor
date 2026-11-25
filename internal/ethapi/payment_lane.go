package ethapi

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/ethapi/override"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// addPaymentLaneFields adds a "paymentLane" object to a marshalled receipt
// when the transaction went through the payment lane. effectiveGasPrice stays
// the nominal price (it is derived from the tx alone); "fee" is what the
// sender actually paid.
func addPaymentLaneFields(fields map[string]interface{}, receipt *types.Receipt) {
	lane, ok := core.PaymentLaneFromLogs(receipt.Logs)
	if !ok {
		return
	}

	discount := new(big.Int).Add(lane.BaseCut, lane.TipCut)
	fee := new(big.Int)

	if receipt.EffectiveGasPrice != nil {
		fee.Mul(new(big.Int).SetUint64(receipt.GasUsed), receipt.EffectiveGasPrice)
		fee.Sub(fee, discount)
	}

	fields["paymentLane"] = map[string]interface{}{
		"gas":             hexutil.Uint64(lane.Gas),
		"baseFeeDiscount": (*hexutil.Big)(lane.BaseCut),
		"tipDiscount":     (*hexutil.Big)(lane.TipCut),
		"fee":             (*hexutil.Big)(fee),
	}
}

// PaymentLaneQuote simulates args on the latest block and prices the result
// as the next block would: its base fee, the lane discount scale derived from
// the latest block, and the tip from args (or the suggested tip). Wallets use
// it to show the fee a payment will actually pay.
//
// The discount is refunded to the transaction signer (msg.From), the account
// that paid for the gas. With a relayer, bundler or paymaster that account is
// not the end user, so the user may never see the discount this quote reports.
// In the response, discount is that signer refund and fee is the nominal fee
// minus it.
func (api *BorAPI) PaymentLaneQuote(ctx context.Context, args TransactionArgs) (map[string]interface{}, error) {
	header, err := api.b.HeaderByNumber(ctx, rpc.LatestBlockNumber)
	if err != nil {
		return nil, fmt.Errorf("latest header unavailable: %w", err)
	}

	if header == nil {
		return nil, errors.New("latest header unavailable")
	}

	// The next base fee is an estimate: from Lisovo on, block producers tune
	// the EIP-1559 target at runtime (dynamic target around a base-fee
	// setpoint), so a node without that config is typically ~1% and up to a
	// few percent off the produced value.
	baseFee := eip1559.CalcBaseFee(api.b.ChainConfig(), header)

	res, err := api.simulateNextBlock(ctx, args, header, baseFee)
	if err != nil {
		return nil, err
	}

	tip, err := api.quoteTip(ctx, args, baseFee)
	if err != nil {
		return nil, err
	}

	lane, scale := nextBlockLane(api.b.ChainConfig(), header)
	nominal := new(big.Int).Mul(new(big.Int).SetUint64(res.UsedGas), new(big.Int).Add(baseFee, tip))
	discount := core.PaymentLaneDiscountFor(lane, baseFee, tip, scale, res.PaymentLaneGas)

	return map[string]interface{}{
		"gasUsed":    hexutil.Uint64(res.UsedGas),
		"paymentGas": hexutil.Uint64(res.PaymentLaneGas),
		"baseFee":    (*hexutil.Big)(baseFee),
		"tip":        (*hexutil.Big)(tip),
		"scaleBps":   hexutil.Uint64(scale),
		"nominalFee": (*hexutil.Big)(nominal),
		"discount":   (*hexutil.Big)(discount),
		"fee":        (*hexutil.Big)(new(big.Int).Sub(nominal, discount)),
	}, nil
}

// simulateNextBlock runs args on the latest state as the next block (its
// number, so the lane fork rules apply at the activation boundary) and
// without fee fields: pricing is done by the caller, and the call must not
// fail on a fee cap meant for that block.
func (api *BorAPI) simulateNextBlock(ctx context.Context, args TransactionArgs, header *types.Header, baseFee *big.Int) (*core.ExecutionResult, error) {
	sim := args
	sim.GasPrice, sim.MaxFeePerGas, sim.MaxPriorityFeePerGas = nil, nil, nil

	latest := rpc.BlockNumberOrHashWithHash(header.Hash(), false)
	next := &override.BlockOverrides{Number: (*hexutil.Big)(new(big.Int).Add(header.Number, common.Big1)), BaseFeePerGas: (*hexutil.Big)(baseFee)}

	res, err := DoCall(ctx, api.b, sim, latest, nil, nil, next, api.b.RPCEVMTimeout(), api.b.RPCGasCap())
	if err != nil {
		return nil, err
	}

	if res.Failed() {
		if len(res.Revert()) == 0 {
			return nil, res.Err
		}

		return nil, newRevertError(res.Revert())
	}

	return res, nil
}

// nextBlockLane returns the lane config and discount scale for the block
// after header, or nil and 0 when the lane is not active there.
func nextBlockLane(cfg *params.ChainConfig, header *types.Header) (*params.PaymentLaneConfig, uint64) {
	if cfg.Bor == nil {
		return nil, 0
	}

	lane := cfg.Bor.PaymentLaneAt(new(big.Int).Add(header.Number, common.Big1))
	if lane == nil {
		return nil, 0
	}

	return lane, lane.ScaleForParent(header.GasUsed, header.GasLimit)
}

// quoteTip is the per-gas tip the quote assumes: the caller's, bounded by its
// fee cap, or the node's suggestion.
func (api *BorAPI) quoteTip(ctx context.Context, args TransactionArgs, baseFee *big.Int) (*big.Int, error) {
	var tip *big.Int

	switch {
	case args.MaxPriorityFeePerGas != nil:
		tip = new(big.Int).Set(args.MaxPriorityFeePerGas.ToInt())
	case args.GasPrice != nil:
		return nonNegative(new(big.Int).Sub(args.GasPrice.ToInt(), baseFee)), nil
	default:
		suggested, err := api.b.SuggestGasTipCap(ctx)
		if err != nil {
			return nil, err
		}

		tip = suggested
	}

	if args.MaxFeePerGas != nil {
		if headroom := new(big.Int).Sub(args.MaxFeePerGas.ToInt(), baseFee); headroom.Cmp(tip) < 0 {
			tip = headroom
		}
	}

	return nonNegative(tip), nil
}

func nonNegative(v *big.Int) *big.Int {
	if v.Sign() < 0 {
		return new(big.Int)
	}

	return v
}
