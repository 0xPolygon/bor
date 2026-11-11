package downloader

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

func TestLegacySyncStateSyncFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		drop    bool
		backoff bool
	}{
		{name: "operational", err: core.ErrStateSyncProcessing},
		{name: "wrapped operational", err: fmt.Errorf("%w: %w", errInvalidChain, core.ErrStateSyncProcessing)},
		{name: "heimdall timeout", err: fmt.Errorf("%w: %w: %w", errInvalidChain, core.ErrStateSyncProcessing, context.DeadlineExceeded)},
		{name: "state sync mismatch", err: fmt.Errorf("%w: %w", errInvalidChain, core.ErrStateSyncMismatch), drop: true},
		{name: "invalid state", err: fmt.Errorf("%w: %w", errInvalidChain, core.ErrStatelessStateRootMismatch), drop: true},
		{name: "operational error text", err: fmt.Errorf("%w: %s", errInvalidChain, core.ErrStateSyncProcessing), drop: true},
		{name: "peer timeout", err: context.DeadlineExceeded, backoff: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []SyncMode{FullSync, StatelessSync} {
				t.Run(mode.String(), func(t *testing.T) {
					testStateSyncPeerResponse(t, mode, tc.err, tc.drop, tc.backoff)
				})
			}
		})
	}
}

func testStateSyncPeerResponse(t *testing.T, mode SyncMode, failure error, drop, backoff bool) {
	t.Helper()
	tester := newTester(t)
	defer tester.terminate()
	d := tester.downloader
	observations := 0
	d.SetFailureObserver(func(string) { observations++ })
	tester.newPeer("server", eth.ETH69, testChainBase.shorten(1).blocks[1:])
	d.synchroniseMock = func(string, common.Hash) error { return failure }
	for range softFailureJailThreshold + 1 {
		err := d.LegacySync("server", tester.chain.Genesis().Hash(), big.NewInt(1000), nil, mode)
		if !errors.Is(err, failure) {
			t.Fatalf("sync error = %v, want %v", err, failure)
		}
		peer := d.peers.Peer("server")
		if drop {
			if peer != nil {
				t.Fatal("invalid data did not drop the peer")
			}
			return
		}
		if peer == nil || peer.backedOff() != backoff {
			t.Fatalf("peer eligibility changed unexpectedly: peer=%v backoff=%v", peer, backoff)
		}
	}
	if !backoff && (observations != 0 || len(d.peers.jailed) != 0) {
		t.Fatalf("operational failures penalized the peer: observations=%d jailed=%v", observations, d.peers.jailed)
	}
	d.synchroniseMock = func(string, common.Hash) error { return nil }
	if err := d.LegacySync("server", tester.chain.Genesis().Hash(), big.NewInt(1000), nil, mode); err != nil {
		t.Fatalf("retry after recovery failed: %v", err)
	}
}
