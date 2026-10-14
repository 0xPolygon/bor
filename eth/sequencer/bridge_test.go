package sequencer

import (
	"context"
	"math/big"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
)

func publishHeader(t *testing.T, p *Publisher, header *types.Header, txs int) {
	t.Helper()

	p.OpenBlock(header.Number.Uint64(), header.Time, header.ParentHash, header.GasLimit, header.BaseFee)

	var body []*types.Transaction

	for i := 0; i < txs; i++ {
		tx := testTx(t, uint64(i))
		body = append(body, tx)
		p.PublishTx(tx)
	}

	p.SealBlock(blockFor(header, body))
}

// A dead producer's sealed run stays recoverable past its first block: our
// rebuild of height 1 has a different hash (another signer's seal), but the
// store's height 2 executes the same way only when the two parents agree
// on everything execution reads.
func TestRecoveryBridgesEquivalentParent(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(h *types.Header)
		bridge bool
	}{
		{name: "seal fields differ only", mutate: func(h *types.Header) {}, bridge: true},
		// Height 1 is the first rebuilt one, so no history slot can differ yet.
		{name: "state root differs", mutate: func(h *types.Header) { h.Root = common.Hash{0x02} }},
		{name: "tx root differs", mutate: func(h *types.Header) { h.TxHash = common.Hash{0x02} }},
		{name: "receipt root differs", mutate: func(h *types.Header) { h.ReceiptHash = common.Hash{0x02} }},
		{name: "gas used differs", mutate: func(h *types.Header) { h.GasUsed++ }},
		{name: "gas limit differs", mutate: func(h *types.Header) { h.GasLimit++ }},
		{name: "timestamp differs", mutate: func(h *types.Header) { h.Time++ }},
		{name: "base fee differs", mutate: func(h *types.Header) { h.BaseFee = big.NewInt(1) }},
		{name: "height differs", mutate: func(h *types.Header) { h.Number = big.NewInt(7) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := startHarness(t)
			p := newTestPublisher(t, h, nil)

			theirs1 := testHeader(1, common.Hash{0xef})
			theirs1.Root = common.Hash{0x01}
			theirs1.TxHash = common.Hash{0x11}
			theirs1.ReceiptHash = common.Hash{0x21}
			theirs1.GasUsed = 42_000
			theirs1.Extra = []byte("producer-a")
			publishHeader(t, p, theirs1, 2)

			theirs2 := testHeader(2, theirs1.Hash())
			publishHeader(t, p, theirs2, 3)
			waitHead(t, h, p, 5*time.Second)

			ours1 := types.CopyHeader(theirs1)
			ours1.Extra = []byte("producer-b")
			ours1.Difficulty = big.NewInt(3)
			tc.mutate(ours1)

			fc := &fakeChain{
				canonical: map[uint64]common.Hash{1: ours1.Hash()},
				known:     map[common.Hash]*types.Header{ours1.Hash(): ours1},
			}

			fresh := newTestPublisher(t, h, fc)
			w := fresh.AdoptWindow(2, ours1.Hash())

			if !tc.bridge {
				if w != nil {
					t.Fatalf("a parent that executes differently must not be bridged: %+v", w)
				}

				if modeKind(fresh) == modeRecover {
					t.Fatal("an unbridged recovery must not run in recover mode")
				}

				return
			}

			if w == nil {
				t.Fatal("an equivalent parent must keep the sealed run recoverable")
			}

			if w.Number != 2 || w.ParentHash != ours1.Hash() || len(w.Txs) != 3 || w.Timestamp != theirs2.Time {
				t.Fatalf("bridged window must carry the store's content on our parent: %+v", w)
			}

			if !w.Complete {
				t.Fatal("a recovered generation must be rebuilt without pool fill")
			}

			fresh.read.breaker.mu.Lock()
			suspended := time.Now().Before(fresh.read.breaker.recoveringUntil)
			fresh.read.breaker.mu.Unlock()

			if !suspended {
				t.Fatal("following a dead producer's run must suspend the frozen-tail rule")
			}

			if modeKind(fresh) != modeRecover {
				t.Fatal("a bridged recovery must publish nothing")
			}
		})
	}
}

