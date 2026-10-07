package miner

import (
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/clique"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
)

func TestBuildToAnnounceRecorderThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		duration   time.Duration
		wantOver2s int64
		wantOver4s int64
	}{
		{name: "fast", duration: 170 * time.Millisecond},
		{name: "just under 2s", duration: 2*time.Second - time.Millisecond},
		{name: "exactly 2s", duration: 2 * time.Second},
		{name: "just over 2s", duration: 2*time.Second + time.Millisecond, wantOver2s: 1},
		{name: "exactly 4s", duration: 4 * time.Second, wantOver2s: 1},
		{name: "just over 4s", duration: 4*time.Second + time.Millisecond, wantOver2s: 1, wantOver4s: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := buildToAnnounceRecorder{timer: metrics.NewTimer(), over2s: metrics.NewCounter(), over4s: metrics.NewCounter()}
			r.observe(tt.duration)

			require.Equal(t, tt.wantOver2s, r.over2s.Snapshot().Count())
			require.Equal(t, tt.wantOver4s, r.over4s.Snapshot().Count())
		})
	}
}

func TestBuildToAnnounceStart(t *testing.T) {
	t.Parallel()

	t.Run("zero build start stays zero", func(t *testing.T) {
		t.Parallel()
		require.True(t, (&worker{}).buildToAnnounceStart(&types.Header{Number: big.NewInt(1)}, time.Time{}).IsZero())
	})

	t.Run("no bor chain config uses build start", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		require.Equal(t, start, (&worker{}).buildToAnnounceStart(&types.Header{Number: big.NewInt(1)}, start))
	})

	t.Run("non-Bor engine has no slot boundary", func(t *testing.T) {
		t.Parallel()
		w := &worker{chainConfig: borUnittestChainConfigWithGiugliano()}
		require.True(t, w.parentSlotBoundary(&types.Header{Number: big.NewInt(1)}).IsZero())
	})

	t.Run("pre-Giugliano uses build start", func(t *testing.T) {
		t.Parallel()
		w, next, cleanup := newSlotBoundaryTestWorker(t, params.BorUnittestChainConfig)
		defer cleanup()

		require.True(t, w.parentSlotBoundary(next).IsZero())
		start := time.Now()
		require.Equal(t, start, w.buildToAnnounceStart(next, start))
	})

	t.Run("Giugliano excludes the wait for the parent slot boundary", func(t *testing.T) {
		t.Parallel()
		w, next, cleanup := newSlotBoundaryTestWorker(t, borUnittestChainConfigWithGiugliano())
		defer cleanup()

		boundary := w.engine.(*bor.Bor).EarliestAnnounceTime(w.chain, next)
		require.Equal(t, boundary, w.parentSlotBoundary(next))

		// The head arrived before the boundary: the wait in Prepare is not build time.
		require.Equal(t, boundary, w.buildToAnnounceStart(next, boundary.Add(-time.Second)))
		// The head arrived after the boundary: building could start at once.
		late := boundary.Add(time.Second)
		require.Equal(t, late, w.buildToAnnounceStart(next, late))
		// No build start means no measurement, even with a boundary.
		require.True(t, w.buildToAnnounceStart(next, time.Time{}).IsZero())
	})
}

func TestBuildStartFrom(t *testing.T) {
	t.Parallel()

	require.True(t, buildStartFrom(nil).IsZero())
	start := time.Now()
	require.Equal(t, start, buildStartFrom(&generateParams{buildStart: start}))
}

// newSlotBoundaryTestWorker returns an idle Bor worker and a header for the
// block after its genesis.
func newSlotBoundaryTestWorker(t *testing.T, chainConfig *params.ChainConfig) (*worker, *types.Header, func()) {
	t.Helper()

	engine, ctrl := getFakeBorFromConfig(t, chainConfig)
	w, backend, closeWorker := newTestWorker(t, DefaultTestConfig(), chainConfig, engine, rawdb.NewMemoryDatabase(), false, 0)

	parent := backend.chain.CurrentHeader()
	next := &types.Header{ParentHash: parent.Hash(), Number: new(big.Int).Add(parent.Number, common.Big1)}

	return w, next, func() {
		closeWorker()
		engine.Close()
		ctrl.Finish()
	}
}

