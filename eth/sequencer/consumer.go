package sequencer

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
)

const consumerRetryDelay = 2 * time.Second

var preconfApplyTimer = metrics.NewRegisteredTimer("sequencer/preconf/apply", nil)

// Consumer follows the sequence store stream on an RPC node, re-executes it
// on top of canonical state, and fills the preconf receipt Index that
// eth_getTransactionReceipt consults for not-yet-imported transactions.
//
// Position and application are handled separately, per the design's
// chain-everything-apply-selectively rule: only commitment gaps and transport
// errors abandon the stream position (warm resume by running head, falling
// back to a cold block anchor, falling back to the earliest retained entry);
// application problems — unknown parents, unavailable state, execution or
// seal divergence — void the speculative work and skip forward until an open
// record re-anchors on a canonical block.
type Consumer struct {
	chain    *core.BlockChain
	config   *params.ChainConfig
	endpoint string
	index    *Index

	cancel context.CancelFunc
	done   chan struct{}
}

// NewConsumer returns a stopped consumer. Determinism preconditions (Rio
// active, coinbase map present) are re-checked per session, not here — a
// node still syncing pre-Rio history becomes eligible once it catches up.
func NewConsumer(endpoint string, chain *core.BlockChain) (*Consumer, error) {
	if chain.Config().Bor == nil {
		return nil, errors.New("sequencer consumer requires a bor chain")
	}

	return &Consumer{
		chain:    chain,
		config:   chain.Config(),
		endpoint: endpoint,
		index:    NewIndex(),
	}, nil
}

// Index exposes the preconf receipts for the RPC layer.
func (c *Consumer) Index() *Index {
	return c.index
}

// Start launches the stream-follow loop.
func (c *Consumer) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done = make(chan struct{})

	go c.run(ctx)
	go c.evictLoop(ctx)
}

// Close stops the consumer and waits for the follow loop to exit.
func (c *Consumer) Close() {
	if c.cancel != nil {
		c.cancel()
		<-c.done
	}
}

// deterministic reports whether the producer's execution context is
// reproducible at the current head: pre-Rio the EVM coinbase is the
// producer's own address, unknowable pre-seal; post-Rio it is
// CalculateCoinbase from the chain config — reproducible only when the
// coinbase map is set.
func (c *Consumer) deterministic() error {
	head := c.chain.CurrentBlock().Number
	if !c.config.Bor.IsRio(head) {
		return errors.New("rio fork not active at current head")
	}

	if common.HexToAddress(c.config.Bor.CalculateCoinbase(head.Uint64())) == (common.Address{}) {
		return errors.New("chain config has no coinbase map")
	}

	return nil
}

func (c *Consumer) run(ctx context.Context) {
	defer close(c.done)

	var sess *session

	for {
		var err error
		if derr := c.deterministic(); derr != nil {
			err = fmt.Errorf("preconf re-execution not deterministic yet: %w", derr)
		} else {
			sess, err = c.follow(ctx, sess)
		}

		if ctx.Err() != nil {
			return
		}

		log.Warn("Sequence stream session ended", "err", err)

		select {
		case <-ctx.Done():
			return
		case <-time.After(consumerRetryDelay):
		}
	}
}

// evictLoop drops preconf receipts for heights the canonical chain has
// imported — the normal receipt path serves them from there on.
func (c *Consumer) evictLoop(ctx context.Context) {
	heads := make(chan core.ChainHeadEvent, 16)
	sub := c.chain.SubscribeChainHeadEvent(heads)

	defer sub.Unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case head := <-heads:
			c.index.EvictThrough(head.Header.Number.Uint64())
		case <-sub.Err():
			return
		}
	}
}

