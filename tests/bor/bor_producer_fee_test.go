//go:build integration
// +build integration

package bor

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestProducerFeeShare_MinedAndImported runs two mining validators across the
// NativeStaking fork and checks every block with transactions: before the fork
// the coinbase keeps all priority fees, and from the fork on the distributor is
// the coinbase and the block's producer is paid 26% of them. Each node imports
// the other's blocks, so the payout has to come out the same on both.
func TestProducerFeeShare_MinedAndImported(t *testing.T) {
	const (
		rioBlock  = 16
		forkBlock = 24
		lastBlock = 30
	)

	feeSafe := common.HexToAddress("0x000000000000000000000000000000000000ba5e")

	faucets := make([]*ecdsa.PrivateKey, 4)
	for i := range faucets {
		faucets[i], _ = crypto.GenerateKey()
	}

	genesis := InitGenesis(t, faucets, "./testdata/genesis_2val.json", 8)
	genesis.Config.Bor.RioBlock = big.NewInt(rioBlock)
	genesis.Config.Bor.Coinbase = map[string]string{
		"0":  "0x0000000000000000000000000000000000000000",
		"16": feeSafe.Hex(),
	}
	genesis.Config.Bor.NativeStakingBlock = big.NewInt(forkBlock)
	genesis.Config.Bor.StakingContract = stakingContract.Hex()
	genesis.Config.Bor.DistributorContract = distributorContract.Hex()

	data, err := os.ReadFile("../../consensus/bor/testdata/staking-contracts-alloc.json")
	require.NoError(t, err)

	var alloc types.GenesisAlloc
	require.NoError(t, json.Unmarshal(data, &alloc))

	for address, account := range alloc {
		genesis.Alloc[address] = account
	}

	genesis.Alloc[feeSafe] = types.Account{Balance: big.NewInt(0)}

	sender := crypto.PubkeyToAddress(faucets[0].PublicKey)
	genesis.Alloc[sender] = types.Account{Balance: big.NewInt(1e18)}

	stacks, nodes, _ := setupMiner(t, 2, genesis)
	defer func() {
		for _, stack := range stacks {
			stack.Close()
		}
	}()

	for _, node := range nodes {
		require.NoError(t, node.StartMining())
	}

	// Keep a few transactions flowing until the chain is past the fork.
	signer := types.NewEIP155Signer(genesis.Config.ChainID)
	nonce := uint64(0)
	deadline := time.Now().Add(3 * time.Minute)

	for nodes[0].BlockChain().CurrentBlock().Number.Uint64() < lastBlock {
		require.True(t, time.Now().Before(deadline), "chain did not reach block %d", lastBlock)

		pending, err := nodes[0].APIBackend.GetPoolNonce(context.Background(), sender)
		require.NoError(t, err)

		for ; nonce < pending+3; nonce++ {
			to := crypto.PubkeyToAddress(faucets[1+nonce%3].PublicKey)
			tx, err := types.SignTx(types.NewTransaction(nonce, to, big.NewInt(100), 21000, big.NewInt(30_000_000_000), nil), signer, faucets[0])
			require.NoError(t, err)

			for _, node := range nodes {
				if err := node.APIBackend.SendTx(context.Background(), tx); err != nil && !strings.Contains(err.Error(), "already known") {
					t.Logf("sending transaction: %v", err)
				}
			}
		}

		time.Sleep(200 * time.Millisecond)
	}

	chain := nodes[0].BlockChain()
	checked := map[string]int{}

	for number := uint64(rioBlock + 1); number <= lastBlock; number++ {
		block := chain.GetBlockByNumber(number)
		require.NotNil(t, block, "block %d", number)

		if len(block.Transactions()) == 0 {
			continue
		}

		receipts := chain.GetReceiptsByHash(block.Hash())
		require.Len(t, receipts, len(block.Transactions()))

		fees := new(big.Int)

		for i, tx := range block.Transactions() {
			if tx.IsBorSystemTx() {
				continue
			}

			tip, err := tx.EffectiveGasTip(block.BaseFee())
			require.NoError(t, err)
			fees.Add(fees, tip.Mul(tip, new(big.Int).SetUint64(receipts[i].GasUsed)))
		}

		if fees.Sign() == 0 {
			continue
		}

		before, err := chain.StateAt(chain.GetBlockByNumber(number - 1).Root())
		require.NoError(t, err)

		after, err := chain.StateAt(block.Root())
		require.NoError(t, err)

		gained := func(address common.Address) *big.Int {
			return new(big.Int).Sub(after.GetBalance(address).ToBig(), before.GetBalance(address).ToBig())
		}

		producer, err := nodes[0].Engine().Author(block.Header())
		require.NoError(t, err)

		if number < forkBlock {
			require.Equal(t, fees, gained(feeSafe), "block %d: the coinbase keeps all priority fees before the fork", number)
			require.Zero(t, gained(producer).Sign(), "block %d: no producer share before the fork", number)
			require.Zero(t, gained(distributorContract).Sign(), "block %d", number)
			checked["before"]++

			continue
		}

		share := new(big.Int).Div(new(big.Int).Mul(fees, big.NewInt(26)), big.NewInt(100))
		require.Equal(t, share, gained(producer), "block %d: producer share", number)
		require.Equal(t, new(big.Int).Sub(fees, share), gained(distributorContract), "block %d: the distributor keeps the rest", number)
		require.Zero(t, gained(feeSafe).Sign(), "block %d: the old coinbase gets nothing from the fork on", number)
		checked["after"]++
	}

	require.Positive(t, checked["before"], "no block with fees between Rio and the fork")
	require.Positive(t, checked["after"], "no block with fees from the fork on")

	// The second node imported the same chain, so it computed the same payouts.
	require.Eventually(t, func() bool {
		other := nodes[1].BlockChain().GetBlockByNumber(lastBlock)
		return other != nil && other.Hash() == chain.GetBlockByNumber(lastBlock).Hash()
	}, 30*time.Second, 200*time.Millisecond, "nodes disagree on block %d", lastBlock)
}
