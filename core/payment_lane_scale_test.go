package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/stretchr/testify/require"
)

func TestPaymentLaneScaleFor(t *testing.T) {
	t.Parallel()

	cfg := laneChainConfig(t)
	cfg.Bor.PaymentLane = map[string]*params.PaymentLaneConfig{"5": cfg.Bor.PaymentLaneAt(big.NewInt(0))}
	cfg.Bor.PaymentLaneBlock = big.NewInt(5)

	quiet := &types.Header{Number: big.NewInt(4), GasLimit: 1_000, GasUsed: 100}
	busy := &types.Header{Number: big.NewInt(4), GasLimit: 1_000, GasUsed: 900}

	tests := []struct {
		name   string
		cfg    *params.ChainConfig
		number *big.Int
		parent *types.Header
		want   uint64
	}{
		{"no config", nil, big.NewInt(5), quiet, 0},
		{"no bor", params.TestChainConfig, big.NewInt(5), quiet, 0},
		{"no number", cfg, nil, quiet, 0},
		{"no parent", cfg, big.NewInt(5), nil, 0},
		{"before the fork", cfg, big.NewInt(4), quiet, 0},
		{"at the fork, quiet parent", cfg, big.NewInt(5), quiet, params.PaymentLaneMaxBps},
		{"at the fork, busy parent", cfg, big.NewInt(5), busy, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, PaymentLaneScaleFor(tt.cfg, tt.number, tt.parent))
		})
	}
}

// A node whose own EIP-1559 target is at or above the non-payment cap in force
// refuses to start, on a fresh database and on an existing one: a target at the
// cap could never be reached by non-payment demand alone, so the base fee could
// only fall.
func TestSetupGenesisRefusesTargetAtOrAboveCap(t *testing.T) {
	t.Parallel()

	newGenesis := func(target uint64) *Genesis {
		cfg := laneChainConfig(t)
		cfg.Bor.PaymentLaneAt(cfg.Bor.PaymentLaneBlock).NonPaymentGasCapPercentage = 70
		cfg.Bor.TargetGasPercentage = &target

		return &Genesis{Config: cfg, BaseFee: big.NewInt(params.InitialBaseFee)}
	}

	db := rawdb.NewMemoryDatabase()
	_, err := newGenesis(70).Commit(db, triedb.NewDatabase(db, nil))
	require.ErrorContains(t, err, "target 70% not below the non-payment gas cap 70%")

	db = rawdb.NewMemoryDatabase()
	_, _, _, err = SetupGenesisBlock(db, triedb.NewDatabase(db, nil), newGenesis(69))
	require.NoError(t, err)

	_, _, _, err = SetupGenesisBlock(db, triedb.NewDatabase(db, nil), newGenesis(70))
	require.ErrorContains(t, err, "target 70% not below the non-payment gas cap 70%")

	_, _, _, err = SetupGenesisBlock(db, triedb.NewDatabase(db, nil), newGenesis(80))
	require.ErrorContains(t, err, "target 80% not below the non-payment gas cap 70%")
}

func TestCheckChainConfigWithoutBor(t *testing.T) {
	t.Parallel()

	require.NoError(t, checkChainConfig(&params.ChainConfig{ChainID: big.NewInt(1)}, big.NewInt(1)))
}

func TestCheckChainConfigForkOrder(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{ChainID: big.NewInt(1), HomesteadBlock: big.NewInt(5), EIP150Block: big.NewInt(2)}
	require.Error(t, checkChainConfig(cfg, big.NewInt(1)))
}
