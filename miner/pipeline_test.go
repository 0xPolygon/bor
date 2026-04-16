package miner

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestShouldLateRefillSpeculativeBlock(t *testing.T) {
	t.Parallel()

	newEnv := func(txs int, gasLimit uint64, remainingGas uint64, withGasPool bool) *environment {
		env := &environment{
			header: &types.Header{
				Number:   big.NewInt(1),
				GasLimit: gasLimit,
			},
			txs: make([]*types.Transaction, txs),
		}
		if withGasPool {
			env.gasPool = new(core.GasPool).AddGas(remainingGas)
		}
		return env
	}

	require.True(t, shouldLateRefillSpeculativeBlock(newEnv(0, 1000, 0, false)))
	require.True(t, shouldLateRefillSpeculativeBlock(newEnv(1, 1000, 600, true)))
	require.True(t, shouldLateRefillSpeculativeBlock(newEnv(2, 1000, 0, false)))
	require.False(t, shouldLateRefillSpeculativeBlock(newEnv(1, 1000, 200, true)))
}
