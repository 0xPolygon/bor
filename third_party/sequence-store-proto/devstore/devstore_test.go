package devstore

import (
	"context"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"
)

const testChainID = 137

// chain builds valid entries producer-side, tracking the running head.
type chain struct {
	t    *testing.T
	head commitment.Head
}

func newChain(t *testing.T) *chain {
	t.Helper()

	return &chain{t: t, head: commitment.Seed(testChainID)}
}

func (c *chain) open(number uint64, parent [32]byte) *pb.Entry {
	c.t.Helper()

	ctx := commitment.OpenContext{
		Number:     number,
		Timestamp:  1750000000 + number,
		ParentHash: parent,
		GasLimit:   45000000,
		BaseFee:    big.NewInt(25000000000),
	}
	entry := &pb.Entry{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{
		BlockNumber:      ctx.Number,
		BlockTimestamp:   ctx.Timestamp,
		ParentHash:       ctx.ParentHash[:],
		GasLimit:         ctx.GasLimit,
		BaseFee:          ctx.BaseFee.Bytes(),
		PrefixCommitment: c.head.Bytes(),
	}}}

	head, err := commitment.FoldOpen(c.head, ctx)
	if err != nil {
		c.t.Fatalf("FoldOpen: %v", err)
	}

	c.head = head

	return entry
}

func (c *chain) record(txs ...[]byte) *pb.Entry {
	entry := &pb.Entry{Kind: &pb.Entry_Record{Record: &pb.Record{
		Transactions:     txs,
		PrefixCommitment: c.head.Bytes(),
	}}}
	c.head = commitment.FoldTxs(c.head, txs)

	return entry
}

func (c *chain) seal(header []byte) (*pb.Entry, [32]byte) {
	entry := &pb.Entry{Kind: &pb.Entry_BlockSeal{BlockSeal: &pb.BlockSeal{
		Header:           header,
		PrefixCommitment: c.head.Bytes(),
	}}}
	sealed := commitment.SealedHash(header)
	c.head = commitment.FoldSeal(c.head, sealed)

	return entry, sealed
}

func mustAppend(t *testing.T, s *Store, entry *pb.Entry) {
	t.Helper()

	if got := s.Append(entry); got != pb.AckStatus_ACK_STATUS_OK {
		t.Fatalf("Append = %v, want OK", got)
	}
}

// seedBlocks publishes two sealed blocks and returns the chain and the two
// sealed hashes.
func seedBlocks(t *testing.T, s *Store) (*chain, [32]byte, [32]byte) {
	t.Helper()

	c := newChain(t)
	mustAppend(t, s, c.open(101, [32]byte{0xef}))
	mustAppend(t, s, c.record([]byte{0x01}))

	seal1, hash1 := c.seal([]byte("header-101"))
	mustAppend(t, s, seal1)
	mustAppend(t, s, c.open(102, hash1))
	mustAppend(t, s, c.record([]byte{0x02}, []byte{0x03}))

	seal2, hash2 := c.seal([]byte("header-102"))
	mustAppend(t, s, seal2)

	return c, hash1, hash2
}

func TestAppendHappyPath(t *testing.T) {
	s := New(testChainID)
	c, _, _ := seedBlocks(t, s)

	if s.Head() != c.head {
		t.Errorf("store head %x diverged from producer head %x", s.Head(), c.head)
	}
}

func TestAppendStaleCommitment(t *testing.T) {
	s := New(testChainID)
	c, _, hash2 := seedBlocks(t, s)

	stale := newChain(t)
	stale.head = commitment.Seed(testChainID) // never advanced: stale prefix

	if got := s.Append(stale.open(103, hash2)); got != pb.AckStatus_ACK_STATUS_STALE_COMMITMENT {
		t.Fatalf("stale Append = %v, want STALE_COMMITMENT", got)
	}

	mustAppend(t, s, c.open(103, hash2))
}

