package core

import (
	"context"
	"errors"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

type shutdownHeaderEngine struct {
	*ethash.Ethash
	closed  atomic.Bool
	verdict error
}

func (e *shutdownHeaderEngine) Close() error {
	e.closed.Store(true)
	return nil
}

func (e *shutdownHeaderEngine) VerifyHeader(chain consensus.ChainHeaderReader, header *types.Header) error {
	if e.closed.Load() {
		return e.verdict
	}
	return e.Ethash.VerifyHeader(chain, header)
}

func (e *shutdownHeaderEngine) VerifyHeaders(chain consensus.ChainHeaderReader, headers []*types.Header) (chan<- struct{}, <-chan error) {
	if !e.closed.Load() {
		return e.Ethash.VerifyHeaders(chain, headers)
	}
	abort := make(chan struct{})
	results := make(chan error, len(headers))
	for _, header := range headers {
		if header.Number.Uint64() == 6 {
			results <- e.verdict
		} else {
			results <- nil
		}
	}
	return abort, results
}

func newShutdownHeaderChain(t *testing.T, verdict error) (*BlockChain, *shutdownHeaderEngine) {
	t.Helper()
	engine := &shutdownHeaderEngine{Ethash: ethash.NewFaker(), verdict: verdict}
	config := *params.TestChainConfig
	config.Bor = &params.BorConfig{RioBlock: big.NewInt(0)}
	genesis := &Genesis{Config: &config, BaseFee: big.NewInt(params.InitialBaseFee)}
	_, blocks, _ := GenerateChainWithGenesis(genesis, engine.Ethash, 8, nil)
	chain, err := NewBlockChain(rawdb.NewMemoryDatabase(), genesis, engine, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		chain.Stop()
	})
	if _, err := chain.InsertChain(blocks, false); err != nil {
		t.Fatal(err)
	}
	if got := chain.CurrentBlock().Number.Uint64(); got != 8 {
		t.Fatalf("fixture head = %d, want 8", got)
	}
	return chain, engine
}

func TestPendingHeaderVerificationSkipsEngineStoppedRewind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict error
		head    uint64
	}{
		{name: "engine stopped", verdict: consensus.NewEngineStoppedError(context.Canceled), head: 8},
		{name: "unauthorized header", verdict: consensus.ErrUnauthorizedSigner, head: 5},
		{name: "valid headers", head: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain, engine := newShutdownHeaderChain(t, tc.verdict)
			entered := make(chan struct{})
			release := make(chan struct{})
			chain.milestoneFetcher = func(context.Context) (uint64, error) {
				close(entered)
				<-release
				return 3, nil
			}
			done := make(chan struct{})
			go func() {
				chain.verifyPendingHeaders()
				close(done)
			}()
			<-entered
			chain.StopInsert()
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			close(release)
			<-done

			if got := chain.CurrentBlock().Number.Uint64(); got != tc.head {
				t.Errorf("head = %d, want %d after %v", got, tc.head, tc.verdict)
			}
			if persisted := rawdb.ReadHeadBlockHash(chain.db); persisted != chain.GetCanonicalHash(tc.head) {
				t.Errorf("persisted head = %s, want canonical %d = %s", persisted, tc.head, chain.GetCanonicalHash(tc.head))
			}
		})
	}
}

func TestConflictingParentPreservesEngineStopped(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict error
		head    uint64
		stopped bool
	}{
		{name: "engine stopped", verdict: consensus.NewEngineStoppedError(context.Canceled), head: 8, stopped: true},
		{name: "unauthorized parent", verdict: consensus.ErrUnauthorizedSigner, head: 7},
		{name: "valid parent", head: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chain, engine := newShutdownHeaderChain(t, tc.verdict)
			chain.StopInsert()
			if err := engine.Close(); err != nil {
				t.Fatal(err)
			}
			incoming := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(9), ParentHash: common.Hash{0x99}})
			err := chain.handleHeaderVerificationError(incoming, 0, consensus.ErrUnknownAncestor)
			if got := errors.Is(err, consensus.ErrEngineStopped); got != tc.stopped {
				t.Errorf("ErrEngineStopped match = %t, want %t; error = %v", got, tc.stopped, err)
			}
			if got := chain.CurrentBlock().Number.Uint64(); got != tc.head {
				t.Errorf("head = %d, want %d after %v", got, tc.head, tc.verdict)
			}
		})
	}
}