// stallWatchHarness wires a producerStallWatch to controllable inputs and its
// own metrics.
type stallWatchHarness struct {
	watch        *producerStallWatch
	stalled      *metrics.Gauge
	stalls       *metrics.Counter
	primary      atomic.Bool
	head         atomic.Uint64
	primaryCalls atomic.Int64
}

func newStallWatchHarness(threshold time.Duration) *stallWatchHarness {
	h := &stallWatchHarness{stalled: metrics.NewGauge(), stalls: metrics.NewCounter()}
	h.primary.Store(true)
	h.watch = newProducerStallWatch(threshold,
		func(*types.Header) bool {
			h.primaryCalls.Add(1)
			return h.primary.Load()
		},
		h.head.Load, h.stalled, h.stalls)

	return h
}

func stallTestHeader(number uint64) *types.Header {
	return &types.Header{Number: new(big.Int).SetUint64(number)}
}

func (h *stallWatchHarness) requireStalledAt(t *testing.T, height int64, episodes int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		return h.stalled.Snapshot().Value() == height && h.stalls.Snapshot().Count() == episodes
	}, time.Second, 5*time.Millisecond)
}

func (h *stallWatchHarness) requireNotStalledFor(t *testing.T, d time.Duration) {
	t.Helper()
	require.Never(t, func() bool {
		return h.stalled.Snapshot().Value() != 0 || h.stalls.Snapshot().Count() != 0
	}, d, 5*time.Millisecond)
}

