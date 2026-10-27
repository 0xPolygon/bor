package eip1559

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

const floorTestGasLimit = 160_000_000

// floorTestConfig mirrors the mainnet producer setup: dynamic target gas with
// 15%/70% around a target base fee, plus the floor at that same value.
func floorTestConfig(targetGwei uint64, floor bool) *params.ChainConfig {
	cfg := copyConfig(config())
	cfg.Bor.BhilaiBlock = big.NewInt(8)
	cfg.Bor.LisovoBlock = big.NewInt(20)
	cfg.Bor.DandeliBlock = big.NewInt(20)

	enabled, minPct, maxPct := true, uint64(15), uint64(70)
	target, buffer := targetGwei*params.GWei, uint64(0)
	cfg.Bor.EnableDynamicTargetGas = &enabled
	cfg.Bor.TargetGasMinPercentage = &minPct
	cfg.Bor.TargetGasMaxPercentage = &maxPct
	cfg.Bor.TargetBaseFee = &target
	cfg.Bor.BaseFeeBuffer = &buffer
	cfg.Bor.BaseFeeFloor = &floor
	return cfg
}

func floorTestParent(baseFeeGwei int64, usedPct uint64) *types.Header {
	return &types.Header{
		Number:   big.NewInt(100),
		GasLimit: floorTestGasLimit,
		GasUsed:  floorTestGasLimit * usedPct / 100,
		BaseFee:  new(big.Int).Mul(big.NewInt(baseFeeGwei), big.NewInt(params.GWei)),
	}
}

// nextHeader builds the child a producer would seal, checking validators accept it.
func nextHeader(t *testing.T, cfg *params.ChainConfig, parent *types.Header, usedPct uint64) *types.Header {
	t.Helper()
	child := &types.Header{
		Number:   new(big.Int).Add(parent.Number, common.Big1),
		GasLimit: floorTestGasLimit,
		GasUsed:  floorTestGasLimit * usedPct / 100,
		BaseFee:  CalcBaseFee(cfg, parent),
	}
	require.NoError(t, verifyBaseFeeWithinBoundaries(parent, child))
	return child
}

func gwei(v int64) *big.Int { return new(big.Int).Mul(big.NewInt(v), big.NewInt(params.GWei)) }

// TestBaseFeeFloorHoldsExactly checks the property nodes rely on: at the floor
// with normal usage the base fee repeats exactly, block after block.
func TestBaseFeeFloorHoldsExactly(t *testing.T) {
	t.Parallel()
	cfg := floorTestConfig(250, true)
	parent := floorTestParent(250, 18)
	for i, used := range []uint64{18, 14, 30, 5, 0, 50, 64, 22, 18, 40} {
		parent = nextHeader(t, cfg, parent, used)
		require.Equal(t, gwei(250), parent.BaseFee, "block %d", i)
	}
}

// TestBaseFeeFloorOffDrifts is the control: the same chain without the floor
// never repeats a base fee.
func TestBaseFeeFloorOffDrifts(t *testing.T) {
	t.Parallel()
	cfg := floorTestConfig(250, false)
	parent := floorTestParent(250, 18)
	for i := 0; i < 20; i++ {
		child := nextHeader(t, cfg, parent, 18)
		require.NotEqual(t, parent.BaseFee, child.BaseFee, "block %d", i)
		parent = child
	}
}

// TestBaseFeeFloorWalksUp checks switching the floor on during a dip: the fee
// climbs by the largest valid step and then holds at the floor.
func TestBaseFeeFloorWalksUp(t *testing.T) {
	t.Parallel()
	cfg := floorTestConfig(250, true)
	parent := floorTestParent(34, 18)
	blocks := 0
	for parent.BaseFee.Cmp(gwei(250)) < 0 {
		child := nextHeader(t, cfg, parent, 18)
		if child.BaseFee.Cmp(gwei(250)) < 0 {
			want := new(big.Int).Add(parent.BaseFee, maxBaseFeeStep(parent.BaseFee))
			require.Equal(t, want, child.BaseFee, "below the floor the fee must take the full 5%% step")
		}
		parent = child
		blocks++
	}
	require.Equal(t, gwei(250), parent.BaseFee)
	require.Equal(t, 41, blocks, "34 to 250 gwei at +5% per block")
	require.Equal(t, gwei(250), nextHeader(t, cfg, parent, 18).BaseFee)
}

// TestBaseFeeFloorRaise checks a floor raise from 250 to 300 gwei.
func TestBaseFeeFloorRaise(t *testing.T) {
	t.Parallel()
	cfg := floorTestConfig(300, true)
	parent := floorTestParent(250, 18)
	var fees []*big.Int
	for i := 0; i < 6; i++ {
		parent = nextHeader(t, cfg, parent, 18)
		fees = append(fees, parent.BaseFee)
	}
	require.Equal(t, gwei(300), fees[3], "250 to 300 takes 4 blocks")
	require.Equal(t, fees[3], fees[4])
	require.Equal(t, fees[4], fees[5])
}

// TestBaseFeeFloorLower checks a floor cut from 250 to 200 gwei: the fee
// decays naturally, then holds at the new floor.
func TestBaseFeeFloorLower(t *testing.T) {
	t.Parallel()
	cfg := floorTestConfig(200, true)
	parent := floorTestParent(250, 18)
	blocks := 0
	for parent.BaseFee.Cmp(gwei(200)) > 0 {
		child := nextHeader(t, cfg, parent, 18)
		if child.BaseFee.Cmp(gwei(200)) > 0 {
			require.Equal(t, calcBaseFee(cfg, parent), child.BaseFee, "above the floor the formula is unchanged")
		}
		parent = child
		blocks++
	}
	require.Equal(t, gwei(200), parent.BaseFee)
	require.Equal(t, 20, blocks)
	require.Equal(t, gwei(200), nextHeader(t, cfg, parent, 18).BaseFee)
}

// TestBaseFeeFloorAllowsRise checks demand still moves the fee above the floor.
func TestBaseFeeFloorAllowsRise(t *testing.T) {
	t.Parallel()
	cfg := floorTestConfig(250, true)
	parent := floorTestParent(250, 100)
	child := nextHeader(t, cfg, parent, 100)
	require.Equal(t, calcBaseFee(cfg, parent), child.BaseFee)
	require.Equal(t, 1, child.BaseFee.Cmp(gwei(250)))
}

// TestBaseFeeFloorDisabled checks every way the floor stays off.
func TestBaseFeeFloorDisabled(t *testing.T) {
	t.Parallel()
	parent := floorTestParent(100, 5)

	off := floorTestConfig(250, false)
	require.Equal(t, calcBaseFee(off, parent), CalcBaseFee(off, parent))

	unset := floorTestConfig(250, true)
	unset.Bor.BaseFeeFloor = nil
	require.Equal(t, calcBaseFee(unset, parent), CalcBaseFee(unset, parent))

	noTarget := floorTestConfig(0, true)
	require.Equal(t, calcBaseFee(noTarget, parent), CalcBaseFee(noTarget, parent))

	preLisovo := floorTestConfig(250, true)
	preLisovo.Bor.LisovoBlock = big.NewInt(1_000)
	require.Equal(t, calcBaseFee(preLisovo, parent), CalcBaseFee(preLisovo, parent))

	noParentFee := floorTestParent(100, 5)
	noParentFee.BaseFee = nil
	require.Equal(t, new(big.Int).SetUint64(params.InitialBaseFee), CalcBaseFee(floorTestConfig(250, true), noParentFee))
}
