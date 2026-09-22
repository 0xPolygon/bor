package sequencer

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

// auditStoreStub serves the two reads the audit makes and nothing else: one
// generation per height, and a Range to resolve the retention floor. The pass
// never streams.
type auditStoreStub struct {
	pb.UnimplementedConsumerServiceServer

	t      *testing.T
	sealed map[uint64]*types.Header
	asked  chan uint64

	// floor is the oldest height this store still serves; zero serves an
	// empty Range, which is a store that cannot place its own floor.
	floor  uint64
	ranged chan *pb.RangeRequest
}

func (s *auditStoreStub) Range(_ context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
	select {
	case s.ranged <- req:
	default:
	}

	if s.floor == 0 {
		return &pb.RangeResponse{}, nil
	}

	return &pb.RangeResponse{Entries: []*pb.Entry{
		{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{BlockNumber: s.floor}}},
	}}, nil
}

func (s *auditStoreStub) GetBlock(_ context.Context, req *pb.GetBlockRequest) (*pb.GetBlockResponse, error) {
	height := req.GetBlockNumber()
	select {
	case s.asked <- height:
	default:
	}

	header, ok := s.sealed[height]
	if !ok {
		return nil, status.Error(codes.NotFound, "unretained")
	}

	return &pb.GetBlockResponse{Entries: sealedGeneration(s.t, header)}, nil
}

// countingListener reports connections, so a test can assert on the dial
// itself rather than on a side effect that happens either way.
type countingListener struct {
	net.Listener

	accepted atomic.Int64
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}

	return conn, err
}

func startAuditStore(t *testing.T, sealed map[uint64]*types.Header) (string, *auditStoreStub, *countingListener) {
	t.Helper()

	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	lis := &countingListener{Listener: base}
	stub := &auditStoreStub{
		t: t, sealed: sealed,
		asked:  make(chan uint64, 256),
		ranged: make(chan *pb.RangeRequest, 8),
	}
	srv := grpc.NewServer()
	pb.RegisterConsumerServiceServer(srv, stub)

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return base.Addr().String(), stub, lis
}

// The pass as the consumer actually runs it: dial the store, read each height
// over gRPC, and route the watermark through the consumer's guarded writer.
func TestAuditPassOverGRPCRecordsMismatches(t *testing.T) {
	h := startExecHarness(t)
	head := h.chain.CurrentBlock().Number.Uint64()

	sealed := map[uint64]*types.Header{}
	for height := uint64(1); height <= head; height++ {
		block := h.chain.GetBlockByNumber(height)
		if block == nil {
			t.Fatalf("no canonical block at %d", height)
		}
		sealed[height] = block.Header()
	}

	// The store's final generation at the head sealed a block the chain never
	// adopted.
	sealed[head] = testHeader(head, common.Hash{0xee})

	endpoint, stub, lis := startAuditStore(t, sealed)

	consumer := newAuditTestConsumer(h)
	consumer.endpoint = endpoint

	if err := rawdb.WritePreconfAuditedThrough(h.chain.DB(), 0); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	consumer.runAuditPass(t.Context())

	if got, ok, _ := rawdb.ReadPreconfAuditedThrough(h.chain.DB()); !ok || got != head {
		t.Fatalf("watermark = (%d, %v), want (%d, true)", got, ok, head)
	}

	records := rawdb.ReadInvalidPreconfsInRange(h.chain.DB(), 1, head)
	if len(records) != 1 || records[0].Number != head || records[0].Reason != unobservedMismatchReason {
		t.Fatalf("records = %+v, want one %s at %d", records, unobservedMismatchReason, head)
	}

	if len(stub.asked) == 0 {
		t.Fatal("the pass never read the store")
	}

	// The counter has to be able to see a dial, or its use below proves nothing.
	if lis.accepted.Load() == 0 {
		t.Fatal("the listener counted no connection for a pass that read the store")
	}
}

// With nothing to walk the pass reads nothing and leaves the watermark alone.
// It still runs: the sweep below the watermark needs no store.
func TestAuditPassWithNothingToAuditReadsNothing(t *testing.T) {
	h := startExecHarness(t)
	head := h.chain.CurrentBlock().Number.Uint64()

	endpoint, stub, lis := startAuditStore(t, map[uint64]*types.Header{})

	consumer := newAuditTestConsumer(h)
	consumer.endpoint = endpoint

	if err := rawdb.WritePreconfAuditedThrough(h.chain.DB(), head); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	consumer.runAuditPass(t.Context())

	if len(stub.asked) != 0 {
		t.Fatalf("the pass read %d heights with nothing to audit", len(stub.asked))
	}
	if got := lis.accepted.Load(); got != 0 {
		t.Fatalf("the pass connected %d times with nothing to audit", got)
	}

	if got, _, _ := rawdb.ReadPreconfAuditedThrough(h.chain.DB()); got != head {
		t.Fatalf("watermark = %d, want it untouched at %d", got, head)
	}
}

