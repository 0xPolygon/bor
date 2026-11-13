package miner

import (
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
)

const (
	buildToAnnounceSlowThreshold     = 2 * time.Second
	buildToAnnounceVerySlowThreshold = 4 * time.Second

	// producerStallThreshold is how long the producer of the next block may
	// go without announcing it before it reports itself stalled.
	producerStallThreshold = 2 * time.Second
)

var (
	// Exact counts of slow announcements. Unlike the build_to_announce timer's
	// decaying percentiles, these do not depend on the scrape interval.
	buildToAnnounceOver2sCounter = metrics.NewRegisteredCounter("worker/build_to_announce/over2s", nil)
	buildToAnnounceOver4sCounter = metrics.NewRegisteredCounter("worker/build_to_announce/over4s", nil)

	// producerStalledGauge holds the block number this node is stalled on as
	// its producer, or 0 when it is not stalled.
	producerStalledGauge = metrics.NewRegisteredGauge("worker/producer/stalled", nil)
	// producerStallsCounter counts stall episodes, so a stall that starts and
	// ends between two scrapes is still visible.
	producerStallsCounter = metrics.NewRegisteredCounter("worker/producer/stalls", nil)

	buildToAnnounceMetrics = buildToAnnounceRecorder{
		timer:  workerBuildToAnnounceTimer,
		over2s: buildToAnnounceOver2sCounter,
		over4s: buildToAnnounceOver4sCounter,
	}
)

type buildToAnnounceRecorder struct {
	timer  *metrics.Timer
	over2s *metrics.Counter
	over4s *metrics.Counter
}

func (r buildToAnnounceRecorder) observe(d time.Duration) {
	r.timer.Update(d)

	if d > buildToAnnounceSlowThreshold {
		r.over2s.Inc(1)
	}

	if d > buildToAnnounceVerySlowThreshold {
		r.over4s.Inc(1)
	}
}

// recordAnnouncement reports the build-to-announce duration of a block that is
// being announced and ends its stall watch. A zero start skips the duration
// metrics.
func (w *worker) recordAnnouncement(block *types.Block, start time.Time) {
	if !start.IsZero() {
		buildToAnnounceMetrics.observe(time.Since(start))
	}

	w.stallWatch.announced(block.NumberU64())
}

// buildToAnnounceStart returns when the producer was first allowed to build
// header: the later of buildStart and the parent slot boundary. Since
// Giugliano, Prepare deliberately holds the in-turn producer until that
// boundary, and the wait is not build time.
func (w *worker) buildToAnnounceStart(header *types.Header, buildStart time.Time) time.Time {
	if buildStart.IsZero() {
		return buildStart
	}

	if boundary := w.parentSlotBoundary(header); boundary.After(buildStart) {
		return boundary
	}

	return buildStart
}

// parentSlotBoundary returns the parent slot boundary for header on
// Giugliano+ Bor chains, or the zero time when there is none. Before
// Giugliano, Prepare does not wait (Seal waits for the block's own time
// instead), so there is no boundary to exclude.
func (w *worker) parentSlotBoundary(header *types.Header) time.Time {
	if header == nil || header.Number == nil || w.chainConfig == nil || w.chainConfig.Bor == nil ||
		!w.chainConfig.Bor.IsGiugliano(header.Number) {
		return time.Time{}
	}

	borEngine, ok := w.engine.(*bor.Bor)
	if !ok {
		return time.Time{}
	}

	return borEngine.EarliestAnnounceTime(w.chain, header)
}

// watchNextBlock starts the stall clock for the child of head. It runs on
// every new head; the producer check only runs if the clock expires.
func (w *worker) watchNextBlock(head *types.Header) {
	if head == nil || head.Number == nil || !w.IsRunning() {
		return
	}

	next := &types.Header{
		ParentHash: head.Hash(),
		Number:     new(big.Int).Add(head.Number, common.Big1),
	}
	w.stallWatch.watch(next, w.buildToAnnounceStart(next, time.Now()))
}

// isCurrentProducer reports whether this node is the producer of next. After
// Rio each span has a single producer, so the authorized signer is the
// producer. Before Rio every validator is authorized, so no node is singled
// out. The check reads the snapshot, which may need a span lookup, so it runs
// from the stall timer rather than the block production path.
func (w *worker) isCurrentProducer(next *types.Header) bool {
	if !w.IsRunning() || w.syncing.Load() || next == nil || next.Number == nil ||
		w.chainConfig == nil || w.chainConfig.Bor == nil || !w.chainConfig.Bor.IsRio(next.Number) {
		return false
	}

	borEngine, ok := w.engine.(*bor.Bor)
	if !ok {
		return false
	}

	return borEngine.IsAuthorizedSigner(w.chain, next)
}

// producerStallWatch reports when this node, as the producer of the next
// block, has not announced it within threshold of being allowed to build it.
//
// A stall clock runs only between a new head and the announcement of its
// child. The clock is a time.AfterFunc, so no goroutine exists until it
// expires; clear and close cancel it, and a callback that is already running
// returns without reporting once its watch has been replaced or closed.
type producerStallWatch struct {
	threshold  time.Duration
	isProducer func(next *types.Header) bool
	headNumber func() uint64
	stalled    *metrics.Gauge
	stalls     *metrics.Counter

	mu        sync.Mutex
	height    uint64 // block being watched; 0 when idle
	timer     *time.Timer
	isStalled bool
	closed    bool
}

func newProducerStallWatch(threshold time.Duration, isProducer func(*types.Header) bool, headNumber func() uint64,
	stalled *metrics.Gauge, stalls *metrics.Counter) *producerStallWatch {
	return &producerStallWatch{
		threshold:  threshold,
		isProducer: isProducer,
		headNumber: headNumber,
		stalled:    stalled,
		stalls:     stalls,
	}
}

// watch replaces any earlier watch with one for next, measured from start.
func (s *producerStallWatch) watch(next *types.Header, start time.Time) {
	if s == nil || next == nil || next.Number == nil {
		return
	}

	height := next.Number.Uint64()

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}

	s.resetLocked()
	s.height = height
	s.timer = time.AfterFunc(time.Until(start.Add(s.threshold)), func() {
		s.expire(height, next)
	})
}

// announced ends the watch once number, or a later block, is announced. An
// announcement can race with the head event for the same block, so an older
// number must not clear the watch for its successor.
func (s *producerStallWatch) announced(number uint64) {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.height == 0 || number < s.height {
		return
	}

	s.resetLocked()
}

// clear cancels the current watch and clears the stalled gauge. Later heads
// start new watches.
func (s *producerStallWatch) clear() {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.resetLocked()
}

// close cancels the current watch and ignores any later ones.
func (s *producerStallWatch) close() {
	if s == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.resetLocked()
	s.closed = true
}

func (s *producerStallWatch) expire(height uint64, next *types.Header) {
	if !s.pending(height) {
		return
	}

	// Called without the lock: the producer check can block on a span lookup.
	if s.headNumber() >= height || !s.isProducer(next) {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed || s.height != height || s.isStalled {
		return
	}

	s.isStalled = true
	s.stalled.Update(int64(height))
	s.stalls.Inc(1)
}

func (s *producerStallWatch) pending(height uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return !s.closed && s.height == height && !s.isStalled
}

func (s *producerStallWatch) resetLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}

	if s.isStalled {
		s.stalled.Update(0)
	}

	s.height = 0
	s.isStalled = false
}
