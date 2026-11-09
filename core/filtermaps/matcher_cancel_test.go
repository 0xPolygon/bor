// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package filtermaps

import (
	"context"
	"errors"
	"os"
	"runtime/pprof"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.uber.org/goleak"
)

// cancelReturnTimeout is how long a cancelled search may take to unwind. The
// unwind is bounded by one database read, so the margin is for a loaded CI
// machine rather than for the search.
const cancelReturnTimeout = 10 * time.Second

// newCancelTestSetup builds an indexed chain, a matcher backend registered
// against it, and an address list matching one of the chain's logs. Searching
// for a real log drives the matcher through the log lookup path in
// getLogsFromMatches, where ethereum/go-ethereum#35630 found its blocked readers.
func newCancelTestSetup(t *testing.T) (*testSetup, *FilterMapsMatcherBackend, []common.Address) {
	t.Helper()

	ts := newTestSetup(t)
	ts.chain.addBlocks(1000, 10, 10, 4, true)
	ts.setHistory(0, false)
	ts.fm.WaitIdle()

	mb := ts.fm.NewMatcherBackend()
	for _, bhash := range ts.chain.canonical {
		for _, receipt := range ts.chain.receipts[bhash] {
			if len(receipt.Logs) > 0 {
				return ts, mb, []common.Address{receipt.Logs[0].Address}
			}
		}
	}
	t.Fatal("test chain generated no logs to search for")
	return nil, nil, nil
}

// awaitCancel waits for a cancelled search to return and reports what it
// returned. On timeout it dumps every goroutine stack so the blocked frames are
// visible in the test output, then fails.
func awaitCancel(t *testing.T, errCh <-chan error) error {
	t.Helper()

	select {
	case err := <-errCh:
		return err
	case <-time.After(cancelReturnTimeout):
		pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
		t.Fatalf("cancelled search did not return within %v; goroutine dump above", cancelReturnTimeout)
		return nil
	}
}

// TestMatcherCancelWhileBlockedOnIndexLock asserts that a search already parked
// in indexLock is released by cancelling it. A context check taken before the
// lock acquisition cannot pass this test, because the goroutine is parked
// before the cancellation arrives.
func TestMatcherCancelWhileBlockedOnIndexLock(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	ts, mb, addresses := newCancelTestSetup(t)
	defer ts.close()
	defer mb.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// stand in for a map renderer write batch that outlives the request
	ts.fm.indexLock.Lock()
	defer ts.fm.indexLock.Unlock()

	errCh := make(chan error, 1)
	go func() {
		_, err := GetPotentialMatches(ctx, mb, 0, 1000, addresses, nil)
		errCh <- err
	}()

	// let the search reach the lock before cancelling it
	time.Sleep(100 * time.Millisecond)
	cancel()

	if err := awaitCancel(t, errCh); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search returned %v, want context.Canceled", err)
	}
}

// gatedBackend holds every matcher worker at its first backend call until the
// test releases them. The test can then cancel with the workers known to be
// running, rather than racing a search that may finish first.
type gatedBackend struct {
	MatcherBackend

	once     sync.Once
	inFlight chan struct{} // closed once a worker has called in
	release  chan struct{} // closed by the test to let the workers continue
}

func newGatedBackend(mb MatcherBackend) *gatedBackend {
	return &gatedBackend{
		MatcherBackend: mb,
		inFlight:       make(chan struct{}),
		release:        make(chan struct{}),
	}
}

// GetFilterMapRows is called from processEpoch, so reaching it means the
// coordinator has handed an epoch to a worker goroutine.
func (b *gatedBackend) GetFilterMapRows(ctx context.Context, mapIndices []uint32, rowIndex uint32, baseLayerOnly bool) ([]FilterRow, error) {
	b.once.Do(func() { close(b.inFlight) })
	<-b.release
	return b.MatcherBackend.GetFilterMapRows(ctx, mapIndices, rowIndex, baseLayerOnly)
}

// TestMatcherCancelReleasesWorkers asserts that cancelling a search terminates
// every goroutine it spawned and reports the cancellation, rather than the
// coordinator waiting out the whole block range. goleak catches any worker that
// outlives the call.
func TestMatcherCancelReleasesWorkers(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	ts, mb, addresses := newCancelTestSetup(t)
	defer ts.close()
	defer mb.Close()

	gated := newGatedBackend(mb)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		_, err := GetPotentialMatches(ctx, gated, 0, 1000, addresses, nil)
		errCh <- err
	}()

	<-gated.inFlight
	cancel()
	close(gated.release)

	if err := awaitCancel(t, errCh); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search returned %v, want context.Canceled", err)
	}
}

// TestMatcherCancelBeforeSearch asserts that a search on an already cancelled
// context reports the cancellation. Returning an empty log set with a nil error
// would look to an RPC caller like a chain with no matching logs.
func TestMatcherCancelBeforeSearch(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	ts, mb, addresses := newCancelTestSetup(t)
	defer ts.close()
	defer mb.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	logs, err := GetPotentialMatches(ctx, mb, 0, 1000, addresses, nil)
	if err == nil {
		t.Fatalf("search on a cancelled context returned %d logs and no error", len(logs))
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("search on a cancelled context returned %v, want context.Canceled", err)
	}
}