// historyRun is a dead producer's run in the store and our rebuild of it,
// at heights 1..n on a shared block 0 (hash 0xef), with real states. Every
// height has the same transactions and receipts on both chains but another
// seal, so the hashes differ from height 1, and from prague on each block's
// EIP-2935 write stores its own chain's parent hash.
type historyRun struct {
	theirs, ours []*types.Header // indexed by height
	chain        *fakeChain      // our chain: headers and every state, nothing canonical
}

// newHistoryRun builds the run. When blockhashAt is set, the transaction at
// that height stores its block's parent hash (BLOCKHASH(n-1)) in an
// application contract: an identical receipt on both chains, different state.
func newHistoryRun(t *testing.T, n, prague, blockhashAt uint64) *historyRun {
	t.Helper()

	db := state.NewDatabaseForTesting()
	app := common.Address{0xa9}

	genesis, err := state.New(types.EmptyRootHash, db)
	if err != nil {
		t.Fatal(err)
	}

	genesis.SetCode(params.HistoryStorageAddress, params.HistoryStorageCode, tracing.CodeChangeGenesis)
	genesis.SetCode(app, []byte{0x00}, tracing.CodeChangeGenesis)

	root, err := genesis.Commit(0, true, false)
	if err != nil {
		t.Fatal(err)
	}

	execute := func(parentRoot common.Hash, number uint64, parent common.Hash) common.Hash {
		s, err := state.New(parentRoot, db)
		if err != nil {
			t.Fatal(err)
		}

		if number >= prague {
			s.SetState(params.HistoryStorageAddress, historySlot(number-1), parent)
		}

		if number == blockhashAt {
			s.SetState(app, common.Hash{}, parent)
		}

		root, err := s.Commit(number, true, false)
		if err != nil {
			t.Fatal(err)
		}

		return root
	}

	run := &historyRun{
		theirs: make([]*types.Header, n+1),
		ours:   make([]*types.Header, n+1),
		chain:  &fakeChain{canonical: map[uint64]common.Hash{}, known: map[common.Hash]*types.Header{}, states: db},
	}

	theirRoot, ourRoot := root, root
	theirParent, ourParent := common.Hash{0xef}, common.Hash{0xef}

	for number := uint64(1); number <= n; number++ {
		theirs := testHeader(number, theirParent)
		theirs.TxHash = common.Hash{0x11, byte(number)}
		theirs.ReceiptHash = common.Hash{0x21, byte(number)}
		theirs.GasUsed = 21_000 * number
		theirs.Extra = []byte("producer-a")
		theirs.Root = execute(theirRoot, number, theirParent)

		ours := types.CopyHeader(theirs)
		ours.ParentHash = ourParent
		ours.Extra = []byte("producer-b")
		ours.Root = execute(ourRoot, number, ourParent)

		run.theirs[number], run.ours[number] = theirs, ours
		run.chain.known[ours.Hash()] = ours

		theirRoot, ourRoot = theirs.Root, ours.Root
		theirParent, ourParent = theirs.Hash(), ours.Hash()
	}

	return run
}

// The parents' states may differ only in the history slots of the heights
// the chains do not share, and only by the store's own hashes there.
// Matching receipts are not enough: a transaction that stored a block hash
// leaves its receipt unchanged and the state different.
func TestHistoryOnlyDiff(t *testing.T) {
	cases := []struct {
		name        string
		prague      uint64   // first block that writes history (0: from the start)
		blockhashAt uint64   // height whose transaction stores BLOCKHASH(n-1)
		links       []uint64 // store heights whose sealed headers the bridge has read
		fork        bool     // the store link at height 2 comes from another fork
		walk        int      // lineageMax override
		noState     bool
		parent      uint64 // height of the two parents compared
		want        bool
	}{
		{name: "same state at the first rebuilt height", parent: 1, noState: true, want: true},
		{name: "history slots alone differ", parent: 3, links: []uint64{1, 2}, want: true},
		{name: "a transaction stored a block hash", blockhashAt: 2, parent: 2, links: []uint64{1}},
		{name: "the store's earlier heights were never read", parent: 3},
		{name: "a store link from another fork", parent: 3, links: []uint64{1}, fork: true},
		{name: "history starts inside the run", prague: 3, parent: 3, links: []uint64{1, 2}, want: true},
		{name: "run longer than the walk", parent: 3, links: []uint64{1, 2}, walk: 1},
		{name: "our parent's state is unavailable", parent: 3, links: []uint64{1, 2}, noState: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.walk != 0 {
				old := lineageMax
				lineageMax = tc.walk
				t.Cleanup(func() { lineageMax = old })
			}

			run := newHistoryRun(t, 3, tc.prague, tc.blockhashAt)
			if tc.noState {
				run.chain.states = nil
			}

			p := barePublisher()
			p.chain = run.chain

			for _, n := range tc.links {
				p.lineage.add(run.theirs[n])
			}

			if tc.fork {
				other := types.CopyHeader(run.theirs[2])
				other.Extra = []byte("producer-c")
				p.lineage.add(other)
			}

			if got := p.historyOnlyDiff(run.theirs[tc.parent], run.ours[tc.parent]); got != tc.want {
				t.Fatalf("historyOnlyDiff = %v, want %v", got, tc.want)
			}
		})
	}
}