// Nothing to walk means nothing to dial, but a served commitment below the
// mark must still be judged by the pass the consumer actually runs.
func TestAuditPassSweepsBelowWatermarkWithNothingToWalk(t *testing.T) {
	h := startExecHarness(t)
	head := h.chain.CurrentBlock().Number.Uint64()
	height := head - 1

	endpoint, stub, lis := startAuditStore(t, map[uint64]*types.Header{})
	consumer := newAuditTestConsumer(h)
	consumer.endpoint = endpoint

	if err := rawdb.WritePreconfAuditedThrough(h.chain.DB(), head); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}
	// Three txs the canonical block lacks, folded against the real header
	// context so only the content differs.
	served := servedTxs(3)
	digest := foldServed(contextSeed(h.chain.GetHeaderByNumber(height)), served)
	if err := rawdb.WritePreconfServed(h.chain.DB(), height, uint64(len(served)), digest); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}

	consumer.runAuditPass(t.Context())

	records := rawdb.ReadInvalidPreconfsInRange(h.chain.DB(), 1, head)
	if len(records) != 1 || records[0].Number != height || records[0].Reason != servedMismatchReason {
		t.Fatalf("records = %+v, want one %s at %d: the sweep did not run through runAuditPass", records, servedMismatchReason, height)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(h.chain.DB(), height); ok {
		t.Fatal("served commitment not cleared after judging")
	}
	if len(stub.asked) != 0 || lis.accepted.Load() != 0 {
		t.Fatalf("the sweep needed the store: asked=%d connections=%d", len(stub.asked), lis.accepted.Load())
	}
	if got, _, _ := rawdb.ReadPreconfAuditedThrough(h.chain.DB()); got != head {
		t.Fatalf("watermark = %d, want it untouched at %d", got, head)
	}
}

// A store that cannot be reached leaves the watermark where it was, so the
// window is retried rather than silently marked audited.
func TestAuditPassHoldsTheWatermarkWhenTheStoreIsUnreachable(t *testing.T) {
	h := startExecHarness(t)
	consumer := newAuditTestConsumer(h)
	consumer.endpoint = "127.0.0.1:1"

	if err := rawdb.WritePreconfAuditedThrough(h.chain.DB(), 1); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	consumer.runAuditPass(t.Context())

	if got, _, _ := rawdb.ReadPreconfAuditedThrough(h.chain.DB()); got != 1 {
		t.Fatalf("watermark = %d, want it held at 1", got)
	}
}

func TestAuditPassHandlesAnUndialableEndpoint(t *testing.T) {
	h := startExecHarness(t)
	consumer := newAuditTestConsumer(h)
	consumer.endpoint = "unknown-scheme://%%"

	if err := rawdb.WritePreconfAuditedThrough(h.chain.DB(), 1); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	consumer.runAuditPass(t.Context())

	if got, _, _ := rawdb.ReadPreconfAuditedThrough(h.chain.DB()); got != 1 {
		t.Fatalf("watermark = %d, want it held at 1", got)
	}
}

// The retention floor as the consumer actually reads it. The unit tests inject
// the reader; this asserts the request the store really receives — after
// unset, which is what resolves to the earliest retained entry — and that the
// pass then leaves the aged-out heights alone instead of probing each one.
func TestAuditPassStartsAtTheStoreFloorOverGRPC(t *testing.T) {
	h := startExecHarness(t)
	head := h.chain.CurrentBlock().Number.Uint64()
	if head < 3 {
		t.Fatalf("harness head is %d, too low for a floor inside the range", head)
	}
	floor := head - 1

	// The store retains only the floor and above; below it, GetBlock would
	// answer NOT_FOUND, and the point is that it is never asked.
	sealed := map[uint64]*types.Header{}
	for height := floor; height <= head; height++ {
		block := h.chain.GetBlockByNumber(height)
		if block == nil {
			t.Fatalf("no canonical block at %d", height)
		}
		sealed[height] = block.Header()
	}

	endpoint, stub, _ := startAuditStore(t, sealed)
	stub.floor = floor

	consumer := newAuditTestConsumer(h)
	consumer.endpoint = endpoint

	if err := rawdb.WritePreconfAuditedThrough(h.chain.DB(), 0); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	consumer.runAuditPass(t.Context())

	var req *pb.RangeRequest
	select {
	case req = <-stub.ranged:
	default:
		t.Fatal("the pass never asked the store for its retention floor")
	}
	if after := req.GetAfter(); after != nil {
		t.Fatalf("the floor read asked to resume after %v, want the earliest retained entry", after)
	}
	if req.GetLimit() != auditFloorEntries {
		t.Fatalf("floor read limit = %d, want %d", req.GetLimit(), auditFloorEntries)
	}

	for {
		select {
		case asked := <-stub.asked:
			if asked < floor {
				t.Fatalf("the pass read height %d, below the store floor %d", asked, floor)
			}
		default:
			if got, ok, _ := rawdb.ReadPreconfAuditedThrough(h.chain.DB()); !ok || got != head {
				t.Fatalf("watermark = (%d, %v), want (%d, true)", got, ok, head)
			}

			return
		}
	}
}