// resumeRequest picks the stream position: warm (the session's running head),
// cold (a block anchor at the canonical head), or from the earliest retained
// entry. attempt counts NOT_FOUND fallbacks within one session start.
func (c *Consumer) resumeRequest(sess *session, attempt int) *pb.StreamRequest {
	if sess != nil && sess.seeded && attempt == 0 {
		return &pb.StreamRequest{After: &pb.StreamRequest_Head{Head: sess.head.Bytes()}}
	}

	if attempt <= 1 {
		return &pb.StreamRequest{After: &pb.StreamRequest_Block{Block: c.chain.CurrentBlock().Number.Uint64()}}
	}

	return &pb.StreamRequest{}
}

// follow runs one streaming session. It returns the session for a warm
// resume when the stream position is still valid, or nil when position was
// lost (commitment gap, malformed entry) and the next attempt must re-anchor.
func (c *Consumer) follow(ctx context.Context, sess *session) (*session, error) {
	conn, err := grpc.NewClient(c.endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return sess, fmt.Errorf("dial sequence store: %w", err)
	}

	defer func() {
		if cerr := conn.Close(); cerr != nil {
			log.Warn("Sequence store connection close", "err", cerr)
		}
	}()

	client := pb.NewConsumerServiceClient(conn)

	for attempt := 0; ; attempt++ {
		stream, serr := client.Stream(ctx, c.resumeRequest(sess, attempt))
		if serr != nil {
			return sess, fmt.Errorf("open stream: %w", serr)
		}

		if attempt > 0 || sess == nil {
			// Any non-warm position invalidates the old fold state.
			sess = &session{consumer: c}
			c.index.Reset()
		}

		sess, err = c.consume(stream, sess)
		if status.Code(err) == codes.NotFound && attempt < 2 {
			continue
		}

		return sess, err
	}
}

func (c *Consumer) consume(stream pb.ConsumerService_StreamClient, sess *session) (*session, error) {
	for {
		frame, err := stream.Recv()
		if err != nil {
			return sess, fmt.Errorf("stream recv: %w", err)
		}

		entry := frame.GetEntry()
		if entry == nil {
			log.Info("Sequence stream live", "head", fmt.Sprintf("%x", sess.head[:8]))

			continue
		}

		if err := sess.handle(entry); err != nil {
			// Position lost: the caller must re-anchor, not resume.
			return nil, err
		}
	}
}

// session is one consistent stretch of the stream: a running commitment
// head, the speculative execution state, and the speculative seal hashes for
// BLOCKHASH resolution. env == nil between blocks and while skipping.
type session struct {
	consumer *Consumer
	head     commitment.Head
	seeded   bool
	env      *blockEnv      // block currently being applied
	parked   *state.StateDB // post-state of the last speculative seal
	tip      common.Hash    // last speculatively sealed hash
	sealed   map[uint64]common.Hash
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

	s.apply(entry)
	s.head = next

	return nil
}

