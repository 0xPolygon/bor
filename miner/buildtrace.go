package miner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

// Build-trace instrumentation (lab-only, flag-gated via miner.buildtrace).
//
// One JSONL record is emitted for every block-build attempt on EVERY exit path
// (completed, newhead_discarded, prepare_failed, state_at_failed, ...), plus
// small "trigger" records for build triggers that die before commitWork and
// "seal" records for the async seal outcome. All hooks are nil-safe: with the
// flag off the tracer pointer is nil and every call is a no-op branch.
//
// Design notes:
//   - emission is non-blocking: records go through a buffered channel to a
//     single writer goroutine; on overflow records are dropped and counted
//     (never stall the build path to observe it).
//   - timestamps are unix nanoseconds (suffix _ns), durations microseconds
//     (suffix _us).

const (
	buildTraceSchemaVersion = 1
	buildTraceChanBuf       = 4096
)

// buildTracer owns the JSONL output file and the writer goroutine.
type buildTracer struct {
	ch      chan any
	dropped atomic.Int64
	written atomic.Int64
	wg      sync.WaitGroup
	closed  atomic.Bool
}

func newBuildTracer(dir string) (*buildTracer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("buildtrace: create dir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("buildtrace-%d.jsonl", time.Now().Unix()))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("buildtrace: open file: %w", err)
	}

	t := &buildTracer{ch: make(chan any, buildTraceChanBuf)}
	t.wg.Add(1)

	go func() {
		defer t.wg.Done()
		defer f.Close()

		w := bufio.NewWriterSize(f, 64*1024)
		enc := json.NewEncoder(w)
		flushTicker := time.NewTicker(2 * time.Second)
		defer flushTicker.Stop()

		for {
			select {
			case rec, ok := <-t.ch:
				if !ok {
					_ = w.Flush()
					return
				}
				if err := enc.Encode(rec); err != nil {
					log.Warn("buildtrace: encode failed", "err", err)
					continue
				}
				t.written.Add(1)
			case <-flushTicker.C:
				_ = w.Flush()
			}
		}
	}()

	log.Info("Build-trace instrumentation enabled", "file", path, "schema", buildTraceSchemaVersion)

	return t, nil
}

// send enqueues a record without ever blocking the build path.
func (t *buildTracer) send(rec any) {
	if t == nil || t.closed.Load() {
		return
	}
	select {
	case t.ch <- rec:
	default:
		t.dropped.Add(1)
	}
}

func (t *buildTracer) stop() {
	if t == nil || !t.closed.CompareAndSwap(false, true) {
		return
	}
	close(t.ch)
	t.wg.Wait()
	if d := t.dropped.Load(); d > 0 {
		log.Warn("buildtrace: records dropped on overflow", "dropped", d, "written", t.written.Load())
	}
}

// ---------------------------------------------------------------------------
// Records
// ---------------------------------------------------------------------------

// buildTraceTxEvent is one attempted transaction inside commitTransactions.
type buildTraceTxEvent struct {
	I          int    `json:"i"`                // attempt index (not tx index; failed attempts count)
	Hash       string `json:"hash"`
	GasLimit   uint64 `json:"gas_limit"`
	GasUsed    uint64 `json:"gas_used,omitempty"` // only on ok
	Blob       bool   `json:"blob,omitempty"`
	Prefetched bool   `json:"prefetched,omitempty"`
	PickUs     int64  `json:"pick_us"`    // loop-head checks + peek/arbitrate for this attempt
	ResolveUs  int64  `json:"resolve_us"` // LazyTransaction.Resolve (disk for blobs)
	ApplyUs    int64  `json:"apply_us,omitempty"`
	DepsSendUs int64  `json:"deps_send_us,omitempty"` // time blocked sending read/write set to chDeps
	Outcome    string `json:"outcome"`                // ok|nonce_low|evm_interrupt|failed|gas_account_skip|evicted|pip15_dropped|replay_protected|size_break|proof_recompute_ok
}

