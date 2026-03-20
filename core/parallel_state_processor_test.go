package core

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

func TestMetadata(t *testing.T) {
	t.Parallel()

	correctTxDependency := [][]uint64{{}, {0}, {}, {1}, {3}, {}, {0, 2}, {5}, {}, {8}}
	wrongTxDependency := [][]uint64{{0}}
	wrongTxDependencyCircular := [][]uint64{{}, {2}, {1}}
	wrongTxDependencyOutOfRange := [][]uint64{{}, {}, {3}}

	var temp map[int][]int

	temp = GetDeps(correctTxDependency)
	assert.Equal(t, true, VerifyDeps(temp))

	temp = GetDeps(wrongTxDependency)
	assert.Equal(t, false, VerifyDeps(temp))

	temp = GetDeps(wrongTxDependencyCircular)
	assert.Equal(t, false, VerifyDeps(temp))

	temp = GetDeps(wrongTxDependencyOutOfRange)
	assert.Equal(t, false, VerifyDeps(temp))
}

func TestNegativeDependencyFromUint64Overflow(t *testing.T) {
	t.Parallel()

	// 0xFFFFFFFFFFFFFFFF casts to -1 when converted to int
	// This should be rejected as an invalid dependency
	txDependencyWithMaxUint64 := [][]uint64{{}, {0xFFFFFFFFFFFFFFFF}}

	deps := GetDeps(txDependencyWithMaxUint64)

	// VerifyDeps should return false because dependency value becomes -1 after casting
	// Currently fails: VerifyDeps incorrectly returns true (missing depTx < 0 check)
	// After fix: VerifyDeps will correctly return false
	assert.Equal(t, false, VerifyDeps(deps), "Dependencies with negative values (from uint64 overflow) should be invalid")
}

func TestExecutionTaskSettleBlobReceiptFields(t *testing.T) {
	t.Parallel()

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatalf("failed to create state db: %v", err)
	}
	finalStateDB, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatalf("failed to create final state db: %v", err)
	}

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("failed to create key: %v", err)
	}
	blobBaseFee := big.NewInt(15)
	blobTx := &types.BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      0,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(params.InitialBaseFee),
		Gas:        params.TxGas,
		To:         common.Address{0x1},
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(2),
		BlobHashes: []common.Hash{{0x1}, {0x2}},
	}
	tx := types.MustSignNewTx(key, types.NewCancunSigner(big.NewInt(1)), blobTx)

	var (
		usedGas  uint64
		receipts types.Receipts
		allLogs  []*types.Log
	)
	task := &ExecutionTask{
		msg: Message{
			From: crypto.PubkeyToAddress(key.PublicKey),
			To:   &blobTx.To,
		},
		config:            params.AllEthashProtocolChanges,
		blockNumber:       big.NewInt(1),
		blockHash:         common.HexToHash("0x1234"),
		blockTime:         1,
		tx:                tx,
		index:             0,
		statedb:           statedb,
		finalStateDB:      finalStateDB,
		result:            &ExecutionResult{UsedGas: params.TxGas},
		shouldDelayFeeCal: new(bool),
		totalUsedGas:      &usedGas,
		receipts:          &receipts,
		allLogs:           &allLogs,
		blockContext: vm.BlockContext{
			BlobBaseFee: blobBaseFee,
		},
	}

	task.Settle()

	if len(receipts) != 1 {
		t.Fatalf("expected 1 receipt, got %d", len(receipts))
	}
	receipt := receipts[0]
	wantBlobGasUsed := uint64(len(blobTx.BlobHashes) * params.BlobTxBlobGasPerBlob)
	if receipt.BlobGasUsed != wantBlobGasUsed {
		t.Fatalf("blob gas used mismatch: got %d want %d", receipt.BlobGasUsed, wantBlobGasUsed)
	}
	if receipt.BlobGasPrice == nil || receipt.BlobGasPrice.Cmp(blobBaseFee) != 0 {
		t.Fatalf("blob gas price mismatch: got %v want %v", receipt.BlobGasPrice, blobBaseFee)
	}
}