// fold computes the entry's post-fold head over the current running head and
// returns the entry's claimed prefix alongside it.
func (s *session) fold(entry *pb.Entry) (commitment.Head, commitment.Head, error) {
	switch kind := entry.GetKind().(type) {
	case *pb.Entry_BlockOpen:
		open := kind.BlockOpen
		if len(open.GetPrefixCommitment()) != 32 || len(open.GetParentHash()) != 32 {
			return commitment.Head{}, commitment.Head{}, errors.New("malformed open entry")
		}

		next, err := commitment.FoldOpen(s.head, openContext(open))
		if err != nil {
			return commitment.Head{}, commitment.Head{}, err
		}

		return commitment.Head(open.GetPrefixCommitment()), next, nil
	case *pb.Entry_Record:
		record := kind.Record
		if len(record.GetPrefixCommitment()) != 32 {
			return commitment.Head{}, commitment.Head{}, errors.New("malformed record entry")
		}

		return commitment.Head(record.GetPrefixCommitment()),
			commitment.FoldTxs(s.head, record.GetTransactions()), nil
	case *pb.Entry_BlockSeal:
		seal := kind.BlockSeal
		if len(seal.GetPrefixCommitment()) != 32 {
			return commitment.Head{}, commitment.Head{}, errors.New("malformed seal entry")
		}

		return commitment.Head(seal.GetPrefixCommitment()),
			commitment.FoldSeal(s.head, commitment.SealedHash(seal.GetHeader())), nil
	default:
		return commitment.Head{}, commitment.Head{}, errors.New("unknown entry kind")
	}
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
	s.consumer.index.ClearFrom(from)
	s.env = nil
	s.parked = nil
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

	if header := s.consumer.chain.GetHeaderByHash(parent); header != nil &&
		s.consumer.chain.GetCanonicalHash(header.Number.Uint64()) == parent {
		if number != header.Number.Uint64()+1 {
			s.skip(number, "open height is not parent height+1", "number", number, "parent", header.Number)

			return
		}

		statedb, err := s.consumer.chain.StateAt(header.Root)
		if err != nil {
			s.skip(number, "parent state unavailable", "parent", parent, "err", err)

			return
		}

		s.consumer.index.ClearFrom(number)
		s.pruneSealed(number)

		s.tip = parent
		s.parked = nil
		s.env = newBlockEnv(s.consumer.chain, s.consumer.config, statedb, open, s.sealed)

		return
	}

	if parent == s.tip && s.parked != nil {
		s.env = newBlockEnv(s.consumer.chain, s.consumer.config, s.parked, open, s.sealed)
		s.parked = nil

		return
	}

	s.skip(number, "open parent neither canonical nor speculative tip", "parent", parent)
}

func (s *session) applyRecord(record *pb.Record) {
	if s.env == nil {
		return // skipping until the next re-anchoring open
	}

	for _, raw := range record.GetTransactions() {
		start := time.Now()

		tx, receipt, err := s.env.applyRaw(raw)
		if err != nil {
			s.skip(s.env.header.Number.Uint64(), "re-execution diverged", "err", err)

			return
		}

		s.consumer.index.Add(tx, receipt)
		preconfApplyTimer.UpdateSince(start)
	}
}

func (s *session) applySeal(seal *pb.BlockSeal) {
	if s.env == nil {
		return // skipping until the next re-anchoring open
	}

	sealed, err := decodeSealedHeader(seal.GetHeader())
	if err != nil {
		s.skip(s.env.header.Number.Uint64(), "undecodable sealed header", "err", err)

		return
	}

	if err := s.env.checkSeal(sealed); err != nil {
		s.skip(s.env.header.Number.Uint64(), "seal cross-check failed", "err", err)

		return
	}

	sealedHash := common.Hash(commitment.SealedHash(seal.GetHeader()))
	number := s.env.header.Number.Uint64()

	s.consumer.index.Seal(number, sealedHash)

	if s.sealed == nil {
		s.sealed = map[uint64]common.Hash{}
	}

	s.sealed[number] = sealedHash

	// BLOCKHASH reaches at most 256 back; older speculative hashes would
	// otherwise accumulate for the whole parked stretch.
	for height := range s.sealed {
		if height+256 < number {
			delete(s.sealed, height)
		}
	}

	// Park the post-block state as the base for the next open.
	s.tip = sealedHash
	s.parked = s.env.statedb
	s.env = nil
}

// pruneSealed drops speculative seal hashes that a canonical re-anchor
// superseded (at or above the new height) or that BLOCKHASH can no longer
// reach (more than 256 below it).
func (s *session) pruneSealed(number uint64) {
	for height := range s.sealed {
		if height >= number || height+256 < number {
			delete(s.sealed, height)
		}
	}
}

func openContext(open *pb.BlockOpen) commitment.OpenContext {
	return commitment.OpenContext{
		Number:     open.GetBlockNumber(),
		Timestamp:  open.GetBlockTimestamp(),
		ParentHash: [32]byte(open.GetParentHash()),
		GasLimit:   open.GetGasLimit(),
		BaseFee:    new(big.Int).SetBytes(open.GetBaseFee()),
	}
}
