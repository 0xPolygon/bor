package sequencer

import (
	"errors"
	"fmt"
	"sync"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// session is one consistent stretch of the stream: a running commitment
// head, the speculative execution state, and the speculative seal hashes for
// BLOCKHASH resolution. env == nil between blocks and while skipping.
type session struct {
	applyMu   sync.Mutex
	consumer  *Consumer
	worker    *preconfWorker
	head      commitment.Head
	seeded    bool
	env       *blockEnv      // block currently being applied
	parked    *state.StateDB // post-state of the last speculative seal
	tip       common.Hash    // last speculatively sealed hash
	tipNumber uint64
	sealed    map[uint64]common.Hash
	verified  map[uint64]*types.Header
	tipHeader *types.Header
	reanchor  bool
}

var errPreconfReanchor = errors.New("preconf application requires canonical re-anchor")

type preconfWorker struct {
	session *session
	env     *blockEnv
}

func newSession(consumer *Consumer) *session {
	s := &session{consumer: consumer}
	consumer.worker.Store(nil)
	return s
}

func (s *session) setEnv(env *blockEnv) {
	s.env = env
	s.worker = nil
}

func (s *session) activateEnv() {
	worker := &preconfWorker{session: s, env: s.env}
	s.worker = worker
	s.consumer.worker.Store(worker)
}

func (s *session) clearEnv() {
	worker := s.worker
	s.env = nil
	s.worker = nil
	if worker != nil {
		s.consumer.worker.CompareAndSwap(worker, nil)
	}
}

func (c *Consumer) interruptPreconfWorker(worker *preconfWorker, generation uint64) bool {
	if worker == nil || worker.env == nil || worker.env.generation != generation {
		return false
	}
	s, env := worker.session, worker.env
	env.interrupt.Store(true)
	if s.applyMu.TryLock() {
		if s.worker == worker && s.env == env {
			s.clearEnv()
			s.parked = nil
		}
		s.applyMu.Unlock()
	}
	return true
}

// handle verifies one entry against the commitment chain, folds it, and
// applies it best-effort. An error means the stream position itself is
// invalid; application problems skip instead (void speculative work, wait
// for a re-anchoring open).
func (s *session) handle(entry *pb.Entry) error {
	prefix, next, err := s.fold(entry)
	if err != nil {
		return err
	}

	if !s.seeded {
		// Cold start: adopt the stream's prefix as the seed; integrity from
		// here comes from the chain and the self-authenticating seals.
		s.head = prefix
		s.seeded = true

		var refold error
		if _, next, refold = s.fold(entry); refold != nil {
			return refold
		}
	} else if prefix != s.head {
		return fmt.Errorf("commitment gap: entry prefix %x != running head %x", prefix[:8], s.head[:8])
	}

	s.applyMu.Lock()
	s.apply(entry)
	s.applyMu.Unlock()
	if s.reanchor {
		return errPreconfReanchor
	}
	s.head = next

	return nil
}

// fold checks an entry's wire shape, computes its post-fold head over the
// running head, and returns the entry's claimed prefix alongside it.
func (s *session) fold(entry *pb.Entry) (commitment.Head, commitment.Head, error) {
	prefix := entryPrefix(entry)
	next, err := foldEntry(s.head, entry)
	if err != nil {
		return commitment.Head{}, commitment.Head{}, err
	}

	return commitment.Head(prefix), next, nil
}

func (s *session) apply(entry *pb.Entry) {
	switch kind := entry.GetKind().(type) {
	case *pb.Entry_BlockOpen:
		s.applyOpen(kind.BlockOpen)
	case *pb.Entry_Record:
		s.applyRecord(kind.Record)
	case *pb.Entry_BlockSeal:
		s.applySeal(kind.BlockSeal)
	}
}

// skip voids the speculative work from a height upward and waits for the
// next re-anchoring open.
func (s *session) skip(from uint64, reason string, args ...any) {
	log.Warn("Preconf application skipped: "+reason, args...)
	s.consumer.invalidatePendingFrom(from)
	s.clearEnv()
	s.parked = nil
	s.tip = common.Hash{}
	s.tipNumber = 0
	s.tipHeader = nil
	s.sealed = nil
	s.verified = nil
}

func (s *session) reanchorFromCanonical(from uint64, reason string, args ...any) {
	s.skip(from, reason, args...)
	s.reanchor = true
}

// applyOpen starts a speculative block. A canonical parent is preferred as
// the base even on the happy path — it bounds how long one speculative
// StateDB lives; the parked post-seal state covers parents the chain hasn't
// imported yet.
func (s *session) applyOpen(open *pb.BlockOpen) {
	parent := common.BytesToHash(open.GetParentHash())
	number := open.GetBlockNumber()

	if s.env != nil {
		// A new open while a block is still open is a producer rebuild of
		// the in-progress height; its state is unusable.
		s.skip(s.env.header.Number.Uint64(), "producer rebuilt in-progress block", "number", number)
	}

	parentHeader, cacheable, ok := s.resolveOpenParent(parent, number)
	if !ok {
		return
	}
	if err := validateOpenExecutionContext(s.consumer.chain, parentHeader, open); err != nil {
		s.skip(number, "open execution context invalid", "parent", parent, "err", err)
		return
	}

	var statedb *state.StateDB
	if cacheable {
		var err error
		statedb, err = s.consumer.chain.StateAt(parentHeader.Root)
		if err != nil {
			s.skip(number, "parent state unavailable", "parent", parent, "err", err)
			return
		}
		s.tip = parent
		s.tipNumber = parentHeader.Number.Uint64()
		s.tipHeader = parentHeader
		s.parked = nil
	} else {
		statedb = s.parked
		s.parked = nil
	}

	if cacheable {
		s.sealed = nil
		s.verified = nil
	}
	s.setEnv(newBlockEnv(s.consumer.chain, statedb, open, s.sealed))
	s.env.cacheable = cacheable
	block, payload, ok := preparePending(s.env, s.env.header, common.Hash{}, nil)
	if !ok {
		s.clearEnv()
		return
	}
	s.publishOpen(block, payload, parent, number, cacheable)
}

func (s *session) resolveOpenParent(parent common.Hash, number uint64) (*types.Header, bool, bool) {
	if header := s.consumer.chain.GetHeaderByHash(parent); header != nil &&
		s.consumer.chain.GetCanonicalHash(header.Number.Uint64()) == parent {
		if number != header.Number.Uint64()+1 {
			s.skip(number, "open height is not parent height+1", "number", number, "parent", header.Number)
			return nil, false, false
		}
		return header, true, true
	}
	if parent != s.tip || s.parked == nil {
		s.skip(number, "open parent neither canonical nor speculative tip", "parent", parent)
		return nil, false, false
	}
	if canonical := s.consumer.chain.GetCanonicalHash(s.tipNumber); canonical != (common.Hash{}) && canonical != parent {
		s.skip(number, "speculative parent lost canonical import", "parent", parent, "canonical", canonical)
		return nil, false, false
	}
	if number != s.tipNumber+1 {
		s.skip(number, "open height is not speculative parent height+1", "number", number, "parent", s.tipNumber)
		return nil, false, false
	}
	if s.tipHeader == nil {
		s.skip(number, "open parent header unavailable", "parent", parent)
		return nil, false, false
	}
	return s.tipHeader, false, true
}

func (s *session) publishOpen(block *types.Block, payload pendingPayload, parent common.Hash, number uint64, cacheable bool) {
	var invalidations []pendingInvalidation
	s.consumer.publishMu.Lock()
	store := s.consumer.pendingStore()
	if !cacheable {
		head := s.consumer.chain.CurrentBlock()
		pending := store.PendingBlock()
		canonicalParent := head.Number.Uint64()+1 == number && head.Hash() == parent
		speculativeParent := head.Number.Uint64()+1 < number && pending != nil &&
			pending.NumberU64()+1 == number && pending.Hash() == parent
		if !canonicalParent && !speculativeParent {
			s.consumer.publishMu.Unlock()
			s.skip(number, "speculative parent was reconciled", "parent", parent)
			return
		}
	}
	s.consumer.index.ClearFrom(number + 1)
	if cacheable {
		head := s.consumer.chain.CurrentBlock()
		if head.Hash() != parent || head.Number.Uint64()+1 != number {
			s.consumer.publishMu.Unlock()
			s.skip(number, "canonical parent advanced before open", "parent", parent, "head", head.Hash())
			return
		}
		logs, pendingInvalidations := store.invalidateFromMemory(number, "reorged")
		invalidations = append(invalidations, pendingInvalidations...)
		s.consumer.enqueuePendingLogs(logs)
	}
	s.env.generation = store.begin(number, parent, cacheable)
	if s.env.generation == 0 {
		s.consumer.publishMu.Unlock()
		store.writeInvalidations(invalidations)
		s.skip(number, "pending cache is full", "limit", pendingEntryLimit)
		return
	}
	if !s.consumer.publishPending(block, payload, s.env.generation) {
		s.clearEnv()
		s.parked = nil
	} else {
		s.consumer.index.ClearFrom(number)
		s.activateEnv()
	}
	s.consumer.publishMu.Unlock()
	store.writeInvalidations(invalidations)
}

func (s *session) applyRecord(record *pb.Record) {
	if s.env == nil || len(record.GetTransactions()) == 0 {
		return
	}
	if !s.acceptRecord(record) {
		return
	}
	s.executeRecord(record)
}

func (s *session) acceptRecord(record *pb.Record) bool {
	for _, raw := range record.GetTransactions() {
		if uint64(len(raw)) > pendingInputLimit-s.env.inputBytes {
			s.skip(s.env.header.Number.Uint64(), "ordered input exceeds limit")
			return false
		}
		s.env.inputBytes += uint64(len(raw))
	}
	return true
}

func (s *session) applySeal(seal *pb.BlockSeal) {
	if s.env == nil {
		return
	}

	sealed, err := decodeSealHeader(seal.GetHeader())
	if err != nil {
		s.skip(s.env.header.Number.Uint64(), "undecodable sealed header", "err", err)

		return
	}

	assembled, reusable, verified, ok := s.sealResult(sealed)
	if !ok {
		return
	}

	sealedHash := common.Hash(commitment.SealedHash(seal.GetHeader()))
	if !verified {
		block, payload, ok := preparePending(s.env, assembled.Header(), common.Hash{}, nil)
		if !ok {
			s.clearEnv()
			s.parked = nil
			return
		}
		s.publishUnverifiedSeal(block, payload, sealed, sealedHash)
		return
	}
	block, payload, ok := preparePending(s.env, assembled.Header(), sealedHash, reusable)
	if !ok {
		s.clearEnv()
		s.parked = nil
		return
	}
	s.publishSeal(block, payload, sealed, sealedHash)
}

func (s *session) publishUnverifiedSeal(block *types.Block, payload pendingPayload, sealed *types.Header, sealedHash common.Hash) {
	number := s.env.header.Number.Uint64()
	s.consumer.publishMu.Lock()
	if !s.consumer.publishPending(block, payload, s.env.generation) {
		s.clearEnv()
		s.parked = nil
		s.consumer.publishMu.Unlock()
		return
	}
	logs := s.indexPublishedTransactions()
	s.consumer.enqueuePendingLogs(logs)
	if s.sealed == nil {
		s.sealed = map[uint64]common.Hash{}
	}
	s.sealed[number] = sealedHash
	for height := range s.sealed {
		if height+256 < number {
			delete(s.sealed, height)
			delete(s.verified, height)
		}
	}
	s.tip = sealedHash
	s.tipNumber = number
	s.tipHeader = types.CopyHeader(sealed)
	s.parked = s.env.statedb
	s.clearEnv()
	s.consumer.publishMu.Unlock()
	log.Warn("Preconf seal verification deferred; retaining only an unsealed pending view", "number", number)
}

func (s *session) publishSeal(block *types.Block, payload pendingPayload, sealed *types.Header, sealedHash common.Hash) {
	number := s.env.header.Number.Uint64()
	s.consumer.publishMu.Lock()
	if !s.consumer.publishPending(block, payload, s.env.generation) {
		s.clearEnv()
		s.parked = nil
		s.consumer.publishMu.Unlock()
		return
	}
	logs := s.indexPublishedTransactions()
	s.consumer.enqueuePendingLogs(logs)
	s.consumer.index.Seal(number, sealedHash)

	if s.sealed == nil {
		s.sealed = map[uint64]common.Hash{}
	}

	s.sealed[number] = sealedHash
	if s.verified == nil {
		s.verified = map[uint64]*types.Header{}
	}
	s.verified[number] = types.CopyHeader(sealed)

	// BLOCKHASH reaches at most 256 back; older speculative hashes would
	// otherwise accumulate for the whole parked stretch.
	for height := range s.sealed {
		if height+256 < number {
			delete(s.sealed, height)
			delete(s.verified, height)
		}
	}

	// Park the post-block state as the base for the next open.
	s.tip = sealedHash
	s.tipNumber = number
	s.tipHeader = types.CopyHeader(sealed)
	s.parked = s.env.statedb
	s.clearEnv()
	s.consumer.publishMu.Unlock()
}
