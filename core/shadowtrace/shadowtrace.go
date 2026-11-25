//go:build lls_shadow

// Package shadowtrace is instrumentation for the LLS shadow-producer
// experiment (a throwaway mainnet clone that runs the real producer build
// path but never seals). It only exists in binaries built with
// -tags lls_shadow; the default build links the no-op twin in noop.go.
//
// Runtime switch: BOR_SHADOW_LLS=1. Live toggles are read from the file in
// BOR_SHADOW_TOGGLES (default /var/lib/bor/shadow.toggles), one key=value per
// line, re-read at most once per second:
//
//	gap8fix=on      an interrupted prefetch run is not recorded as prefetched
//	txlog=all|slow  emit every traced tx, or only injected/slow/never-prefetched
//
// Every record is a log.Info line prefixed "SHADOW" so it can be grepped
// out of the journal.
package shadowtrace

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"
)

// Compiled reports whether the instrumentation is linked in.
const Compiled = true

var (
	enabledOnce sync.Once
	enabled     bool
)

// Enabled reports whether shadow mode is switched on for this process.
func Enabled() bool {
	enabledOnce.Do(func() {
		enabled = os.Getenv("BOR_SHADOW_LLS") == "1"
		if enabled {
			log.Info("SHADOW mode enabled", "toggles", togglesPath())
			go pruneLoop()
		}
	})

	return enabled
}

// SetEnabledForTest forces shadow mode on or off (tests only).
func SetEnabledForTest(v bool) {
	enabledOnce.Do(func() {})
	enabled = v
}

// ---------------------------------------------------------------------------
// Live toggles

var (
	toggleMu     sync.Mutex
	toggleRead   time.Time
	toggleValues = map[string]string{}
)

func togglesPath() string {
	if p := os.Getenv("BOR_SHADOW_TOGGLES"); p != "" {
		return p
	}

	return "/var/lib/bor/shadow.toggles"
}

