//go:build integration
// +build integration

package bor

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// TestReservedBlockspaceZeroFeeProduction is the end-to-end (in-process devnet)
// validation of the reserved-blockspace feature across all four tickets: a real
// two-node bor network produces and cross-verifies blocks; a zero-fee tx from a
// config-whitelisted reserved sender is admitted to the pool (POS-3570), mined
// past the ReservedBlockspace fork with the reserved header fields set by the
// real Prepare path (POS-3637), and executes fee-free so the sender pays only
// its call value with no gas debit (POS-3573). An identical zero-fee tx from a
// non-reserved sender is rejected at admission.
//
// Run with: go test -tags=integration -run TestReservedBlockspaceZeroFeeProduction ./tests/bor/
func TestReservedBlockspaceZeroFeeProduction(t *testing.T) {
	faucets := make([]*ecdsa.PrivateKey, 10)
	for i := range faucets {
		faucets[i], _ = crypto.GenerateKey()
	}
	reservedAddr := crypto.PubkeyToAddress(faucets[0].PublicKey)
	nonReservedAddr := crypto.PubkeyToAddress(faucets[1].PublicKey)

	genesis := InitGenesis(t, faucets, "./testdata/genesis_2val.json", 8)
	// Reserved fork at block 5 (Cancun is active from block 3, so the reserved
	// header fields encode in the post-Cancun BlockExtraData format).
	reservedFork := uint64(5)
	genesis.Config.Bor.ReservedBlockspaceBlock = new(big.Int).SetUint64(reservedFork)
	genesis.Config.Bor.ReservedClients = []params.ReservedClient{
		{Addresses: []common.Address{reservedAddr}, QuotaGas: 30_000_000},
	}
	startBalance := new(big.Int).SetUint64(1_000_000_000_000_000_000) // 1 ETH
	genesis.Alloc[reservedAddr] = types.Account{Balance: new(big.Int).Set(startBalance)}
	genesis.Alloc[nonReservedAddr] = types.Account{Balance: new(big.Int).Set(startBalance)}

	stacks, nodes, _ := setupMiner(t, 2, genesis)
	defer func() {
		for _, stack := range stacks {
			stack.Close()
		}
	}()
	for _, node := range nodes {
		if err := node.StartMining(); err != nil {
			t.Fatal("start mining:", err)
		}
	}

	waitForBlock := func(target uint64) {
		t.Helper()
		deadline := time.After(120 * time.Second)
		for {
			if nodes[0].BlockChain().CurrentBlock().Number.Uint64() >= target {
				return
			}
			select {
			case <-deadline:
				t.Fatalf("timeout waiting for block %d (at %d)", target, nodes[0].BlockChain().CurrentBlock().Number.Uint64())
			case <-time.After(200 * time.Millisecond):
			}
		}
	}

	// Build past the reserved fork before submitting.
	waitForBlock(reservedFork + 2)

	signer := types.LatestSigner(genesis.Config)
	recipient := common.HexToAddress("0x00000000000000000000000000000000000000aa")

	zeroFeeTx := func(from *ecdsa.PrivateKey, nonce uint64) *types.Transaction {
		tx, err := types.SignNewTx(from, signer, &types.DynamicFeeTx{
			ChainID:   genesis.Config.ChainID,
			Nonce:     nonce,
			GasTipCap: big.NewInt(0),
			GasFeeCap: big.NewInt(0),
			Gas:       21000,
			To:        &recipient,
			Value:     big.NewInt(100),
		})
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}

	// Reserved sender: zero-fee tx must be accepted by the pool.
	rNonce, err := nodes[0].APIBackend.GetPoolNonce(context.Background(), reservedAddr)
	if err != nil {
		t.Fatal(err)
	}
	rtx := zeroFeeTx(faucets[0], rNonce)
	for _, node := range nodes {
		if err := node.APIBackend.SendTx(context.Background(), rtx); err != nil {
			t.Fatalf("reserved zero-fee tx rejected by pool: %v", err)
		}
	}

	// Non-reserved sender: identical zero-fee tx must be rejected at admission.
	oNonce, err := nodes[0].APIBackend.GetPoolNonce(context.Background(), nonReservedAddr)
	if err != nil {
		t.Fatal(err)
	}
	otx := zeroFeeTx(faucets[1], oNonce)
	if err := nodes[0].APIBackend.SendTx(context.Background(), otx); err == nil {
		t.Fatal("non-reserved zero-fee tx should be rejected at admission, but was accepted")
	}

	// Wait for the reserved tx to be mined, then locate its block.
	var includedBlock *types.Block
	deadline := time.After(60 * time.Second)
	for includedBlock == nil {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for reserved tx inclusion")
		case <-time.After(200 * time.Millisecond):
		}
		head := nodes[0].BlockChain().CurrentBlock().Number.Uint64()
		for n := reservedFork; n <= head && includedBlock == nil; n++ {
			blk := nodes[0].BlockChain().GetBlockByNumber(n)
			if blk == nil {
				continue
			}
			for _, tx := range blk.Transactions() {
				if tx.Hash() == rtx.Hash() {
					includedBlock = blk
					break
				}
			}
		}
	}
	t.Logf("reserved zero-fee tx mined in block %d", includedBlock.NumberU64())

	// The producing block must carry the reserved header fields (set by Prepare).
	rc, rg := includedBlock.Header().GetReservedInfo(genesis.Config)
	if rc == nil || rg == nil {
		t.Fatalf("block %d missing reserved header fields: count=%v gas=%v", includedBlock.NumberU64(), rc, rg)
	}

	// The reserved sender paid only its call value — no gas was debited.
	state, err := nodes[0].BlockChain().StateAt(includedBlock.Root())
	if err != nil {
		t.Fatal(err)
	}
	got := state.GetBalance(reservedAddr).ToBig()
	want := new(big.Int).Sub(startBalance, big.NewInt(100))
	if got.Cmp(want) != 0 {
		t.Fatalf("reserved sender balance = %s, want %s (call value only, no gas debit)", got, want)
	}

	// Both nodes agreed on the block (the validator verified the producer's
	// header, including the reserved-field presence check).
	other := nodes[1].BlockChain().GetBlockByNumber(includedBlock.NumberU64())
	if other == nil || other.Hash() != includedBlock.Hash() {
		t.Fatalf("nodes disagree at block %d: %v vs %s", includedBlock.NumberU64(), other, includedBlock.Hash())
	}
}
