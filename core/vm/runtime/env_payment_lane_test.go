package runtime

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// The runtime has no parent header, so the payment lane gate cannot be
// evaluated. A lane-active chain must still price at full price: the scale is
// routed through the shared helper with an unknown parent, not left to a
// caller's guess.
func TestNewEnvPaymentLaneFullPriceWithoutParent(t *testing.T) {
	t.Parallel()

	chainCfg := *params.BorUnittestChainConfig
	bor := *chainCfg.Bor
	bor.PaymentLaneBlock = big.NewInt(1)
	bor.PaymentLane = map[string]*params.PaymentLaneConfig{"1": {Assets: []common.Address{common.HexToAddress("0x70ce")}, ActiveGasPercentage: 70}}
	chainCfg.Bor = &bor

	db, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)

	cfg := &Config{ChainConfig: &chainCfg, BlockNumber: big.NewInt(2), State: db}
	setDefaults(cfg)

	evm := NewEnv(cfg)
	require.NotNil(t, evm.PaymentLane(), "the lane is in force at block 2")
	require.Zero(t, evm.Context.PaymentLaneScaleBps, "no parent header means full price")
}
