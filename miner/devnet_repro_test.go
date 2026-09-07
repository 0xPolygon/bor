//go:build devnet_repro

package miner

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

// TestCommitFakeTransactionUsesSpoofedSender verifies that commitFakeTransaction
// attributes the state change to FakeTxSpec.From, not to whatever address would
// be recovered from the wrapping transaction's (irrelevant) signature.
func TestCommitFakeTransactionUsesSpoofedSender(t *testing.T) {
	w, _ := newCliqueWorkerForSizeTest(t)
	env := newSizeTestEnv(t, w)
	defer env.discard()

	spoofed := common.HexToAddress("0x0000000000000000000000000000000000009999")
	// Fund the spoofed account directly in the test env's state — on the real
	// clone this balance already exists on-chain; here we seed it explicitly.
	env.state.AddBalance(spoofed, uint256.MustFromBig(big.NewInt(1_000_000_000_000_000_000)), 0)

	spec := FakeTxSpec{
		From:     spoofed,
		To:       common.HexToAddress("0x000000000000000000000000000000000000aa"),
		Data:     nil,
		Value:    big.NewInt(0),
		GasLimit: 21000,
		GasPrice: big.NewInt(1_000_000_000),
	}

	nonceBefore := env.state.GetNonce(spoofed)
	tcountBefore := env.tcount

	err := commitFakeTransaction(env, spec)
	require.NoError(t, err)

	require.Equal(t, tcountBefore+1, env.tcount, "env.tcount must grow by exactly 1")
	require.Equal(t, nonceBefore+1, env.state.GetNonce(spoofed), "spoofed sender's nonce must be incremented, proving it was used as msg.From")
	require.Len(t, env.receipts, 1)
	require.Equal(t, types.ReceiptStatusSuccessful, env.receipts[0].Status)
}

// TestDevnetInjectFakeTxNoopWhenNothingStaged verifies the hook does nothing
// when no fake tx has been staged.
func TestDevnetInjectFakeTxNoopWhenNothingStaged(t *testing.T) {
	w, _ := newCliqueWorkerForSizeTest(t)
	env := newSizeTestEnv(t, w)
	defer env.discard()

	tcountBefore := env.tcount
	devnetInjectFakeTx(w, env)
	require.Equal(t, tcountBefore, env.tcount, "no-op when nothing staged")
}
