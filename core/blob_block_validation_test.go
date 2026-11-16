// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
)

func blobTestTx(t *testing.T) *types.Transaction {
	t.Helper()
	return types.NewTx(&types.BlobTx{
		ChainID:    uint256.NewInt(1),
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(1),
		Gas:        params.TxGas,
		To:         common.Address{1},
		BlobFeeCap: uint256.NewInt(1),
		BlobHashes: []common.Hash{{0x01}},
	})
}

// TestValidateBlobUsage checks the body/header blob-usage agreement logic in
// isolation, including the "data blobs present" guard whose absence let a blob
// transaction reach pricing in a block whose header declared no blob gas.
func TestValidateBlobUsage(t *testing.T) {
	t.Parallel()

	oneBlob := uint64(params.BlobTxBlobGasPerBlob)
	twoBlobs := 2 * oneBlob
	plain := types.NewTx(&types.LegacyTx{Gas: params.TxGas, To: &common.Address{1}})
	blob := blobTestTx(t)

	withSidecar := types.NewTx(&types.BlobTx{
		ChainID: uint256.NewInt(1), Gas: params.TxGas, To: common.Address{1},
		BlobHashes: []common.Hash{{0x01}},
		Sidecar:    &types.BlobTxSidecar{},
	})

	tests := []struct {
		name        string
		blobGasUsed *uint64
		txs         types.Transactions
		wantErr     string
	}{
		{name: "no blobs, header nil", txs: types.Transactions{plain}},
		{name: "blob without blob gas in header", txs: types.Transactions{blob}, wantErr: "data blobs present in block body"},
		{name: "blob with matching blob gas", blobGasUsed: &oneBlob, txs: types.Transactions{blob}},
		{name: "blob with mismatched blob gas", blobGasUsed: &twoBlobs, txs: types.Transactions{blob}, wantErr: "blob gas used mismatch"},
		{name: "no blobs but header counts gas", blobGasUsed: &oneBlob, txs: types.Transactions{plain}, wantErr: "blob gas used mismatch"},
		{name: "sidecar attached", blobGasUsed: &oneBlob, txs: types.Transactions{withSidecar}, wantErr: "unexpected blob sidecar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBlobUsage(&types.Header{BlobGasUsed: tt.blobGasUsed}, tt.txs)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// TestValidateBodyBlobConsistency drives the blob-usage check through the
// production ValidateBody path, proving a blob body is rejected before the
// ancestor/state checks and that a well-formed body still passes.
func TestValidateBodyBlobConsistency(t *testing.T) {
	t.Parallel()

	cfg := *params.TestChainConfig
	cfg.ShanghaiBlock = big.NewInt(0)
	cfg.CancunBlock = big.NewInt(0)

	gspec := &Genesis{Config: &cfg, GasLimit: 30_000_000, BaseFee: big.NewInt(params.InitialBaseFee)}
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), DefaultConfig())
	require.NoError(t, err)
	defer chain.Stop()

	parent := chain.CurrentBlock()
	newBody := func(txs types.Transactions, withdrawals types.Withdrawals, setBlobGas bool) *types.Block {
		h := &types.Header{
			ParentHash: parent.Hash(), Number: big.NewInt(1), Time: parent.Time + 1,
			Difficulty: big.NewInt(1), GasLimit: parent.GasLimit, BaseFee: big.NewInt(params.InitialBaseFee),
		}
		if withdrawals != nil {
			empty := types.EmptyWithdrawalsHash
			h.WithdrawalsHash = &empty
		}
		if setBlobGas {
			zero, used := uint64(0), uint64(params.BlobTxBlobGasPerBlob)
			h.ExcessBlobGas, h.BlobGasUsed = &zero, &used
		}
		return types.NewBlock(h, &types.Body{Transactions: txs, Withdrawals: withdrawals}, nil, trie.NewStackTrie(nil))
	}

	// Blob body with a header that declares no blob gas is rejected by the
	// body check, before the ancestor lookup.
	require.ErrorContains(t, chain.validator.ValidateBody(newBody(types.Transactions{blobTestTx(t)}, nil, false)),
		"data blobs present in block body")

	// A header blob-gas value that disagrees with the body is rejected.
	badCount := newBody(types.Transactions{blobTestTx(t)}, nil, true)
	hdr := badCount.Header()
	twoBlobs := uint64(2 * params.BlobTxBlobGasPerBlob)
	hdr.BlobGasUsed = &twoBlobs
	require.ErrorContains(t, chain.validator.ValidateBody(badCount.WithSeal(hdr)), "blob gas used mismatch")

	// A tx-root mismatch is caught too.
	good := newBody(nil, types.Withdrawals{}, false)
	tampered := good.Header()
	tampered.TxHash = common.Hash{0xde, 0xad}
	require.ErrorContains(t, chain.validator.ValidateBody(good.WithSeal(tampered)), "transaction root hash mismatch")

	// A well-formed empty body passes every body-level check (the parent is the
	// known genesis, so the ancestor check is satisfied).
	require.NoError(t, chain.validator.ValidateBody(good))

	orphan := good.Header()
	orphan.ParentHash = common.Hash{0xbe, 0xef}
	require.ErrorIs(t, chain.validator.ValidateBody(good.WithSeal(orphan)), consensus.ErrUnknownAncestor)

	uncle := &types.Header{Number: big.NewInt(0), Difficulty: big.NewInt(1)}
	withUncles := types.NewBlock(good.Header(), &types.Body{Withdrawals: types.Withdrawals{}, Uncles: []*types.Header{uncle, uncle, uncle}}, nil, trie.NewStackTrie(nil))
	require.ErrorContains(t, chain.validator.ValidateBody(withUncles), "too many uncles")

	stateless := types.NewBlockWithHeader(&types.Header{
		ParentHash: parent.Hash(), Number: big.NewInt(1), Root: common.Hash{0xab}, Difficulty: big.NewInt(1),
	})
	rawdb.WriteBlock(chain.db, stateless)
	child := good.Header()
	child.ParentHash, child.Number = stateless.Hash(), big.NewInt(2)
	require.ErrorIs(t, chain.validator.ValidateBody(good.WithSeal(child)), consensus.ErrPrunedAncestor)
}

// TestCheckBlobFeeCap covers the blob fee-cap precheck, including the Bor case
// where Cancun is active but no blob schedule exists, so BlobBaseFee is nil.
func TestCheckBlobFeeCap(t *testing.T) {
	t.Parallel()

	cancun := *params.TestChainConfig
	cancun.CancunBlock = big.NewInt(0)
	preCancun := *params.TestChainConfig
	preCancun.CancunBlock = nil

	from := common.Address{0xaa}
	newEVM := func(cfg *params.ChainConfig, blobBaseFee *big.Int, noBaseFee bool) *vm.EVM {
		sdb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
		require.NoError(t, err)
		ctx := vm.BlockContext{BlockNumber: big.NewInt(1), BlobBaseFee: blobBaseFee}
		return vm.NewEVM(ctx, sdb, cfg, vm.Config{NoBaseFee: noBaseFee})
	}
	msg := func(feeCap *big.Int, blob bool) *Message {
		m := &Message{From: from, BlobGasFeeCap: feeCap}
		if blob {
			m.BlobHashes = []common.Hash{{0x01}}
		}
		return m
	}

	tests := []struct {
		name    string
		evm     *vm.EVM
		msg     *Message
		wantErr error
	}{
		{name: "nil blob base fee", evm: newEVM(&cancun, nil, false), msg: msg(big.NewInt(1), true), wantErr: ErrNoBlobBaseFee},
		{name: "fee cap below base", evm: newEVM(&cancun, big.NewInt(10), false), msg: msg(big.NewInt(1), true), wantErr: ErrBlobFeeCapTooLow},
		{name: "fee cap at base", evm: newEVM(&cancun, big.NewInt(1), false), msg: msg(big.NewInt(1), true)},
		{name: "no base fee with nil blob base fee", evm: newEVM(&cancun, nil, true), msg: msg(big.NewInt(0), true), wantErr: ErrNoBlobBaseFee},
		{name: "no base fee skips zeroed blob base fee", evm: newEVM(&cancun, big.NewInt(0), true), msg: msg(big.NewInt(0), true)},
		{name: "no base fee skips zero cap", evm: newEVM(&cancun, big.NewInt(10), true), msg: msg(big.NewInt(0), true)},
		{name: "no base fee still checks nonzero cap", evm: newEVM(&cancun, big.NewInt(10), true), msg: msg(big.NewInt(1), true), wantErr: ErrBlobFeeCapTooLow},
		{name: "no blob gas", evm: newEVM(&cancun, nil, false), msg: msg(big.NewInt(1), false)},
		{name: "pre cancun", evm: newEVM(&preCancun, nil, false), msg: msg(big.NewInt(1), true)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &stateTransition{evm: tt.evm, msg: tt.msg, state: tt.evm.StateDB}
			err := st.checkBlobFeeCap()
			if tt.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// TestPreCheckReportsMissingBlobBaseFee proves the nil-BlobBaseFee condition is
// surfaced as an error by the full precheck rather than panicking, which is the
// crash this change prevents on stateless import of a forged blob block.
func TestPreCheckReportsMissingBlobBaseFee(t *testing.T) {
	t.Parallel()

	cfg := *params.TestChainConfig
	cfg.CancunBlock = big.NewInt(0)

	sdb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)
	ctx := vm.BlockContext{BlockNumber: big.NewInt(1), BaseFee: big.NewInt(0), BlobBaseFee: nil}
	evm := vm.NewEVM(ctx, sdb, &cfg, vm.Config{NoBaseFee: true})

	to := common.Address{1}
	m := &Message{
		From: common.Address{0xaa}, To: &to,
		GasFeeCap: big.NewInt(0), GasTipCap: big.NewInt(0),
		BlobGasFeeCap: big.NewInt(1), BlobHashes: []common.Hash{{0x01}},
		SkipNonceChecks: true, SkipTransactionChecks: true,
	}
	st := newStateTransition(evm, m, new(GasPool).AddGas(m.GasLimit))
	require.ErrorIs(t, st.preCheck(), ErrNoBlobBaseFee)
}
