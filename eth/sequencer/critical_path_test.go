package sequencer

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/miner"
)

// sequenceBarrierBudget mirrors miner.sequenceBarrierTimeout, which lives in
// the package that calls this one, so the waits measured here are the ones
// production pays.
const sequenceBarrierBudget = 120 * time.Millisecond

// shortProbeInterval keeps the breaker's recovery window inside a test's
// patience; production pays five seconds for the reasons on the var.
func shortProbeInterval(t *testing.T) {
	t.Helper()

	previous := readProbeInterval
	readProbeInterval = 50 * time.Millisecond

	t.Cleanup(func() { readProbeInterval = previous })
}

// stalledStore completes the gRPC handshake and then never answers. That is
// the shape of a gateway paused with its connections already established,
// and it is what burns a caller's whole budget.
//
// A listener that merely accepts TCP does not reproduce it: gRPC fails an
// RPC immediately on a channel that never reached READY, so the call returns
// Unavailable in microseconds instead of hanging. The campaign measured the
// difference from the other side — a killed envoy (fail fast) cost 0.71
// block/s where frozen gateways (hang) cost 0.47.
type stalledStore struct {
	pb.UnimplementedConsumerServiceServer

	release chan struct{}
}

func (s *stalledStore) stall(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return status.Error(codes.Unavailable, "shutting down")
	}
}

func (s *stalledStore) Range(ctx context.Context, _ *pb.RangeRequest) (*pb.RangeResponse, error) {
	return nil, s.stall(ctx)
}

func (s *stalledStore) GetBlock(ctx context.Context, _ *pb.GetBlockRequest) (*pb.GetBlockResponse, error) {
	return nil, s.stall(ctx)
}

func stalledEndpoint(t *testing.T) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	store := &stalledStore{release: make(chan struct{})}
	srv := grpc.NewServer()
	pb.RegisterConsumerServiceServer(srv, store)

	go func() { _ = srv.Serve(lis) }()

	t.Cleanup(func() {
		close(store.release)
		srv.Stop()
	})

	return lis.Addr().String()
}

// A frozen read path must not be paid for on every block.
//
// The store's reads are advisory: every caller on the critical path already
// has a safe answer for a tail it cannot read. A gateway pool that accepts
// connections without answering used to cost the full read budget at each of
// them, on every block — 250 ms at build start plus a second in the pre-seal
// mirror — which is how a consumer-side outage halved block throughput while
// the write path was perfectly healthy.
//
// The write path here is the live harness, so this isolates the read path:
// nothing about publishing is degraded.
func TestFrozenReadPathIsNotPaidForEveryBlock(t *testing.T) {
	// The production probe interval on purpose: it is what has to outlast a
	// run of blocks for the breaker to be worth anything, and shortening it
	// here would hand almost every iteration its own probe and measure the
	// unfixed behaviour.
	h := startHarness(t)

	p, err := NewPublisher(h.addr, stalledEndpoint(t), testChainID, 0,
		&fakeChain{canonical: map[uint64]common.Hash{}}, nil)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	t.Cleanup(p.Close)

	const blocks = 8

	start := time.Now()

	for number := uint64(2); number < 2+blocks; number++ {
		p.AdoptWindow(number, common.Hash{byte(number)})
		p.AwaitSequenced(sequenceBarrierBudget, number, nil)
	}

	elapsed := time.Since(start)

	// Two silent reads open the breaker, so the first block pays and the
	// rest are refused until the probe interval elapses. Even allowing the
	// worst case — the breaker tripping only at the end of the first
	// block's budgets — eight blocks cannot reach a quarter of what paying
	// for every block would cost.
	unfixed := blocks * (checkTailTimeout + tailReadTimeout)
	if elapsed > unfixed/4 {
		t.Fatalf("%d blocks spent %v on a frozen read path; paying every "+
			"block would cost about %v, so this is not short-circuiting",
			blocks, elapsed, unfixed)
	}

	if !p.read.breaker.isOpen() {
		t.Fatal("the read breaker never opened against a path that answered nothing")
	}
}

