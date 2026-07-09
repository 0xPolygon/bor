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

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
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

		// Re-reference ring + pressure baseline: owned by this goroutine, no locks.
		var (
			ring         rerefRing
			lastPressure map[string]int64
		)

		for {
			select {
			case rec, ok := <-t.ch:
				if !ok {
					_ = w.Flush()
					return
				}
				switch v := rec.(type) {
				case core.ImportTraceData:
					rec = t.buildImportRecord(v, &ring, &lastPressure)
				case buildTraceRecord:
					// Annotate build misses with re-reference distance against
					// canonical history (the ring is fed by imports).
					for i := range v.Misses {
						v.Misses[i].D = ring.distance(v.Number, v.Misses[i].K)
					}
					rec = v
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
	core.SetImportTraceHook(nil)
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
	I          int    `json:"i"` // attempt index (not tx index; failed attempts count)
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

	// I4 — per-tx state-read time split by cache hit/miss (process reader deltas).
	RdAcctHitN   int64 `json:"rd_ah_n,omitempty"`
	RdAcctHitUs  int64 `json:"rd_ah_us,omitempty"`
	RdAcctMissN  int64 `json:"rd_am_n,omitempty"`
	RdAcctMissUs int64 `json:"rd_am_us,omitempty"`
	RdStorHitN   int64 `json:"rd_sh_n,omitempty"`
	RdStorHitUs  int64 `json:"rd_sh_us,omitempty"`
	RdStorMissN  int64 `json:"rd_sm_n,omitempty"`
	RdStorMissUs int64 `json:"rd_sm_us,omitempty"`
}

// buildTraceMissEvent is one process-cache miss (I1).
type buildTraceMissEvent struct {
	K  uint64 `json:"k"`           // fnv64 of addr / addr||slot / codeHash
	S  bool   `json:"s,omitempty"` // true = storage slot
	C  bool   `json:"c,omitempty"` // true = contract code (miss vs shared code LRU)
	Us int64  `json:"us"`          // backing-resolution latency
	D  int64  `json:"d,omitempty"` // re-reference distance in blocks (0 = not seen in window)
}

// importTraceRecord is one per-imported-block measurement (import-path lab).
type importTraceRecord struct {
	Type   string `json:"type"` // "import"
	Schema int    `json:"schema"`

	Number      uint64 `json:"number"`
	Txs         int    `json:"txs"`
	GasUsed     uint64 `json:"gas_used"`
	ExecUs      int64  `json:"exec_us"`
	ValUs       int64  `json:"val_us"`
	ParallelWon bool   `json:"parallel_won,omitempty"`

	ProcReads     *state.ReadDetailStats `json:"proc_reads,omitempty"`
	PrefReads     *state.ReadDetailStats `json:"pref_reads,omitempty"`
	Misses        []buildTraceMissEvent  `json:"misses,omitempty"`
	MissesDropped int64                  `json:"misses_dropped,omitempty"`
	TouchedKeys   int                    `json:"touched_keys"`

	// Per-segment exec wall time (tracing.ExecSegments.SnapshotUs keys).
	ExecSegments map[string]int64 `json:"exec_segments,omitempty"`
	// Opcode-family timing (sampled blocks only; wall time inflated).
	OpFams       map[string]core.OpFamStat `json:"opcode_families,omitempty"`
	OpFamSampled bool                      `json:"opfam_sampled,omitempty"`

	// Re-reference distance histogram for misses: bucket label → count.
	MissDistHist map[string]int `json:"miss_dist_hist,omitempty"`

	// Node-global lower-layer meter deltas since the previous import record.
	SnapDeltas map[string]int64 `json:"snap_deltas,omitempty"`

	// Every keyDumpEvery-th block: the full touched-key set (95%-locality study).
	TouchedDump []uint64 `json:"touched_dump,omitempty"`

	EmittedAtNs int64 `json:"emitted_at_ns"`
}

const (
	rerefWindowBlocks = 256 // re-reference ring depth (blocks)
	keyDumpEvery      = 500 // full touched-set dump cadence
)

// rerefRing holds per-block touched-key sets; owned by the writer goroutine.
type rerefRing struct {
	entries []rerefEntry
}

type rerefEntry struct {
	number uint64
	keys   map[uint64]struct{}
}

func (r *rerefRing) push(number uint64, keys []uint64) {
	set := make(map[uint64]struct{}, len(keys))
	for _, k := range keys {
		set[k] = struct{}{}
	}
	r.entries = append(r.entries, rerefEntry{number: number, keys: set})
	if len(r.entries) > rerefWindowBlocks {
		r.entries = r.entries[1:]
	}
}

// distance returns how many blocks back the key was last touched (1 = previous
// pushed block), or 0 if not seen inside the window.
func (r *rerefRing) distance(current uint64, key uint64) int64 {
	for i := len(r.entries) - 1; i >= 0; i-- {
		if _, ok := r.entries[i].keys[key]; ok {
			return int64(current - r.entries[i].number)
		}
	}
	return 0
}

// distBucket maps a distance to a histogram label.
func distBucket(d int64) string {
	switch {
	case d == 0:
		return "gt_window"
	case d == 1:
		return "1"
	case d <= 2:
		return "2"
	case d <= 4:
		return "4"
	case d <= 8:
		return "8"
	case d <= 16:
		return "16"
	case d <= 32:
		return "32"
	case d <= 64:
		return "64"
	case d <= 128:
		return "128"
	default:
		return "256"
	}
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
	CommitLoopUs       int64               `json:"commit_loop_us"`
	LoopBreak          string              `json:"loop_break,omitempty"` // timeout_flag|gas_exhausted|pool_drained|size_limit|interrupt_signal
	InterruptSignal    string              `json:"interrupt_signal,omitempty"`
	TimeoutFlagFired   bool                `json:"timeout_flag_fired,omitempty"`
	TimeoutFlagSetAtNs int64               `json:"timeout_flag_set_at_ns,omitempty"`
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

	// I1/I4 — process-reader detail: read time split hit/miss + per-miss events.
	ProcReads     *state.ReadDetailStats `json:"proc_reads,omitempty"`
	PrefReads     *state.ReadDetailStats `json:"pref_reads,omitempty"` // prefetch reader's own resolution costs
	Misses        []buildTraceMissEvent  `json:"misses,omitempty"`
	MissesDropped int64                  `json:"misses_dropped,omitempty"`

	// Per-segment exec wall time over the whole build (tracing.ExecSegments).
	ExecSegments map[string]int64 `json:"exec_segments,omitempty"`
	// Opcode-family timing (sampled builds only; wall time inflated).
	OpFams       map[string]core.OpFamStat `json:"opcode_families,omitempty"`
	OpFamSampled bool                      `json:"opfam_sampled,omitempty"`

	// Prefetch attribution counters from the shared per-block cache (v2.6.0 meters, per build).
	PfAcctHitFromPrefetch int64 `json:"pf_acct_hit_from_pf,omitempty"`
	PfStorHitFromPrefetch int64 `json:"pf_stor_hit_from_pf,omitempty"`
	PfAcctInsert          int64 `json:"pf_acct_insert,omitempty"`
	PfStorInsert          int64 `json:"pf_stor_insert,omitempty"`

	// I5 — node-global lower-layer meter deltas over the build window (includes
	// concurrent import activity on the clone — interference caveat, ranking #14).
	SnapDeltas map[string]int64 `json:"snap_deltas,omitempty"`

	EmittedAtNs int64 `json:"emitted_at_ns"`
}

// pressureMeterNames are the lower-layer meters snapshotted per build (I5).
// PBSS nodes serve state via pathdb; the legacy snapshot.Tree meters stay for
// hash-scheme portability (dead meters read 0 and never emit a delta).
var pressureMeterNames = []string{
	"pathdb/clean/state/hit",
	"pathdb/clean/state/miss",
	"pathdb/clean/node/hit",
	"pathdb/clean/node/miss",
	"pathdb/dirty/state/hit",
	"pathdb/dirty/state/miss",
	"pathdb/dirty/node/hit",
	"pathdb/dirty/node/miss",
	"pathdb/biased/address/hit",
	"pathdb/biased/address/miss",
	"state/snapshot/clean/account/hit",
	"state/snapshot/clean/account/miss",
	"state/snapshot/clean/storage/hit",
	"state/snapshot/clean/storage/miss",
}

// pressureGaugeNames are cumulative pebble cache counters exposed as gauges.
var pressureGaugeNames = []string{
	"eth/db/chaindata/cache/block/hit",
	"eth/db/chaindata/cache/block/miss",
	"eth/db/chaindata/cache/table/hit",
	"eth/db/chaindata/cache/table/miss",
	"eth/db/chaindata/filter/hit",
	"eth/db/chaindata/filter/miss",
}

// snapshotPressureMeters captures current counts of the lower-layer meters.
func snapshotPressureMeters() map[string]int64 {
	out := make(map[string]int64, len(pressureMeterNames)+len(pressureGaugeNames))
	for _, name := range pressureMeterNames {
		if m, ok := metrics.DefaultRegistry.Get(name).(*metrics.Meter); ok {
			out[name] = m.Snapshot().Count()
		}
	}
	for _, name := range pressureGaugeNames {
		if g, ok := metrics.DefaultRegistry.Get(name).(*metrics.Gauge); ok {
			out[name] = g.Snapshot().Value()
		}
	}
	return out
}

// buildImportRecord converts hook data into the emitted record, updating the
// re-reference ring and the pressure baseline. Writer-goroutine only.
func (t *buildTracer) buildImportRecord(d core.ImportTraceData, ring *rerefRing, lastPressure *map[string]int64) importTraceRecord {
	rec := importTraceRecord{
		Type:          "import",
		Schema:        buildTraceSchemaVersion,
		Number:        d.Number,
		Txs:           d.Txs,
		GasUsed:       d.GasUsed,
		ExecUs:        d.ExecUs,
		ValUs:         d.ValUs,
		ParallelWon:   d.ParallelWon,
		MissesDropped: d.MissesDropped,
		TouchedKeys:   len(d.Touched),
		EmittedAtNs:   time.Now().UnixNano(),
	}
	pr, pf := d.ProcReads, d.PrefReads
	rec.ProcReads, rec.PrefReads = &pr, &pf

	// Distances are computed BEFORE pushing this block's touched set, so a key
	// touched in block N-1 gets distance 1.
	if len(d.Misses) > 0 {
		rec.Misses = make([]buildTraceMissEvent, len(d.Misses))
		rec.MissDistHist = make(map[string]int, 12)
		for i, m := range d.Misses {
			dist := ring.distance(d.Number, m.Key)
			rec.Misses[i] = buildTraceMissEvent{K: m.Key, S: m.Storage, C: m.Code, Us: m.LatencyUs, D: dist}
			rec.MissDistHist[distBucket(dist)]++
		}
	}
	rec.ExecSegments = d.Segments
	rec.OpFams = d.OpFams
	rec.OpFamSampled = d.OpFamSampled
	ring.push(d.Number, d.Touched)

	if d.Number%keyDumpEvery == 0 {
		rec.TouchedDump = d.Touched
	}

	cur := snapshotPressureMeters()
	if *lastPressure != nil {
		deltas := make(map[string]int64, len(cur))
		for name, v := range cur {
			if dd := v - (*lastPressure)[name]; dd != 0 {
				deltas[name] = dd
			}
		}
		if len(deltas) > 0 {
			rec.SnapDeltas = deltas
		}
	}
	*lastPressure = cur

	return rec
}

// handleImport is the core.ImportTraceHook target: hand off to the writer.
func (t *buildTracer) handleImport(d core.ImportTraceData) {
	t.send(d)
}

// buildTriggerRecord captures build triggers and their disposition, including
// the ones that never reach commitWork (suppressed-trigger log, ranking #5).
type buildTriggerRecord struct {
	Type        string `json:"type"`        // "trigger"
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

	// I1/I4 collectors attached to the per-block stats readers.
	procDetail *state.ReadDetail
	prefDetail *state.ReadDetail
	// I5 baseline of lower-layer meters at build start.
	pressureStart map[string]int64

	// Per-segment exec timers, attached to the build EVM (makeEnv).
	segments *tracing.ExecSegments
	// Sampled opcode-family tracer (every buildOpFamSampleEvery-th build).
	opFam *core.OpFamTracer
}

// buildOpFamSampleEvery selects which builds get the opcode-family tracer.
const buildOpFamSampleEvery = 8

func (t *buildTracer) begin() *buildTrace {
	if t == nil {
		return nil
	}
	seq := buildTraceSeq.Add(1)
	bt := &buildTrace{
		tracer:        t,
		pressureStart: snapshotPressureMeters(),
		segments:      &tracing.ExecSegments{},
		rec: buildTraceRecord{
			Type:        "build",
			Schema:      buildTraceSchemaVersion,
			Seq:         seq,
			BuildMode:   "baseline",
			Outcome:     "unknown",
			TriggerAtNs: time.Now().UnixNano(),
		},
	}
	if seq%buildOpFamSampleEvery == 0 {
		bt.opFam = core.NewOpFamTracer()
	}
	return bt
}

// attachReaders installs read-detail collectors on the per-block stats readers.
func (bt *buildTrace) attachReaders(process, prefetch state.ReaderWithStats) {
	if bt == nil {
		return
	}
	if r, ok := process.(state.ReaderWithDetail); ok {
		bt.procDetail = &state.ReadDetail{}
		r.SetReadDetail(bt.procDetail)
	}
	if r, ok := prefetch.(state.ReaderWithDetail); ok {
		bt.prefDetail = &state.ReadDetail{}
		r.SetReadDetail(bt.prefDetail)
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

	// I1/I4 — reader detail totals + per-miss events.
	if bt.procDetail != nil {
		s := bt.procDetail.Snapshot()
		bt.rec.ProcReads = &s
		misses, dropped := bt.procDetail.TakeMisses()
		bt.rec.MissesDropped = dropped
		if len(misses) > 0 {
			evs := make([]buildTraceMissEvent, len(misses))
			for i, m := range misses {
				evs[i] = buildTraceMissEvent{K: m.Key, S: m.Storage, C: m.Code, Us: m.LatencyUs}
			}
			bt.rec.Misses = evs
		}
	}
	if bt.prefDetail != nil {
		s := bt.prefDetail.Snapshot()
		bt.rec.PrefReads = &s
	}

	// Exec segments + sampled opcode families (attached to the build EVM).
	if bt.segments != nil && bt.segments.TxN.Load() > 0 {
		bt.rec.ExecSegments = bt.segments.SnapshotUs()
	}
	if bt.opFam != nil {
		bt.rec.OpFams = bt.opFam.Result()
		bt.rec.OpFamSampled = true
	}

	// Prefetch attribution counters (shared per-block cache provenance).
	if env.processReader != nil {
		ps := env.processReader.GetPrefetchStats()
		bt.rec.PfAcctHitFromPrefetch = ps.AccountHitFromPrefetch
		bt.rec.PfStorHitFromPrefetch = ps.StorageHitFromPrefetch
	}
	if env.prefetchReader != nil {
		ps := env.prefetchReader.GetPrefetchStats()
		bt.rec.PfAcctInsert = ps.AccountInsert
		bt.rec.PfStorInsert = ps.StorageInsert
	}

	// I5 — lower-layer meter deltas over the build window.
	if bt.pressureStart != nil {
		end := snapshotPressureMeters()
		deltas := make(map[string]int64, len(end))
		for name, v := range end {
			if d := v - bt.pressureStart[name]; d != 0 {
				deltas[name] = d
			}
		}
		if len(deltas) > 0 {
			bt.rec.SnapDeltas = deltas
		}
	}
}
