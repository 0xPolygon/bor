package devstore

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"
)

const (
	defaultRangeLimit = 512
	maxRangeLimit     = 4096
)

// resolveAfter turns a resume position into the log index to read from.
// Callers hold s.mu.
func (s *Store) resolveAfter(head []byte, block *uint64) (int, error) {
	switch {
	case head != nil:
		if len(head) != 32 {
			return 0, status.Error(codes.InvalidArgument, "resume head must be 32 bytes")
		}

		pos, ok := s.resume[commitment.Head(head)]
		if !ok {
			return 0, status.Error(codes.NotFound, "unknown resume head")
		}

		return pos, nil
	case block != nil:
		gen, ok := s.gens[*block]
		if !ok {
			return 0, status.Error(codes.NotFound, "unknown block")
		}

		// A still-open latest generation is served from its open record:
		// resolving past its in-progress entries would strand the consumer
		// mid-block, without the block's context or the re-anchor signal.
		if !gen.sealed {
			return gen.positions[0], nil
		}

		return gen.positions[len(gen.positions)-1] + 1, nil
	default:
		return 0, nil
	}
}

func streamAfter(req *pb.StreamRequest) ([]byte, *uint64) {
	switch after := req.GetAfter().(type) {
	case *pb.StreamRequest_Head:
		return after.Head, nil
	case *pb.StreamRequest_Block:
		return nil, &after.Block
	default:
		return nil, nil
	}
}

func rangeAfter(req *pb.RangeRequest) ([]byte, *uint64) {
	switch after := req.GetAfter().(type) {
	case *pb.RangeRequest_Head:
		return after.Head, nil
	case *pb.RangeRequest_Block:
		return nil, &after.Block
	default:
		return nil, nil
	}
}

// tail snapshots the log from pos and the current notify channel.
func (s *Store) tail(pos int) ([]*pb.Entry, chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.log[pos:], s.notify
}

// Stream sends entries from the resume position to the tip, marks the
// transition with a Live frame, then follows live appends.
func (s *Store) Stream(req *pb.StreamRequest, srv pb.ConsumerService_StreamServer) error {
	s.mu.Lock()
	head, block := streamAfter(req)

	pos, err := s.resolveAfter(head, block)
	s.mu.Unlock()

	if err != nil {
		return err
	}

	live := false

	for {
		batch, notify := s.tail(pos)

		if err := sendEntries(srv, batch); err != nil {
			return err
		}

		pos += len(batch)

		if len(batch) > 0 {
			continue
		}

		if !live {
			live = true

			if err := srv.Send(&pb.StreamResponse{Frame: &pb.StreamResponse_Live{Live: &pb.Live{}}}); err != nil {
				return err
			}
		}

		select {
		case <-srv.Context().Done():
			return srv.Context().Err()
		case <-notify:
		}
	}
}

func sendEntries(srv pb.ConsumerService_StreamServer, batch []*pb.Entry) error {
	for _, entry := range batch {
		if err := srv.Send(&pb.StreamResponse{Frame: &pb.StreamResponse_Entry{Entry: entry}}); err != nil {
			return err
		}
	}

	return nil
}

// Range returns up to limit entries after the resume position, long-polling
// up to wait_ms for more when fewer are ready.
func (s *Store) Range(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
	limit := int(req.GetLimit())
	if limit == 0 {
		limit = defaultRangeLimit
	} else if limit > maxRangeLimit {
		limit = maxRangeLimit
	}

	s.mu.Lock()
	head, block := rangeAfter(req)

	pos, err := s.resolveAfter(head, block)
	s.mu.Unlock()

	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(time.Duration(req.GetWaitMs()) * time.Millisecond)
	entries := make([]*pb.Entry, 0, limit)

	for {
		batch, notify := s.tail(pos)
		if take := limit - len(entries); len(batch) > take {
			batch = batch[:take]
		}

		entries = append(entries, batch...)
		pos += len(batch)

		if len(entries) == limit || !time.Now().Before(deadline) {
			break
		}

		wait := time.NewTimer(time.Until(deadline))
		select {
		case <-ctx.Done():
			wait.Stop()

			return nil, ctx.Err()
		case <-wait.C:
		case <-notify:
			wait.Stop()
		}
	}

	return s.rangeResponse(entries, pos), nil
}

// rangeResponse assembles next and live: next is the position the response
// ends at (the seed when that position is the chain start), live reports
// whether it is the tip.
func (s *Store) rangeResponse(entries []*pb.Entry, pos int) *pb.RangeResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := s.seed
	if pos > 0 {
		next = s.heads[pos-1]
	}

	return &pb.RangeResponse{
		Entries: entries,
		Next:    next[:],
		Live:    pos == len(s.log),
	}
}

// GetBlock returns the latest generation at a height — open, records, and
// seal once sealed.
func (s *Store) GetBlock(_ context.Context, req *pb.GetBlockRequest) (*pb.GetBlockResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	gen, ok := s.gens[req.GetBlockNumber()]
	if !ok {
		return nil, status.Error(codes.NotFound, "unknown block")
	}

	entries := make([]*pb.Entry, 0, len(gen.positions))
	for _, pos := range gen.positions {
		entries = append(entries, s.log[pos])
	}

	return &pb.GetBlockResponse{Entries: entries}, nil
}