func toggle(key string) string {
	toggleMu.Lock()
	defer toggleMu.Unlock()

	if time.Since(toggleRead) > time.Second {
		toggleRead = time.Now()
		next := map[string]string{}

		if f, err := os.Open(togglesPath()); err == nil {
			sc := bufio.NewScanner(f)
			for sc.Scan() {
				line := strings.TrimSpace(sc.Text())
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}

				if k, v, ok := strings.Cut(line, "="); ok {
					next[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
			}
			f.Close()
		}

		toggleValues = next
	}

	return toggleValues[key]
}

// Gap8Fix reports whether an interrupted prefetch run must be treated as a
// failure (not added to prefetchedTxHashes).
func Gap8Fix() bool { return Enabled() && toggle("gap8fix") == "on" }

// ---------------------------------------------------------------------------
// Pool arrival + injected set (node-wide, keyed by tx hash)

type seenRec struct {
	at       time.Time
	injected bool
}

var seen sync.Map // common.Hash -> seenRec

// PoolSeen records the first time a tx entered the local pool.
func PoolSeen(hash common.Hash, at time.Time) {
	if !Enabled() {
		return
	}

	seen.LoadOrStore(hash, seenRec{at: at})
}

// MarkInjected records a tx added by the shadow injection RPC.
func MarkInjected(hash common.Hash, at time.Time) {
	if !Enabled() {
		return
	}

	seen.Store(hash, seenRec{at: at, injected: true})
}

// IsInjected reports whether hash was added by the shadow injection RPC.
func IsInjected(hash common.Hash) bool {
	if v, ok := seen.Load(hash); ok {
		return v.(seenRec).injected
	}

	return false
}

func pruneLoop() {
	for range time.Tick(time.Minute) {
		cutoff := time.Now().Add(-30 * time.Minute)
		// Injected txs are one-shot (retired after their build); keep their
		// mark much longer so a late peer request is still refused.
		injCutoff := time.Now().Add(-6 * time.Hour)

		seen.Range(func(k, v any) bool {
			r := v.(seenRec)
			if (!r.injected && r.at.Before(cutoff)) || (r.injected && r.at.Before(injCutoff)) {
				seen.Delete(k)
			}

			return true
		})
	}
}

// ---------------------------------------------------------------------------
// Per-build trace

// Drop reasons: why a tx offered to the prefetcher did not get a run.
const (
	DropIdleBudget   = "idle-budget"   // idle gas budget too small for the tx
	DropIdleChanFull = "idle-chanfull" // idle batch cut: stream channel full
	DropPlanChanFull = "plan-chanfull" // builder plan channel full (sendPlan)
	DropFwdChanFull  = "fwd-chanfull"  // builder provider → stream channel full
	DropHandoffDrain = "handoff-drain" // buffered idle tx drained at handoff
	DropAbortSkip    = "abort-skip"    // worker dequeued it while evmAbort was set
)

// Prefetch run outcomes.
const (
	OutcomeCompleted   = "completed"
	OutcomeReverted    = "reverted"
	OutcomeInterrupted = "interrupted" // EVM stopped by evmAbort/hardKill (gap 8)
	OutcomeError       = "error"       // ApplyMessage error / preload / msg error
	OutcomePreAborted  = "pre-aborted" // interrupt already set before start
)

type txTrace struct {
	sendSrc     string // first source that offered it: idle / plan / overflow
	firstSend   time.Time
	sends       int
	drops       map[string]int
	runs        int
	firstStart  time.Time
	lastEnd     time.Time
	outcome     string // last run outcome
	markedDone  bool   // added to prefetchedTxHashes
	intrMarked  int    // interrupted runs that were still marked done (gap 8)
	pfGas       uint64
	applied     bool
	pass        int
	applyStart  time.Time
	applyDur    time.Duration
	prefetched  bool
	gasUsed     uint64
	l1Hit       int64
	l1Miss      int64
	applyErr    string
	applyResult string
}

type buildTrace struct {
	mu           sync.Mutex
	number       uint64
	start        time.Time
	parentActual time.Time
	actualTime   time.Time
	headerReady  time.Time
	idleStart    time.Time
	handoff      time.Time
	builderStart time.Time
	passes       []time.Time
	fillEnd      time.Time
	outcome      string
	sealSkipped  bool
	gap8         int
	txs          map[common.Hash]*txTrace
}

var (
	hookMu     sync.Mutex
	startHooks []func(number uint64, start time.Time)
	endHooks   []func(number uint64)
)

// OnBuildStart registers fn to run (in its own goroutine) at every BuildStart.
func OnBuildStart(fn func(number uint64, start time.Time)) {
	hookMu.Lock()
	startHooks = append(startHooks, fn)
	hookMu.Unlock()
}

// OnBuildEnd registers fn to run (in its own goroutine) at every BuildEnd.
func OnBuildEnd(fn func(number uint64)) {
	hookMu.Lock()
	endHooks = append(endHooks, fn)
	hookMu.Unlock()
}

func runStartHooks(number uint64, start time.Time) {
	hookMu.Lock()
	hooks := append([]func(uint64, time.Time){}, startHooks...)
	hookMu.Unlock()

	for _, fn := range hooks {
		go fn(number, start)
	}
}

func runEndHooks(number uint64) {
	hookMu.Lock()
	hooks := append([]func(uint64){}, endHooks...)
	hookMu.Unlock()

	for _, fn := range hooks {
		go fn(number)
	}
}

var (
	buildsMu sync.Mutex
	builds   = map[uint64]*buildTrace{}
	current  atomic.Uint64
)

func (b *buildTrace) tx(hash common.Hash) *txTrace {
	t := b.txs[hash]
	if t == nil {
		t = &txTrace{}
		b.txs[hash] = t
	}

	return t
}

func get(number uint64) *buildTrace {
	buildsMu.Lock()
	defer buildsMu.Unlock()

	return builds[number]
}

// BuildStart opens the trace of a production build at number. A rebuild of
// the same height flushes the previous trace first.
func BuildStart(number uint64, start, parentActual, actualTime time.Time) {
	if !Enabled() {
		return
	}

	buildsMu.Lock()
	prev := builds[number]
	b := &buildTrace{
		number: number, start: start, parentActual: parentActual,
		actualTime: actualTime, txs: map[common.Hash]*txTrace{},
	}
	builds[number] = b
	buildsMu.Unlock()

	current.Store(number)
	runStartHooks(number, start)

	if prev != nil {
		prev.mu.Lock()
		if prev.outcome == "" {
			prev.outcome = "superseded"
		}
		prev.mu.Unlock()
		emit(prev)
	}
}

// HeaderReady records the prepared header's announce (actual) time; the
// time between BuildStart and HeaderReady is the bor.Prepare wait.
func HeaderReady(number uint64, actualTime time.Time) {
	with(number, func(b *buildTrace) {
		b.headerReady = time.Now()
		b.actualTime = actualTime
	})
}

// Current returns the number of the build most recently started.
func Current() uint64 { return current.Load() }

// IdleStart marks the start of the idle prefetch phase.
func IdleStart(number uint64) {
	with(number, func(b *buildTrace) {
		if b.idleStart.IsZero() {
			b.idleStart = time.Now()
		}
	})
}

// BuilderStart marks the moment the builder flips builderStarted.
func BuilderStart(number uint64) {
	with(number, func(b *buildTrace) { b.builderStart = time.Now() })
}

// Handoff marks the completed idle→builder handoff.
func Handoff(number uint64) {
	with(number, func(b *buildTrace) { b.handoff = time.Now() })
}

// PassStart records the start of a fillTransactions pass.
func PassStart(number uint64) {
	with(number, func(b *buildTrace) { b.passes = append(b.passes, time.Now()) })
}

// Sent records that a tx was handed to the prefetch stream.
func Sent(number uint64, hash common.Hash, src string) {
	with(number, func(b *buildTrace) {
		t := b.tx(hash)
		if t.sends == 0 {
			t.sendSrc, t.firstSend = src, time.Now()
		}
		t.sends++
	})
}

// Dropped records that a tx offered to the prefetcher was not run.
func Dropped(number uint64, hash common.Hash, reason string) {
	with(number, func(b *buildTrace) {
		t := b.tx(hash)
		if t.drops == nil {
			t.drops = map[string]int{}
		}
		t.drops[reason]++
	})
}

// RunStart records the start of a prefetch EVM run.
func RunStart(number uint64, hash common.Hash) {
	with(number, func(b *buildTrace) {
		t := b.tx(hash)
		t.runs++
		if t.firstStart.IsZero() {
			t.firstStart = time.Now()
		}
	})
}

// RunEnd records the end of a prefetch run and whether it was marked done.
func RunEnd(number uint64, hash common.Hash, outcome string, gas uint64, markedDone bool) {
	with(number, func(b *buildTrace) {
		t := b.tx(hash)
		t.lastEnd, t.outcome, t.pfGas = time.Now(), outcome, gas
		if markedDone {
			t.markedDone = true
			if outcome == OutcomeInterrupted {
				t.intrMarked++
				b.gap8++
			}
		}
	})
}

// Applied records one builder apply of a tx.
func Applied(number uint64, hash common.Hash, start time.Time, dur time.Duration,
	prefetched bool, gasUsed uint64, l1Hit, l1Miss int64, result string, err error,
) {
	with(number, func(b *buildTrace) {
		t := b.tx(hash)
		t.applied = true
		t.pass = len(b.passes)
		t.applyStart, t.applyDur, t.prefetched = start, dur, prefetched
		t.gasUsed, t.l1Hit, t.l1Miss, t.applyResult = gasUsed, l1Hit, l1Miss, result
		if err != nil {
			t.applyErr = err.Error()
		}
	})
}

// FillEnd marks the end of the fill loop.
func FillEnd(number uint64) {
	with(number, func(b *buildTrace) { b.fillEnd = time.Now() })
}

// BuildEnd closes the trace. Emission is delayed so late prefetch-worker
// events (async shutdown) still land in it.
func BuildEnd(number uint64, outcome string) {
	b := get(number)
	if b == nil {
		return
	}

	b.mu.Lock()
	if b.outcome == "" {
		b.outcome = outcome
	}
	b.mu.Unlock()

	runEndHooks(number)

	time.AfterFunc(500*time.Millisecond, func() {
		buildsMu.Lock()
		if builds[number] == b {
			delete(builds, number)
		}
		buildsMu.Unlock()
		emit(b)
	})
}

// SealSkipped records that consensus refused to seal the shadow block.
func SealSkipped(number uint64, err error) {
	if !Enabled() {
		return
	}

	with(number, func(b *buildTrace) { b.sealSkipped = true })
	log.Info("SHADOW seal-skipped", "number", number, "err", err)
}

func with(number uint64, fn func(*buildTrace)) {
	if !Enabled() {
		return
	}

	b := get(number)
	if b == nil {
		return
	}

	b.mu.Lock()
	fn(b)
	b.mu.Unlock()
}

// ms renders t relative to base in milliseconds; -1 when t is unset.
func ms(t, base time.Time) float64 {
	if t.IsZero() || base.IsZero() {
		return -1
	}

	return float64(t.Sub(base).Microseconds()) / 1000
}

// slowApply is the apply time above which a tx is always logged in txlog=slow.
const slowApply = 20 * time.Millisecond

func emit(b *buildTrace) {
	b.mu.Lock()
	defer b.mu.Unlock()

	all := toggle("txlog") == "all"

	var applied, appliedPf, neverPf, gap8Applied int

	for hash, t := range b.txs {
		if t.applied {
			applied++
			if t.prefetched {
				appliedPf++
			}
			if t.runs == 0 {
				neverPf++
			}
			if t.intrMarked > 0 {
				gap8Applied++
			}
		}

		injected := IsInjected(hash)
		interesting := injected || t.applyDur >= slowApply || (t.applied && t.runs == 0) || t.intrMarked > 0
		if !all && !interesting {
			continue
		}

		var arrival time.Time
		if v, ok := seen.Load(hash); ok {
			arrival = v.(seenRec).at
		}

		drops := ""
		for k, v := range t.drops {
			drops += k + ":" + itoa(v) + ","
		}

		log.Info("SHADOW tx", "number", b.number, "hash", hash, "injected", injected,
			"arrivalMs", ms(arrival, b.start),
			"sendSrc", t.sendSrc, "sends", t.sends, "firstSendMs", ms(t.firstSend, b.start),
			"drops", strings.TrimSuffix(drops, ","),
			"runs", t.runs, "pfStartMs", ms(t.firstStart, b.start), "pfEndMs", ms(t.lastEnd, b.start),
			"pfOutcome", t.outcome, "pfGas", t.pfGas, "markedDone", t.markedDone, "gap8", t.intrMarked,
			"applied", t.applied, "pass", t.pass, "applyStartMs", ms(t.applyStart, b.start),
			"applyMs", float64(t.applyDur.Microseconds())/1000, "prefetched", t.prefetched,
			"gasUsed", t.gasUsed, "l1Hit", t.l1Hit, "l1Miss", t.l1Miss,
			"result", t.applyResult, "applyErr", t.applyErr)
	}

	passMs := make([]string, 0, len(b.passes))
	for _, p := range b.passes {
		passMs = append(passMs, ftoa(ms(p, b.start)))
	}

	log.Info("SHADOW build", "number", b.number, "outcome", b.outcome,
		"start", b.start.UTC().Format(time.RFC3339Nano),
		"startVsParentActualMs", ms(b.start, b.parentActual),
		"headerReadyMs", ms(b.headerReady, b.start), "actualTimeMs", ms(b.actualTime, b.start),
		"idleStartMs", ms(b.idleStart, b.start), "builderStartMs", ms(b.builderStart, b.start),
		"handoffMs", ms(b.handoff, b.start), "fillEndMs", ms(b.fillEnd, b.start),
		"passes", len(b.passes), "passMs", strings.Join(passMs, ","),
		"traced", len(b.txs), "applied", applied, "appliedPrefetched", appliedPf,
		"appliedNeverRun", neverPf, "gap8Runs", b.gap8, "gap8Applied", gap8Applied,
		"sealSkipped", b.sealSkipped)
}

func itoa(v int) string { return strconv.Itoa(v) }

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
