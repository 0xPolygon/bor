package downloader

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

func TestStatelessImportStateSyncFailure(t *testing.T) {
	failure := fmt.Errorf("finalize: %w: %w", core.ErrStateSyncProcessing, context.DeadlineExceeded)
	tester := newTester(t)
	defer tester.terminate()
	d := tester.downloader
	tester.newPeer("server", eth.ETH69, testChainBase.shorten(1).blocks[1:])
	chain := &fakeStatelessChain{results: []error{failure, failure, nil}}
	d.blockchain = chain
	d.cancelCh = make(chan struct{})
	results := []*fetchResult{{Header: &types.Header{Number: big.NewInt(1)}}}
	d.synchroniseMock = func(string, common.Hash) error { return d.importBlockResultsStateless(results) }
	observations := 0
	d.SetFailureObserver(func(string) { observations++ })
	for range 2 {
		err := d.LegacySync("server", common.Hash{}, big.NewInt(1000), nil, StatelessSync)
		if !errors.Is(err, core.ErrStateSyncProcessing) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("stateless import lost operational cause: %v", err)
		}
		peer := d.peers.Peer("server")
		if peer == nil || peer.backedOff() || observations != 0 || len(d.peers.jailed) != 0 {
			t.Fatal("stateless operational error penalized the serving peer")
		}
	}
	if err := d.LegacySync("server", common.Hash{}, big.NewInt(1000), nil, StatelessSync); err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
}

func TestStatelessImportInvalidData(t *testing.T) {
	for _, failure := range []error{core.ErrStateSyncMismatch, core.ErrStatelessStateRootMismatch} {
		t.Run(failure.Error(), func(t *testing.T) {
			d := &Downloader{blockchain: &fakeStatelessChain{results: []error{failure}}}
			err := d.importBlockResultsStateless([]*fetchResult{{Header: &types.Header{Number: big.NewInt(1)}}})
			if !errors.Is(err, errInvalidBody) || errors.Is(err, core.ErrStateSyncProcessing) {
				t.Fatalf("invalid data classification changed: %v", err)
			}
		})
	}
}