// A takeover follows a dead producer's run height after height while each
// parent's state is the store's up to the history slots. The bridge keeps
// the store headers it reads, which is what lets a later height account for
// every slot the run has written so far. A transaction that stored a block
// hash ends the bridge at the next height, though its receipt matched.
func TestRecoveryBridgeFollowsTheRunsHistory(t *testing.T) {
	cases := []struct {
		name        string
		blockhashAt uint64
		bridged     []bool // at heights 2, 3, 4
	}{
		{name: "history slots alone differ", bridged: []bool{true, true, true}},
		{name: "a transaction stored a block hash at 2", blockhashAt: 2, bridged: []bool{true, false}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := newHistoryRun(t, 4, 0, tc.blockhashAt)

			h := startHarness(t)
			p := newTestPublisher(t, h, nil)

			// One block at a time: a journal holding more collapses the
			// oldest for a backfill this chainless publisher cannot run.
			for number := uint64(1); number <= 4; number++ {
				publishHeader(t, p, run.theirs[number], 2)
				waitHead(t, h, p, 5*time.Second)
			}

			fresh := classifierPublisher(t, h, run.chain)

			for i, want := range tc.bridged {
				number := uint64(i + 2)
				parent := run.ours[number-1].Hash()
				run.chain.canonical[number-1] = parent

				w := fresh.AdoptWindow(number, parent)
				if got := w != nil && w.ParentHash == parent && w.Complete; got != want {
					t.Fatalf("height %d bridged = %v, want %v (%+v)", number, got, want, w)
				}
			}
		})
	}
}

// The lineage keeps the links of the lineageMax heights up to the newest
// read, and walks them only while each one is the parent of the one above.
func TestStoreLineage(t *testing.T) {
	old := lineageMax
	lineageMax = 3
	t.Cleanup(func() { lineageMax = old })

	chain := make([]*types.Header, 7)
	parent := common.Hash{0xef}

	for n := 1; n < len(chain); n++ {
		chain[n] = testHeader(uint64(n), parent)
		parent = chain[n].Hash()
	}

	var l storeLineage
	for _, header := range chain[1:6] {
		l.add(header)
	}

	if len(l.links) != 3 || l.links[3].hash != chain[3].Hash() || l.links[5].hash != chain[5].Hash() {
		t.Fatalf("links %v, want heights 3..5", l.links)
	}

	got := l.hashesBelow(chain[6])
	if want := []common.Hash{chain[5].Hash(), chain[4].Hash(), chain[3].Hash()}; !slices.Equal(got, want) {
		t.Fatalf("hashesBelow = %v, want %v", got, want)
	}

	// A read lower down replaces the store's tail: the links above it go,
	// and the walk stops at a link that is not the parent it needs.
	other := types.CopyHeader(chain[4])
	other.Extra = []byte("fork")
	l.add(other)

	if len(l.links) != 2 {
		t.Fatalf("links %v, want heights 3..4", l.links)
	}

	if got := l.hashesBelow(chain[5]); !slices.Equal(got, []common.Hash{chain[4].Hash()}) {
		t.Fatalf("walk crossed a fork: %v", got)
	}
}

