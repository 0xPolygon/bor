package core

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/beacon"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
)

// Stateless nodes on earlier releases read the system address during the
// EIP-2935 system call, so before Amsterdam a block's witness has to carry its
// account proof.
func TestWitnessCarriesSystemAddressBeforeAmsterdam(t *testing.T) {
	config := *params.MergedTestChainConfig
	alloc := withSystemContracts(types.GenesisAlloc{
		params.SystemAddress: {Balance: common.Big1},
	})
	// Enough accounts that the system address sits below a shared trie root.
	for i := 0; i < 2048; i++ {
		alloc[common.BigToAddress(big.NewInt(int64(i+1)))] = types.Account{Balance: common.Big1}
	}
	gspec := &Genesis{
		Config:     &config,
		Alloc:      alloc,
		BaseFee:    big.NewInt(params.InitialBaseFee),
		Difficulty: common.Big1,
		GasLimit:   5_000_000,
	}
	engine := beacon.New(ethash.NewFaker())

	_, blocks, _ := GenerateChainWithGenesis(gspec, engine, 2, func(_ int, b *BlockGen) { b.SetPoS() })

	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, engine, DefaultConfig())
	require.NoError(t, err)
	defer chain.Stop()

	_, err = chain.InsertChain(blocks[:1], false)
	require.NoError(t, err)
	witness, err := chain.InsertBlockWithoutSetHead(blocks[1], true)
	require.NoError(t, err)
	require.NotNil(t, witness)

	db := triedb.NewDatabase(witness.MakeHashDB(rawdb.NewMemoryDatabase()), triedb.HashDefaults)
	tr, err := trie.NewStateTrie(trie.StateTrieID(witness.Root()), db)
	require.NoError(t, err)

	acc, err := tr.GetAccount(params.SystemAddress)
	require.NoError(t, err, "witness is missing the system address proof")
	require.NotNil(t, acc)
}
