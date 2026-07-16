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

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/params"
)

// reservedRegistrySetupABI is the minimal setter surface the test drives to seed
// the genesis-deployed registry: claim ownership, then register one client with a
// whitelisted address. Bor's own embedded ABI is read-only, so the writes live here.
const reservedRegistrySetupABI = `[
 {"inputs":[{"name":"initialOwner","type":"address"},{"name":"maxTotalGas","type":"uint64"},{"name":"maxClientGas","type":"uint64"}],"name":"initialize","outputs":[],"stateMutability":"nonpayable","type":"function"},
 {"inputs":[{"name":"admin","type":"address"},{"name":"gasQuota","type":"uint64"},{"name":"feeMode","type":"uint8"},{"name":"effectiveFrom","type":"uint64"},{"name":"metadata","type":"string"},{"name":"addresses","type":"address[]"}],"name":"createClient","outputs":[{"name":"clientId","type":"uint256"}],"stateMutability":"nonpayable","type":"function"}
]`

// TestReservedBlockspaceZeroFeeProduction is the end-to-end (in-process devnet)
// validation of the reserved-blockspace feature against the real registry contract.
// A two-node bor network produces and cross-verifies blocks; the registry contract
// is deployed in genesis and seeded at runtime via initialize()+createClient() so a
// client's address becomes reserved through actual contract state — the same source
// (registry → Go reader → per-block snapshot) the txpool and EVM classify from in
// production. A zero-fee tx from that registered address is admitted to the pool,
// mined past the ReservedBlockspace fork with the reserved header fields set by
// the real Prepare path, and executes fee-free so the sender pays only its call
// value with no gas debit. An identical zero-fee tx from an address absent from
// the registry is rejected at admission.
//
// Run with: go test -tags=integration -run TestReservedBlockspaceZeroFeeProduction ./tests/bor/
func TestReservedBlockspaceZeroFeeProduction(t *testing.T) {
	faucets := make([]*ecdsa.PrivateKey, 10)
	for i := range faucets {
		faucets[i], _ = crypto.GenerateKey()
	}
	reservedAddr := crypto.PubkeyToAddress(faucets[0].PublicKey)
	nonReservedAddr := crypto.PubkeyToAddress(faucets[1].PublicKey)
	ownerKey := faucets[2]
	ownerAddr := crypto.PubkeyToAddress(ownerKey.PublicKey)

	registryAddr := common.HexToAddress(params.DefaultReservedRegistryContract)
	setupABI, err := abi.JSON(strings.NewReader(reservedRegistrySetupABI))
	if err != nil {
		t.Fatal(err)
	}

	genesis := InitGenesis(t, faucets, "./testdata/genesis_2val.json", 8)
	// Reserved fork at block 5 (Cancun is active from block 3, so the reserved
	// header fields encode in the post-Cancun BlockExtraData format).
	reservedFork := uint64(5)
	genesis.Config.Bor.ReservedBlockspaceBlock = new(big.Int).SetUint64(reservedFork)
	// The registry runtime bytecode (solc 0.8.33) uses PUSH0, a Shanghai opcode.
	// This genesis activates Cancun at 3 but omits Shanghai; activate it at 2 so
	// the contract is callable before the reserved fork.
	genesis.Config.ShanghaiBlock = big.NewInt(2)
	// Deploy the registry contract in genesis (empty storage — ownership and
	// clients are established at runtime, exactly as on a real network) and point
	// the chain config at it so the engine wires the reader into txpool/EVM/miner.
	genesis.Config.Bor.ReservedRegistryContract = params.DefaultReservedRegistryContract
	genesis.Alloc[registryAddr] = types.Account{
		Balance: new(big.Int),
		Code:    common.FromHex(params.ReservedBlockspaceRegistryCode),
	}

	startBalance := new(big.Int).SetUint64(1_000_000_000_000_000_000) // 1 ETH
	genesis.Alloc[reservedAddr] = types.Account{Balance: new(big.Int).Set(startBalance)}
	genesis.Alloc[nonReservedAddr] = types.Account{Balance: new(big.Int).Set(startBalance)}
	genesis.Alloc[ownerAddr] = types.Account{Balance: new(big.Int).Set(startBalance)}

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

	// waitMined blocks until tx is found in a canonical block and returns it.
	waitMined := func(txHash common.Hash, what string) *types.Block {
		t.Helper()
		deadline := time.After(60 * time.Second)
		for {
			head := nodes[0].BlockChain().CurrentBlock().Number.Uint64()
			for n := uint64(0); n <= head; n++ {
				blk := nodes[0].BlockChain().GetBlockByNumber(n)
				if blk == nil {
					continue
				}
				for _, tx := range blk.Transactions() {
					if tx.Hash() == txHash {
						return blk
					}
				}
			}
			select {
			case <-deadline:
				t.Fatalf("timeout waiting for %s to be mined", what)
			case <-time.After(200 * time.Millisecond):
			}
		}
	}

	signer := types.LatestSigner(genesis.Config)

	// Seed the registry: initialize() claims ownership, createClient() registers
	// reservedAddr as the sole whitelisted address of a fee-free client. Both are
	// ordinary fee-paying txs from the owner; they execute in nonce order.
	sendOwnerTx := func(nonce uint64, data []byte) *types.Transaction {
		t.Helper()
		tx, err := types.SignNewTx(ownerKey, signer, &types.DynamicFeeTx{
			ChainID:   genesis.Config.ChainID,
			Nonce:     nonce,
			GasTipCap: big.NewInt(30_000_000_000),
			GasFeeCap: big.NewInt(100_000_000_000),
			Gas:       1_000_000,
			To:        &registryAddr,
			Value:     big.NewInt(0),
			Data:      data,
		})
		if err != nil {
			t.Fatal(err)
		}
		// Submit to the producer only; p2p propagates to the peer.
		if err := nodes[0].APIBackend.SendTx(context.Background(), tx); err != nil {
			t.Fatalf("registry setup tx rejected: %v", err)
		}
		return tx
	}

	initData, err := setupABI.Pack("initialize", ownerAddr, uint64(8_000_000), uint64(5_000_000))
	if err != nil {
		t.Fatal(err)
	}
	createData, err := setupABI.Pack("createClient",
		ownerAddr, uint64(5_000_000), uint8(0), uint64(0), "e2e", []common.Address{reservedAddr})
	if err != nil {
		t.Fatal(err)
	}

	// London activates at block 1 in this genesis; the dynamic-fee setup txs are
	// rejected before then ("pool not yet in London").
	waitForBlock(2)

	oNonce, err := nodes[0].APIBackend.GetPoolNonce(context.Background(), ownerAddr)
	if err != nil {
		t.Fatal(err)
	}
	sendOwnerTx(oNonce, initData)
	createTx := sendOwnerTx(oNonce+1, createData)
	seedBlock := waitMined(createTx.Hash(), "createClient")
	t.Logf("registry seeded: createClient mined in block %d", seedBlock.NumberU64())

	// Land the reserved tx inside node0's primary-producer window. With sprint=8
	// and two validators, node0 is the in-turn producer for blocks 1-8 and 17-24
	// while node1 produces 9-16 (see bor_test.go). Submitting at the start of the
	// 17-24 window lets node0 mine the tx canonically with room to spare, so the
	// chain doesn't reorg it out at the 9/17 producer handover. The parent state by
	// then long carries the client (createClient mined at block ~4), and the block
	// is well past the reserved fork (5).
	if seedBlock.NumberU64() >= 16 {
		t.Fatalf("registry seeded too late (block %d) for the 17-24 producer window", seedBlock.NumberU64())
	}
	waitForBlock(17)

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
		// Tolerate "already known" from the peer that received it via p2p — what
		// matters is that admission classified the reserved sender, not rejected it.
		if err := node.APIBackend.SendTx(context.Background(), rtx); err != nil &&
			!strings.Contains(err.Error(), "already known") {
			t.Fatalf("reserved zero-fee tx rejected by pool: %v", err)
		}
	}

	// Non-reserved sender: identical zero-fee tx must be rejected at admission.
	nrNonce, err := nodes[0].APIBackend.GetPoolNonce(context.Background(), nonReservedAddr)
	if err != nil {
		t.Fatal(err)
	}
	otx := zeroFeeTx(faucets[1], nrNonce)
	if err := nodes[0].APIBackend.SendTx(context.Background(), otx); err == nil {
		t.Fatal("non-reserved zero-fee tx should be rejected at admission, but was accepted")
	}

	firstSeen := waitMined(rtx.Hash(), "reserved zero-fee tx")
	t.Logf("reserved zero-fee tx first seen in block %d", firstSeen.NumberU64())

	// Let the tip settle: both miners may briefly fork at the tip (the peer can
	// build a competing block before the reserved tx propagates). Wait for several
	// confirmations so fork choice converges, then read the tx's settled canonical
	// block. A real classification split would never converge — the validator would
	// reject the producer's block as a bad block — so convergence here is itself the
	// consensus-parity assertion.
	canonicalTxBlock := func(n *eth.Ethereum) *types.Block {
		head := n.BlockChain().CurrentBlock().Number.Uint64()
		for h := firstSeen.NumberU64(); h <= head; h++ {
			blk := n.BlockChain().GetBlockByNumber(h)
			if blk == nil {
				continue
			}
			for _, tx := range blk.Transactions() {
				if tx.Hash() == rtx.Hash() {
					return blk
				}
			}
		}
		return nil
	}

	var includedBlock, peerBlock *types.Block
	settleDeadline := time.After(60 * time.Second)
	for {
		includedBlock = canonicalTxBlock(nodes[0])
		peerBlock = canonicalTxBlock(nodes[1])
		if includedBlock != nil && peerBlock != nil &&
			includedBlock.Hash() == peerBlock.Hash() &&
			nodes[1].BlockChain().CurrentBlock().Number.Uint64() >= includedBlock.NumberU64()+3 {
			break
		}
		select {
		case <-settleDeadline:
			h0 := nodes[0].BlockChain().CurrentBlock()
			h1 := nodes[1].BlockChain().CurrentBlock()
			t.Logf("node0 head=%d %s | node1 head=%d %s", h0.Number.Uint64(), h0.Hash(), h1.Number.Uint64(), h1.Hash())
			if includedBlock != nil {
				t.Logf("node0 tx-block=%d %s; node1 has it by hash: %v",
					includedBlock.NumberU64(), includedBlock.Hash(),
					nodes[1].BlockChain().GetBlockByHash(includedBlock.Hash()) != nil)
			}
			if peerBlock != nil {
				t.Logf("node1 tx-block=%d %s; node0 has it by hash: %v",
					peerBlock.NumberU64(), peerBlock.Hash(),
					nodes[0].BlockChain().GetBlockByHash(peerBlock.Hash()) != nil)
			}
			// Compare canonical hashes at each height up to the lower head to find
			// where (if anywhere) the chains forked.
			lo := h0.Number.Uint64()
			if h1.Number.Uint64() < lo {
				lo = h1.Number.Uint64()
			}
			for n := uint64(1); n <= lo; n++ {
				b0 := nodes[0].BlockChain().GetBlockByNumber(n)
				b1 := nodes[1].BlockChain().GetBlockByNumber(n)
				if b0 == nil || b1 == nil || b0.Hash() != b1.Hash() {
					t.Logf("chains first differ at height %d: node0=%v node1=%v", n, b0.Hash(), b1.Hash())
					break
				}
			}
			t.Fatalf("nodes never converged on the reserved tx block")
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Logf("reserved zero-fee tx settled in canonical block %d on both nodes", includedBlock.NumberU64())

	// The producing block must carry the reserved header fields (set by Prepare).
	rc, rg := includedBlock.Header().GetReservedInfo(genesis.Config)
	if rc == nil || rg == nil {
		t.Fatalf("block %d missing reserved header fields: count=%v gas=%v", includedBlock.NumberU64(), rc, rg)
	}

	// The reserved sender paid only its call value — no gas was debited — and both
	// nodes computed the same post-state for the block (identical hash above).
	state, err := nodes[0].BlockChain().StateAt(includedBlock.Root())
	if err != nil {
		t.Fatal(err)
	}
	got := state.GetBalance(reservedAddr).ToBig()
	want := new(big.Int).Sub(startBalance, big.NewInt(100))
	if got.Cmp(want) != 0 {
		t.Fatalf("reserved sender balance = %s, want %s (call value only, no gas debit)", got, want)
	}
}
