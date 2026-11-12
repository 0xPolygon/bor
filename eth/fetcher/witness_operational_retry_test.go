package fetcher

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/trie"
)

func TestWitnessOperationalFetchRetry(t *testing.T) {
	for _, name := range []string{"timeout", "closed response", "request failure"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				tw := newTestWitnessManager()
				defer tw.Close()
				m := tw.manager
				block := createTestBlock(101)
				hash := block.Hash()
				announce := createTestBlockAnnounce("server", block, nil)
				m.pending[hash] = &witnessRequestState{op: &blockOrHeaderInject{block: block}, announce: announce}
				for range 6 {
					switch name {
					case "timeout":
						start := time.Now()
						m.awaitWitnessResponse("server", hash, make(chan *eth.Response), time.Now())
						if time.Since(start) != 10*time.Second {
							t.Fatal("witness request did not retain its response timeout")
						}
					case "closed response":
						m.processWitnessResponse("server", hash, nil, time.Now())
					case "request failure":
						announce.fetchWitness = func(common.Hash, chan *eth.Response) (*eth.Request, error) {
							return &eth.Request{Peer: "server"}, context.DeadlineExceeded
						}
						m.initiateWitnessFetch(hash, announce, nil)
					}
					if len(tw.DroppedPeers()) != 0 || !m.isPending(hash) {
						t.Fatal("operational failure dropped a peer or discarded pending work")
					}
					if delay := time.Until(announce.time); delay <= 0 || delay > time.Second {
						t.Fatalf("retry delay = %v, want (0, 1s]", delay)
					}
				}
				witness := createTestWitnessForBlock(block)
				m.processWitnessResponse("server", hash, &eth.Response{Res: []*stateless.Witness{witness}, Done: make(chan error, 1)}, time.Now())
				if got := <-tw.enqueueCh; got.op.witness != witness || m.isPending(hash) {
					t.Fatal("witness was not queued after recovery")
				}
			})
		})
	}
}

func TestWitnessFetchDoesNotOverlap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		tw := newTestWitnessManager()
		defer tw.Close()
		m := tw.manager
		hash := addPendingEntry(m, "0xa1", "server", time.Now().Add(-time.Second))
		ready, _ := m.collectReadyHashesLocked(time.Now())
		m.buildPeerRequestsLocked(ready, time.Now())
		if next := m.earliestPendingAnnounceLocked(); !next.IsZero() {
			t.Fatal("in-flight request kept the polling timer armed")
		}
		time.Sleep(fetchTimeout + time.Second)
		if ready, _ := m.collectReadyHashesLocked(time.Now()); len(ready) != 0 {
			t.Fatal("second request scheduled while the first is still awaiting a response")
		}
		m.processWitnessResponse("server", hash, nil, time.Now())
		if next := m.earliestPendingAnnounceLocked(); !next.Equal(m.pending[hash].announce.time) {
			t.Fatal("failed request did not rearm the polling timer")
		}
		time.Sleep(time.Second + time.Nanosecond)
		if ready, _ := m.collectReadyHashesLocked(time.Now()); len(ready) != 1 {
			t.Fatal("request did not become eligible after its failure and retry delay")
		}
		m.buildPeerRequestsLocked([]common.Hash{hash}, time.Now())
		m.processWitnessResponse("server", hash, &eth.Response{Res: []*stateless.Witness{}, Done: make(chan error, 1)}, time.Now())
		time.Sleep(time.Second)
		if ready, _ := m.collectReadyHashesLocked(time.Now()); len(ready) != 1 {
			t.Fatal("body-not-ready response left the request in flight")
		}
	})
}

func TestLiveImportStateSyncRetry(t *testing.T) {
	for _, provenance := range []WitnessProvenance{WitnessFromProducer, WitnessUnproven, WitnessNotProducer} {
		t.Run(fmt.Sprint(provenance), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, op := operationalImportFetcher(t, provenance)
				calls := 0
				start := time.Now()
				f.insertChain = func(blocks types.Blocks, witnesses []*stateless.Witness) (int, error) {
					calls++
					if blocks[0] != op.block || witnesses[0] != op.witness {
						t.Fatal("retry replaced the block or witness")
					}
					if calls == 1 {
						return 0, fmt.Errorf("finalize: %w: %w", core.ErrStateSyncProcessing, context.DeadlineExceeded)
					}
					return 0, nil
				}
				imported := false
				f.importedHook = func(*types.Header, *types.Block) { imported = true }
				if f.runBlockImport(op) || !imported || calls != 2 || time.Since(start) != 10*time.Second {
					t.Fatalf("live retry failed: imported=%v calls=%d elapsed=%v", imported, calls, time.Since(start))
				}
				if op.witnessImportFailures != 0 {
					t.Fatal("operational retry consumed the invalid-witness retry budget")
				}
			})
		})
	}
}

