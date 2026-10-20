package fetcher

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
)

func TestBlockFetchTimeoutKeepsPeerAndAllowsRetry(t *testing.T) {
	hashes, blocks := makeChain(1, 42, genesis)
	block := blocks[hashes[0]]
	for _, headers := range []bool{true, false} {
		name := "body"
		if headers {
			name = "header"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tester := newTester(false)
			defer tester.fetcher.Stop()
			imported := make(chan struct{}, 1)
			tester.fetcher.importedHook = func(*types.Header, *types.Block) { imported <- struct{}{} }
			request, started := requestDelayedBlock(t, tester, blocks, block, headers)
			prepareBlockTimeoutState(t, tester.fetcher, block.Hash(), headers)
			awaitBlockTimeout(t, request, started)
			assertBlockTimeoutReleased(t, tester.fetcher, block.Hash(), headers)
			assertBlockPeerRetained(t, tester)
			err := tester.fetcher.Notify("slow", block.Hash(), block.NumberU64(), time.Now(),
				tester.makeHeaderFetcher("slow", blocks, 0), tester.makeBodyFetcher("slow", blocks, 0), nil)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-imported:
			case <-time.After(3 * time.Second):
				t.Fatal("timed-out block could not be fetched again")
			}
			assertBlockPeerRetained(t, tester)
		})
	}
}

