package vm

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

// A block context priced by a chain that carries a lane scale must be metered
// with the lane parameters, or the scale would silently do nothing. When the
// two disagree, NewEVM fails safe to full price instead of trusting a scale
// nothing can apply.
func TestNewEVMPaymentLaneScaleWithoutLaneParams(t *testing.T) {
	t.Parallel()

	asset := common.HexToAddress("0x70ce")

	withLane := func() *params.ChainConfig {
		cfg := *params.BorUnittestChainConfig
		bor := *cfg.Bor
		bor.PaymentLaneBlock = big.NewInt(1)
		bor.PaymentLane = map[string]*params.PaymentLaneConfig{"1": {Assets: []common.Address{asset}, ActiveGasPercentage: 70}}
		cfg.Bor = &bor

		return &cfg
	}
	withoutLane := func() *params.ChainConfig {
		cfg := *params.BorUnittestChainConfig
		bor := *cfg.Bor
		bor.PaymentLaneBlock = nil
		bor.PaymentLane = nil
		cfg.Bor = &bor

		return &cfg
	}
	blockCtx := func(scale uint64) BlockContext {
		return BlockContext{
			CanTransfer:         func(StateDB, common.Address, *uint256.Int) bool { return true },
			Transfer:            func(StateDB, common.Address, common.Address, *uint256.Int) {},
			BlockNumber:         big.NewInt(1),
			Difficulty:          big.NewInt(1),
			PaymentLaneScaleBps: scale,
		}
	}

	// Scale set with the lane parameters in force at the block: kept.
	require.Equal(t, uint64(params.PaymentLaneMaxBps),
		NewEVM(blockCtx(params.PaymentLaneMaxBps), nil, withLane(), Config{}).Context.PaymentLaneScaleBps)
	// Scale set but no lane parameters: dropped, so the block prices in full.
	require.Zero(t, NewEVM(blockCtx(params.PaymentLaneMaxBps), nil, withoutLane(), Config{}).Context.PaymentLaneScaleBps)
	// No scale, lane parameters present (a congested parent): nothing to drop.
	require.Zero(t, NewEVM(blockCtx(0), nil, withLane(), Config{}).Context.PaymentLaneScaleBps)
}