// buildTraceRecord is the per-build-attempt record, emitted on every exit path.
type buildTraceRecord struct {
	Type   string `json:"type"` // "build"
	Schema int    `json:"schema"`
	Seq    uint64 `json:"seq"`

	Number     uint64 `json:"number"`
	ParentHash string `json:"parent_hash"`
	BuildMode  string `json:"build_mode"` // baseline (stress modes later)
	Outcome    string `json:"outcome"`    // completed|newhead_discarded|prepare_failed|state_at_failed|fill_error
	FillErr    string `json:"fill_err,omitempty"`

	// Trigger → stage timings.
	TriggerAtNs   int64 `json:"trigger_at_ns"`   // commitWork entry
	PreBuildUs    int64 `json:"pre_build_us"`    // commitWork entry → buildAndCommitBlock (incl. StateAtWithReaders)
	StateAtUs     int64 `json:"state_at_us"`     // StateAtWithReaders alone
	PrepareWorkUs int64 `json:"prepare_work_us"` // prepareWork total (incl. bor.Prepare slot wait)
	MakeHeaderUs  int64 `json:"make_header_us"`  // inside prepareWork (incl. engine.Prepare)
	MakeEnvUs     int64 `json:"make_env_us"`     // inside prepareWork

	// fillTransactions decomposition.
	PendingPlainUs   int64 `json:"pending_plain_us"`
	PendingBlobUs    int64 `json:"pending_blob_us"`
	PendingPlainAcct int   `json:"pending_plain_accts"`
	PendingPlainTxs  int   `json:"pending_plain_txs"`
	PendingBlobAcct  int   `json:"pending_blob_accts"`
	PendingBlobTxs   int   `json:"pending_blob_txs"`
	// Interrupt-flag state observed right after each Pending call: if true and
	// the corresponding count is zero, the pool returned empty *because* of the
	// interrupt (legacypool returns an empty map on interrupt).
	InterruptAtPendingPlain bool  `json:"interrupt_at_pending_plain,omitempty"`
	InterruptAtPendingBlob  bool  `json:"interrupt_at_pending_blob,omitempty"`
	HeapInitUs              int64 `json:"heap_init_us"`
	FillUs                  int64 `json:"fill_us"` // fillTransactions total

	// commitTransactions loop.
	CommitLoopUs       int64  `json:"commit_loop_us"`
	LoopBreak          string `json:"loop_break,omitempty"` // timeout_flag|gas_exhausted|pool_drained|size_limit|interrupt_signal
	InterruptSignal    string `json:"interrupt_signal,omitempty"`
	TimeoutFlagFired   bool   `json:"timeout_flag_fired,omitempty"`
	TimeoutFlagSetAtNs int64  `json:"timeout_flag_set_at_ns,omitempty"`
	TxEvents           []buildTraceTxEvent `json:"txs,omitempty"`

	// Result.
	TxCount  int    `json:"tx_count"`
	GasUsed  uint64 `json:"gas_used"`
	GasLimit uint64 `json:"gas_limit"`

	// Header fields (EVM-visible; fidelity audit against canonical).
	HeaderTime         uint64 `json:"header_time"`
	HeaderActualTimeNs int64  `json:"header_actual_time_ns,omitempty"`
	ParentTime         uint64 `json:"parent_time"`
	BaseFee            string `json:"base_fee,omitempty"`
	Coinbase           string `json:"coinbase"`
	Difficulty         string `json:"difficulty,omitempty"`

	// Slot geometry.
	InterruptTimerArmed  bool  `json:"interrupt_timer_armed"`
	TimeUntilInterruptUs int64 `json:"time_until_interrupt_us,omitempty"` // at build start

	// Prefetch (streaming prefetcher, #2192).
	PrefetchEnabled      bool `json:"prefetch_enabled"`
	PrefetchedTotal      int  `json:"prefetched_total,omitempty"`       // idle + builder phases
	PrefetchedBuilder    int  `json:"prefetched_builder,omitempty"`     // builder phase only
	PrefetchedOfIncluded int  `json:"prefetched_of_included,omitempty"` // included txs that were warm

	// Dependency DAG (BlockSTM read/write sets): deps[i] = indices i depends on.
	// With per-tx apply_us this yields the critical-path / speedup ceiling offline.
	Deps map[int][]int `json:"deps,omitempty"`

	// commit() phase (finalize + root + task hand-off), when reached.
	CommitUs     int64 `json:"commit_us,omitempty"`
	TaskQueuedNs int64 `json:"task_queued_ns,omitempty"`

	EmittedAtNs int64 `json:"emitted_at_ns"`
}

