// Package sequencer connects Bor to the sequence store: the block producer
// publishes each block's lifecycle (open, transactions, seal) as it happens,
// and RPC nodes re-execute the stream to serve preconfirmation receipts ahead
// of block announcement. The wire contract and commitment chain live in
// github.com/0xPolygon/sequence-store-proto.
package sequencer

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/0xPolygon/sequence-store-proto/commitment"
	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rlp"
)

const (
	publishQueueSize    = 4096
	publishRetryBackoff = time.Second
)

var (
	publishAckTimer    = metrics.NewRegisteredTimer("sequencer/publish/ack", nil)
	publishedCounter   = metrics.NewRegisteredCounter("sequencer/publish/entries", nil)
	publishDropMeter   = metrics.NewRegisteredMeter("sequencer/publish/dropped", nil)
	publishFailedGauge = metrics.NewRegisteredGauge("sequencer/publish/failed", nil)
)

// Publisher streams block-production entries to the sequence store. Enqueue
// methods are called from the worker's goroutines and never block: folds are
// computed inline (sub-microsecond), transport happens on a background
// goroutine. Transport errors and RATE_LIMITED acks are retried by reopening
// the stream and resending unacknowledged entries in order (the store's head
// check makes resends exact: nothing rejected ever advanced it). Protocol
// rejections (STALE_COMMITMENT, MALFORMED) and
// queue overflow permanently disable publishing — fail-safe: preconfs stop,
// block production is never affected — because recovering from them requires
// the consumer-API tail resync this version does not implement.
type Publisher struct {
	// mu makes fold-and-enqueue atomic: opens/txs arrive from the worker's
	// main loop while seals arrive from its result loop, and the fold order
	// must match the queue order exactly.
	mu     sync.Mutex
	head   commitment.Head
	queue  chan *queued
	failed atomic.Bool
	cancel context.CancelFunc
	done   chan struct{}

	// refresh is the producer's mempool re-snapshot cadence while a block is
	// open (continuous building); zero keeps the one-shot fill.
	refresh time.Duration
}

type queued struct {
	entry *pb.Entry
	at    time.Time
}

