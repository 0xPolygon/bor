package fetcher

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
)

func newShutdownTestFetcher(insert chainInsertFn, verify headerVerifierFn, drop peerDropFn) *BlockFetcher {
	parent := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(0)})
	return NewBlockFetcher(
		false,
		func(common.Hash) *types.Header { return nil },
		func(common.Hash) *types.Block { return parent },
		verify,
		func(*types.Block, *stateless.Witness, bool) {},
		func() uint64 { return 0 },
		nil,
		nil,
		insert,
		drop,
		false,
		false,
		0,
		nil,
		nil,
	)
}

func TestBlockImportDoesNotDropForEngineShutdown(t *testing.T) {
	dropped := make(chan string, 1)
	abort := consensus.NewEngineStoppedError(context.Canceled)
	f := newShutdownTestFetcher(
		func(types.Blocks, []*stateless.Witness) (int, error) {
			return 0, errors.Join(core.ErrStateSyncProcessing, abort)
		},
		func(*types.Header) error { return abort },
		func(id string) { dropped <- id },
	)
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
	f.runBlockImport(&blockOrHeaderInject{origin: "peer", block: block})
	select {
	case id := <-dropped:
		t.Fatalf("shutdown abort dropped supplier %q", id)
	default:
	}
}

func TestBlockImportDropsForInvalidHeaderDuringShutdown(t *testing.T) {
	dropped := make(chan string, 1)
	f := newShutdownTestFetcher(
		func(types.Blocks, []*stateless.Witness) (int, error) { return 0, nil },
		func(*types.Header) error { return errors.New("invalid signature") },
		func(id string) { dropped <- id },
	)
	f.Stop()
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
	f.runBlockImport(&blockOrHeaderInject{origin: "peer", block: block})
	select {
	case id := <-dropped:
		if id != "peer" {
			t.Fatalf("dropped wrong peer %q", id)
		}
	default:
		t.Fatal("invalid header was hidden by fetcher shutdown")
	}
}

func TestBlockFetcherStopDrainsImport(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	f := newShutdownTestFetcher(
		func(types.Blocks, []*stateless.Witness) (int, error) {
			close(started)
			<-release
			return 0, nil
		},
		func(*types.Header) error { return nil },
		func(string) {},
	)
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
	f.importBlocks(&blockOrHeaderInject{origin: "peer", block: block})
	<-started

	stopped := make(chan struct{})
	go func() {
		f.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("fetcher stop returned with an import still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("fetcher stop did not return after import completion")
	}
}