// buildTriggerRecord captures build triggers and their disposition, including
// the ones that never reach commitWork (suppressed-trigger log, ranking #5).
type buildTriggerRecord struct {
	Type        string `json:"type"` // "trigger"
	Source      string `json:"source"`      // start|chainhead|veblop_timer|mainloop
	Disposition string `json:"disposition"` // committed|dedup_skip|veblop_pending_skip|veblop_stall_hold|disable_pending_block_skip|syncing|not_running_no_etherbase
	HeadNumber  uint64 `json:"head_number"`
	AtNs        int64  `json:"at_ns"`
}

// buildSealRecord captures the async seal outcome from taskLoop; on this lab
// node the expected steady state is an UnauthorizedSignerError per block.
type buildSealRecord struct {
	Type    string `json:"type"` // "seal"
	Number  uint64 `json:"number"`
	Hash    string `json:"hash"`
	Err     string `json:"err,omitempty"`
	Outcome string `json:"outcome"` // queued_ok|error
	AtNs    int64  `json:"at_ns"`
}

// ---------------------------------------------------------------------------
// Per-attempt collector
// ---------------------------------------------------------------------------

var buildTraceSeq atomic.Uint64

// buildTrace accumulates one build attempt. It is created at commitWork entry
// and emitted exactly once via emit(); all mutators are nil-safe so the
// generateWork path (bt == nil) needs no branches.
type buildTrace struct {
	rec     buildTraceRecord
	tracer  *buildTracer
	emitted bool
}

func (t *buildTracer) begin() *buildTrace {
	if t == nil {
		return nil
	}
	return &buildTrace{
		tracer: t,
		rec: buildTraceRecord{
			Type:        "build",
			Schema:      buildTraceSchemaVersion,
			Seq:         buildTraceSeq.Add(1),
			BuildMode:   "baseline",
			Outcome:     "unknown",
			TriggerAtNs: time.Now().UnixNano(),
		},
	}
}

func (bt *buildTrace) setOutcome(o string) {
	if bt == nil {
		return
	}
	bt.rec.Outcome = o
}

// emit sends the record exactly once (guarded so deferred emission on early
// returns cannot double-fire with the explicit final emission).
func (bt *buildTrace) emit() {
	if bt == nil || bt.emitted {
		return
	}
	bt.emitted = true
	bt.rec.EmittedAtNs = time.Now().UnixNano()
	bt.tracer.send(bt.rec)
}

func (bt *buildTrace) addTxEvent(ev buildTraceTxEvent) {
	if bt == nil {
		return
	}
	bt.rec.TxEvents = append(bt.rec.TxEvents, ev)
}

// countSyncMap is a helper for the prefetch attribution sync.Maps.
func countSyncMap(m *sync.Map) int {
	if m == nil {
		return 0
	}
	n := 0
	m.Range(func(_, _ any) bool { n++; return true })
	return n
}

// finishBuild fills the result-side fields from the finished (or discarded)
// build environment: tx count, gas used, and prefetch attribution counts.
func (bt *buildTrace) finishBuild(env *environment, genParams *generateParams) {
	if bt == nil || env == nil {
		return
	}
	bt.rec.TxCount = env.tcount
	bt.rec.GasLimit = env.header.GasLimit
	if env.gasPool != nil {
		bt.rec.GasUsed = env.header.GasLimit - env.gasPool.Gas()
	}
	if genParams == nil {
		return
	}
	bt.rec.PrefetchedTotal = countSyncMap(genParams.prefetchedTxHashes)
	bt.rec.PrefetchedBuilder = countSyncMap(genParams.builderPrefetchedTxHashes)
	if genParams.prefetchedTxHashes != nil {
		warm := 0
		for _, tx := range env.txs {
			if _, ok := genParams.prefetchedTxHashes.Load(tx.Hash()); ok {
				warm++
			}
		}
		bt.rec.PrefetchedOfIncluded = warm
	}
}
