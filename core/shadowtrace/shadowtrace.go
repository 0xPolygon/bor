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
//	gap8fix=on       an interrupted prefetch run is not recorded as prefetched
//	txlog=all|slow   emit every traced tx, or only injected/slow/heavy/never-prefetched
//	nodiscard=on     a new head does not discard the running build (it finishes;
//	                 the next build starts on the newest head, skipping heights)
//	xbwarm=on        A/B early warm: heavy pool txs of arm A are executed once
//	                 against the head state when they enter the pool
//	heavygas=N       gas-limit threshold of a "heavy" tx (default 5000000)
//
// Every record is a log.Info line prefixed "SHADOW" so it can be grepped
// out of the journal. Hashes are printed in full (Hex), never abbreviated.
package shadowtrace

import (
	"bufio"
	"os"
	"sort"
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

		if !mapsEqual(next, toggleValues) {
			log.Info("SHADOW toggles", "values", fmtMap(next))
		}

		toggleValues = next
	}

	return toggleValues[key]
}

// SetToggleForTest overrides one toggle value (tests only).
func SetToggleForTest(key, value string) {
	toggleMu.Lock()
	defer toggleMu.Unlock()

	toggleRead = time.Now().Add(time.Hour)
	toggleValues[key] = value
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

func fmtMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}

	return strings.Join(parts, ",")
}

// Gap8Fix reports whether an interrupted prefetch run must be treated as a
// failure (not added to prefetchedTxHashes).
func Gap8Fix() bool { return Enabled() && toggle("gap8fix") == "on" }

// NoDiscard reports whether a new head must not interrupt the running build.
func NoDiscard() bool { return Enabled() && toggle("nodiscard") == "on" }

// XBWarm reports whether the A/B early warm of heavy pool txs is on.
func XBWarm() bool { return Enabled() && toggle("xbwarm") == "on" }

// HeavyGas is the gas-limit threshold of a heavy tx.
func HeavyGas() uint64 {
	if v, err := strconv.ParseUint(toggle("heavygas"), 10, 64); err == nil && v > 0 {
		return v
	}

	return 5_000_000
}

// Arm splits txs into two fixed A/B groups by the last bit of the hash.
// A = treated (warmed when xbwarm=on), B = control.
func Arm(hash common.Hash) string {
	if hash[31]&1 == 0 {
		return "A"
	}

	return "B"
}

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

// ---------------------------------------------------------------------------
// Heavy txs: arrival, A/B arm, early warm, inclusion

type heavyRec struct {
	at          time.Time
	gas         uint64
	arm         string
	from, to    string
	sel         string
	warmStart   time.Time
	warmEnd     time.Time
	warmOutcome string
	warmGas     uint64
	warmHead    uint64
	warmReadMs  float64
}

var (
	heavyMu sync.Mutex
	heavy   = map[common.Hash]*heavyRec{}
)

// HeavySeen records a heavy tx entering the pool and returns its A/B arm.
func HeavySeen(hash common.Hash, at time.Time, gas uint64, from, to, sel string) string {
	if !Enabled() {
		return ""
	}

	arm := Arm(hash)

	heavyMu.Lock()
	if _, ok := heavy[hash]; ok {
		heavyMu.Unlock()
		return arm
	}
	heavy[hash] = &heavyRec{at: at, gas: gas, arm: arm, from: from, to: to, sel: sel}
	heavyMu.Unlock()

	log.Info("SHADOW heavy-arrival", "hash", hash.Hex(), "gasLimit", gas, "arm", arm,
		"from", from, "to", to, "sel", sel, "buildNumber", Current(),
		"sinceBuildStartMs", ms(at, currentStart()), "xbwarm", XBWarm())

	return arm
}

// WarmStart records the start of an early-warm run.
func WarmStart(hash common.Hash) time.Time {
	now := time.Now()

	heavyMu.Lock()
	if r := heavy[hash]; r != nil {
		r.warmStart = now
	}
	heavyMu.Unlock()

	return now
}