func TestAppendRejections(t *testing.T) {
	prefix := func(s *Store) []byte { h := s.Head(); return h[:] }

	tests := []struct {
		name  string
		entry func(s *Store, hash2 [32]byte) *pb.Entry
		want  pb.AckStatus
	}{
		{"empty_record", func(s *Store, _ [32]byte) *pb.Entry {
			return &pb.Entry{Kind: &pb.Entry_Record{Record: &pb.Record{PrefixCommitment: prefix(s)}}}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
		{"short_prefix", func(_ *Store, _ [32]byte) *pb.Entry {
			return &pb.Entry{Kind: &pb.Entry_Record{Record: &pb.Record{
				Transactions: [][]byte{{0x01}}, PrefixCommitment: []byte{0x01},
			}}}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
		{"record_without_open_block", func(s *Store, _ [32]byte) *pb.Entry {
			return &pb.Entry{Kind: &pb.Entry_Record{Record: &pb.Record{
				Transactions: [][]byte{{0x01}}, PrefixCommitment: prefix(s),
			}}}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
		{"seal_without_open_block", func(s *Store, _ [32]byte) *pb.Entry {
			return &pb.Entry{Kind: &pb.Entry_BlockSeal{BlockSeal: &pb.BlockSeal{
				Header: []byte("h"), PrefixCommitment: prefix(s),
			}}}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
		{"base_fee_oversized", func(s *Store, hash2 [32]byte) *pb.Entry {
			fee := make([]byte, 33)
			fee[0] = 0x01

			return &pb.Entry{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{
				BlockNumber: 103, ParentHash: hash2[:], GasLimit: 1,
				BaseFee: fee, PrefixCommitment: prefix(s),
			}}}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
		{"open_short_prefix", func(_ *Store, hash2 [32]byte) *pb.Entry {
			return &pb.Entry{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{
				BlockNumber: 103, ParentHash: hash2[:], GasLimit: 1,
				PrefixCommitment: []byte{0x01},
			}}}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
		{"open_short_parent", func(s *Store, _ [32]byte) *pb.Entry {
			return &pb.Entry{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{
				BlockNumber: 103, ParentHash: []byte{0xaa}, GasLimit: 1,
				PrefixCommitment: prefix(s),
			}}}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
		{"seal_short_prefix", func(s *Store, _ [32]byte) *pb.Entry {
			return &pb.Entry{Kind: &pb.Entry_BlockSeal{BlockSeal: &pb.BlockSeal{
				Header: []byte("h"), PrefixCommitment: []byte{0x01},
			}}}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
		{"base_fee_max_width_ok_shape", func(s *Store, hash2 [32]byte) *pb.Entry {
			// 32-byte fee with a leading zero is non-minimal — rejected —
			// which pins the boundary between width and minimality checks.
			fee := make([]byte, 32)
			fee[1] = 0x01

			return &pb.Entry{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{
				BlockNumber: 103, ParentHash: hash2[:], GasLimit: 1,
				BaseFee: fee, PrefixCommitment: prefix(s),
			}}}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
		{"base_fee_leading_zero", func(s *Store, hash2 [32]byte) *pb.Entry {
			return &pb.Entry{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{
				BlockNumber: 103, ParentHash: hash2[:], GasLimit: 1,
				BaseFee: []byte{0x00, 0x01}, PrefixCommitment: prefix(s),
			}}}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
		{"known_parent_wrong_height", func(s *Store, hash2 [32]byte) *pb.Entry {
			return &pb.Entry{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{
				BlockNumber: 200, ParentHash: hash2[:], GasLimit: 1,
				PrefixCommitment: prefix(s),
			}}}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
		{"missing_kind", func(_ *Store, _ [32]byte) *pb.Entry {
			return &pb.Entry{}
		}, pb.AckStatus_ACK_STATUS_MALFORMED},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(testChainID)
			_, _, hash2 := seedBlocks(t, s)
			before := s.Head()

			if got := s.Append(tt.entry(s, hash2)); got != tt.want {
				t.Errorf("Append = %v, want %v", got, tt.want)
			}

			if s.Head() != before {
				t.Error("rejected entry advanced the head")
			}
		})
	}
}

func TestReAnchorSameHeight(t *testing.T) {
	s := New(testChainID)
	c, hash1, _ := seedBlocks(t, s)

	// Replacement block at the already-sealed height 102, back on parent 101.
	mustAppend(t, s, c.open(102, hash1))
	mustAppend(t, s, c.record([]byte{0x04}))

	seal, _ := c.seal([]byte("header-102-replacement"))
	mustAppend(t, s, seal)

	resp, err := s.GetBlock(context.Background(), &pb.GetBlockRequest{BlockNumber: 102})
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}

	got := resp.GetEntries()[2].GetBlockSeal().GetHeader()
	if string(got) != "header-102-replacement" {
		t.Errorf("latest generation seal = %q, want the replacement", got)
	}
}

func setupGRPC(t *testing.T) (*Store, pb.PublisherServiceClient, pb.ConsumerServiceClient) {
	t.Helper()

	store := New(testChainID)

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterPublisherServiceServer(srv, store)
	pb.RegisterConsumerServiceServer(srv, store)

	go func() {
		_ = srv.Serve(lis)
	}()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return store, pb.NewPublisherServiceClient(conn), pb.NewConsumerServiceClient(conn)
}

func TestPublishAndStreamEndToEnd(t *testing.T) {
	store, pub, con := setupGRPC(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Publish one sealed block over the pipelined stream.
	ps, err := pub.Publish(ctx)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	c := newChain(t)
	entries := []*pb.Entry{c.open(101, [32]byte{0xef}), c.record([]byte{0x01}, []byte{0x02})}
	seal, _ := c.seal([]byte("header-101"))
	entries = append(entries, seal)

	for _, entry := range entries {
		if err := ps.Send(&pb.PublishRequest{Entry: entry}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	for i := range entries {
		resp, err := ps.Recv()
		if err != nil {
			t.Fatalf("Recv ack %d: %v", i, err)
		}

		if resp.GetStatus() != pb.AckStatus_ACK_STATUS_OK {
			t.Fatalf("ack %d = %v, want OK", i, resp.GetStatus())
		}
	}

	// Stream from the beginning: all three envelopes, then Live.
	cs, err := con.Stream(ctx, &pb.StreamRequest{})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	running := commitment.Seed(testChainID)

	for i := 0; i < len(entries); i++ {
		frame, err := cs.Recv()
		if err != nil {
			t.Fatalf("stream recv %d: %v", i, err)
		}

		entry := frame.GetEntry()
		if entry == nil {
			t.Fatalf("frame %d is not an entry", i)
		}

		running = verifyAndFold(t, running, entry)
	}

	if running != store.Head() {
		t.Errorf("consumer head %x diverged from store head %x", running, store.Head())
	}

	frame, err := cs.Recv()
	if err != nil {
		t.Fatalf("live frame: %v", err)
	}

	if frame.GetLive() == nil {
		t.Fatal("expected Live frame after tail")
	}
}

// verifyAndFold is the consumer-side chain check: prefix must equal the
// running head, then the entry folds into it.
func verifyAndFold(t *testing.T, head commitment.Head, entry *pb.Entry) commitment.Head {
	t.Helper()

	switch kind := entry.GetKind().(type) {
	case *pb.Entry_BlockOpen:
		open := kind.BlockOpen
		if commitment.Head(open.GetPrefixCommitment()) != head {
			t.Fatal("open prefix mismatch")
		}

		next, err := commitment.FoldOpen(head, commitment.OpenContext{
			Number:     open.GetBlockNumber(),
			Timestamp:  open.GetBlockTimestamp(),
			ParentHash: [32]byte(open.GetParentHash()),
			GasLimit:   open.GetGasLimit(),
			BaseFee:    new(big.Int).SetBytes(open.GetBaseFee()),
		})
		if err != nil {
			t.Fatalf("FoldOpen: %v", err)
		}

		return next
	case *pb.Entry_Record:
		if commitment.Head(kind.Record.GetPrefixCommitment()) != head {
			t.Fatal("record prefix mismatch")
		}

		return commitment.FoldTxs(head, kind.Record.GetTransactions())
	case *pb.Entry_BlockSeal:
		if commitment.Head(kind.BlockSeal.GetPrefixCommitment()) != head {
			t.Fatal("seal prefix mismatch")
		}

		return commitment.FoldSeal(head, commitment.SealedHash(kind.BlockSeal.GetHeader()))
	default:
		t.Fatal("unknown entry kind")

		return head
	}
}

// The store's core guarantee per the design's equivocation analysis: writers
// contending for one head produce exactly one winner.
func TestConcurrentPublishersSingleWinner(t *testing.T) {
	s := New(testChainID)
	c, _, hash2 := seedBlocks(t, s)
	entry := c.open(103, hash2)

	var (
		ok, stale atomic.Int32
		wg        sync.WaitGroup
	)

	for range 16 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			switch s.Append(entry) {
			case pb.AckStatus_ACK_STATUS_OK:
				ok.Add(1)
			case pb.AckStatus_ACK_STATUS_STALE_COMMITMENT:
				stale.Add(1)
			default:
			}
		}()
	}

	wg.Wait()

	if ok.Load() != 1 || stale.Load() != 15 {
		t.Errorf("contended append: %d OK, %d STALE; want 1 and 15", ok.Load(), stale.Load())
	}

	if s.Head() != c.head {
		t.Error("store head diverged after contended append")
	}
}