func TestRecoveryBridgeNeedsTheStoreParent(t *testing.T) {
	h := startHarness(t)
	p := newTestPublisher(t, h, nil)

	// Height 2's parent is not sealed at height 1 in the store, so there is
	// nothing to compare our parent against.
	theirs2 := testHeader(2, common.Hash{0xab})
	publishHeader(t, p, theirs2, 1)
	waitHead(t, h, p, 5*time.Second)

	ours1 := testHeader(1, common.Hash{0xef})
	fc := &fakeChain{
		canonical: map[uint64]common.Hash{1: ours1.Hash()},
		known:     map[common.Hash]*types.Header{ours1.Hash(): ours1},
	}

	fresh := newTestPublisher(t, h, fc)
	if w := fresh.AdoptWindow(2, ours1.Hash()); w != nil {
		t.Fatalf("an unverifiable parent must not be bridged: %+v", w)
	}
}

// A dead producer's live window on an equivalent parent is seeded rather
// than rebuilt from the pool, and the build stays muted so the seal flush
// supersedes the window with a block that carries it as a prefix.
func TestLiveWindowBridgesEquivalentParent(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(h *types.Header)
		bridge bool
	}{
		{name: "seal fields differ only", mutate: func(h *types.Header) {}, bridge: true},
		{name: "receipt root differs", mutate: func(h *types.Header) { h.ReceiptHash = common.Hash{0x02} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := startHarness(t)
			p := newTestPublisher(t, h, nil)

			theirs1 := testHeader(1, common.Hash{0xef})
			theirs1.ReceiptHash = common.Hash{0x21}
			theirs1.Extra = []byte("producer-a")
			publishHeader(t, p, theirs1, 2)

			theirs2 := testHeader(2, theirs1.Hash())
			p.OpenBlock(2, theirs2.Time, theirs2.ParentHash, theirs2.GasLimit, theirs2.BaseFee)

			for i := 0; i < 3; i++ {
				p.PublishTx(testTx(t, uint64(i)))
			}

			waitHead(t, h, p, 5*time.Second)

			ours1 := types.CopyHeader(theirs1)
			ours1.Extra = []byte("producer-b")
			tc.mutate(ours1)

			fc := &fakeChain{
				canonical: map[uint64]common.Hash{1: ours1.Hash()},
				known:     map[common.Hash]*types.Header{ours1.Hash(): ours1},
			}

			fresh := newTestPublisher(t, h, fc)
			w := fresh.AdoptWindow(2, ours1.Hash())

			if modeKind(fresh) != modeMuted {
				t.Fatal("a foreign window on another parent must keep the build muted")
			}

			if !tc.bridge {
				if w != nil {
					t.Fatalf("a parent that executes differently must not be bridged: %+v", w)
				}

				return
			}

			if w == nil || w.ParentHash != ours1.Hash() || len(w.Txs) != 3 || w.Timestamp != theirs2.Time {
				t.Fatalf("bridged live window must carry the store's order on our parent: %+v", w)
			}

			if w.Complete {
				t.Fatal("a live window is not closed: the build may extend it")
			}

			fresh.mu.Lock()
			bridged := fresh.bridged
			fresh.mu.Unlock()

			if bridged != (bridgedWindow{height: 2, storeParent: theirs1.Hash()}) {
				t.Fatalf("the seal flush must learn which window was bridged: %+v", bridged)
			}
		})
	}
}

