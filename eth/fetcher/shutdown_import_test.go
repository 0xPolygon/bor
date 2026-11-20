package fetcher

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
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

func TestBlockFetcherStopDrainsImportBroadcasts(t *testing.T) {
	for _, preImport := range []bool{true, false} {
		name := "post-import"
		if preImport {
			name = "pre-import"
		}
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			finished := make(chan struct{})
			var releaseOnce sync.Once
			var resourceClosed atomic.Bool

			f := newShutdownTestFetcher(
				func(types.Blocks, []*stateless.Witness) (int, error) {
					if preImport {
						select {
						case <-entered:
						case <-time.After(time.Second):
							t.Error("pre-import broadcast did not start before insertion completed")
						}
						return 0, consensus.NewEngineStoppedError(context.Canceled)
					}
					return 0, nil
				},
				func(*types.Header) error { return nil },
				func(string) {},
			)
			defer f.Stop()
			defer releaseOnce.Do(func() { close(release) })
			f.broadcastBlock = func(_ *types.Block, _ *stateless.Witness, propagate bool) {
				if propagate != preImport {
					return
				}
				close(entered)
				<-release
				if resourceClosed.Load() {
					t.Error("broadcast accessed chain resources after fetcher shutdown released them")
				}
				close(finished)
			}

			block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
			f.importBlocks(&blockOrHeaderInject{origin: "peer", block: block})
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("import did not start its broadcast callback")
			}

			stopped := make(chan struct{})
			go func() {
				f.Stop()
				close(stopped)
			}()
			returnedEarly := false
			select {
			case <-stopped:
				returnedEarly = true
				resourceClosed.Store(true)
			case <-time.After(100 * time.Millisecond):
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("broadcast callback did not finish")
			}
			if !returnedEarly {
				select {
				case <-stopped:
					resourceClosed.Store(true)
				case <-time.After(time.Second):
					t.Fatal("fetcher stop did not finish after broadcast completion")
				}
			}
			if returnedEarly {
				t.Fatal("fetcher stop returned while an import-owned broadcast was running")
			}
		})
	}
}

func TestBlockImportBroadcastsBeforeAndAfterInsertion(t *testing.T) {
	broadcasts := make(chan bool, 2)
	f := newShutdownTestFetcher(
		func(types.Blocks, []*stateless.Witness) (int, error) { return 0, nil },
		func(*types.Header) error { return nil },
		func(string) {},
	)
	f.broadcastBlock = func(_ *types.Block, _ *stateless.Witness, propagate bool) {
		broadcasts <- propagate
	}
	defer f.Stop()
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
	if f.runBlockImport(&blockOrHeaderInject{origin: "peer", block: block}) {
		t.Fatal("valid import requested a witness retry")
	}
	got := make(map[bool]bool, 2)
	for range 2 {
		select {
		case propagate := <-broadcasts:
			got[propagate] = true
		case <-time.After(time.Second):
			t.Fatal("validated import did not run both broadcast phases")
		}
	}
	if !got[true] || !got[false] {
		t.Fatalf("broadcast phases = %v, want pre-import and post-import", got)
	}
}

func TestInvalidHeaderReportsAndDropsSupplier(t *testing.T) {
	dropped := make(chan string, 1)
	observed := make(chan error, 1)
	f := newShutdownTestFetcher(
		func(types.Blocks, []*stateless.Witness) (int, error) { return 0, nil },
		func(*types.Header) error { return errors.New("invalid signature") },
		func(id string) { dropped <- id },
	)
	f.SetInvalidHeaderObserver(func(_ string, err error) { observed <- err })
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(1)})
	f.runBlockImport(&blockOrHeaderInject{origin: "peer", block: block})
	select {
	case err := <-observed:
		if err == nil || err.Error() != "invalid signature" {
			t.Fatalf("observed error = %v, want invalid signature", err)
		}
	case <-time.After(time.Second):
		t.Fatal("invalid header was not reported")
	}
	select {
	case id := <-dropped:
		if id != "peer" {
			t.Fatalf("dropped wrong peer %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("invalid header did not drop its supplier")
	}
}

func TestBlockFetcherStopDrainsHeaderImportAndRejectsNewWork(t *testing.T) {
	parent := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(0)})
	header := &types.Header{Number: big.NewInt(1), ParentHash: parent.Hash()}
	started := make(chan struct{})
	release := make(chan struct{})
	f := NewBlockFetcher(
		false,
		func(common.Hash) *types.Header { return parent.Header() },
		func(common.Hash) *types.Block { return parent },
		func(*types.Header) error { return nil },
		func(*types.Block, *stateless.Witness, bool) {},
		func() uint64 { return 1 },
		nil,
		func([]*types.Header) (int, error) {
			close(started)
			<-release
			return 0, nil
		},
		func(types.Blocks, []*stateless.Witness) (int, error) { return 0, nil },
		func(string) {},
		false,
		false,
		0,
		nil,
		nil,
	)
	f.importHeaders("peer", header)
	<-started

	stopped := make(chan struct{})
	var stops sync.WaitGroup
	stops.Add(2)
	go func() {
		defer stops.Done()
		f.Stop()
		close(stopped)
	}()
	go func() {
		defer stops.Done()
		f.Stop()
	}()
	select {
	case <-stopped:
		t.Fatal("fetcher stop returned with a header import still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("fetcher stop did not drain the header import")
	}
	stops.Wait()
	if f.beginImport() {
		f.importWg.Done()
		t.Fatal("import admission remained open after shutdown")
	}
}

func TestHeaderImportDropsForInvalidHeader(t *testing.T) {
	dropped := make(chan string, 1)
	parent := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(0)})
	header := &types.Header{Number: big.NewInt(1), ParentHash: parent.Hash()}
	f := NewBlockFetcher(
		false,
		func(common.Hash) *types.Header { return parent.Header() },
		func(common.Hash) *types.Block { return parent },
		func(*types.Header) error { return errors.New("invalid signature") },
		func(*types.Block, *stateless.Witness, bool) {},
		func() uint64 { return 1 },
		nil,
		func([]*types.Header) (int, error) { return 0, nil },
		func(types.Blocks, []*stateless.Witness) (int, error) { return 0, nil },
		func(id string) { dropped <- id },
		false,
		false,
		0,
		nil,
		nil,
	)
	f.Start()
	defer f.Stop()
	f.importHeaders("peer", header)
	select {
	case id := <-dropped:
		if id != "peer" {
			t.Fatalf("dropped wrong peer %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("invalid header did not drop its supplier")
	}
}
