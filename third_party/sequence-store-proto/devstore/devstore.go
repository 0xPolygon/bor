// Package devstore is an in-memory reference implementation of the sequence
// store for tests and devnets: the full wire contract — structural
// validation, the head check, the commitment fold, resume, and block-indexed
// reads — with no persistence, no authentication, and no replication. Not
// for production.
package devstore

import (
	"errors"
	"io"
	"math/big"
	"sync"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"
)

// generation is one publication generation of a block height: the positions
// of its open, records, and seal in the log. A re-anchor at the same height
// starts a new generation; block-addressed reads resolve to the latest.
type generation struct {
	positions []int
	sealed    bool
}

// Store holds one append-only chain of entries guarded by the head check.
// It implements both generated gRPC service interfaces.
type Store struct {
	pb.UnimplementedPublisherServiceServer
	pb.UnimplementedConsumerServiceServer

	mu    sync.Mutex
	seed  commitment.Head
	head  commitment.Head
	log   []*pb.Entry
	heads []commitment.Head // post-fold head per log position

	// resume maps a post-fold head to the position right after its entry;
	// the seed maps to 0.
	resume map[commitment.Head]int

	gens         map[uint64]*generation
	sealedHeight map[[32]byte]uint64 // sealed hash -> height, for known-parent checks
	openHeight   uint64
	openActive   bool

	// notify is closed and replaced on every append; readers wait on it.
	notify chan struct{}
}

// New returns an empty store whose head is the genesis seed for chainID.
func New(chainID uint64) *Store {
	seed := commitment.Seed(chainID)

	return &Store{
		seed:         seed,
		head:         seed,
		resume:       map[commitment.Head]int{seed: 0},
		gens:         map[uint64]*generation{},
		sealedHeight: map[[32]byte]uint64{},
		notify:       make(chan struct{}),
	}
}

// Head returns the store's current commitment head.
func (s *Store) Head() commitment.Head {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.head
}

// Append runs the ingress path for one entry: structural validation, the
// head check, per-kind rules, fold, append. The store takes
// ownership of an accepted entry — mutating it afterwards corrupts the log
// for every reader.
func (s *Store) Append(entry *pb.Entry) pb.AckStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	prefix, ok := validate(entry)
	if !ok {
		return pb.AckStatus_ACK_STATUS_MALFORMED
	}

	if commitment.Head(prefix) != s.head {
		return pb.AckStatus_ACK_STATUS_STALE_COMMITMENT
	}

	var (
		next   commitment.Head
		status pb.AckStatus
	)

	switch kind := entry.GetKind().(type) {
	case *pb.Entry_BlockOpen:
		next, status = s.applyOpen(kind.BlockOpen)
	case *pb.Entry_Record:
		next, status = s.applyRecord(kind.Record)
	case *pb.Entry_BlockSeal:
		next, status = s.applySeal(kind.BlockSeal)
	}

	if status != pb.AckStatus_ACK_STATUS_OK {
		return status
	}

	pos := len(s.log)
	s.log = append(s.log, entry)
	s.heads = append(s.heads, next)
	s.resume[next] = pos + 1
	s.head = next

	close(s.notify)
	s.notify = make(chan struct{})

	return pb.AckStatus_ACK_STATUS_OK
}

func (s *Store) applyOpen(open *pb.BlockOpen) (commitment.Head, pb.AckStatus) {
	// Whenever the named parent is store-known, the open's height must be
	// the parent's height + 1; an unknown parent is a forward jump and the
	// height is taken as claimed.
	parent := [32]byte(open.GetParentHash())
	if height, known := s.sealedHeight[parent]; known && open.GetBlockNumber() != height+1 {
		return commitment.Head{}, pb.AckStatus_ACK_STATUS_MALFORMED
	}

	next, err := commitment.FoldOpen(s.head, commitment.OpenContext{
		Number:     open.GetBlockNumber(),
		Timestamp:  open.GetBlockTimestamp(),
		ParentHash: parent,
		GasLimit:   open.GetGasLimit(),
		BaseFee:    new(big.Int).SetBytes(open.GetBaseFee()),
	})
	if err != nil {
		return commitment.Head{}, pb.AckStatus_ACK_STATUS_MALFORMED
	}

	s.openHeight = open.GetBlockNumber()
	s.openActive = true
	s.gens[s.openHeight] = &generation{positions: []int{len(s.log)}}

	return next, pb.AckStatus_ACK_STATUS_OK
}

func (s *Store) applyRecord(record *pb.Record) (commitment.Head, pb.AckStatus) {
	if !s.openActive {
		return commitment.Head{}, pb.AckStatus_ACK_STATUS_MALFORMED
	}

	gen := s.gens[s.openHeight]
	gen.positions = append(gen.positions, len(s.log))

	return commitment.FoldTxs(s.head, record.GetTransactions()), pb.AckStatus_ACK_STATUS_OK
}

func (s *Store) applySeal(seal *pb.BlockSeal) (commitment.Head, pb.AckStatus) {
	if !s.openActive {
		return commitment.Head{}, pb.AckStatus_ACK_STATUS_MALFORMED
	}

	sealedHash := commitment.SealedHash(seal.GetHeader())

	gen := s.gens[s.openHeight]
	gen.positions = append(gen.positions, len(s.log))
	gen.sealed = true
	s.sealedHeight[sealedHash] = s.openHeight
	s.openActive = false

	return commitment.FoldSeal(s.head, sealedHash), pb.AckStatus_ACK_STATUS_OK
}

// validate applies the structural rules the MALFORMED status enumerates and
// returns the entry's prefix commitment.
func validate(entry *pb.Entry) ([]byte, bool) {
	switch kind := entry.GetKind().(type) {
	case *pb.Entry_BlockOpen:
		open := kind.BlockOpen
		if len(open.GetParentHash()) != 32 || !minimalBaseFee(open.GetBaseFee()) {
			return nil, false
		}

		return open.GetPrefixCommitment(), len(open.GetPrefixCommitment()) == 32
	case *pb.Entry_Record:
		record := kind.Record
		if len(record.GetTransactions()) == 0 {
			return nil, false
		}

		return record.GetPrefixCommitment(), len(record.GetPrefixCommitment()) == 32
	case *pb.Entry_BlockSeal:
		seal := kind.BlockSeal

		return seal.GetPrefixCommitment(), len(seal.GetPrefixCommitment()) == 32
	default:
		return nil, false
	}
}

// minimalBaseFee accepts a big-endian integer with no leading zero bytes
// (empty means zero) of at most 32 bytes.
func minimalBaseFee(fee []byte) bool {
	if len(fee) > 32 {
		return false
	}

	return len(fee) == 0 || fee[0] != 0
}

// Publish implements the pipelined write path: one ack per entry, in order.
func (s *Store) Publish(stream pb.PublisherService_PublishServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}

		status := s.Append(req.GetEntry())
		if err := stream.Send(&pb.PublishResponse{Status: status}); err != nil {
			return err
		}
	}
}