// NewPublisher dials the store and starts the transport goroutine. The
// producer starts from the genesis seed: this version assumes a store that is
// empty at startup (the e2e/devnet case).
func NewPublisher(endpoint string, chainID uint64, refresh time.Duration) (*Publisher, error) {
	conn, err := grpc.NewClient(endpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	p := &Publisher{
		head:    commitment.Seed(chainID),
		queue:   make(chan *queued, publishQueueSize),
		cancel:  cancel,
		done:    make(chan struct{}),
		refresh: refresh,
	}

	go p.run(ctx, conn)

	return p, nil
}

// OpenBlock publishes block context the moment the producer opens it. A
// re-open of the same height (work-cycle restart) or an open on a different
// parent (reorg mid-build) is a re-anchor; the store and consumers handle
// both, so the caller just reports what it is building.
func (p *Publisher) OpenBlock(number uint64, timestamp uint64, parent common.Hash, gasLimit uint64, baseFee *big.Int) {
	if p.failed.Load() {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	open := commitment.OpenContext{
		Number:     number,
		Timestamp:  timestamp,
		ParentHash: parent,
		GasLimit:   gasLimit,
		BaseFee:    baseFee,
	}

	next, err := commitment.FoldOpen(p.head, open)
	if err != nil {
		p.fail("fold open context", "err", err)

		return
	}

	fee := []byte{}
	if baseFee != nil && baseFee.Sign() > 0 {
		fee = baseFee.Bytes()
	}

	p.enqueue(&pb.Entry{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{
		BlockNumber:      number,
		BlockTimestamp:   timestamp,
		ParentHash:       parent.Bytes(),
		GasLimit:         gasLimit,
		BaseFee:          fee,
		PrefixCommitment: p.head.Bytes(),
	}}}, next)
}

// PublishTx publishes one committed transaction.
func (p *Publisher) PublishTx(tx *types.Transaction) {
	if p.failed.Load() {
		return
	}

	raw, err := tx.MarshalBinary()
	if err != nil {
		p.fail("encode transaction", "hash", tx.Hash(), "err", err)

		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	next := commitment.FoldTx(p.head, raw)

	p.enqueue(&pb.Entry{Kind: &pb.Entry_Record{Record: &pb.Record{
		Transactions:     [][]byte{raw},
		PrefixCommitment: p.head.Bytes(),
	}}}, next)
}

// SealBlock publishes the sealed header, closing the block.
func (p *Publisher) SealBlock(header *types.Header) {
	if p.failed.Load() {
		return
	}

	raw, err := rlp.EncodeToBytes(header)
	if err != nil {
		p.fail("encode sealed header", "number", header.Number, "err", err)

		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	next := commitment.FoldSeal(p.head, commitment.SealedHash(raw))

	p.enqueue(&pb.Entry{Kind: &pb.Entry_BlockSeal{BlockSeal: &pb.BlockSeal{
		Header:           raw,
		PrefixCommitment: p.head.Bytes(),
	}}}, next)
}

// RefreshInterval is the mempool re-snapshot cadence the worker should use
// while a block is open; zero disables continuous building.
func (p *Publisher) RefreshInterval() time.Duration {
	return p.refresh
}

// Close stops the transport goroutine and waits for it to exit.
func (p *Publisher) Close() {
	p.cancel()
	<-p.done
}

func (p *Publisher) enqueue(entry *pb.Entry, next commitment.Head) {
	select {
	case p.queue <- &queued{entry: entry, at: time.Now()}:
		p.head = next
	default:
		// A full queue means the store cannot keep up; blocking the mining
		// goroutine is never acceptable, so publishing shuts down instead.
		p.fail("publish queue overflow")
		publishDropMeter.Mark(1)
	}
}

func (p *Publisher) fail(msg string, args ...any) {
	if p.failed.CompareAndSwap(false, true) {
		publishFailedGauge.Update(1)
		log.Error("Sequencer publishing disabled: "+msg, args...)
	}
}

// run owns transport: one Publish stream at a time, reopened with an
// in-order resend of unacknowledged entries on retryable failures.
func (p *Publisher) run(ctx context.Context, conn *grpc.ClientConn) {
	defer close(p.done)
	defer func() {
		if err := conn.Close(); err != nil {
			log.Warn("Sequencer connection close", "err", err)
		}
	}()

	client := pb.NewPublisherServiceClient(conn)

	var unacked []*queued

	for {
		retryable, err := p.pump(ctx, client, &unacked)
		if ctx.Err() != nil {
			return
		}

		if !retryable {
			p.fail("publishing terminated", "err", err)

			return
		}

		log.Warn("Sequencer stream interrupted, retrying", "err", err, "unacked", len(unacked))

		select {
		case <-ctx.Done():
			return
		case <-time.After(publishRetryBackoff):
		}
	}
}

type ackResult struct {
	status pb.AckStatus
	err    error
}

// pump runs one stream session: resends unacked entries, forwards the queue,
// and retires entries as acks arrive (the store acks in entry order, so the
// oldest unacked entry owns each ack). Returns whether the failure is
// retryable on a fresh stream.
//
// A resent entry may already have been applied — the previous stream can
// break after the store durably appended but before the ack arrived. Its
// resend then fails the head check, so within the resent window a
// STALE_COMMITMENT means "already applied, ack lost" and retires the entry;
// this is sound under the store's single-writer fence, and a genuinely
// stolen head still surfaces as STALE on the first post-resend entry, which
// stays fatal.
func (p *Publisher) pump(ctx context.Context, client pb.PublisherServiceClient, unacked *[]*queued) (bool, error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := client.Publish(sctx)
	if err != nil {
		return true, err
	}

	acks := make(chan ackResult)

	go func() {
		for {
			resp, rerr := stream.Recv()

			result := ackResult{err: rerr}
			if rerr == nil {
				result.status = resp.GetStatus()
			}

			select {
			case acks <- result:
			case <-sctx.Done():
				return
			}

			if rerr != nil {
				return
			}
		}
	}()

	resent := len(*unacked)

	for _, q := range *unacked {
		if serr := stream.Send(&pb.PublishRequest{Entry: q.entry}); serr != nil {
			return true, serr
		}
	}

	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case q := <-p.queue:
			if len(*unacked) >= 2*publishQueueSize {
				return false, errors.New("too many unacknowledged entries")
			}

			*unacked = append(*unacked, q)

			if serr := stream.Send(&pb.PublishRequest{Entry: q.entry}); serr != nil {
				return true, serr
			}

			publishedCounter.Inc(1)
		case ack := <-acks:
			if ack.err != nil {
				return true, ack.err
			}

			if len(*unacked) == 0 {
				return false, errors.New("ack without a pending entry")
			}

			inResentWindow := resent > 0
			if inResentWindow {
				resent--
			}

			switch {
			case ack.status == pb.AckStatus_ACK_STATUS_OK:
				publishAckTimer.UpdateSince((*unacked)[0].at)
				*unacked = (*unacked)[1:]
			case ack.status == pb.AckStatus_ACK_STATUS_STALE_COMMITMENT && inResentWindow:
				// Already applied before the previous stream broke; the ack
				// was lost, not the entry.
				*unacked = (*unacked)[1:]
			case ack.status == pb.AckStatus_ACK_STATUS_RATE_LIMITED:
				// The rejected entry never advanced the store head, and
				// everything pipelined behind it failed the head check too —
				// a fresh stream resending unacked in order is exact.
				return true, errors.New("store throttled")
			default:
				return false, fmt.Errorf("store rejected entry: %v", ack.status)
			}
		}
	}
}
