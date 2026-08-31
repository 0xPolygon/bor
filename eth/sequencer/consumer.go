package sequencer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
)

const consumerRetryDelay = 2 * time.Second

// Consumer follows the sequence store stream on an RPC node, re-executes it
// from canonical or parked speculative state, and fills the preconf receipt
// Index exposed through the explicit Bor preconfirmation receipt endpoint.
//
// Position and application are handled separately, per the design's
// chain-everything-apply-selectively rule: only commitment gaps and transport
// errors abandon the stream position (warm resume by running head, falling
// back to a cold block anchor, falling back to the earliest retained entry);
// application problems — unknown parents, unavailable state, execution or
// seal divergence — void the speculative work and skip forward until an open
// record re-anchors on a canonical block.
type Consumer struct {
	chain      *core.BlockChain
	endpoint   string
	index      *Index
	publishMu  sync.Mutex
	storeMu    sync.Mutex
	store      *PendingStore
	logsFeed   event.Feed
	logsScope  event.SubscriptionScope
	logsMu     sync.Mutex
	logsQueue  [][]*types.Log
	logsBusy   bool
	logsClosed bool
	logsWG     sync.WaitGroup
	worker     atomic.Pointer[preconfWorker]
	reconciled atomic.Pointer[types.Header]

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewConsumer returns a stopped consumer. Determinism preconditions (Rio
// active, coinbase map present) are re-checked per session, not here — a
// node still syncing pre-Rio history becomes eligible once it catches up.
func NewConsumer(endpoint string, chain *core.BlockChain) (*Consumer, error) {
	if chain.Config().Bor == nil {
		return nil, errors.New("sequencer consumer requires a bor chain")
	}

	consumer := &Consumer{
		chain:    chain,
		endpoint: endpoint,
		index:    NewIndex(),
		store:    NewPendingStore(chain.DB()),
	}
	consumer.reconciled.Store(types.CopyHeader(chain.CurrentBlock()))
	return consumer, nil
}

// Index exposes the preconf receipts for the RPC layer.
func (c *Consumer) Index() *Index {
	return c.index
}

func (c *Consumer) SubscribePendingLogs(ch chan<- []*types.Log) event.Subscription {
	sub := c.logsFeed.Subscribe(ch)
	tracked := c.logsScope.Track(sub)
	if tracked != nil {
		return tracked
	}
	sub.Unsubscribe()
	return sub
}

func (c *Consumer) enqueuePendingLogs(logs []*types.Log) {
	if len(logs) == 0 {
		return
	}
	c.logsMu.Lock()
	if c.logsClosed {
		c.logsMu.Unlock()
		return
	}
	if len(c.logsQueue) == pendingLogsQueueLimit {
		c.logsQueue[0] = nil
		c.logsQueue = c.logsQueue[1:]
		preconfPendingLogsDropped.Inc(1)
	}
	c.logsQueue = append(c.logsQueue, logs)
	if c.logsBusy {
		c.logsMu.Unlock()
		return
	}
	c.logsBusy = true
	c.logsWG.Add(1)
	c.logsMu.Unlock()
	go c.dispatchPendingLogs()
}

func (c *Consumer) dispatchPendingLogs() {
	defer c.logsWG.Done()
	for {
		c.logsMu.Lock()
		if len(c.logsQueue) == 0 {
			c.logsBusy = false
			c.logsMu.Unlock()
			return
		}
		logs := c.logsQueue[0]
		c.logsQueue[0] = nil
		c.logsQueue = c.logsQueue[1:]
		c.logsMu.Unlock()
		c.logsFeed.Send(logs)
	}
}

// Start launches the stream-follow loop.
func (c *Consumer) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel

	c.wg.Add(2)
	go func() {
		defer c.wg.Done()
		c.run(ctx)
	}()
	go func() {
		defer c.wg.Done()
		c.evictLoop(ctx)
	}()
}

// Close stops the consumer and waits for its loops to exit.
func (c *Consumer) Close() {
	if c.cancel != nil {
		c.cancel()
		c.wg.Wait()
	}
	c.logsMu.Lock()
	c.logsClosed = true
	for index := range c.logsQueue {
		c.logsQueue[index] = nil
	}
	c.logsQueue = nil
	c.logsMu.Unlock()
	c.logsScope.Close()
	c.logsWG.Wait()
}

// deterministic reports whether the producer's execution context is
// reproducible at the current head: pre-Rio the EVM coinbase is the
// producer's own address, unknowable pre-seal; post-Rio it is
// CalculateCoinbase from the chain config — reproducible only when the
// coinbase map is set.
func (c *Consumer) deterministic() error {
	config := c.chain.Config().Bor

	head := c.chain.CurrentBlock().Number
	if !config.IsRio(head) {
		return errors.New("rio fork not active at current head")
	}

	if common.HexToAddress(config.CalculateCoinbase(head.Uint64())) == (common.Address{}) {
		return errors.New("chain config has no coinbase map")
	}

	return nil
}

func (c *Consumer) run(ctx context.Context) {
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
		c.invalidatePendingFromReason(0, "session_lost")

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
	c.handleCanonicalHead()

	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-heads:
			if !ok {
				return
			}
			c.handleCanonicalHead()
		case <-sub.Err():
			return
		}
	}
}

func (c *Consumer) handleCanonicalHead() {
	c.publishMu.Lock()
	head := c.chain.CurrentBlock()
	number := head.Number.Uint64()
	c.index.EvictThrough(number)
	logs, invalidations := c.pendingStore().reconcileThroughMemory(number, c.chain.GetBlockByNumber, c.chain.GetReceiptsByHash)
	var clearFrom *uint64
	for _, invalidation := range invalidations {
		if invalidation.number <= number || (clearFrom != nil && invalidation.number >= *clearFrom) {
			continue
		}
		height := invalidation.number
		clearFrom = &height
	}
	if clearFrom != nil {
		c.index.ClearFrom(*clearFrom)
	}
	c.reconciled.Store(types.CopyHeader(head))
	c.enqueuePendingLogs(logs)
	c.publishMu.Unlock()
	c.pendingStore().writeInvalidations(invalidations)
}

// resumeRequest picks the stream position, never asking the same anchor
// twice: a warm session walks head → block anchor → earliest retained
// entry, and a cold one — no head to resume — goes straight from the
// block anchor to the earliest entry. attempt counts NOT_FOUND fallbacks
// within one session start; a failed attempt resets the session, so
// seededness names the rung that just failed.
func (c *Consumer) resumeRequest(sess *session, attempt int) *pb.StreamRequest {
	warm := sess != nil && sess.seeded
	if warm && attempt == 0 {
		return &pb.StreamRequest{After: &pb.StreamRequest_Head{Head: sess.head.Bytes()}}
	}

	if attempt == 0 || (warm && attempt == 1) {
		return &pb.StreamRequest{After: &pb.StreamRequest_Block{Block: c.chain.CurrentBlock().Number.Uint64()}}
	}

	return &pb.StreamRequest{}
}

// follow runs one streaming session. It returns the session for a warm
// resume when the stream position is still valid, or nil when position was
// lost (commitment gap, malformed entry) and the next attempt must re-anchor.
func (c *Consumer) follow(ctx context.Context, sess *session) (*session, error) {
	// The endpoint is an operator-controlled internal service. Transport security
	// is provided by that network boundary; Bor still authenticates sealed headers.
	conn, err := grpc.NewClient(c.endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(pendingInputLimit+1024*1024)))
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
			sess = newSession(c)
			c.invalidatePendingFromReason(0, "session_lost")
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
