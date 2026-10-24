package fetcher

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/downloader/whitelist"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/trie"
)

// TestAcceptableWitnessSizeCeilingDegenerateSignedSizes pins the two inputs that
// must not collapse the size oracle into a zero ceiling (which would reject
// every honest server): a signed size of 0 falls back to the absolute cap, and a
// hostile signed size near MaxUint64 saturates instead of wrapping and then
// clamps to the absolute cap.
func TestAcceptableWitnessSizeCeilingDegenerateSignedSizes(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	abs := tw.manager.MaxWitnessSize()
	if abs == 0 {
		t.Fatal("absolute witness size cap must be positive")
	}
	if got := tw.manager.acceptableWitnessSizeCeiling(0); got != abs {
		t.Fatalf("signedSize=0 must fall back to the absolute cap %d, got %d", abs, got)
	}
	// The exported form used by the handler's broadcast path must agree with the
	// fetch path's ceiling, so both delivery paths judge a witness identically.
	if got, want := tw.manager.AcceptableWitnessSizeCeiling(1000), tw.manager.acceptableWitnessSizeCeiling(1000); got != want {
		t.Fatalf("AcceptableWitnessSizeCeiling = %d, want %d", got, want)
	}
	for _, hostile := range []uint64{math.MaxUint64, math.MaxUint64 / 2, math.MaxUint64/wit2SizeBandMultiplier + 1} {
		if got := tw.manager.acceptableWitnessSizeCeiling(hostile); got != abs {
			t.Fatalf("signedSize=%d must saturate and clamp to the absolute cap %d, got %d", hostile, abs, got)
		}
	}
}