// WarmEnd records the end of an early-warm run.
func WarmEnd(hash common.Hash, start time.Time, head uint64, outcome string, gas uint64, readDur time.Duration) {
	now := time.Now()

	heavyMu.Lock()
	r := heavy[hash]
	if r != nil {
		r.warmEnd, r.warmOutcome, r.warmGas, r.warmHead = now, outcome, gas, head
		r.warmReadMs = durMs(readDur)
	}
	heavyMu.Unlock()

	var queuedMs float64 = -1
	if r != nil {
		queuedMs = ms(start, r.at)
	}

	log.Info("SHADOW warm", "hash", hash.Hex(), "head", head, "outcome", outcome, "gas", gas,
		"queuedMs", queuedMs, "durMs", ms(now, start), "readMs", durMs(readDur),
		"buildNumber", Current(), "endSinceBuildStartMs", ms(now, currentStart()))
}

// ---------------------------------------------------------------------------
// Chain heads (import times) — node-wide

var (
	headMu sync.Mutex
	headAt = map[uint64]time.Time{}
)

// HeadSeen records a new chain head (end of its import) and retires the
// heavy txs it included.
func HeadSeen(number uint64, at time.Time, txs []common.Hash, gasUsed uint64, txCount int) {
	if !Enabled() {
		return
	}

	headMu.Lock()
	headAt[number] = at
	for n := range headAt {
		if n+200 < number {
			delete(headAt, n)
		}
	}
	headMu.Unlock()

	cur := Current()
	log.Info("SHADOW head", "number", number, "txs", txCount, "gasUsed", gasUsed,
		"buildNumber", cur, "sinceBuildStartMs", ms(at, currentStart()))

	heavyMu.Lock()
	defer heavyMu.Unlock()

	for _, h := range txs {
		r := heavy[h]
		if r == nil {
			continue
		}

		delete(heavy, h)
		log.Info("SHADOW heavy-included", "hash", h.Hex(), "number", number, "arm", r.arm,
			"gasLimit", r.gas, "waitMs", ms(at, r.at), "warmed", !r.warmEnd.IsZero(),
			"warmOutcome", r.warmOutcome, "warmEndBeforeHeadMs", ms(at, r.warmEnd))
	}
}

