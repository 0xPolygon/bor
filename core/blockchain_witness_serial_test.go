package core

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
)

// recordingParallelStateProcessor fails on invocation while recording that it
// ran, letting tests assert which blocks reached the parallel processor.
type recordingParallelStateProcessor struct {
	called atomic.Bool
}

func (p *recordingParallelStateProcessor) Process(block *types.Block, statedb *state.StateDB, cfg vm.Config, author *common.Address, interruptCtx context.Context) (*ProcessResult, error) {
	p.called.Store(true)
	return nil, errors.New("parallel processor invoked")
}

func TestWitnessBlockRunsSerialProcessor(t *testing.T) {
	t.Parallel()

	testWitnessBlockRunsSerialProcessor(t, rawdb.HashScheme, false)
	testWitnessBlockRunsSerialProcessor(t, rawdb.PathScheme, false)

	testWitnessBlockRunsSerialProcessor(t, rawdb.HashScheme, true)
	testWitnessBlockRunsSerialProcessor(t, rawdb.PathScheme, true)
}

func testWitnessBlockRunsSerialProcessor(t *testing.T, scheme string, enforce bool) {
	db, _, blockchain, err := newCanonical(ethash.NewFaker(), 10, true, scheme)
	if err != nil {
		t.Fatalf("failed to create canonical chain: %v", err)
	}
	defer blockchain.Stop()

	probe := &recordingParallelStateProcessor{}
	blockchain.parallelProcessor = probe
	blockchain.enforceParallelProcessor = enforce
	// Slow the serial processor down so the parallel probe always finishes
	// first on non-witness blocks, making the invocation record deterministic.
	blockchain.processor = NewSlowSerialStateProcessor(blockchain.processor)

	parent := blockchain.GetBlockByHash(blockchain.CurrentBlock().Hash())
	blocks := makeBlockChain(blockchain.chainConfig, parent, 2, ethash.NewFaker(), db, canonicalSeed)

	witness, err := blockchain.InsertBlockWithoutSetHead(blocks[0], true)
	if err != nil {
		t.Fatalf("failed to import witness-recording block: %v", err)
	}
	if witness == nil {
		t.Fatal("expected a witness from witness-recording import")
	}
	if probe.called.Load() {
		t.Fatal("parallel processor ran for a witness-recording block")
	}

	if enforce {
		return
	}

	// Non-witness blocks still reach the parallel processor; the failing
	// probe exercises the serial fallback.
	if _, err := blockchain.InsertChain(blocks[1:], false); err != nil {
		t.Fatalf("failed to import non-witness block: %v", err)
	}
	if !probe.called.Load() {
		t.Fatal("parallel processor did not run for a non-witness block")
	}
}
