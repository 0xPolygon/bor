package core

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

// prepareStatelessBenchmark sets up the stateless and full-state chains, generates blocks,
// constructs witnesses using full-state execution, and starts header verification.
func prepareStatelessBenchmark(b *testing.B) (stateLessChain *BlockChain, stateFullChain *BlockChain, blocks types.Blocks, witnesses []*stateless.Witness, stopHeaders func(), errChans []chan error) {
	b.Helper()

	engine := ethash.NewFaker()
	gspec := &Genesis{Config: params.TestChainConfig}
	cfg := DefaultConfig()
	cfg.Stateless = true

	stateLessChain, err := NewBlockChain(rawdb.NewMemoryDatabase(), gspec, engine, cfg)
	if err != nil {
		b.Fatalf("failed to create stateless chain: %v", err)
	}

	stateFullChain, err = NewBlockChain(rawdb.NewMemoryDatabase(), gspec, engine, cfg)
	if err != nil {
		b.Fatalf("failed to create full-state chain: %v", err)
	}

	_, blocks, _ = GenerateChainWithGenesis(gspec, engine, b.N, func(i int, b *BlockGen) {
		b.SetCoinbase(common.Address{1})
	})

	headers := make([]*types.Header, len(blocks))
	for i, blk := range blocks {
		headers[i] = blk.Header()
	}

	witnesses = make([]*stateless.Witness, len(blocks))
	for i, blk := range blocks {
		var w *stateless.Witness
		w, _, err = stateFullChain.insertChain(types.Blocks{blk}, true, true)
		if err != nil {
			b.Fatalf("failed to build witness via full-state insert: %v", err)
		}
		witnesses[i] = w
	}

	stopHeaders, errChans = stateLessChain.prepareHeaderVerification(headers)
	return
}

func BenchmarkInsertChainStatelessSequential(b *testing.B) {
	stateLessChain, stateFullChain, blocks, witnesses, stopHeaders, errChans := prepareStatelessBenchmark(b)
	defer stateLessChain.Stop()
	defer stateFullChain.Stop()
	defer stopHeaders()

	b.ReportAllocs()
	b.ResetTimer()

	_, err := stateLessChain.insertChainStatelessSequential(blocks, witnesses, errChans, &insertStats{startTime: mclock.Now()})
	if err != nil {
		b.Fatalf("sequential stateless insert failed: %v", err)
	}
}

func BenchmarkInsertChainStatelessParallel(b *testing.B) {
	stateLessChain, stateFullChain, blocks, witnesses, stopHeaders, errChans := prepareStatelessBenchmark(b)
	defer stateLessChain.Stop()
	defer stateFullChain.Stop()
	defer stopHeaders()

	b.ReportAllocs()
	b.ResetTimer()

	_, err := stateLessChain.insertChainStatelessParallel(blocks, witnesses, errChans, &insertStats{startTime: mclock.Now()}, stopHeaders)
	if err != nil {
		b.Fatalf("parallel stateless insert failed: %v", err)
	}
}