func headTime(number uint64) time.Time {
	headMu.Lock()
	defer headMu.Unlock()

	return headAt[number]
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

		heavyMu.Lock()
		for h, r := range heavy {
			if r.at.Before(cutoff) {
				delete(heavy, h)
				log.Info("SHADOW heavy-expired", "hash", h.Hex(), "arm", r.arm, "gasLimit", r.gas)
			}
		}
		heavyMu.Unlock()
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

type runRec struct {
	start, end time.Time
	outcome    string
	marked     bool
	pass       int
}

const maxListed = 24 // cap on per-tx send/run entries kept for the log line

type txTrace struct {
	sendSrc    string // first source that offered it: idle / plan / overflow
	firstSend  time.Time
	sends      int
	sendList   []string // "src@pass@ms"
	drops      map[string]int
	runs       int
	firstStart time.Time
	lastEnd    time.Time
	outcome    string // last run outcome
	markedDone bool   // added to prefetchedTxHashes
	intrMarked int    // interrupted runs that were still marked done (gap 8)
	pfGas      uint64
	runList    []runRec
	applied    bool
	pass       int
	apply      ApplyInfo
	applyErr   string
	heavy      *heavyRec // snapshot at apply time (nil if not heavy)
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
	fillReason   string
	outcome      string
	sealSkipped  bool
	emitted      bool // printed once: a same-height rebuild flushes early
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
	buildsMu   sync.Mutex
	builds     = map[uint64]*buildTrace{}
	current    atomic.Uint64
	curStartNs atomic.Int64
)

func currentStart() time.Time {
	if ns := curStartNs.Load(); ns != 0 {
		return time.Unix(0, ns)
	}

	return time.Time{}
}

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
// the same height flushes the previous trace first. It also prints the heavy
// txs waiting in the pool at this moment (the cross-block candidates).
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
	curStartNs.Store(start.UnixNano())
	runStartHooks(number, start)

	parentHead := headTime(number - 1)
	log.Info("SHADOW build-start", "number", number, "start", start.UTC().Format(time.RFC3339Nano),
		"parentHeadMs", ms(parentHead, start), "xbwarm", XBWarm(), "gap8fix", Gap8Fix(),
		"nodiscard", NoDiscard())

	heavyMu.Lock()
	for h, r := range heavy {
		log.Info("SHADOW pool-heavy", "number", number, "hash", h.Hex(), "arm", r.arm,
			"gasLimit", r.gas, "waitMs", ms(start, r.at), "warmed", !r.warmEnd.IsZero(),
			"warmEndMs", ms(r.warmEnd, start), "warmHead", r.warmHead, "to", r.to, "sel", r.sel)
	}
	heavyMu.Unlock()

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
		now := time.Now()
		if t.sends == 0 {
			t.sendSrc, t.firstSend = src, now
		}
		t.sends++
		if len(t.sendList) < maxListed {
			t.sendList = append(t.sendList, src+"@"+itoa(len(b.passes))+"@"+ftoa(ms(now, b.start)))
		}
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

// RunStart records the start of a prefetch EVM run and returns its start.
func RunStart(number uint64, hash common.Hash) time.Time {
	now := time.Now()

	with(number, func(b *buildTrace) {
		t := b.tx(hash)
		t.runs++
		if t.firstStart.IsZero() {
			t.firstStart = now
		}
	})

	return now
}

// RunEnd records the end of one prefetch run (started at start) and whether
// it was marked done.
func RunEnd(number uint64, hash common.Hash, start time.Time, outcome string, gas uint64, markedDone bool) {
	now := time.Now()

	with(number, func(b *buildTrace) {
		t := b.tx(hash)
		t.lastEnd, t.outcome, t.pfGas = now, outcome, gas
		if len(t.runList) < maxListed {
			t.runList = append(t.runList, runRec{start: start, end: now, outcome: outcome, marked: markedDone, pass: len(b.passes)})
		}
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
func Applied(number uint64, hash common.Hash, a ApplyInfo) {
	var hr *heavyRec

	if Enabled() {
		heavyMu.Lock()
		if r := heavy[hash]; r != nil {
			c := *r
			hr = &c
		}
		heavyMu.Unlock()
	}

	with(number, func(b *buildTrace) {
		t := b.tx(hash)
		// Later passes re-meet committed txs (nonce too low); keep the
		// first successful apply.
		if t.applied && t.applyErr == "" {
			return
		}
		t.applied = true
		t.pass = len(b.passes)
		t.apply = a
		t.applyErr = ""
		if a.Err != nil {
			t.applyErr = a.Err.Error()
		}
		if hr != nil {
			t.heavy = hr
		}
	})
}

// FillEnd marks the end of the fill loop and why it ended.
func FillEnd(number uint64, reason string) {
	with(number, func(b *buildTrace) {
		b.fillEnd = time.Now()
		b.fillReason = reason
	})
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

func durMs(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// slowApply is the apply time above which a tx is always logged in txlog=slow.
const slowApply = 20 * time.Millisecond

func runListString(rs []runRec, base time.Time) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		m := "u"
		if r.marked {
			m = "m"
		}
		parts = append(parts, ftoa(ms(r.start, base))+"-"+ftoa(ms(r.end, base))+":"+r.outcome+":"+m+":p"+itoa(r.pass))
	}

	return strings.Join(parts, ";")
}

func emit(b *buildTrace) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.emitted {
		return
	}
	b.emitted = true

	all := toggle("txlog") == "all"
	heavyGas := HeavyGas()
	newHead := headTime(b.number) // the canonical block at our height

	var applied, appliedPf, neverPf, gap8Applied, doneBefore int
	var gasUsed uint64

	for hash, t := range b.txs {
		a := t.apply

		// The best evidence of a useful prefetch: a run that was marked done
		// and had ended before the apply started.
		doneBeforeApply := false
		lead := -1.0
		if t.applied {
			for _, r := range t.runList {
				if r.marked && r.outcome != OutcomeInterrupted && !r.end.After(a.Start) {
					doneBeforeApply = true
				}
			}
			if !t.firstStart.IsZero() {
				lead = ms(a.Start, t.firstStart)
			}
		}

		if t.applied {
			applied++
			if a.Err == nil {
				gasUsed += a.GasUsed
			}
			if a.Prefetched {
				appliedPf++
			}
			if t.runs == 0 {
				neverPf++
			}
			if t.intrMarked > 0 {
				gap8Applied++
			}
			if doneBeforeApply {
				doneBefore++
			}
		}

		injected := IsInjected(hash)
		isHeavy := t.heavy != nil || a.GasLimit >= heavyGas
		interesting := injected || isHeavy || a.Dur >= slowApply || (t.applied && t.runs == 0) || t.intrMarked > 0
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

		to := ""
		if a.To != nil {
			to = a.To.Hex()
		}

		arm, warmEnd, warmOutcome, warmHead := "", -1.0, "", uint64(0)
		if t.heavy != nil {
			arm, warmOutcome, warmHead = t.heavy.arm, t.heavy.warmOutcome, t.heavy.warmHead
			warmEnd = ms(t.heavy.warmEnd, b.start)
		} else if isHeavy {
			arm = Arm(hash)
		}

		log.Info("SHADOW tx", "number", b.number, "hash", hash.Hex(), "injected", injected,
			"from", a.From.Hex(), "to", to, "sel", a.Selector, "tipGwei", a.TipGwei, "gasLimit", a.GasLimit,
			"heavy", isHeavy, "arm", arm, "warmEndMs", warmEnd, "warmOutcome", warmOutcome, "warmHead", warmHead,
			"arrivalMs", ms(arrival, b.start),
			"sendSrc", t.sendSrc, "sends", t.sends, "firstSendMs", ms(t.firstSend, b.start),
			"sendList", strings.Join(t.sendList, ","),
			"drops", strings.TrimSuffix(drops, ","),
			"runs", t.runs, "pfStartMs", ms(t.firstStart, b.start), "pfEndMs", ms(t.lastEnd, b.start),
			"runList", runListString(t.runList, b.start),
			"pfOutcome", t.outcome, "pfGas", t.pfGas, "markedDone", t.markedDone, "gap8", t.intrMarked,
			"doneBeforeApply", doneBeforeApply, "leadMs", lead,
			"applied", t.applied, "pass", t.pass, "index", a.Index, "gasBefore", a.GasBefore, "gasLeft", a.GasLeft,
			"applyStartMs", ms(a.Start, b.start), "applyMs", durMs(a.Dur),
			"readMs", durMs(a.ReadDur), "prefetched", a.Prefetched,
			"gasUsed", a.GasUsed, "l1Hit", a.L1Hit, "l1Miss", a.L1Miss, "hitFromPf", a.HitFromPrefetch,
			"afterNewHead", !newHead.IsZero() && t.applied && a.Start.After(newHead),
			"result", a.Result, "applyErr", t.applyErr)
	}

	passMs := make([]string, 0, len(b.passes))
	for _, p := range b.passes {
		passMs = append(passMs, ftoa(ms(p, b.start)))
	}

	log.Info("SHADOW build", "number", b.number, "outcome", b.outcome,
		"start", b.start.UTC().Format(time.RFC3339Nano),
		"parentHeadMs", ms(headTime(b.number-1), b.start), "newHeadMs", ms(newHead, b.start),
		"startVsParentActualMs", ms(b.start, b.parentActual),
		"headerReadyMs", ms(b.headerReady, b.start), "actualTimeMs", ms(b.actualTime, b.start),
		"idleStartMs", ms(b.idleStart, b.start), "builderStartMs", ms(b.builderStart, b.start),
		"handoffMs", ms(b.handoff, b.start), "fillEndMs", ms(b.fillEnd, b.start), "fillReason", b.fillReason,
		"passes", len(b.passes), "passMs", strings.Join(passMs, ","),
		"traced", len(b.txs), "applied", applied, "gasUsed", gasUsed, "appliedPrefetched", appliedPf,
		"appliedDoneBeforeApply", doneBefore,
		"appliedNeverRun", neverPf, "gap8Runs", b.gap8, "gap8Applied", gap8Applied,
		"sealSkipped", b.sealSkipped)
}

func itoa(v int) string { return strconv.Itoa(v) }

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