func TestProducerStallWatch(t *testing.T) {
	t.Parallel()

	const threshold = 50 * time.Millisecond

	t.Run("reports the stalled height once threshold passes", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(threshold)
		h.head.Store(9)

		h.watch.watch(stallTestHeader(10), time.Now())
		require.Zero(t, h.stalled.Snapshot().Value())
		h.requireStalledAt(t, 10, 1)
	})

	t.Run("does not fire before threshold", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(300 * time.Millisecond)

		// 250ms of the 300ms threshold have already elapsed.
		h.watch.watch(stallTestHeader(10), time.Now().Add(-250*time.Millisecond))
		require.Zero(t, h.stalls.Snapshot().Count())
		h.requireStalledAt(t, 10, 1)
	})

	t.Run("fires at once when the deadline has passed", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(time.Hour)

		h.watch.watch(stallTestHeader(10), time.Now().Add(-2*time.Hour))
		h.requireStalledAt(t, 10, 1)
	})

	t.Run("ignores nodes that are not the primary producer", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(threshold)
		h.primary.Store(false)

		h.watch.watch(stallTestHeader(10), time.Now())
		h.requireNotStalledFor(t, 4*threshold)
		require.Equal(t, int64(1), h.primaryCalls.Load())
	})

	t.Run("ignores a block another producer already delivered", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(threshold)
		h.head.Store(10)

		h.watch.watch(stallTestHeader(10), time.Now())
		h.requireNotStalledFor(t, 4*threshold)
	})

	t.Run("announcement before threshold skips the producer check", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(threshold)

		h.watch.watch(stallTestHeader(10), time.Now())
		h.watch.announced(10)
		h.requireNotStalledFor(t, 4*threshold)
		require.Zero(t, h.primaryCalls.Load())
	})

	t.Run("announcement clears a reported stall", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(threshold)

		h.watch.watch(stallTestHeader(10), time.Now())
		h.requireStalledAt(t, 10, 1)

		h.watch.announced(10)
		require.Zero(t, h.stalled.Snapshot().Value())
		require.Equal(t, int64(1), h.stalls.Snapshot().Count())
	})

	t.Run("new head clears a reported stall and starts a new clock", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(threshold)

		h.watch.watch(stallTestHeader(10), time.Now())
		h.requireStalledAt(t, 10, 1)

		h.head.Store(10)
		h.watch.watch(stallTestHeader(11), time.Now())
		require.Zero(t, h.stalled.Snapshot().Value())
		h.requireStalledAt(t, 11, 2)
	})

	t.Run("older announcement keeps the newer watch", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(threshold)
		h.head.Store(10)

		h.watch.watch(stallTestHeader(11), time.Now())
		h.watch.announced(10)
		h.requireStalledAt(t, 11, 1)
	})

	t.Run("clear cancels the watch", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(threshold)

		h.watch.watch(stallTestHeader(10), time.Now())
		h.requireStalledAt(t, 10, 1)

		h.watch.clear()
		require.Zero(t, h.stalled.Snapshot().Value())

		h.watch.watch(stallTestHeader(11), time.Now())
		h.requireStalledAt(t, 11, 2)
	})

	t.Run("close clears a reported stall and ignores later watches", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(threshold)

		h.watch.watch(stallTestHeader(10), time.Now())
		h.requireStalledAt(t, 10, 1)

		h.watch.close()
		require.Zero(t, h.stalled.Snapshot().Value())

		h.watch.watch(stallTestHeader(11), time.Now())
		require.Zero(t, watchedHeight(h.watch))
		require.Never(t, func() bool { return h.stalls.Snapshot().Count() != 1 }, 4*threshold, 5*time.Millisecond)
	})

	t.Run("replacing a watch stops its timer", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(time.Hour)

		h.watch.watch(stallTestHeader(10), time.Now())
		h.watch.mu.Lock()
		timer := h.watch.timer
		h.watch.mu.Unlock()

		h.watch.announced(10)
		require.False(t, timer.Stop(), "the replaced timer must already be stopped")
	})

	t.Run("a late expiry of a replaced watch is ignored", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(time.Hour)

		h.watch.watch(stallTestHeader(10), time.Now())
		h.watch.announced(10)
		h.watch.expire(10, stallTestHeader(10))

		require.Zero(t, h.primaryCalls.Load())
		require.Zero(t, h.stalls.Snapshot().Count())
	})

	t.Run("announcement during the producer check is not reported", func(t *testing.T) {
		t.Parallel()
		h := newStallWatchHarness(time.Hour)
		h.watch.isPrimary = func(*types.Header) bool {
			h.watch.announced(10)
			return true
		}

		h.watch.watch(stallTestHeader(10), time.Now())
		h.watch.expire(10, stallTestHeader(10))

		require.Zero(t, h.stalled.Snapshot().Value())
		require.Zero(t, h.stalls.Snapshot().Count())
	})

	t.Run("nil watch is a no-op", func(t *testing.T) {
		t.Parallel()
		var s *producerStallWatch
		require.NotPanics(t, func() {
			s.watch(stallTestHeader(1), time.Now())
			s.announced(1)
			s.clear()
			s.close()
		})
	})
}

func TestWatchNextBlock(t *testing.T) {
	t.Parallel()

	head := &types.Header{Number: big.NewInt(41)}
	h := newStallWatchHarness(time.Hour)
	w := &worker{stallWatch: h.watch}

	w.watchNextBlock(head)
	require.Zero(t, watchedHeight(h.watch), "an idle miner must not watch")

	w.running.Store(true)
	w.watchNextBlock(head)
	require.Equal(t, uint64(42), watchedHeight(h.watch))

	w.recordAnnouncement(types.NewBlockWithHeader(&types.Header{Number: big.NewInt(42)}), time.Time{})
	require.Zero(t, watchedHeight(h.watch))
}

func watchedHeight(s *producerStallWatch) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.height
}

func TestIsStalledPrimaryProducer(t *testing.T) {
	t.Parallel()

	w, next, cleanup := newSlotBoundaryTestWorker(t, params.BorUnittestChainConfig)
	defer cleanup()

	require.False(t, w.isStalledPrimaryProducer(next), "an idle miner is never the stalled producer")

	w.running.Store(true)
	require.True(t, w.isStalledPrimaryProducer(next), "the sole validator is the primary producer")

	w.syncing.Store(true)
	require.False(t, w.isStalledPrimaryProducer(next), "a syncing node is never the stalled producer")
	w.syncing.Store(false)

	nonBor := &worker{}
	nonBor.running.Store(true)
	require.False(t, nonBor.isStalledPrimaryProducer(next), "non-Bor engines have no primary producer")
}

