package core

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/internal/blocktest"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// TestGasConsumptionComparison logs gas consumption for each transaction
// to help identify where PEVM and Bor EVM differ in gas accounting.
// This test uses the same storage-heavy contract scenario as BenchmarkEVMvsPEVMStorageHeavy.
func TestGasConsumptionComparison(t *testing.T) {
	// Chain config and header
	cfg := params.MainnetChainConfig
	now := uint64(time.Now().Unix())
	header := &types.Header{
		Number:     big.NewInt(1),
		Time:       now,
		GasLimit:   30_000_000,
		Coinbase:   common.Address{0x01},
		Difficulty: big.NewInt(0),
		BaseFee:    big.NewInt(params.InitialBaseFee),
	}

	// Pre-state
	stateDB := state.NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), nil)
	
	// Keys and accounts
	key1, _ := crypto.GenerateKey()
	addr1 := crypto.PubkeyToAddress(key1.PublicKey)
	contractAddr := crypto.CreateAddress(addr1, 0) // Contract address
	
	// Create contract bytecode
	contractCode := createStorageHeavyContract()

	// Setup state
	st1, _ := state.New(common.Hash{}, stateDB)
	bal1, _ := uint256.FromBig(big.NewInt(1000000000000000000)) // 1e18
	st1.SetBalance(addr1, bal1, tracing.BalanceChangeUnspecified)
	st1.SetNonce(addr1, 0, tracing.NonceChangeUnspecified)
	
	// Deploy contract
	st1.SetCode(contractAddr, contractCode)
	st1.SetNonce(contractAddr, 1, tracing.NonceChangeUnspecified)
	st1.Commit(0, false, false)

	// Gas price
	gasPrice := new(big.Int).Set(header.BaseFee)
	if gasPrice.Sign() == 0 {
		gasPrice = big.NewInt(params.InitialBaseFee)
	}

	// Number of transactions (keep small for manageable logging output)
	numTxs := 3
	
	// Build transactions calling the contract
	txs := make(types.Transactions, 0, numTxs)
	for j := 0; j < numTxs; j++ {
		// Each transaction calls the contract with empty data (will execute the contract code)
		txs = append(txs, makeStorageHeavyTx(t, key1, contractAddr, uint64(j), 5_000_000, gasPrice, cfg, header, []byte{}))
	}

	// Create block
	body := &types.Body{Transactions: txs}
	block := types.NewBlock(header, body, nil, blocktest.NewHasher())

	// Create HeaderChain and StateProcessor
	chainDb := rawdb.NewMemoryDatabase()
	genesisHeader := &types.Header{
		Number:     big.NewInt(0),
		Time:       now,
		GasLimit:   30_000_000,
		Coinbase:   common.Address{0x01},
		Difficulty: big.NewInt(0),
		BaseFee:    big.NewInt(params.InitialBaseFee),
	}
	rawdb.WriteHeader(chainDb, genesisHeader)
	rawdb.WriteCanonicalHash(chainDb, genesisHeader.Hash(), 0)
	rawdb.WriteHeadHeaderHash(chainDb, genesisHeader.Hash())

	hc, err := NewHeaderChain(chainDb, cfg, ethash.NewFaker(), func() bool { return false })
	if err != nil {
		t.Fatalf("Failed to create HeaderChain: %v", err)
	}
	processor := NewStateProcessor(cfg, hc)

	prevOpcodeEnv := os.Getenv(opcodeLogEnvVar)
	os.Setenv(opcodeLogEnvVar, "1")
	defer func() {
		if prevOpcodeEnv == "" {
			os.Unsetenv(opcodeLogEnvVar)
		} else {
			os.Setenv(opcodeLogEnvVar, prevOpcodeEnv)
		}
	}()

	// Copy state BEFORE executing on EVM so both paths start from the same initial state
	st2 := st1.Copy()

	// Baseline: Process block using StateProcessor without PEVM
	os.Unsetenv("USE_PEVM")
	res1, err := processor.Process(block, st1, vm.Config{
		Tracer: newOpcodeGasTracer("BOR"),
	}, nil, context.Background())
	if err != nil {
		t.Fatalf("StateProcessor.Process (baseline) failed: %v", err)
	}

	// Test PEVM: Process block using StateProcessor with USE_PEVM=true
	os.Setenv("USE_PEVM", "true")
	defer os.Unsetenv("USE_PEVM")
	res2, err := processor.Process(block, st2, vm.Config{}, nil, context.Background())
	if err != nil {
		t.Fatalf("StateProcessor.Process (PEVM) failed: %v", err)
	}

	// Compare and highlight differences
	fmt.Println("=== Comparison ===")
	if res1.GasUsed != res2.GasUsed {
		fmt.Printf("⚠️  Total gas mismatch: BOR-EVM=%d PEVM=%d (diff=%d)\n",
			res1.GasUsed, res2.GasUsed, res1.GasUsed-res2.GasUsed)
	} else {
		fmt.Printf("✅ Total gas matches: %d\n", res1.GasUsed)
	}

	if len(res1.Receipts) != len(res2.Receipts) {
		fmt.Printf("⚠️  Receipt count mismatch: BOR-EVM=%d PEVM=%d\n",
			len(res1.Receipts), len(res2.Receipts))
	} else {
		fmt.Printf("✅ Receipt count matches: %d\n", len(res1.Receipts))
	}

	for i := 0; i < len(res1.Receipts) && i < len(res2.Receipts); i++ {
		r1 := res1.Receipts[i]
		r2 := res2.Receipts[i]
		if r1.GasUsed != r2.GasUsed {
			fmt.Printf("⚠️  Tx %d gas mismatch: BOR-EVM=%d PEVM=%d (diff=%d)\n",
				i, r1.GasUsed, r2.GasUsed, r1.GasUsed-r2.GasUsed)
		} else {
			fmt.Printf("✅ Tx %d gas matches: %d\n", i, r1.GasUsed)
		}
		if r1.CumulativeGasUsed != r2.CumulativeGasUsed {
			fmt.Printf("⚠️  Tx %d cumulative gas mismatch: BOR-EVM=%d PEVM=%d (diff=%d)\n",
				i, r1.CumulativeGasUsed, r2.CumulativeGasUsed, r1.CumulativeGasUsed-r2.CumulativeGasUsed)
		}
		if r1.Status != r2.Status {
			fmt.Printf("⚠️  Tx %d status mismatch: BOR-EVM=%d PEVM=%d\n",
				i, r1.Status, r2.Status)
		}
	}

	// Verify state matches (same as benchmark)
	root1 := st1.IntermediateRoot(cfg.IsEIP158(header.Number))
	root2 := st2.IntermediateRoot(cfg.IsEIP158(header.Number))
	if root1 != root2 {
		t.Errorf("State root mismatch: EVM=%x PEVM=%x", root1, root2)
	} else {
		fmt.Printf("✅ State root matches: %x\n", root1)
	}
}

// createStorageHeavyContract and makeStorageHeavyTx live in pevm_execute_test.go

