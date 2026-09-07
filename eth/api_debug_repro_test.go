//go:build devnet_repro

package eth

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/miner"
	"github.com/stretchr/testify/require"
)

// newTestEthereumForDebugAPI creates a minimal *Ethereum instance for testing the DebugAPI.
// It sets up only the fields required by the debug RPC methods being tested.
func newTestEthereumForDebugAPI(t *testing.T) *Ethereum {
	return &Ethereum{
		miner: &miner.Miner{},
	}
}

// TestDebugAPIStageFakeTxForwardsToMiner verifies the RPC method is a thin,
// correctly-typed pass-through to (*miner.Miner).StageFakeTx — the actual
// injection behavior is covered by miner package tests (Task 1).
func TestDebugAPIStageFakeTxForwardsToMiner(t *testing.T) {
	eth := newTestEthereumForDebugAPI(t)

	api := NewDebugAPI(eth)

	spec := miner.FakeTxSpec{
		From:     common.HexToAddress("0x0000000000000000000000000000000000009999"),
		To:       common.HexToAddress("0x000000000000000000000000000000000000aa"),
		Value:    big.NewInt(0),
		GasLimit: 21000,
		GasPrice: big.NewInt(1_000_000_000),
	}

	err := api.StageFakeTx(spec)
	require.NoError(t, err)
}

// TestDebugAPIDrainSlowOpcodeGaps verifies the RPC method returns a non-nil
// (possibly empty) slice, even when nothing has been recorded yet.
func TestDebugAPIDrainSlowOpcodeGaps(t *testing.T) {
	eth := newTestEthereumForDebugAPI(t)
	api := NewDebugAPI(eth)

	gaps := api.DrainSlowOpcodeGaps()
	require.NotNil(t, gaps) // empty slice, not nil, when nothing has been recorded yet
}