func TestAnnounceTaskBlockRecordsAnnouncement(t *testing.T) {
	t.Parallel()

	mux := new(event.TypeMux)
	sub := mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	h := newStallWatchHarness(time.Hour)
	w := &worker{mux: mux, stallWatch: h.watch}
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(42)})
	h.watch.watch(block.Header(), time.Now())

	over4sBefore := buildToAnnounceOver4sCounter.Snapshot().Count()
	go w.announceTaskBlock(&task{announceStart: time.Now().Add(-5 * time.Second)}, block, nil)

	select {
	case ev := <-sub.Chan():
		require.Same(t, block, ev.Data.(core.NewMinedBlockEvent).Block)
	case <-time.After(time.Second):
		t.Fatal("mined block event was not announced")
	}

	require.Zero(t, watchedHeight(h.watch))
	require.Greater(t, buildToAnnounceOver4sCounter.Snapshot().Count(), over4sBefore)
}

func TestAnnounceInlineSealedBlockEndsStallWatch(t *testing.T) {
	t.Parallel()

	mux := new(event.TypeMux)
	sub := mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	h := newStallWatchHarness(time.Hour)
	w := &worker{mux: mux, stallWatch: h.watch}
	block := types.NewBlockWithHeader(&types.Header{Number: big.NewInt(7)})
	h.watch.watch(block.Header(), time.Now())

	go w.announceInlineSealedBlock(block, time.Now())
	select {
	case <-sub.Chan():
	case <-time.After(time.Second):
		t.Fatal("mined block event was not announced")
	}

	require.Zero(t, watchedHeight(h.watch))
}

// newCliqueTestWorker returns an idle worker on a one-second clique chain.
func newCliqueTestWorker(t *testing.T) (*worker, *testWorkerBackend, func()) {
	t.Helper()

	db := rawdb.NewMemoryDatabase()
	config := *params.AllCliqueProtocolChanges
	config.Clique = &params.CliqueConfig{Period: 1, Epoch: 30000}

	return newTestWorker(t, DefaultTestConfig(), &config, clique.New(config.Clique, db), db, false, 0)
}

func TestWorkerStallWatchLifecycle(t *testing.T) {
	t.Parallel()

	w, _, closeWorker := newCliqueTestWorker(t)
	require.Equal(t, 2*time.Second, w.stallWatch.threshold)

	// Without an etherbase the worker refuses to build, so the only watch is
	// the one armed on start.
	w.setEtherbase(common.Address{})
	w.start()
	require.Eventually(t, func() bool { return watchedHeight(w.stallWatch) == 1 }, time.Second, 5*time.Millisecond)

	w.stop()
	require.Zero(t, watchedHeight(w.stallWatch))

	closeWorker()
	w.stallWatch.mu.Lock()
	closed := w.stallWatch.closed
	w.stallWatch.mu.Unlock()
	require.True(t, closed)
}

func TestWorkerStallWatchFollowsChainHead(t *testing.T) {
	t.Parallel()

	w, b, closeWorker := newCliqueTestWorker(t)
	defer closeWorker()

	h := newStallWatchHarness(time.Hour)
	h.primary.Store(false)
	w.stallWatch = h.watch

	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	w.start()
	b.txPool.Add([]*types.Transaction{b.newRandomTx(true)}, false)

	select {
	case <-sub.Chan():
	case <-time.After(5 * time.Second):
		t.Fatal("no block was mined")
	}

	// Each new head watches its child, so the watch moves past block 1.
	require.Eventually(t, func() bool { return watchedHeight(h.watch) >= 2 }, 5*time.Second, 10*time.Millisecond)
}
