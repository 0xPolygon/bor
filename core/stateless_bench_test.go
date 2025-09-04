package core

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

// FIXME: missing trie node
func BenchmarkInsertChainStatelessSequential(b *testing.B) {
	// Create test chain
	var (
		engine = ethash.NewFaker()
		gspec  = &Genesis{
			Config: params.TestChainConfig,
		}
	)

	// Create blockchain with stateless config
	cacheConfig := &CacheConfig{
		TriesInMemory: 0,
	}
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), cacheConfig, gspec, nil, engine, vm.Config{}, nil, nil, nil)
	if err != nil {
		b.Fatalf("failed to create chain: %v", err)
	}
	defer chain.Stop()

	_, blocks, _ := GenerateChainWithGenesis(gspec, engine, b.N, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{1})
	})

	mockHeaderChain := stateless.NewMockHeaderReader()
	mockHeaderChain.AddHeader(gspec.ToBlock().Header()) // Add genesis header
	headers := make([]*types.Header, len(blocks))
	for i, blk := range blocks {
		headers[i] = blk.Header()
		mockHeaderChain.AddHeader(blk.Header())
	}

	witnesses := make([]*stateless.Witness, len(blocks))
	for i, block := range blocks {
		w, err := stateless.NewWitness(block.Header(), mockHeaderChain)
		if err != nil {
			b.Fatalf("failed to create witness: %v", err)
		}
		witnesses[i] = w
	}

	b.ReportAllocs()
	b.ResetTimer()

	stopHeaders, errChans := chain.prepareHeaderVerification(headers)
	defer stopHeaders()

	_, err = chain.insertChainStatelessSequential(blocks, witnesses, errChans, &insertStats{startTime: mclock.Now()})
	if err != nil {
		b.Fatalf("sequential stateless insert failed: %v", err)
	}
}

// FIXME: missing trie node
func BenchmarkInsertChainStatelessParallel(b *testing.B) {
	// Create test chain
	var (
		engine = ethash.NewFaker()
		gspec  = &Genesis{
			Config: params.TestChainConfig,
		}
	)

	// Create blockchain with stateless config
	cacheConfig := &CacheConfig{
		TriesInMemory: 0,
	}
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), cacheConfig, gspec, nil, engine, vm.Config{}, nil, nil, nil)
	if err != nil {
		b.Fatalf("failed to create chain: %v", err)
	}
	defer chain.Stop()

	_, blocks, _ := GenerateChainWithGenesis(gspec, engine, b.N, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{1})
	})

	mockHeaderChain := stateless.NewMockHeaderReader()
	mockHeaderChain.AddHeader(gspec.ToBlock().Header()) // Add genesis header
	headers := make([]*types.Header, len(blocks))
	for i, blk := range blocks {
		headers[i] = blk.Header()
		mockHeaderChain.AddHeader(blk.Header())
	}

	witnesses := make([]*stateless.Witness, len(blocks))
	for i, block := range blocks {
		w, err := stateless.NewWitness(block.Header(), mockHeaderChain)
		if err != nil {
			b.Fatalf("failed to create witness: %v", err)
		}
		witnesses[i] = w
	}

	// Enable parallel stateless import to align ProcessBlockWithWitnesses behavior
	chain.ParallelStatelessImportEnable()

	b.ReportAllocs()
	b.ResetTimer()

	stopHeaders, errChans := chain.prepareHeaderVerification(headers)
	defer stopHeaders()

	_, err = chain.insertChainStatelessParallel(blocks, witnesses, errChans, &insertStats{startTime: mclock.Now()}, stopHeaders)
	if err != nil {
		b.Fatalf("parallel stateless insert failed: %v", err)
	}
}