func TestLiveImportStateSyncRetryStops(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, op := operationalImportFetcher(t, WitnessNotProducer)
				calls := 0
				f.insertChain = func(types.Blocks, []*stateless.Witness) (int, error) {
					calls++
					return 0, core.ErrStateSyncProcessing
				}
				start := time.Now()
				if stop {
					go func() { time.Sleep(time.Second); close(f.quit) }()
				}
				if f.runBlockImport(op) {
					t.Fatal("operational error requested another witness")
				}
				if stop && (calls != 1 || time.Since(start) != time.Second) {
					t.Fatal("shutdown did not cancel the retry promptly")
				}
				if !stop && (calls != 3 || time.Since(start) != 20*time.Second) {
					t.Fatalf("retry budget was not bounded: calls=%d elapsed=%v", calls, time.Since(start))
				}
			})
		})
	}
}

func TestLiveImportInvalidWitnessResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		strike bool
		retry  bool
	}{
		{name: "invalid witness", err: stateless.ErrInvalidWitness, strike: true, retry: true},
		{name: "incomplete witness during state sync", err: fmt.Errorf("%w: %w", core.ErrStateSyncProcessing, &trie.MissingNodeError{}), strike: true, retry: true},
		{name: "state root mismatch", err: core.ErrStatelessStateRootMismatch, retry: true},
		{name: "state sync mismatch", err: core.ErrStateSyncMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f, op := operationalImportFetcher(t, WitnessNotProducer)
				calls, strikes, exclusions := 0, 0, 0
				f.insertChain = func(types.Blocks, []*stateless.Witness) (int, error) { calls++; return 0, tc.err }
				f.wm.parentStrikeWitnessServer = func(string) { strikes++ }
				f.wm.parentExcludeWitnessSource = func(string, common.Hash) { exclusions++ }
				start := time.Now()
				if retry := f.runBlockImport(op); retry != tc.retry || calls != 1 || time.Since(start) != 0 {
					t.Fatalf("invalid data entered operational retry: retry=%v calls=%d", retry, calls)
				}
				if (strikes == 1) != tc.strike || (exclusions == 1) != tc.retry {
					t.Fatalf("invalid witness consequences changed: strikes=%d exclusions=%d", strikes, exclusions)
				}
			})
		})
	}
}

func TestWitnessMalformedResponseStillDropsPeer(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()
	hash := addPendingEntry(tw.manager, "0xa1", "server", time.Now())
	tw.manager.processWitnessResponse("server", hash, &eth.Response{Res: "invalid", Done: make(chan error, 1)}, time.Now())
	if peers := tw.DroppedPeers(); len(peers) != 1 || peers[0] != "server" {
		t.Fatalf("malformed response did not drop the serving peer: %v", peers)
	}
}

func TestLiveImportShutdownCompletes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f, op := operationalImportFetcher(t, WitnessNotProducer)
		f.done = make(chan common.Hash)
		f.insertChain = func(types.Blocks, []*stateless.Witness) (int, error) { return 0, core.ErrStateSyncProcessing }
		f.importBlocks(op)
		synctest.Wait()
		close(f.quit)
		synctest.Wait()
	})
}

func operationalImportFetcher(t *testing.T, provenance WitnessProvenance) (*BlockFetcher, *blockOrHeaderInject) {
	t.Helper()
	block := createTestBlock(101)
	op := &blockOrHeaderInject{origin: "block-peer", block: block, witness: createTestWitnessForBlock(block), witnessPeer: "witness-peer"}
	op.setWitnessProvenance(provenance)
	op.fetchWitness = func(common.Hash, chan *eth.Response) (*eth.Request, error) {
		t.Fatal("operational failure re-fetched a witness")
		return nil, errors.New("unexpected fetch")
	}
	f := &BlockFetcher{
		quit:           make(chan struct{}),
		getBlock:       func(common.Hash) *types.Block { return createTestBlock(100) },
		verifyHeader:   func(*types.Header) error { return nil },
		broadcastBlock: func(*types.Block, *stateless.Witness, bool) {},
		dropPeer:       func(string) { t.Error("operational error dropped a peer") },
		wm: &witnessManager{
			parentStrikeWitnessServer:  func(string) { t.Error("operational error struck a peer") },
			parentExcludeWitnessSource: func(string, common.Hash) { t.Error("operational error excluded a peer") },
		},
	}
	return f, op
}
