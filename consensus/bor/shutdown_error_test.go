package bor

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
	"github.com/ethereum/go-ethereum/consensus/bor/valset"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

func TestShutdownErrorRequiresEngineOwnership(t *testing.T) {
	b := &Bor{}
	activeCancellation := fmt.Errorf("fetch: %w", context.Canceled)
	if got := b.shutdownError(activeCancellation); got != activeCancellation {
		t.Fatal("active-engine dependency failure was reclassified as shutdown")
	}

	validationFailure := errors.New("invalid receipt root")
	b.closed.Store(true)
	if got := b.shutdownError(validationFailure); got != validationFailure {
		t.Fatal("validation failure racing engine Close was reclassified")
	}
	for _, cause := range []error{context.Canceled, heimdall.ErrShutdownDetected} {
		err := b.shutdownError(fmt.Errorf("fetch: %w", cause))
		if !errors.Is(err, consensus.ErrEngineStopped) {
			t.Fatalf("engine-owned %v was not classified as shutdown", cause)
		}
		if !errors.Is(err, cause) {
			t.Fatalf("engine-owned %v lost its original cause", cause)
		}
	}
}

type cancelSpanner struct {
	fakeSpanner
}

func (s *cancelSpanner) GetCurrentValidatorsByHash(context.Context, common.Hash, uint64) ([]*valset.Validator, error) {
	return nil, context.Canceled
}

func (s *cancelSpanner) GetCurrentValidatorsByBlockNrOrHash(context.Context, rpc.BlockNumberOrHash, uint64) ([]*valset.Validator, error) {
	return nil, context.Canceled
}

func TestVerifyHeaderClassifiesOnlyClosedEngineCancellation(t *testing.T) {
	sp := &cancelSpanner{}
	chain, engine := newChainAndBorForTest(t, sp, defaultBorConfig(), false, common.Address{}, uint64(time.Now().Add(-time.Minute).Unix()))
	defer chain.Stop()
	parent := chain.HeaderChain().GetHeaderByNumber(0)
	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     big.NewInt(1),
		Time:       parent.Time + 2,
		Extra:      make([]byte, 32+65),
		UncleHash:  uncleHash,
		Difficulty: big.NewInt(1),
		GasLimit:   parent.GasLimit,
	}
	if err := engine.VerifyHeader(chain, header); !errors.Is(err, context.Canceled) || errors.Is(err, consensus.ErrEngineStopped) {
		t.Fatalf("active header dependency failure misclassified: %v", err)
	}
	engine.closed.Store(true)
	if err := engine.VerifyHeader(chain, header); !errors.Is(err, consensus.ErrEngineStopped) || !errors.Is(err, context.Canceled) {
		t.Fatalf("closed-engine header cancellation not preserved: %v", err)
	}

	invalid := types.CopyHeader(header)
	invalid.Number = nil
	if err := engine.VerifyHeader(chain, invalid); !errors.Is(err, errUnknownBlock) || errors.Is(err, consensus.ErrEngineStopped) {
		t.Fatalf("invalid header hidden by engine shutdown: %v", err)
	}
}

func TestCloseMarksEngineShutdownOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	engine := &Bor{quit: make(chan struct{}), ctxCancel: cancel}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	if !engine.closed.Load() {
		t.Fatal("Close did not record engine-owned shutdown")
	}
	if ctx.Err() == nil {
		t.Fatal("Close did not cancel engine work")
	}
}