func TestBlockBodyTimeoutPreservesOtherAnnouncements(t *testing.T) {
	hashes, blocks := makeChain(1, 43, genesis)
	block := blocks[hashes[0]]
	tester := newTester(false)
	defer tester.fetcher.Stop()
	f := tester.fetcher
	other := common.Hash{0xff}
	announced, resume := make(chan struct{}), make(chan struct{})
	defer close(resume)
	f.announceChangeHook = func(hash common.Hash, added bool) {
		if hash == other && added {
			close(announced)
			<-resume
		}
	}
	request, started := requestDelayedBlock(t, tester, blocks, block, false)
	if err := f.Notify("slow", other, block.NumberU64(), time.Now(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-announced:
	case <-time.After(time.Second):
		t.Fatal("second announcement was not registered")
	}
	if f.completing[block.Hash()] == nil || len(f.announced[other]) != 1 || f.announces["slow"] != 1 {
		t.Fatal("expected a body request and one counted announcement from the same peer")
	}
	// The hook pauses scheduling B while the real worker deadline for A expires.
	select {
	case release := <-f.fetchRelease:
		if release.headers || len(release.announces) != 1 || release.announces[0] != f.completing[block.Hash()] {
			t.Fatal("received a release for a different request")
		}
		f.releaseFetchOwnership(release)
	case <-time.After(12 * time.Second):
		t.Fatal("body request did not time out")
	}
	awaitBlockTimeout(t, request, started)
	if f.completing[block.Hash()] != nil {
		t.Error("timeout did not release the body request")
	}
	if len(f.announced[other]) != 1 || f.announces["slow"] != 1 {
		t.Errorf("body timeout changed unrelated announcement accounting: count=%d, want 1", f.announces["slow"])
	}
	assertBlockPeerRetained(t, tester)
}

type delayedBlockRequest struct {
	request *eth.Request
	started time.Time
}

func requestDelayedBlock(t *testing.T, tester *fetcherTester, blocks map[common.Hash]*types.Block, block *types.Block, headers bool) (*eth.Request, time.Time) {
	t.Helper()
	requests := make(chan delayedBlockRequest, 1)
	header := tester.makeHeaderFetcher("slow", blocks, 0)
	body := tester.makeBodyFetcher("slow", blocks, 0)
	if headers {
		header = func(common.Hash, chan *eth.Response) (*eth.Request, error) {
			req := &eth.Request{Cancel: make(chan struct{})}
			requests <- delayedBlockRequest{request: req, started: time.Now()}
			return req, nil
		}
	} else {
		body = func([]common.Hash, chan *eth.Response) (*eth.Request, error) {
			req := &eth.Request{Cancel: make(chan struct{})}
			requests <- delayedBlockRequest{request: req, started: time.Now()}
			return req, nil
		}
	}
	if err := tester.fetcher.Notify("slow", block.Hash(), block.NumberU64(), time.Now(), header, body, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-requests:
		return request.request, request.started
	case <-time.After(3 * time.Second):
		t.Fatal("block request was not started")
		return nil, time.Time{}
	}
}

func prepareBlockTimeoutState(t *testing.T, f *BlockFetcher, hash common.Hash, headers bool) {
	t.Helper()
	resume := pauseBlockTimeoutLoop(t, f)
	defer resume()
	f.mu.Lock()
	defer f.mu.Unlock()
	announce := pendingBlockRelease(f, headers)[hash]
	if announce == nil {
		t.Fatal("request was not registered in the expected fetch stage")
	}
	// Isolate the worker's real deadline from the loop's legacy announcement-age sweep.
	announce.time = time.Now().Add(time.Hour)
}

func awaitBlockTimeout(t *testing.T, request *eth.Request, started time.Time) {
	t.Helper()
	select {
	case <-request.Cancel:
	case <-time.After(12 * time.Second):
		t.Fatal("timed-out request was not cancelled")
	}
	if elapsed := time.Since(started); elapsed < 2*fetchTimeout || elapsed > 12*time.Second {
		t.Fatalf("request timed out after %v, want between %v and 12s", elapsed, 2*fetchTimeout)
	}
}

func assertBlockTimeoutReleased(t *testing.T, f *BlockFetcher, hash common.Hash, headers bool) {
	t.Helper()
	resume := pauseBlockTimeoutLoop(t, f)
	defer resume()
	f.mu.RLock()
	defer f.mu.RUnlock()
	if pendingBlockRelease(f, headers)[hash] != nil {
		t.Error("timeout did not release the matching fetch request")
	}
	if count := f.announces["slow"]; count != 0 {
		t.Errorf("timeout left %d outstanding announcements, want 0", count)
	}
}

func pauseBlockTimeoutLoop(t *testing.T, f *BlockFetcher) func() {
	t.Helper()
	filter := make(chan *headerFilterTask)
	select {
	case f.headerFilter <- filter:
	case <-time.After(time.Second):
		t.Fatal("fetcher did not reach the state inspection barrier")
	}
	return func() {
		select {
		case filter <- &headerFilterTask{}:
		case <-time.After(time.Second):
			t.Fatal("fetcher did not resume from the state inspection barrier")
		}
		select {
		case <-filter:
		case <-time.After(time.Second):
			t.Fatal("fetcher did not complete the state inspection barrier")
		}
	}
}

func assertBlockPeerRetained(t *testing.T, tester *fetcherTester) {
	t.Helper()
	tester.lock.RLock()
	defer tester.lock.RUnlock()
	if tester.drops["slow"] {
		t.Fatal("slow peer was disconnected")
	}
}

func TestEmptyBodyResponseAllowsReannouncement(t *testing.T) {
	hashes, blocks := makeChain(1, 63, genesis)
	block := blocks[hashes[0]]
	if len(block.Transactions()) == 0 && len(block.Uncles()) == 0 {
		t.Fatal("fixture block needs a body to reach the body fetch stage")
	}
	tester := newTester(false)
	defer tester.fetcher.Stop()
	f := tester.fetcher

	imported := make(chan struct{}, 1)
	f.importedHook = func(*types.Header, *types.Block) { imported <- struct{}{} }
	completing := make(chan []common.Hash, 4)
	f.completingHook = func(hs []common.Hash) { completing <- hs }

	err := f.Notify("silent", block.Hash(), block.NumberU64(), time.Now(),
		tester.makeHeaderFetcher("silent", blocks, -gatherSlack), emptyBodyFetcher("silent"), nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-completing:
	case <-time.After(5 * time.Second):
		t.Fatal("body request was never issued")
	}
	// An announcement arriving while the request is still outstanding is
	// deduplicated against it, so recovery depends on the record being released
	// once the empty response ends the request.
	awaitCompletionReleased(t, f, block.Hash())
	err = f.Notify("healthy", block.Hash(), block.NumberU64(), time.Now(),
		tester.makeHeaderFetcher("healthy", blocks, -gatherSlack),
		tester.makeBodyFetcher("healthy", blocks, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-imported:
	case <-time.After(5 * time.Second):
		t.Fatal("block was not recovered after an empty body response")
	}

	tester.lock.RLock()
	defer tester.lock.RUnlock()
	if tester.drops["silent"] {
		t.Fatal("empty body response dropped the peer")
	}
}

func awaitCompletionReleased(t *testing.T, f *BlockFetcher, hash common.Hash) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.RLock()
		held := f.completing[hash] != nil
		f.mu.RUnlock()
		if !held {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("completion record was retained after the response ended the request")
}

func emptyBodyFetcher(peer string) bodyRequesterFn {
	return func(_ []common.Hash, sink chan *eth.Response) (*eth.Request, error) {
		req := &eth.Request{Peer: peer}
		bodies := make([]*eth.BlockBody, 0)
		res := &eth.Response{Req: req, Res: (*eth.BlockBodiesResponse)(&bodies), Done: make(chan error, 1)}

		go func() { sink <- res }()

		return req, nil
	}
}