// The seal flush may displace a bridged window without the chain's word only
// when nothing it holds would be dropped: our block carries it in full, in
// order, at the height and on the store parent the bridge recorded.
func TestFlushCarriesBridgedWindow(t *testing.T) {
	storeParent := common.Hash{0xaa}

	cases := []struct {
		name   string
		window []int // indexes into the block's transactions
		bridge bridgedWindow
		want   bool
	}{
		{name: "carried prefix", window: []int{0, 1}, bridge: bridgedWindow{height: 2, storeParent: storeParent}, want: true},
		{name: "carried whole block", window: []int{0, 1, 2}, bridge: bridgedWindow{height: 2, storeParent: storeParent}, want: true},
		{name: "reordered", window: []int{1, 0}, bridge: bridgedWindow{height: 2, storeParent: storeParent}},
		{name: "window longer than block", window: []int{0, 1, 2, 3}, bridge: bridgedWindow{height: 2, storeParent: storeParent}},
		{name: "not bridged at this height", window: []int{0, 1}, bridge: bridgedWindow{height: 3, storeParent: storeParent}},
		{name: "bridged from another parent", window: []int{0, 1}, bridge: bridgedWindow{height: 2, storeParent: common.Hash{0xbb}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := barePublisher()
			parent := publishBlock(t, p, 1, common.Hash{0xef}, 1).Hash()

			header := testHeader(2, parent)
			p.OpenBlock(2, header.Time, parent, header.GasLimit, header.BaseFee)

			txs := []*types.Transaction{testTx(t, 0), testTx(t, 1), testTx(t, 2), testTx(t, 3)}
			for _, tx := range txs[:3] {
				p.PublishTx(tx)
			}

			p.SealBlock(blockFor(header, txs[:3]))

			window := []*pb.Entry{openEntry(commitment.OpenContext{
				Number: 2, Timestamp: header.Time, ParentHash: storeParent,
				GasLimit: header.GasLimit, BaseFee: header.BaseFee,
			}, commitment.Head{})}

			for _, i := range tc.window {
				raw, err := txs[i].MarshalBinary()
				if err != nil {
					t.Fatal(err)
				}

				window = append(window, recordEntry(raw, commitment.Head{}))
			}

			info := tailInfo{tipOpen: true, tipOpenHeight: 2, tipOpenParent: storeParent, window: window}

			p.mu.Lock()
			p.bridged = tc.bridge
			got := p.carriesBridgedWindowLocked(info, 2)
			p.mu.Unlock()

			if got != tc.want {
				t.Fatalf("carriesBridgedWindowLocked = %v, want %v", got, tc.want)
			}
		})
	}
}

// A takeover producer is often CPU-starved, so its store reads are slow.
// Once the probe has found our height sealed in the store, the walk and
// the recovery fetches run on their own budget: a recovery that missed the
// build-start deadline used to fall back to a pool rebuild, losing the dead
// producer's order at the first height and every bridged height after it.
func TestRecoverySurvivesSlowReads(t *testing.T) {
	h := startHarness(t)
	p := newTestPublisher(t, h, nil)
	sealed := publishBlock(t, p, 1, common.Hash{0xef}, 2)
	waitHead(t, h, p, 5*time.Second)

	old := checkTailTimeout
	checkTailTimeout = 60 * time.Millisecond
	t.Cleanup(func() { checkTailTimeout = old })

	conn, err := grpc.NewClient(h.addr, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any,
			cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption,
		) error {
			time.Sleep(40 * time.Millisecond)

			return invoke(ctx, method, req, reply, cc, opts...)
		}))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	fresh := barePublisher()
	fresh.chain = &fakeChain{canonical: map[uint64]common.Hash{}}
	fresh.read.setClient(pb.NewConsumerServiceClient(conn))

	w := fresh.AdoptWindow(1, common.Hash{0xef})
	if w == nil || w.Number != 1 || len(w.Txs) != 2 || w.Timestamp != sealed.Time {
		t.Fatalf("a slow store must still yield the recovery: %+v", w)
	}

	if modeKind(fresh) != modeRecover {
		t.Fatalf("mode = %d, want a recovery", modeKind(fresh))
	}
}

// A recovery build is the store's sealed generation, rebuilt exactly; the
// seal barrier must pass it even when the store's tail shows a newer window
// at that height, or the rebuild loops until the height goes to the pool.
func TestBarrierPassesRecoveryBuild(t *testing.T) {
	h := startHarness(t)
	p := newTestPublisher(t, h, &fakeChain{})

	sealed := publishBlock(t, p, 1, common.Hash{0xef}, 1)
	waitHead(t, h, p, 5*time.Second)
	parent := sealHash(t, sealed)

	p.OpenBlock(2, 1700000002, parent, 30_000_000, fee25())
	p.PublishTx(testTx(t, 0))
	waitDrained(t, p, 5*time.Second)

	appendForeignRecord(t, h, testTx(t, 7))

	p.mu.Lock()
	p.mode = buildMode{kind: modeRecover, height: 2}
	p.mu.Unlock()

	if !p.AwaitSequenced(time.Second, 2, []*types.Transaction{testTx(t, 0)}) {
		t.Fatal("a recovery build must pass the seal barrier")
	}

	if p.ResyncNeeded() {
		t.Fatal("a recovery build must not arm a rebuild")
	}
}
