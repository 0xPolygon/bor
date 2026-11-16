package core

import (
	"context"
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
)

func TestV2WriteLimit(t *testing.T) {
	tests := []struct {
		gasUsed uint64
		want    int64
	}{
		{0, 0},
		{3, 0},
		{4, 1},
		{params.TxGas, int64(params.TxGas / 4)},
		{30_000_000, 7_500_000},
		{math.MaxUint64, int64(uint64(math.MaxUint64) / 4)},
	}
	for _, tt := range tests {
		if got := v2WriteLimit(tt.gasUsed); got != tt.want {
			t.Errorf("v2WriteLimit(%d) = %d, want %d", tt.gasUsed, got, tt.want)
		}
		if got := v2WriteLimit(tt.gasUsed); got < 0 {
			t.Errorf("v2WriteLimit(%d) = %d, must not be negative", tt.gasUsed, got)
		}
	}
}

// transferBlock builds one block of plain value transfers on a Bor-enabled
// test chain.
func transferBlock(t *testing.T, txs int) (*Genesis, *types.Block) {
	t.Helper()
	key, err := crypto.HexToECDSA("b71c71a67e1177ad4e901695e1b4b9ee17ae16c6668d313eac2f96dbcda3f291")
	if err != nil {
		t.Fatal(err)
	}
	sender := crypto.PubkeyToAddress(key.PublicKey)

	chainConfig := *params.AllEthashProtocolChanges
	borConfig := *chainConfig.Bor
	borConfig.MadhugiriBlock = big.NewInt(0)
	chainConfig.Bor = &borConfig
	genesis := &Genesis{
		Config:   &chainConfig,
		GasLimit: 45_000_000,
		BaseFee:  big.NewInt(params.InitialBaseFee),
		Alloc: types.GenesisAlloc{
			sender: {Balance: new(big.Int).Exp(big.NewInt(10), big.NewInt(30), nil)},
		},
	}
	signer := types.LatestSigner(genesis.Config)
	_, blocks, _ := GenerateChainWithGenesis(genesis, ethash.NewFaker(), 1, func(_ int, block *BlockGen) {
		for i := 0; i < txs; i++ {
			to := common.BigToAddress(big.NewInt(int64(0x1000 + i)))
			tx, err := types.SignTx(types.NewTransaction(uint64(i), to, big.NewInt(1), params.TxGas,
				big.NewInt(params.BorDefaultMinerGasPrice), nil), signer, key)
			if err != nil {
				t.Fatal(err)
			}
			block.AddTx(tx)
		}
	})
	return genesis, blocks[0]
}

func newV2Chain(t *testing.T, genesis *Genesis, enforce bool, writeLimit func(uint64) int64) *BlockChain {
	t.Helper()
	chain, err := NewParallelBlockChain(rawdb.NewMemoryDatabase(), genesis, ethash.NewFaker(), DefaultConfig(), 4, enforce)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(chain.Stop)
	if writeLimit != nil {
		chain.parallelProcessor.(*V2StateProcessor).writeLimit = writeLimit
	}
	return chain
}

func TestV2ProcessWriteBudget(t *testing.T) {
	genesis, block := transferBlock(t, 20)

	tests := []struct {
		name       string
		writeLimit func(uint64) int64
		wantErr    error
	}{
		{"default limit", nil, nil},
		{"tiny limit", func(uint64) int64 { return 1 }, errV2WriteBudgetExhausted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chain := newV2Chain(t, genesis, true, tt.writeLimit)
			statedb, err := chain.StateAt(chain.CurrentBlock().Root)
			if err != nil {
				t.Fatal(err)
			}
			res, err := chain.parallelProcessor.Process(block, statedb, vm.Config{}, nil, context.Background())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Process error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && res.GasUsed != block.GasUsed() {
				t.Fatalf("gas used = %d, want %d", res.GasUsed, block.GasUsed())
			}
		})
	}
}

// A block that trips the write budget is still imported, with the same
// result as serial execution, whether or not the parallel processor is
// enforced.
func TestProcessBlockFallsBackToSerialOnWriteBudget(t *testing.T) {
	genesis, block := transferBlock(t, 20)

	serial, err := NewBlockChain(rawdb.NewMemoryDatabase(), genesis, ethash.NewFaker(), DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer serial.Stop()
	wantReceipts, _, wantGas, wantState, _, err := serial.ProcessBlock(block, serial.CurrentBlock(), nil, nil, nil)
	if err != nil {
		t.Fatalf("serial ProcessBlock: %v", err)
	}
	wantRoot := wantState.IntermediateRoot(true)

	for _, enforce := range []bool{false, true} {
		chain := newV2Chain(t, genesis, enforce, func(uint64) int64 { return 1 })
		receipts, _, gas, statedb, _, err := chain.ProcessBlock(block, chain.CurrentBlock(), nil, nil, nil)
		if err != nil {
			t.Fatalf("enforce=%v: ProcessBlock: %v", enforce, err)
		}
		if gas != wantGas {
			t.Errorf("enforce=%v: gas = %d, want %d", enforce, gas, wantGas)
		}
		if root := statedb.IntermediateRoot(true); root != wantRoot {
			t.Errorf("enforce=%v: state root = %x, want %x", enforce, root, wantRoot)
		}
		if got, want := types.DeriveSha(receipts, trie.NewStackTrie(nil)), types.DeriveSha(wantReceipts, trie.NewStackTrie(nil)); got != want {
			t.Errorf("enforce=%v: receipt root = %x, want %x", enforce, got, want)
		}
	}
}
