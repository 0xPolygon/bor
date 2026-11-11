package core

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
)

type stateSyncFinalizer struct {
	consensus.Engine
	receipts []*types.Receipt
	err      error
}

func (e *stateSyncFinalizer) Finalize(consensus.ChainHeaderReader, *types.Header, vm.StateDB, *types.Body, []*types.Receipt) ([]*types.Receipt, error) {
	return e.receipts, e.err
}

func TestStateSyncProcessorErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		receipts []*types.Receipt
		err      error
		want     error
	}{
		{name: "success"},
		{name: "receipt count mismatch", receipts: []*types.Receipt{{}}, want: ErrStateSyncMismatch},
		{name: "operational failure", err: fmt.Errorf("%w: %w", ErrStateSyncProcessing, context.DeadlineExceeded), want: ErrStateSyncProcessing},
		{name: "state sync mismatch", err: ErrStateSyncMismatch, want: ErrStateSyncMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := *params.TestChainConfig
			config.Bor = &params.BorConfig{MadhugiriBlock: big.NewInt(0)}
			chain := &HeaderChain{config: &config, engine: &stateSyncFinalizer{Engine: ethash.NewFaker(), receipts: tc.receipts, err: tc.err}}
			for name, processor := range map[string]Processor{
				"serial": NewStateProcessor(chain),
				"v2":     NewV2StateProcessor(chain, nil, 1),
			} {
				t.Run(name, func(t *testing.T) {
					statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
					if err != nil {
						t.Fatal(err)
					}
					defer statedb.StopPrefetcher()
					block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1), BaseFee: big.NewInt(1), GasLimit: 30_000_000})
					result, err := processor.Process(block, statedb, vm.Config{}, nil, t.Context())
					if !errors.Is(err, tc.want) {
						t.Fatalf("error = %v, want %v", err, tc.want)
					}
					if tc.want != nil && result != nil {
						t.Fatal("failed block returned a processing result")
					}
					if tc.want == ErrStateSyncMismatch && errors.Is(err, ErrStateSyncProcessing) {
						t.Fatal("invalid block was labeled as an operational failure")
					}
				})
			}
		})
	}
}
