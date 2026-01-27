// Copyright 2021 The go-ethereum Authors
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

package beacon

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// setupTestState creates a test state database and funds the test account
func setupTestState(t *testing.T) (common.Address, *big.Int, *triedb.Database, *state.StateDB) {
	key, _ := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	address := crypto.PubkeyToAddress(key.PublicKey)
	funds := big.NewInt(0).Mul(big.NewInt(1000), big.NewInt(params.Ether))

	db := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(db, triedb.HashDefaults)
	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabase(tdb, nil))

	statedb.SetBalance(address, uint256.MustFromBig(funds), tracing.BalanceChangeUnspecified)
	statedb.SetNonce(address, 0, tracing.NonceChangeUnspecified)

	return address, funds, tdb, statedb
}

// verifyFinalizeAndAssembleSuccess checks common success conditions for FinalizeAndAssemble
func verifyFinalizeAndAssembleSuccess(t *testing.T, block *types.Block, receipts []*types.Receipt, commitTime time.Duration, err error, header *types.Header) {
	if err != nil {
		t.Fatalf("FinalizeAndAssemble failed: %v", err)
	}
	if block == nil {
		t.Fatal("FinalizeAndAssemble returned nil block")
	}
	if receipts == nil {
		t.Fatal("FinalizeAndAssemble returned nil receipts")
	}
	if commitTime < 0 {
		t.Fatalf("FinalizeAndAssemble returned negative commit time: %v", commitTime)
	}
	if block.Number().Cmp(header.Number) != 0 {
		t.Errorf("Block number mismatch: got %v, want %v", block.Number(), header.Number)
	}
	if block.GasLimit() != header.GasLimit {
		t.Errorf("Block gas limit mismatch: got %v, want %v", block.GasLimit(), header.GasLimit)
	}
	if block.Root() == (common.Hash{}) {
		t.Error("Block root hash is empty")
	}
	if header.Root == (common.Hash{}) {
		t.Error("Header root hash is empty")
	}
}

// mockChainReader is a minimal implementation of consensus.ChainHeaderReader for testing
type mockChainReader struct {
	config *params.ChainConfig
}

// newMockChainReader creates a new mock chain reader with the given configuration
func newMockChainReader(config *params.ChainConfig) consensus.ChainHeaderReader {
	return &mockChainReader{config: config}
}

func (m *mockChainReader) Config() *params.ChainConfig {
	return m.config
}

func (m *mockChainReader) CurrentHeader() *types.Header {
	return &types.Header{}
}

func (m *mockChainReader) GetHeader(hash common.Hash, number uint64) *types.Header {
	return nil
}

func (m *mockChainReader) GetHeaderByNumber(number uint64) *types.Header {
	return nil
}

func (m *mockChainReader) GetHeaderByHash(hash common.Hash) *types.Header {
	return nil
}

func (m *mockChainReader) GetTd(hash common.Hash, number uint64) *big.Int {
	return big.NewInt(0)
}

// createBeaconHeader creates a standard test header with the given parameters
func createBeaconHeader(blockNum int64, gasLimit uint64, time uint64, difficulty *big.Int) *types.Header {
	return &types.Header{
		Number:     big.NewInt(blockNum),
		GasLimit:   gasLimit,
		GasUsed:    0,
		Time:       time,
		Difficulty: difficulty,
		Coinbase:   common.Address{},
		ParentHash: common.Hash{},
	}
}

// createBeaconBody creates a standard empty body with optional withdrawals
func createBeaconBody(withdrawals []*types.Withdrawal) *types.Body {
	return &types.Body{
		Transactions: []*types.Transaction{},
		Uncles:       []*types.Header{},
		Withdrawals:  withdrawals,
	}
}

// setupBeaconEngine creates a beacon engine and chain reader with the given config
func setupBeaconEngine(config *params.ChainConfig) (*Beacon, consensus.ChainHeaderReader) {
	engine := New(ethash.NewFaker())
	chain := newMockChainReader(config)
	return engine, chain
}

