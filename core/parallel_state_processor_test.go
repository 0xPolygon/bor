package core

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
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

// Test that when a tx fails, we only apply sender's balance and nonce changes,
// and discard all other writes (other accounts' balances, storage, code, etc.).
func TestSettleFailedTxAppliesOnlySenderBalanceAndNonce(t *testing.T) {
	t.Parallel()

	// Build a minimal StateDB setup with MV tracking for the transactional state
	db := state.NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), triedb.HashDefaults), nil)
	mvhm := blockstm.MakeMVHashMap()

	// Base state with MV enabled (to record MV writes)
	sBase, _ := state.NewWithMVHashmap(common.Hash{}, db, nil, mvhm)

	// Final state which will receive writes during Settle (no MV needed)
	sFinal := sBase.Copy()
	sFinal.SetMVHashmap(nil)

	// Transactional state where we simulate writes produced by a failed tx
	sTx := sBase.Copy()

	sender := common.HexToAddress("0x0000000000000000000000000000000000000001")
	recipient := common.HexToAddress("0x0000000000000000000000000000000000000002")
	contract := common.HexToAddress("0x0000000000000000000000000000000000000003")
	key := common.HexToHash("0x01")
	val := common.HexToHash("0x02")

	// Simulate a variety of writes within the failed tx
	// - storage write (should be discarded)
	// - recipient balance change (should be discarded)
	// - sender balance and nonce (should be kept)
	sTx.SetState(contract, key, val)
	sTx.SetBalance(recipient, uint256.NewInt(100), tracing.BalanceChangeTransfer)
	sTx.SetBalance(sender, uint256.NewInt(25), tracing.BalanceChangeTransfer)
	sTx.SetNonce(sender, 9, tracing.NonceChangeUnspecified)
	sTx.Finalise(true)

	// Prepare an expected state which only has sender's balance and nonce applied
	sExpected := sFinal.Copy()
	sExpected.SetBalance(sender, uint256.NewInt(25), tracing.BalanceChangeUnspecified)
	sExpected.SetNonce(sender, 9, tracing.NonceChangeUnspecified)
	sExpected.Finalise(true)

	// Construct a minimal ExecutionTask to hit the failed-transaction Settle path
	to := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	tx := types.NewTx(&types.LegacyTx{Nonce: 0, To: &to, Value: big.NewInt(0), Gas: 21000, GasPrice: big.NewInt(1)})

	shouldDelay := false
	usedGas := uint64(0)
	receipts := types.Receipts{}
	allLogs := []*types.Log{}

	task := &ExecutionTask{
		msg:               Message{From: sender, To: &to},
		config:            params.BorUnittestChainConfig,
		gasLimit:          30_000_000,
		blockNumber:       big.NewInt(0),
		blockHash:         common.Hash{},
		blockTime:         0,
		tx:                tx,
		index:             0,
		statedb:           sTx,
		cleanStateDB:      nil,
		finalStateDB:      sFinal,
		header:            &types.Header{},
		evmConfig:         vm.Config{},
		result:            &ExecutionResult{Err: assert.AnError},
		shouldDelayFeeCal: &shouldDelay,
		sender:            sender,
		totalUsedGas:      &usedGas,
		receipts:          &receipts,
		allLogs:           &allLogs,
		dependencies:      nil,
		coinbase:          common.HexToAddress("0x000000000000000000000000000000000000ba5e"),
		blockContext:      vm.BlockContext{},
	}

	// Invoke Settle: should apply only sender balance/nonce from sTx's MV writes
	task.Settle()

	// Verify only sender's balance and nonce were applied; other writes discarded
	assert.Equal(t, sExpected.IntermediateRoot(true), sFinal.IntermediateRoot(true))
	// Extra spot-checks for clarity
	assert.Equal(t, uint256.NewInt(25), sFinal.GetBalance(sender))
	assert.Equal(t, uint64(9), sFinal.GetNonce(sender))
	assert.Equal(t, uint256.NewInt(0), sFinal.GetBalance(recipient))
	assert.Equal(t, common.Hash{}, sFinal.GetState(contract, key))
}
