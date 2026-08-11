package core

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/consensus/bor/registryreader"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

// reservedValidationConfig clones the unittest config with Cancun active from
// genesis and the reserved fork either at `forkBlock` (nil = never).
func reservedValidationConfig(forkBlock *big.Int) *params.ChainConfig {
	cc := *params.BorUnittestChainConfig
	bor := *cc.Bor
	cc.CancunBlock = big.NewInt(0)
	bor.ReservedBlockspaceBlock = forkBlock
	cc.Bor = &bor
	return &cc
}

// headerWithReservedGasUsed builds a post-Cancun header whose Extra encodes a
// BlockExtraData carrying the given ReservedGasUsed (nil = field absent).
func headerWithReservedGasUsed(t *testing.T, number int64, reserved *uint64) *types.Header {
	t.Helper()
	enc, err := rlp.EncodeToBytes(&types.BlockExtraData{TxDependency: [][]uint64{}})
	require.NoError(t, err)
	extra := make([]byte, types.ExtraVanityLength)
	extra = append(extra, enc...)
	extra = append(extra, make([]byte, types.ExtraSealLength)...)
	h := &types.Header{Number: big.NewInt(number), Extra: extra}
	if reserved != nil {
		require.NoError(t, h.SetReservedGasUsed(*reserved))
	}
	return h
}

// TestValidateReservedGasUsed covers the anti-cheat guard: the header's
// stamped ReservedGasUsed must equal the gas the reserved (fee-free) txs
// actually used (res.ReservedGasUsed), post-fork; pre-fork the check is skipped.
func TestValidateReservedGasUsed(t *testing.T) {
	t.Parallel()
	fork := reservedValidationConfig(big.NewInt(0))
	n := uint64(50_000)

	cases := []struct {
		name           string
		cfg            *params.ChainConfig
		number         int64
		headerReserved *uint64 // nil = field absent
		resReserved    uint64
		wantMismatch   bool
	}{
		{"match", fork, 100, &n, 50_000, false},
		{"mismatch is rejected", fork, 100, &n, 40_000, true},
		{"absent header field compares as 0 — matches res 0", fork, 100, nil, 0, false},
		{"absent header field, res nonzero — mismatch", fork, 100, nil, 30_000, true},
		// Boundary: fork at block 100.
		{"pre-fork (N-1) skips the check regardless", reservedValidationConfig(big.NewInt(100)), 99, nil, 99_999, false},
		{"at fork (N) enforces", reservedValidationConfig(big.NewInt(100)), 100, &n, 40_000, true},
		{"post-fork (N+1) enforces", reservedValidationConfig(big.NewInt(100)), 101, &n, 40_000, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := &BlockValidator{config: tc.cfg}
			h := headerWithReservedGasUsed(t, tc.number, tc.headerReserved)
			err := v.validateReservedGasUsed(h, &ProcessResult{ReservedGasUsed: tc.resReserved})
			if tc.wantMismatch {
				require.ErrorIs(t, err, ErrReservedGasUsedMismatch)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestSumReservedGasUsed covers the receipt↔tx hash matching, empty-set
// short-circuit, and the missing-receipt (contributes 0) branch.
func TestSumReservedGasUsed(t *testing.T) {
	t.Parallel()
	signer := types.LatestSignerForChainID(big.NewInt(1))
	keyA, _ := crypto.GenerateKey()
	keyB, _ := crypto.GenerateKey()
	a := crypto.PubkeyToAddress(keyA.PublicKey)
	b := crypto.PubkeyToAddress(keyB.PublicKey)

	to := crypto.PubkeyToAddress(keyA.PublicKey)
	mkTx := func(key *ecdsa.PrivateKey, nonce uint64) *types.Transaction {
		tx, err := types.SignNewTx(key, signer, &types.DynamicFeeTx{
			ChainID: big.NewInt(1), Nonce: nonce, Gas: 21000, To: &to,
			GasTipCap: big.NewInt(0), GasFeeCap: big.NewInt(0),
		})
		require.NoError(t, err)
		return tx
	}
	txA0 := mkTx(keyA, 0)
	txA1 := mkTx(keyA, 1)
	txB0 := mkTx(keyB, 0)
	txs := types.Transactions{txA0, txA1, txB0}
	receipts := types.Receipts{
		{TxHash: txA0.Hash(), GasUsed: 21_000},
		{TxHash: txA1.Hash(), GasUsed: 30_000},
		{TxHash: txB0.Hash(), GasUsed: 40_000},
	}

	// a0 and b0 reserved; a1 not in the set. Matched by hash regardless of
	// order, but indexes are positions within txs (txA0=0, txA1=1, txB0=2).
	reserved := map[registryreader.ReservedKey]struct{}{
		{From: a, Nonce: 0}: {},
		{From: b, Nonce: 0}: {},
	}
	gas, idx := sumReservedGasUsed(txs, receipts, signer, reserved)
	require.Equal(t, uint64(21_000+40_000), gas)
	require.Equal(t, []uint64{0, 2}, idx)

	// Empty set short-circuits to (0, nil).
	gas, idx = sumReservedGasUsed(txs, receipts, signer, nil)
	require.Zero(t, gas)
	require.Nil(t, idx)
	gas, idx = sumReservedGasUsed(txs, receipts, signer, map[registryreader.ReservedKey]struct{}{})
	require.Zero(t, gas)
	require.Nil(t, idx)

	// A reserved key whose tx has no receipt contributes 0 gas (cannot happen
	// in a valid block; guards against a silent nonzero on a malformed input)
	// but is still reported as reserved by index - classification does not
	// depend on receipt presence.
	onlyA1 := map[registryreader.ReservedKey]struct{}{{From: a, Nonce: 1}: {}}
	gas, idx = sumReservedGasUsed(txs, types.Receipts{{TxHash: txA0.Hash(), GasUsed: 21_000}}, signer, onlyA1)
	require.Zero(t, gas)
	require.Equal(t, []uint64{1}, idx)
}