// A recovered read path must be used again, and quickly: a breaker that
// stayed open would leave the producer building blind long after the
// gateways came back, which trades one outage for a permanent one.
func TestReadBreakerRecoversAfterTheProbeInterval(t *testing.T) {
	shortProbeInterval(t)

	h := startHarness(t)
	p := newTestPublisher(t, h, &fakeChain{canonical: map[uint64]common.Hash{}})

	sealed := publishBlock(t, p, 1, common.Hash{0xef}, 1)
	waitHead(t, h, p, 5*time.Second)

	for i := 0; i < readFailuresToTrip; i++ {
		p.read.breaker.silent()
	}

	if !p.read.breaker.isOpen() {
		t.Fatal("consecutive silent reads did not open the breaker")
	}

	// Inside the interval every read is refused, so the reader hands back
	// errReadPathDown without a round trip.
	if _, err := p.read.blockKnown(t.Context(), 1); err != errReadPathDown {
		t.Fatalf("read inside the probe interval returned %v, want a refusal", err)
	}

	time.Sleep(readProbeInterval)

	// The probe goes out against a healthy store, answers, and closes it.
	if _, err := p.read.blockKnown(t.Context(), 1); err != nil {
		t.Fatalf("the probe read failed against a live store: %v", err)
	}

	if p.read.breaker.isOpen() {
		t.Fatal("a probe that the store answered left the breaker open")
	}

	// And the build-start read works normally again.
	if w := p.AdoptWindow(2, sealHash(t, sealed)); w != nil {
		t.Fatalf("clean boundary after recovery offered an adoption: %+v", w)
	}
}

// The breaker must not open on an answer it simply did not like. NOT_FOUND
// is the store's normal reply for a height it never held — every build-start
// probe below the store's floor gets one — so counting it as silence would
// keep the producer off a perfectly healthy read path.
func TestReadBreakerIgnoresAnsweredReads(t *testing.T) {
	h := startHarness(t)
	p := newTestPublisher(t, h, &fakeChain{canonical: map[uint64]common.Hash{}})

	for i := 0; i < readFailuresToTrip*4; i++ {
		if _, err := p.read.blockKnown(t.Context(), 9_000_000+uint64(i)); err != nil {
			t.Fatalf("probe of an unheld height returned an error: %v", err)
		}
	}

	if p.read.breaker.isOpen() {
		t.Fatal("NOT_FOUND answers opened the breaker")
	}
}

// Giving up on the ack must not invent a refusal.
//
// A build with nothing gated — muted, or one whose publishing failed —
// carries height 0. Asking the chain about height 0 compares the genesis
// hash against an empty one and answers "somebody else owns this height",
// so a shortcut that consulted the chain alone would refuse a seal nobody
// contested, and refuse every one of them while the write path stayed down.
func TestWriteDownGiveUpDoesNotRefuseAnUngatedSeal(t *testing.T) {
	h := startHarness(t)
	p := newTestPublisher(t, h, &fakeChain{canonical: map[uint64]common.Hash{
		0: {0x9e}, // a genesis hash, as any real chain has
	}})

	// Nothing sealed, so nothing is gated: p.gate is its zero value, and
	// its height is 0 — the value the chain must not be asked about.
	if p.gate.height != 0 {
		t.Fatalf("gate height = %d, want an ungated publisher", p.gate.height)
	}

	p.writeDown.Store(true)

	if v := p.ConfirmSeal(sequenceBarrierBudget); v != miner.SealUnknown {
		t.Fatalf("verdict = %v for an ungated build, want Unknown", v)
	}
}

// And when the chain does show a rival at the gated height, giving up early
// must still surface that refusal: it is the only one left once the ack
// cannot arrive, and it costs one canonical lookup rather than a wait.
func TestWriteDownGiveUpStillHonoursTheChainsRefusal(t *testing.T) {
	h := startHarness(t)

	rival := common.Hash{0xbb}
	p := newTestPublisher(t, h, &fakeChain{canonical: map[uint64]common.Hash{2: rival}})

	sealed := publishBlock(t, p, 1, common.Hash{0xef}, 1)
	waitHead(t, h, p, 5*time.Second)

	// Our block at height 2 is not the one the chain took.
	p.SealBlock(blockFor(testHeader(2, sealHash(t, sealed)), nil))
	p.writeDown.Store(true)

	if v := p.ConfirmSeal(sequenceBarrierBudget); v != miner.SealRefused {
		t.Fatalf("verdict = %v with a rival block canonical at the height, want Refused", v)
	}
}

