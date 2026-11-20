package eth

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
)

func TestHandlerRequestStopIsNonWaitingAndIdempotent(t *testing.T) {
	h := &handler{quitSync: make(chan struct{}), peers: newPeerSet()}
	h.requestStop()
	h.requestStop()
	select {
	case <-h.quitSync:
	default:
		t.Fatal("requestStop left sync admission open")
	}
	if !h.peers.closed {
		t.Fatal("requestStop left peer admission open")
	}
}

func TestChainSyncerDoesNotRetryEngineShutdown(t *testing.T) {
	cs := &chainSyncer{force: time.NewTimer(time.Hour), doneCh: make(chan error, 1)}
	defer cs.force.Stop()
	cs.operationalRetryUntil = time.Now().Add(time.Hour)
	abort := fmt.Errorf("%w: %w", core.ErrStateSyncProcessing, consensus.NewEngineStoppedError(context.Canceled))
	cs.onSyncDone(abort)
	if !cs.operationalRetryUntil.IsZero() {
		t.Fatal("engine shutdown retained the operational retry delay")
	}
}
