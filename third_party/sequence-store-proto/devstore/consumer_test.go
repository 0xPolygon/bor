package devstore

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"
)

func TestResumePositions(t *testing.T) {
	store, _, con := setupGRPC(t)
	_, _, _ = seedBlocks(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// after=head of the second entry resumes at the third.
	store.mu.Lock()
	secondHead := store.heads[1]
	total := len(store.log)
	store.mu.Unlock()

	resp, err := con.Range(ctx, &pb.RangeRequest{
		After: &pb.RangeRequest_Head{Head: secondHead[:]},
	})
	if err != nil {
		t.Fatalf("Range after=head: %v", err)
	}

	if got, want := len(resp.GetEntries()), total-2; got != want {
		t.Errorf("after=head returned %d entries, want %d", got, want)
	}

	if !resp.GetLive() {
		t.Error("expected live=true at tip")
	}

	// after=block:101 resumes after block 101's seal (3 entries in).
	resp, err = con.Range(ctx, &pb.RangeRequest{
		After: &pb.RangeRequest_Block{Block: 101},
	})
	if err != nil {
		t.Fatalf("Range after=block: %v", err)
	}

	if got, want := len(resp.GetEntries()), total-3; got != want {
		t.Errorf("after=block returned %d entries, want %d", got, want)
	}

	// Unknown head and unknown block are NOT_FOUND; a short head is
	// INVALID_ARGUMENT.
	for name, req := range map[string]*pb.RangeRequest{
		"unknown_head":  {After: &pb.RangeRequest_Head{Head: make([]byte, 32)}},
		"unknown_block": {After: &pb.RangeRequest_Block{Block: 999}},
	} {
		if _, err := con.Range(ctx, req); status.Code(err) != codes.NotFound {
			t.Errorf("%s: code = %v, want NotFound", name, status.Code(err))
		}
	}

	_, err = con.Range(ctx, &pb.RangeRequest{After: &pb.RangeRequest_Head{Head: []byte{0x01}}})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("short head: code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestRangePaging(t *testing.T) {
	store, _, con := setupGRPC(t)
	_, _, _ = seedBlocks(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var (
		got  int
		next []byte
	)

	for {
		req := &pb.RangeRequest{Limit: 2}
		if next != nil {
			req.After = &pb.RangeRequest_Head{Head: next}
		}

		resp, err := con.Range(ctx, req)
		if err != nil {
			t.Fatalf("Range: %v", err)
		}

		got += len(resp.GetEntries())
		next = resp.GetNext()

		if resp.GetLive() {
			break
		}
	}

	store.mu.Lock()
	total := len(store.log)
	head := store.head
	store.mu.Unlock()

	if got != total {
		t.Errorf("paged %d entries, want %d", got, total)
	}

	if commitment.Head(next) != head {
		t.Errorf("final next = %x, want store head %x", next, head)
	}

	// An empty response at the tip echoes the presented position.
	resp, err := con.Range(ctx, &pb.RangeRequest{After: &pb.RangeRequest_Head{Head: next}})
	if err != nil {
		t.Fatalf("Range at tip: %v", err)
	}

	if len(resp.GetEntries()) != 0 || commitment.Head(resp.GetNext()) != head {
		t.Errorf("tip range = %d entries, next %x; want 0 entries, next %x",
			len(resp.GetEntries()), resp.GetNext(), head)
	}
}

func TestRangeLongPoll(t *testing.T) {
	store, _, con := setupGRPC(t)
	c, _, hash2 := seedBlocks(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	head := store.Head()

	done := make(chan *pb.RangeResponse, 1)

	go func() {
		resp, err := con.Range(ctx, &pb.RangeRequest{
			After:  &pb.RangeRequest_Head{Head: head[:]},
			WaitMs: 5000,
		})
		if err != nil {
			t.Errorf("long-poll Range: %v", err)
		}

		done <- resp
	}()

	time.Sleep(50 * time.Millisecond)
	mustAppend(t, store, c.open(103, hash2))

	select {
	case resp := <-done:
		if len(resp.GetEntries()) != 1 {
			t.Errorf("long-poll returned %d entries, want 1", len(resp.GetEntries()))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long-poll did not return after append")
	}
}

func TestGetBlockUnknown(t *testing.T) {
	_, _, con := setupGRPC(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := con.GetBlock(ctx, &pb.GetBlockRequest{BlockNumber: 1})
	if status.Code(err) != codes.NotFound {
		t.Errorf("code = %v, want NotFound", status.Code(err))
	}
}

// After the Live frame, appends keep flowing as envelopes and Live is never
// repeated.
func TestStreamFollowsAfterLive(t *testing.T) {
	store, _, con := setupGRPC(t)
	c, _, hash2 := seedBlocks(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	head := store.Head()

	cs, err := con.Stream(ctx, &pb.StreamRequest{After: &pb.StreamRequest_Head{Head: head.Bytes()}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	frame, err := cs.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}

	if frame.GetLive() == nil {
		t.Fatal("expected Live first when starting at the tip")
	}

	mustAppend(t, store, c.open(103, hash2))
	mustAppend(t, store, c.record([]byte{0x05}))

	for i := range 2 {
		frame, err := cs.Recv()
		if err != nil {
			t.Fatalf("recv live entry %d: %v", i, err)
		}

		if frame.GetEntry() == nil {
			t.Fatalf("frame %d after Live is not an entry (duplicate Live?)", i)
		}
	}
}

func TestStreamAfterBlock(t *testing.T) {
	store, _, con := setupGRPC(t)
	_, _, _ = seedBlocks(t, store)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cs, err := con.Stream(ctx, &pb.StreamRequest{After: &pb.StreamRequest_Block{Block: 101}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Block 102's three entries, then Live.
	for i := range 3 {
		frame, err := cs.Recv()
		if err != nil {
			t.Fatalf("recv %d: %v", i, err)
		}

		if frame.GetEntry() == nil {
			t.Fatalf("frame %d is not an entry", i)
		}
	}

	frame, err := cs.Recv()
	if err != nil {
		t.Fatalf("live: %v", err)
	}

	if frame.GetLive() == nil {
		t.Fatal("expected Live after block 102's entries")
	}
}

// A block-addressed resume of a still-open generation serves it from its
// open record, never from the middle.
func TestResumeUnsealedGeneration(t *testing.T) {
	store, _, con := setupGRPC(t)
	c, _, hash2 := seedBlocks(t, store)

	mustAppend(t, store, c.open(103, hash2))
	mustAppend(t, store, c.record([]byte{0x06}))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := con.Range(ctx, &pb.RangeRequest{After: &pb.RangeRequest_Block{Block: 103}})
	if err != nil {
		t.Fatalf("Range: %v", err)
	}

	if got := len(resp.GetEntries()); got != 2 {
		t.Fatalf("unsealed resume returned %d entries, want 2", got)
	}

	open := resp.GetEntries()[0].GetBlockOpen()
	if open.GetBlockNumber() != 103 {
		t.Errorf("first entry is not block 103's open (number %d)", open.GetBlockNumber())
	}
}
