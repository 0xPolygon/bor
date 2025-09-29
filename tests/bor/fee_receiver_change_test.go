//go:build integration
// +build integration

package bor

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestCoinbaseRedirection tests that transaction fees are redirected to the
// configured Rio coinbase address after the Rio hardfork activation
func TestCoinbaseRedirection(t *testing.T) {
	// Rio hardfork coinbase address
	rioCoinbase := common.HexToAddress("0x000000000000000000000000000000000000ba5e")

	// Generate test accounts
	faucets := make([]*ecdsa.PrivateKey, 10)
	for i := 0; i < len(faucets); i++ {
		faucets[i], _ = crypto.GenerateKey()
	}

	// Initialize genesis with validator configuration
	genesis := InitGenesis(t, faucets, "./testdata/genesis_2val.json", 8)

	// Configure Rio hardfork to activate at block 16
	genesis.Config.Bor.RioBlock = big.NewInt(16)
	genesis.Config.Bor.Coinbase = map[string]string{
		"0":  "0x0000000000000000000000000000000000000000", // Default to block producer
		"16": rioCoinbase.Hex(),                            // Rio activates at block 16
	}

	// Add initial balances for test accounts
	genesis.Alloc[rioCoinbase] = types.Account{Balance: big.NewInt(0)}

	// Fund test accounts so they can send transactions
	for i := 0; i < 2; i++ {
		testAddr := crypto.PubkeyToAddress(faucets[i].PublicKey)
		genesis.Alloc[testAddr] = types.Account{Balance: big.NewInt(1e18)} // 1 ETH each
	}

	// Setup mining nodes using the same approach as TestForkWithBlockTime
	stacks, nodes, _ := setupMiner(t, 2, genesis)

	defer func() {
		for _, stack := range stacks {
			stack.Close()
		}
	}()

	// Start mining on all nodes
	for _, node := range nodes {
		if err := node.StartMining(); err != nil {
			t.Fatal("Error occurred while starting miner", "node", node, "error", err)
		}
	}

	// Get both validators' addresses (either could be mining blocks)
	validator1 := crypto.PubkeyToAddress(keys[0].PublicKey)
	validator2 := crypto.PubkeyToAddress(keys[1].PublicKey)

	// Send transactions to all nodes to ensure availability for mining
	sendTransactions := func(from *ecdsa.PrivateKey, count int, startNonce uint64) []*types.Transaction {
		transactions := make([]*types.Transaction, 0, count)

		for i := 0; i < count; i++ {
			to := crypto.PubkeyToAddress(faucets[(i+2)%len(faucets)].PublicKey)
			tx, _ := types.SignTx(types.NewTransaction(
				startNonce+uint64(i),
				to,
				big.NewInt(100),
				21000,
				big.NewInt(30000000000), // 30 gwei
				nil,
			), types.NewEIP155Signer(genesis.Config.ChainID), from)
			transactions = append(transactions, tx)
		}

		// Send to all nodes to ensure availability
		for _, node := range nodes {
			for _, tx := range transactions {
				if err := node.APIBackend.SendTx(context.Background(), tx); err != nil {
					// Only log if not "already known" error
					if !strings.Contains(err.Error(), "already known") {
						t.Logf("Warning: failed to send transaction to node: %v", err)
					}
				}
			}
		}

		return transactions
	}

	// Send transactions for pre-Rio blocks
	fromAddr0 := crypto.PubkeyToAddress(faucets[0].PublicKey)
	earlyNonce, err := nodes[0].APIBackend.GetPoolNonce(context.Background(), fromAddr0)
	if err != nil {
		t.Fatalf("failed to get nonce: %v", err)
	}
	sendTransactions(faucets[0], 5, earlyNonce)

	// Wait for block 14 (just before Rio)
	for {
		block := nodes[0].BlockChain().CurrentBlock()
		if block.Number.Uint64() >= 14 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Get initial validator balances from genesis block
	genesisBlock := nodes[0].BlockChain().GetBlockByNumber(0)
	if genesisBlock == nil {
		t.Fatal("Failed to get genesis block")
	}
	genesisState, err := nodes[0].BlockChain().StateAt(genesisBlock.Root())
	if err != nil {
		t.Fatalf("failed to get genesis state: %v", err)
	}
	validator1Initial := genesisState.GetBalance(validator1).ToBig()
	validator2Initial := genesisState.GetBalance(validator2).ToBig()

	// Get current balances at block 14
	statedb, err := nodes[0].BlockChain().State()
	if err != nil {
		t.Fatalf("failed to get state: %v", err)
	}
	rioCoinbaseBalanceBefore := statedb.GetBalance(rioCoinbase)
	validator1BalanceBefore := statedb.GetBalance(validator1)
	validator2BalanceBefore := statedb.GetBalance(validator2)

	// Calculate fees received from pre-Rio transactions
	validator1PreRioFees := new(big.Int).Sub(validator1BalanceBefore.ToBig(), validator1Initial)
	validator2PreRioFees := new(big.Int).Sub(validator2BalanceBefore.ToBig(), validator2Initial)
	totalPreRioFees := new(big.Int).Add(validator1PreRioFees, validator2PreRioFees)

	// Rio coinbase should have no balance before Rio
	if rioCoinbaseBalanceBefore.ToBig().Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("FAIL: Rio coinbase has %v wei before Rio activation (expected 0)", rioCoinbaseBalanceBefore.ToBig())
	}

	// Send transactions for Rio activation block
	fromAddr1 := crypto.PubkeyToAddress(faucets[1].PublicKey)
	nonce1, err := nodes[0].APIBackend.GetPoolNonce(context.Background(), fromAddr1)
	if err != nil {
		t.Fatalf("failed to get nonce: %v", err)
	}
	sendTransactions(faucets[1], 5, nonce1)

	// Wait for blocks 16 and 17 to be mined
	for {
		block := nodes[0].BlockChain().CurrentBlock()
		if block.Number.Uint64() >= 17 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Count transactions in blocks
	preRioTxCount := 0
	rioBlockTxCount := 0

	for i := uint64(1); i < 16; i++ {
		block := nodes[0].BlockChain().GetBlockByNumber(i)
		if block != nil {
			preRioTxCount += len(block.Transactions())
		}
	}

	block16 := nodes[0].BlockChain().GetBlockByNumber(16)
	if block16 != nil {
		rioBlockTxCount = len(block16.Transactions())
	}

	// Get final balances
	statedbAfter, err := nodes[0].BlockChain().State()
	if err != nil {
		t.Fatalf("failed to get state: %v", err)
	}
	rioCoinbaseBalanceAfter := statedbAfter.GetBalance(rioCoinbase)
	validator1BalanceAfter := statedbAfter.GetBalance(validator1)
	validator2BalanceAfter := statedbAfter.GetBalance(validator2)

	// Calculate balance changes
	rioCoinbaseGained := new(big.Int).Sub(rioCoinbaseBalanceAfter.ToBig(), rioCoinbaseBalanceBefore.ToBig())
	validator1PostRioGain := new(big.Int).Sub(validator1BalanceAfter.ToBig(), validator1BalanceBefore.ToBig())
	validator2PostRioGain := new(big.Int).Sub(validator2BalanceAfter.ToBig(), validator2BalanceBefore.ToBig())

	// Validators should not receive any fees after Rio activation
	if validator1PostRioGain.Cmp(big.NewInt(0)) != 0 || validator2PostRioGain.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("FAIL: Validators received fees after Rio activation (v1: %v wei, v2: %v wei)",
			validator1PostRioGain, validator2PostRioGain)
	}

	// Verify fee distribution
	if preRioTxCount > 0 && totalPreRioFees.Cmp(big.NewInt(0)) > 0 {
		t.Logf("✓ Pre-Rio: %d transactions, validators received %v wei in fees", preRioTxCount, totalPreRioFees)
	} else if preRioTxCount > 0 {
		t.Fatalf("FAIL: %d pre-Rio transactions but validators received no fees", preRioTxCount)
	}

	if rioBlockTxCount > 0 && rioCoinbaseGained.Cmp(big.NewInt(0)) > 0 {
		t.Logf("✓ Rio block: %d transactions, Rio coinbase received %v wei in fees", rioBlockTxCount, rioCoinbaseGained)
	} else if rioBlockTxCount > 0 {
		t.Fatalf("FAIL: %d Rio block transactions but Rio coinbase received no fees", rioBlockTxCount)
	}

	t.Log("✓ Coinbase redirection at Rio hardfork verified successfully")
}
