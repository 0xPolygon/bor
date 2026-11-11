package eth

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

func TestChainSyncerStateSyncRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cleanup := newChainSyncerTestHandler(t)
		defer cleanup()
		cs := newChainSyncer(h)
		cs.force = time.NewTimer(forceSyncCycle)
		defer cs.force.Stop()
		peer := registerPeerWithTD(t, h.peers, 1_000_000)
		if err := h.downloader.RegisterPeer(peer.ID(), eth.ETH68, &ethPeer{Peer: peer}); err != nil {
			t.Fatal(err)
		}
		cs.onPeerEvent()
		failure := fmt.Errorf("import: %w: %w", core.ErrStateSyncProcessing, context.DeadlineExceeded)
		cs.onSyncDone(failure)
		if op, wait := cs.nextSyncOp(); op != nil || wait != forceSyncCycle {
			t.Fatalf("operational failure: op=%v wait=%v, want nil/%v", op, wait, forceSyncCycle)
		}
		registerPeerWithTD(t, h.peers, 100)
		cs.onPeerEvent()
		time.Sleep(forceSyncCycle - time.Nanosecond)
		if op, wait := cs.nextSyncOp(); op != nil || wait != time.Nanosecond {
			t.Fatalf("peer event bypassed retry delay: op=%v wait=%v", op, wait)
		}
		time.Sleep(time.Nanosecond)
		if op, wait := cs.nextSyncOp(); op == nil || op.peer.ID() != peer.ID() || wait != 0 {
			t.Fatalf("retry did not resume with the original peer: op=%v wait=%v", op, wait)
		}
		cs.onSyncDone(failure)
		cs.onSyncDone(nil)
		if op, wait := cs.nextSyncOp(); op == nil || wait != 0 {
			t.Fatalf("successful sync retained retry delay: op=%v wait=%v", op, wait)
		}
		cs.onSyncDone(fmt.Errorf("import: %w", core.ErrStateSyncMismatch))
		if op, wait := cs.nextSyncOp(); op == nil || wait != 0 {
			t.Fatalf("invalid data received an operational retry delay: op=%v wait=%v", op, wait)
		}
	})
}

func TestChainSyncerStateSyncRetryShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, cleanup := newChainSyncerTestHandler(t)
		defer cleanup()
		cs := h.chainSync
		cs.force = time.NewTimer(forceSyncCycle)
		defer cs.force.Stop()
		start := time.Now()
		cs.onSyncDone(core.ErrStateSyncProcessing)
		cs.shutdown()
		if time.Since(start) != 0 {
			t.Fatal("shutdown waited for the operational retry delay")
		}
	})
}