// Both read choke points feed the breaker. Range is the one the tail walks
// use, but the per-height generation fetch is a separate call, and a store
// that answers neither has to open the breaker whichever one asked.
func TestReadBreakerOpensOnSilentGenerationFetches(t *testing.T) {
	h := startHarness(t)

	p, err := NewPublisher(h.addr, stalledEndpoint(t), testChainID, 0,
		&fakeChain{canonical: map[uint64]common.Hash{}}, nil)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	t.Cleanup(p.Close)

	for i := 0; i < readFailuresToTrip; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), checkTailTimeout)
		_, err := p.read.generation(ctx, uint64(10+i))
		cancel()

		if err == nil {
			t.Fatal("a stalled store answered a generation fetch")
		}
	}

	if !p.read.breaker.isOpen() {
		t.Fatal("silent generation fetches left the breaker closed")
	}
}

// A frozen read path must not hold the broadcast gate either.
//
// The gate waits for the store's verdict on the seal: an ack, a STALE, or
// the deadline. With the read path silent the recheck at the deadline cannot
// run and the wait ends where it started, so the whole budget is spent
// resolving nothing — measured on a devnet as 21 of 47 blocks settling
// Unknown after paying for it, with the contested ones paying four seconds.
func TestFrozenReadPathDoesNotHoldTheSealGate(t *testing.T) {
	h := startHarness(t)

	p, err := NewPublisher(h.addr, stalledEndpoint(t), testChainID, 0,
		&fakeChain{canonical: map[uint64]common.Hash{}}, nil)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	t.Cleanup(p.Close)

	for i := 0; i < readFailuresToTrip; i++ {
		p.read.breaker.silent()
	}

	// A height nothing published, so no ack can resolve it: the gate stands
	// pending exactly as it does when the seal's own ack never comes back.
	p.mu.Lock()
	p.gate = sealGate{height: 500, hash: common.Hash{0x5a}}
	p.mu.Unlock()

	const budget = 400 * time.Millisecond

	start := time.Now()
	v := p.ConfirmSeal(budget)
	elapsed := time.Since(start)

	if v != miner.SealUnknown {
		t.Fatalf("verdict = %v with the read path down, want Unknown", v)
	}

	if elapsed > budget/4 {
		t.Fatalf("gate spent %v of a %v budget with the read path down; the "+
			"wait cannot resolve anything in that state", elapsed, budget)
	}
}

// Except for a withheld seal, which keeps its wait and its refusal.
//
// refuseOnTimeout is set from a read that already found this height closed
// in the store with content this block does not carry. That refusal rests on
// evidence in hand, not on an answer still owed, so a read path that went
// quiet afterwards must not turn it into a broadcast — that is the
// displacement the gate exists to prevent.
func TestReadDownGiveUpKeepsAWithheldSealRefusal(t *testing.T) {
	h := startHarness(t)

	p, err := NewPublisher(h.addr, stalledEndpoint(t), testChainID, 0,
		&fakeChain{canonical: map[uint64]common.Hash{}}, nil)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}

	t.Cleanup(p.Close)

	for i := 0; i < readFailuresToTrip; i++ {
		p.read.breaker.silent()
	}

	p.mu.Lock()
	p.gate = sealGate{height: 500, hash: common.Hash{0x5a}, refuseOnTimeout: true}
	p.mu.Unlock()

	if v := p.ConfirmSeal(20 * time.Millisecond); v != miner.SealRefused {
		t.Fatalf("verdict = %v for a withheld seal, want Refused", v)
	}
}

// What counts as silence, directly. The breaker's whole value rests on this
// distinction, and one case is not reachable through a gRPC call: a deadline
// that fires before the RPC leaves the client returns the raw context error
// rather than a status, and treating that as an answer would leave the
// breaker closed against a path that never replied.
func TestIsSilent(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"no error", nil, false},
		{"raw context deadline", context.DeadlineExceeded, true},
		{"raw context cancel", context.Canceled, true},
		{"status deadline", status.Error(codes.DeadlineExceeded, "too slow"), true},
		{"status unavailable", status.Error(codes.Unavailable, "no backend"), true},
		{"not found is an answer", status.Error(codes.NotFound, "no such height"), false},
		{"invalid argument is an answer", status.Error(codes.InvalidArgument, "bad range"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSilent(tc.err); got != tc.want {
				t.Fatalf("isSilent(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