// TestSaturatingMulUint64 pins the overflow guard used by the size band.
func TestSaturatingMulUint64(t *testing.T) {
	cases := []struct{ a, b, want uint64 }{
		{0, 3, 0},
		{3, 0, 0},
		{7, 3, 21},
		{math.MaxUint64 / 3, 3, math.MaxUint64 / 3 * 3},
		{math.MaxUint64/3 + 1, 3, math.MaxUint64},
		{math.MaxUint64 / 2, 2, math.MaxUint64 - 1}, // exactly at the bound: must multiply, not saturate
		{math.MaxUint64/2 + 1, 2, math.MaxUint64},
		{math.MaxUint64, 2, math.MaxUint64},
	}
	for _, c := range cases {
		if got := saturatingMulUint64(c.a, c.b); got != c.want {
			t.Fatalf("saturatingMulUint64(%d, %d) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestChargeDivergedWitnessImportFailure covers the decision helper behind the
// WIT2 import-failure consequence: only a fetched witness accepted on the size
// oracle alone is charged (strike + source exclusion), the retry budget is
// honoured, and a BP-identical or non-fetched witness is left alone.
func TestChargeDivergedWitnessImportFailure(t *testing.T) {
	tester := newTester(false)
	defer tester.fetcher.Stop()

	var (
		mu       sync.Mutex
		strikes  []string
		excluded []string
	)
	tester.fetcher.SetWitnessServerStriker(func(id string) {
		mu.Lock()
		strikes = append(strikes, id)
		mu.Unlock()
	})
	tester.fetcher.SetWitnessSourceExcluder(func(peer string, _ common.Hash) {
		mu.Lock()
		excluded = append(excluded, peer)
		mu.Unlock()
	})

	block := createTestBlock(501)
	witness := createTestWitnessForBlock(block)
	noopFetch := func(common.Hash, chan *eth.Response) (*eth.Request, error) { return nil, errors.New("noop") }
	importErr := fmt.Errorf("%w (remote: 1 local: 2)", core.ErrGasUsedMismatch) // witness-attributable

	// BP-identical witness (not diverged): the BP's fault, nothing charged.
	identical := &blockOrHeaderInject{origin: "o", block: block, witness: witness, witnessPeer: "srv", fetchWitness: noopFetch}
	if tester.fetcher.chargeDivergedWitnessImportFailure(identical, importErr) {
		t.Fatal("a BP-identical witness failing import must not trigger a re-fetch")
	}
	// Diverged but not fetched (no serving peer): nobody to charge.
	unfetched := &blockOrHeaderInject{origin: "o", block: block, witness: witness, witnessDiverged: true, fetchWitness: noopFetch}
	if tester.fetcher.chargeDivergedWitnessImportFailure(unfetched, importErr) {
		t.Fatal("a diverged witness with no serving peer must not trigger a re-fetch")
	}
	if len(strikes) != 0 || len(excluded) != 0 {
		t.Fatalf("nothing should be charged yet; strikes=%v excluded=%v", strikes, excluded)
	}

	// Diverged, fetched: charged and retried, up to the budget.
	op := &blockOrHeaderInject{origin: "o", block: block, witness: witness, witnessPeer: "srv-1", witnessDiverged: true, fetchWitness: noopFetch}
	if !tester.fetcher.chargeDivergedWitnessImportFailure(op, importErr) {
		t.Fatal("first import failure with a diverged fetched witness must trigger a re-fetch")
	}
	if op.witnessImportFailures != 1 {
		t.Fatalf("failure counter = %d, want 1", op.witnessImportFailures)
	}
	// Each re-fetch lands on another server and fails again; the last attempt
	// within the budget is still charged but no longer re-fetched.
	for attempt := 2; attempt <= maxWitnessImportRetries; attempt++ {
		op.witnessPeer = fmt.Sprintf("srv-%d", attempt)
		got := tester.fetcher.chargeDivergedWitnessImportFailure(op, importErr)
		if want := attempt < maxWitnessImportRetries; got != want {
			t.Fatalf("attempt %d: re-fetch = %v, want %v", attempt, got, want)
		}
	}
	if op.witnessImportFailures != maxWitnessImportRetries {
		t.Fatalf("failure counter = %d, want %d", op.witnessImportFailures, maxWitnessImportRetries)
	}
	// A witness without a fetch closure is still charged but can never be
	// re-fetched.
	orphan := &blockOrHeaderInject{origin: "o", block: block, witness: witness, witnessPeer: "srv-9", witnessDiverged: true}
	if tester.fetcher.chargeDivergedWitnessImportFailure(orphan, importErr) {
		t.Fatal("without a fetch closure there is no way to re-fetch; must return false")
	}

	mu.Lock()
	defer mu.Unlock()
	wantStrikes := maxWitnessImportRetries + 1 // every charged attempt of op, plus the orphan
	if len(strikes) != wantStrikes || strikes[0] != "srv-1" || strikes[len(strikes)-1] != "srv-9" {
		t.Fatalf("every charged failure must strike its server exactly once: got %v, want %d strikes starting with srv-1 and ending with srv-9", strikes, wantStrikes)
	}
	if len(excluded) != len(strikes) {
		t.Fatalf("every charged failure must exclude its server for the block: %v", excluded)
	}
}

// TestChargeDivergedWitnessImportFailureIgnoresNonWitnessErrors pins the error
// gate: an import failure the witness could not have caused — a local
// interruption, a whitelist mismatch, a stopped chain, an unknown error — is not
// charged to the serving peer even when the witness was fetched and diverged.
// Without this gate every import failure on a stateless node would strike and
// exclude honest witness sources, since diverged is the normal case there.
func TestChargeDivergedWitnessImportFailureIgnoresNonWitnessErrors(t *testing.T) {
	tester := newTester(false)
	defer tester.fetcher.Stop()

	var (
		mu       sync.Mutex
		strikes  []string
		excluded []string
	)
	tester.fetcher.SetWitnessServerStriker(func(id string) {
		mu.Lock()
		strikes = append(strikes, id)
		mu.Unlock()
	})
	tester.fetcher.SetWitnessSourceExcluder(func(peer string, _ common.Hash) {
		mu.Lock()
		excluded = append(excluded, peer)
		mu.Unlock()
	})

	block := createTestBlock(503)
	witness := createTestWitnessForBlock(block)
	fetch := func(common.Hash, chan *eth.Response) (*eth.Request, error) { return nil, errors.New("noop") }

	// The first entry is the #2401 case: a contract bytecode missing from local
	// disk surfaces from ExecuteStateless as ErrStatelessIncompleteState wrapping
	// a *state.MissingCodeError. The witness never carried code, so the server
	// must not be struck, excluded, or re-fetched from for it.
	missingCode := fmt.Errorf("%w: %w", core.ErrStatelessIncompleteState,
		&state.MissingCodeError{Addr: common.HexToAddress("0xc0de"), Hash: common.HexToHash("0xf98d")})
	for _, importErr := range []error{
		missingCode,
		errors.New("insertion is interrupted"),
		errors.New("blockchain is stopped"),
		whitelist.ErrMismatch,
		errors.New("unknown parent"),
		fmt.Errorf("propagated block verification failed: %w", errors.New("invalid timestamp")),
		nil,
	} {
		op := &blockOrHeaderInject{origin: "o", block: block, witness: witness, witnessPeer: "srv", witnessDiverged: true, fetchWitness: fetch}
		if tester.fetcher.chargeDivergedWitnessImportFailure(op, importErr) {
			t.Fatalf("error %v is not witness-attributable and must not trigger a re-fetch", importErr)
		}
		if op.witnessImportFailures != 0 {
			t.Fatalf("error %v must not consume the retry budget", importErr)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(strikes) != 0 || len(excluded) != 0 {
		t.Fatalf("non-witness import errors must not strike or exclude the server; strikes=%v excluded=%v", strikes, excluded)
	}
}

// TestIsWitnessAttributableImportError pins the allowlist: incomplete-witness
// and execution-mismatch failures are attributable, everything else — notably
// errors that wrap nothing the witness carries — is not.
func TestIsWitnessAttributableImportError(t *testing.T) {
	attributable := []error{
		&trie.MissingNodeError{NodeHash: common.HexToHash("0x01"), Path: []byte{0x1}},
		fmt.Errorf("%w: %w", core.ErrStatelessIncompleteState, &trie.MissingNodeError{NodeHash: common.HexToHash("0x02")}), // incomplete witness
		core.ErrStatelessStateRootMismatch,
		fmt.Errorf("%w (remote: 1 local: 2)", core.ErrGasUsedMismatch),
		fmt.Errorf("%w (remote: 0a local: 0b)", core.ErrReceiptRootMismatch),
		fmt.Errorf("%w (remote: 0a local: 0b)", core.ErrBloomMismatch),
		fmt.Errorf("%w (remote: 0a local: 0b)", core.ErrRequestsHashMismatch),
		errors.New("invalid merkle root (remote: 0a local: 0b) dberr: <nil>"),
		errors.New("stateless self-validation root mismatch (cross: 0a local: 0b)"),
		errors.New("stateless self-validation receipt root mismatch: remote 0a != local 0b"),
	}
	for _, err := range attributable {
		if !isWitnessAttributableImportError(err) {
			t.Fatalf("%v must be attributable to the witness server", err)
		}
	}
	notAttributable := []error{
		nil,
		errors.New("insertion is interrupted"),
		errors.New("blockchain is stopped"),
		whitelist.ErrMismatch,
		errors.New("unknown parent"),
		fmt.Errorf("%w: %w", core.ErrStatelessIncompleteState, &state.MissingCodeError{Addr: common.HexToAddress("0x01"), Hash: common.HexToHash("0x02")}), // #2401: missing code, not the witness
		core.ErrStatelessIncompleteState, // sentinel alone: cause unknown, not charged
		errors.New("leveldb: closed"),
	}
	for _, err := range notAttributable {
		if isWitnessAttributableImportError(err) {
			t.Fatalf("%v must not be attributable to the witness server", err)
		}
	}
}

// TestRetryAfterImportFailureReRegistersPending pins the witness manager side of
// the re-fetch: the block goes back into pending with the witness cleared, the
// fetch closure and failure count carried over, and a second registration for
// the same hash is a no-op.
func TestRetryAfterImportFailureReRegistersPending(t *testing.T) {
	tw := newTestWitnessManager()
	defer tw.Close()

	block := createTestBlock(502)
	hash := block.Hash()
	fetch := func(common.Hash, chan *eth.Response) (*eth.Request, error) { return nil, errors.New("noop") }

	// No fetch closure: nothing to retry with.
	tw.manager.retryAfterImportFailure(&blockOrHeaderInject{origin: "o", block: block, witness: createTestWitnessForBlock(block)})
	if tw.PendingCount() != 0 {
		t.Fatal("an op without a fetch closure must not be re-registered")
	}

	op := &blockOrHeaderInject{
		origin: "o", block: block, witness: createTestWitnessForBlock(block),
		witnessPeer: "srv-1", witnessDiverged: true, witnessImportFailures: 1, fetchWitness: fetch,
	}
	tw.manager.retryAfterImportFailure(op)
	if tw.PendingCount() != 1 {
		t.Fatalf("pending count = %d, want 1", tw.PendingCount())
	}
	tw.manager.mu.Lock()
	state := tw.manager.pending[hash]
	tw.manager.mu.Unlock()
	if state == nil || state.announce == nil {
		t.Fatal("re-registered block must have a pending state with an announce to fetch on")
	}
	if state.op.witness != nil || state.op.witnessPeer != "" || state.op.witnessDiverged {
		t.Fatal("re-registered op must start without the failed witness and its provenance")
	}
	if state.op.witnessImportFailures != 1 {
		t.Fatalf("failure count must carry over to bound the cycle, got %d", state.op.witnessImportFailures)
	}
	if state.op.fetchWitness == nil || state.announce.fetchWitness == nil {
		t.Fatal("fetch closure must carry over so the next fetch can run")
	}

	// Already pending: no duplicate registration.
	tw.manager.retryAfterImportFailure(op)
	if tw.PendingCount() != 1 {
		t.Fatalf("duplicate re-registration must be a no-op; pending count = %d", tw.PendingCount())
	}

	// A block that meanwhile became known locally is not re-fetched.
	known := createTestBlock(504)
	tw.manager.parentGetBlock = func(h common.Hash) *types.Block {
		if h == known.Hash() {
			return known
		}
		return nil
	}
	tw.manager.retryAfterImportFailure(&blockOrHeaderInject{origin: "o", block: known, witness: createTestWitnessForBlock(known), witnessPeer: "srv-2", witnessDiverged: true, fetchWitness: fetch})
	if tw.PendingCount() != 1 {
		t.Fatalf("a locally known block must not be re-registered; pending count = %d", tw.PendingCount())
	}

	// A block whose witness was marked unavailable is not re-fetched either.
	unavailable := createTestBlock(505)
	tw.manager.markWitnessUnavailable(unavailable.Hash())
	tw.manager.retryAfterImportFailure(&blockOrHeaderInject{origin: "o", block: unavailable, witness: createTestWitnessForBlock(unavailable), witnessPeer: "srv-3", witnessDiverged: true, fetchWitness: fetch})
	if tw.PendingCount() != 1 {
		t.Fatalf("an unavailable-marked block must not be re-registered; pending count = %d", tw.PendingCount())
	}
}

// TestImportFailureWithDivergedWitnessRefetchesFromAnotherPeer drives the whole
// WIT2 import-failure consequence through the real BlockFetcher loop: a block
// whose fetched witness is accepted on the size oracle alone fails import, the
// serving peer is struck and excluded as a source for that block, the witness
// is fetched again from another peer, and the block then imports. Before this,
// the failure was logged at debug and the block forgotten, so a peer relaying
// the valid BP announce could serve unusable within-band bytes at no cost.
func TestImportFailureWithDivergedWitnessRefetchesFromAnotherPeer(t *testing.T) {
	hashes, blocks := makeChain(1, 0, genesis)
	block := blocks[hashes[0]]
	hash := block.Hash()

	tester := newTester(false)
	defer tester.fetcher.Stop()

	// BP-signed commitment on file: a hash the served witness will NOT match,
	// with a size it is within band of — so every fetched witness is accepted
	// as a non-deterministic variant (diverged), never as the BP's own bytes.
	reference, err := stateless.NewWitness(block.Header(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := reference.EncodeRLP(&buf); err != nil {
		t.Fatal(err)
	}
	signedSize := uint64(buf.Len())
	tester.fetcher.wm.parentSignedWitnessHash = func(h common.Hash) (common.Hash, uint64, bool) {
		if h == hash {
			return common.HexToHash("0xd1ffe7e17"), signedSize, true
		}
		return common.Hash{}, 0, false
	}

	var (
		mu       sync.Mutex
		strikes  []string
		excluded []string
		fetches  atomic.Int32
		imports  atomic.Int32
	)
	tester.fetcher.SetWitnessServerStriker(func(id string) {
		mu.Lock()
		strikes = append(strikes, id)
		mu.Unlock()
	})
	tester.fetcher.SetWitnessSourceExcluder(func(peer string, h common.Hash) {
		if h != hash {
			t.Errorf("exclusion for unexpected block %s", h)
		}
		mu.Lock()
		excluded = append(excluded, peer)
		mu.Unlock()
	})
	// First import fails (an unusable witness), the second succeeds.
	tester.fetcher.insertChain = func(blocks types.Blocks, witnesses []*stateless.Witness) (int, error) {
		if imports.Add(1) == 1 {
			return 0, fmt.Errorf("%w (cross: 01 local: 02)", core.ErrStatelessStateRootMismatch)
		}
		return tester.insertChain(blocks, witnesses)
	}
	imported := make(chan *types.Block, 1)
	tester.fetcher.importedHook = func(_ *types.Header, b *types.Block) { imported <- b }

	// Each fetch is answered by a distinct "server", as the handler's source
	// exclusion would arrange in production.
	fetchWitness := func(h common.Hash, sink chan *eth.Response) (*eth.Request, error) {
		n := fetches.Add(1)
		req := &eth.Request{Peer: fmt.Sprintf("server-%d", n), Cancel: make(chan struct{})}
		go func() {
			w, err := stateless.NewWitness(block.Header(), nil)
			if err != nil {
				return
			}
			sink <- &eth.Response{Req: req, Res: []*stateless.Witness{w}, Time: time.Millisecond, Done: make(chan error, 1)}
		}()
		return req, nil
	}
	if err := tester.fetcher.InjectBlockWithWitnessRequirement("origin", block, fetchWitness); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-imported:
		if got.Hash() != hash {
			t.Fatalf("imported unexpected block %s", got.Hash())
		}
	case <-time.After(10 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("block never imported after the witness re-fetch (fetches=%d imports=%d strikes=%v excluded=%v)",
			fetches.Load(), imports.Load(), strikes, excluded)
	}

	if n := fetches.Load(); n != 2 {
		t.Fatalf("witness fetch count = %d, want 2 (original + one re-fetch)", n)
	}
	if n := imports.Load(); n != 2 {
		t.Fatalf("import attempt count = %d, want 2", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(strikes) != 1 || strikes[0] != "server-1" {
		t.Fatalf("exactly the first server must be struck once, got %v", strikes)
	}
	if len(excluded) != 1 || excluded[0] != "server-1" {
		t.Fatalf("exactly the first server must be excluded for the block, got %v", excluded)
	}
}

// waitForCachedWitness blocks until the witness manager has cached a broadcast
// witness for hash (it arrives on the manager loop asynchronously).
func waitForCachedWitness(t *testing.T, f *BlockFetcher, hash common.Hash) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.wm.witnessCache.Get(hash) != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("broadcast witness never reached the witness cache")
}

// waitForPendingWitness blocks until the witness manager has registered hash as
// pending a witness fetch (block injection is processed on the manager loop).
func waitForPendingWitness(t *testing.T, f *BlockFetcher, hash common.Hash) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f.wm.isPending(hash) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("block never became pending a witness")
}

// divergedBroadcastImportFailureHarness wires the striker/excluder recorders,
// the fail-once insertChain and the imported hook shared by the two push-path
// import-failure tests below.
type divergedBroadcastImportFailureHarness struct {
	mu       sync.Mutex
	strikes  []string
	excluded []string
	fetches  atomic.Int32
	imports  atomic.Int32
	imported chan *types.Block
}

func newDivergedBroadcastImportFailureHarness(t *testing.T, tester *fetcherTester, hash common.Hash) *divergedBroadcastImportFailureHarness {
	t.Helper()
	hs := &divergedBroadcastImportFailureHarness{imported: make(chan *types.Block, 1)}
	tester.fetcher.SetWitnessServerStriker(func(id string) {
		hs.mu.Lock()
		hs.strikes = append(hs.strikes, id)
		hs.mu.Unlock()
	})
	tester.fetcher.SetWitnessSourceExcluder(func(peer string, h common.Hash) {
		if h != hash {
			t.Errorf("exclusion for unexpected block %s", h)
		}
		hs.mu.Lock()
		hs.excluded = append(hs.excluded, peer)
		hs.mu.Unlock()
	})
	// First import fails (an unusable witness), the second succeeds.
	tester.fetcher.insertChain = func(blocks types.Blocks, witnesses []*stateless.Witness) (int, error) {
		if hs.imports.Add(1) == 1 {
			return 0, fmt.Errorf("%w (cross: 01 local: 02)", core.ErrStatelessStateRootMismatch)
		}
		return tester.insertChain(blocks, witnesses)
	}
	tester.fetcher.importedHook = func(_ *types.Header, b *types.Block) { hs.imported <- b }
	return hs
}

// fetchWitnessFor answers each fetch from a distinct "server" with a valid
// witness, after release is closed (nil release = answer immediately).
func (hs *divergedBroadcastImportFailureHarness) fetchWitnessFor(block *types.Block, release <-chan struct{}) witnessRequesterFn {
	return func(h common.Hash, sink chan *eth.Response) (*eth.Request, error) {
		n := hs.fetches.Add(1)
		req := &eth.Request{Peer: fmt.Sprintf("server-%d", n), Cancel: make(chan struct{})}
		go func() {
			if release != nil {
				<-release
			}
			w, err := stateless.NewWitness(block.Header(), nil)
			if err != nil {
				return
			}
			sink <- &eth.Response{Req: req, Res: []*stateless.Witness{w}, Time: time.Millisecond, Done: make(chan error, 1)}
		}()
		return req, nil
	}
}

func (hs *divergedBroadcastImportFailureHarness) assertPusherCharged(t *testing.T, hash common.Hash) {
	t.Helper()
	select {
	case got := <-hs.imported:
		if got.Hash() != hash {
			t.Fatalf("imported unexpected block %s", got.Hash())
		}
	case <-time.After(10 * time.Second):
		hs.mu.Lock()
		defer hs.mu.Unlock()
		t.Fatalf("block never imported after the witness re-fetch (fetches=%d imports=%d strikes=%v excluded=%v)",
			hs.fetches.Load(), hs.imports.Load(), hs.strikes, hs.excluded)
	}
	if n := hs.imports.Load(); n != 2 {
		t.Fatalf("import attempt count = %d, want 2", n)
	}
	hs.mu.Lock()
	defer hs.mu.Unlock()
	if len(hs.strikes) != 1 || hs.strikes[0] != "pusher" {
		t.Fatalf("exactly the pusher must be struck once, got %v", hs.strikes)
	}
	if len(hs.excluded) != 1 || hs.excluded[0] != "pusher" {
		t.Fatalf("exactly the pusher must be excluded for the block, got %v", hs.excluded)
	}
}

// TestImportFailureWithDivergedBroadcastWitnessChargesPusher is the push-path
// twin of TestImportFailureWithDivergedWitnessRefetchesFromAnotherPeer: a
// within-band witness that arrives by NewWitness broadcast BEFORE its block (so
// it waits in the witness cache) and then fails import must cost the pusher a
// strike and an exclusion, and the witness must be fetched again from another
// peer — the consequence the paged-fetch path already carries. Before this,
// the cached witness was attached without provenance, so
// chargeDivergedWitnessImportFailure returned on its first guard and a push was
// the free way to deliver unusable within-band bytes.
func TestImportFailureWithDivergedBroadcastWitnessChargesPusher(t *testing.T) {
	hashes, blocks := makeChain(1, 0, genesis)
	block := blocks[hashes[0]]
	hash := block.Hash()

	tester := newTester(false)
	defer tester.fetcher.Stop()
	hs := newDivergedBroadcastImportFailureHarness(t, tester, hash)

	pushed, err := stateless.NewWitness(block.Header(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// The body arrives first, accepted on the size oracle alone (diverged).
	if err := tester.fetcher.InjectWitness("pusher", pushed, true); err != nil {
		t.Fatal(err)
	}
	waitForCachedWitness(t, tester.fetcher, hash)

	if err := tester.fetcher.InjectBlockWithWitnessRequirement("origin", block, hs.fetchWitnessFor(block, nil)); err != nil {
		t.Fatal(err)
	}
	hs.assertPusherCharged(t, hash)
	if n := hs.fetches.Load(); n != 1 {
		t.Fatalf("witness fetch count = %d, want 1 (only the re-fetch after the pushed witness failed import)", n)
	}
}

// TestImportFailureWithDivergedBroadcastWitnessOnPendingBlockChargesPusher
// covers the other attach site: the block is already pending a witness fetch
// when the divergent body is pushed. handleBroadcast attaches it (first witness
// to arrive wins), the import fails, and the pusher — not the in-flight fetch's
// server — must be the one struck and excluded, with the witness then fetched
// again. Fetch responses are held back until the strike has landed so the
// pushed body is the one imported first.
func TestImportFailureWithDivergedBroadcastWitnessOnPendingBlockChargesPusher(t *testing.T) {
	hashes, blocks := makeChain(1, 0, genesis)
	block := blocks[hashes[0]]
	hash := block.Hash()

	tester := newTester(false)
	defer tester.fetcher.Stop()
	hs := newDivergedBroadcastImportFailureHarness(t, tester, hash)

	release := make(chan struct{})
	if err := tester.fetcher.InjectBlockWithWitnessRequirement("origin", block, hs.fetchWitnessFor(block, release)); err != nil {
		t.Fatal(err)
	}
	// The block must be pending before the push arrives, or the push would
	// take the before-the-block cache path covered by the previous test.
	waitForPendingWitness(t, tester.fetcher, hash)
	pushed, err := stateless.NewWitness(block.Header(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tester.fetcher.InjectWitness("pusher", pushed, true); err != nil {
		t.Fatal(err)
	}
	// The pushed body imports (and fails) first; once the pusher has been
	// struck, let every fetch answer so the re-fetch can complete.
	deadline := time.Now().Add(5 * time.Second)
	for {
		hs.mu.Lock()
		struck := len(hs.strikes) > 0
		hs.mu.Unlock()
		if struck {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pusher never struck (imports=%d fetches=%d)", hs.imports.Load(), hs.fetches.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	hs.assertPusherCharged(t, hash)
	if n := hs.fetches.Load(); n < 1 {
		t.Fatalf("witness fetch count = %d, want at least the re-fetch", n)
	}
}