func TestFinalizeAndAssembleReturnsCommitTime(t *testing.T) {
	t.Parallel()

	t.Run("PoS block without withdrawals", func(t *testing.T) {
		config := *params.MergedTestChainConfig
		_, _, _, statedb := setupTestState(t)
		engine, chain := setupBeaconEngine(&config)
		header := createBeaconHeader(1, 5000000, 1000000, common.Big0)
		body := createBeaconBody([]*types.Withdrawal{})

		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})
		verifyFinalizeAndAssembleSuccess(t, block, receipts, commitTime, err, header)
		t.Logf("Commit time for PoS block: %v", commitTime)
	})

	t.Run("PoS block with larger state", func(t *testing.T) {
		config := *params.MergedTestChainConfig
		_, _, _, statedb := setupTestState(t)

		// Add multiple storage entries to increase state size
		testAddr := common.Address{0xaa}
		statedb.SetBalance(testAddr, uint256.NewInt(100), tracing.BalanceChangeUnspecified)
		for i := 0; i < 100; i++ {
			key := common.BigToHash(big.NewInt(int64(i)))
			val := common.BigToHash(big.NewInt(int64(i * 2)))
			statedb.SetState(testAddr, key, val)
		}

		engine, chain := setupBeaconEngine(&config)
		header := createBeaconHeader(2, 5000000, 1000001, common.Big0)
		header.ParentHash = common.Hash{0x01}
		body := createBeaconBody([]*types.Withdrawal{})

		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})
		verifyFinalizeAndAssembleSuccess(t, block, receipts, commitTime, err, header)
		t.Logf("Commit time for PoS block with 100 storage entries: %v", commitTime)
	})

	t.Run("pre-merge block delegates to ethone", func(t *testing.T) {
		config := *params.TestChainConfig
		_, _, _, statedb := setupTestState(t)
		engine, chain := setupBeaconEngine(&config)
		header := createBeaconHeader(1, 5000000, 1000000, big.NewInt(1)) // Non-zero difficulty
		body := createBeaconBody([]*types.Withdrawal{})

		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})
		verifyFinalizeAndAssembleSuccess(t, block, receipts, commitTime, err, header)
		t.Logf("Commit time for pre-merge block (delegated to ethash): %v", commitTime)
	})

	t.Run("PoS block with withdrawals before Shanghai should fail", func(t *testing.T) {
		config := *params.MergedTestChainConfig
		config.ShanghaiBlock = big.NewInt(1000000)
		_, _, _, statedb := setupTestState(t)
		engine, chain := setupBeaconEngine(&config)
		header := createBeaconHeader(1, 5000000, 1000000, common.Big0)
		body := createBeaconBody([]*types.Withdrawal{{Validator: 1, Address: common.Address{0x01}, Amount: 100}})

		_, _, _, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})

		if err == nil {
			t.Fatal("FinalizeAndAssemble should have failed with withdrawals before Shanghai")
		}
		if err.Error() != "withdrawals set before Shanghai activation" {
			t.Errorf("unexpected error: got %v, want 'withdrawals set before Shanghai activation'", err)
		}
	})

	t.Run("PoS block after Shanghai with nil withdrawals initializes empty slice", func(t *testing.T) {
		config := *params.MergedTestChainConfig
		config.ShanghaiBlock = big.NewInt(0)
		_, _, _, statedb := setupTestState(t)
		engine, chain := setupBeaconEngine(&config)
		header := createBeaconHeader(1, 5000000, 1000000, common.Big0)
		body := createBeaconBody(nil) // nil should be initialized to empty slice

		block, receipts, commitTime, err := engine.FinalizeAndAssemble(chain, header, statedb, body, []*types.Receipt{})
		verifyFinalizeAndAssembleSuccess(t, block, receipts, commitTime, err, header)

		// Verify withdrawals were initialized to empty slice (not nil)
		if body.Withdrawals == nil {
			t.Error("Withdrawals should have been initialized to empty slice, got nil")
		}
		if len(body.Withdrawals) != 0 {
			t.Errorf("Withdrawals should be empty, got %d elements", len(body.Withdrawals))
		}

		t.Logf("Commit time for Shanghai PoS block with initialized withdrawals: %v", commitTime)
	})
}
