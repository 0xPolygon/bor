// Package stallrec captures evidence for multi-second stalls on the block
// production and import paths.
//
// It is inert unless BOR_STALLREC_DIR is set. When enabled it keeps a
// runtime/trace flight recorder running (the last ~20s of execution trace in
// memory) and tracks in-flight operations (build, finalize, pending, import).
// Two artifacts are written per stall:
//
//   - <ts>-<kind>-<num>.midstall.stacks: all goroutine stacks, taken by a
//     watchdog while the operation is still running past its threshold, so it
//     shows who is blocked on what at the moment of the stall.
//   - <ts>-<kind>-<num>-<dur>.trace: the flight-recorder window, written when
//     the operation finishes over its threshold. Analyse with
//     `go tool trace -pprof=sync|syscall|sched <file>`.
//
// Default thresholds: build 2.5s, finalize and pending 700ms, import 1.5s;
// override with BOR_STALLREC_<KIND>_MS. Stack dumps are written at most every
// 2s and traces at most every 10s. A stack dump briefly stops the world.
package stallrec

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/pprof"
	"runtime/trace"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

// Operation kinds.
const (
	KindBuild    = "build"
	KindFinalize = "finalize"
	KindPending  = "pending"
	KindImport   = "import"
)

// Default thresholds: an operation running longer than this is a stall.
var thresholds = map[string]time.Duration{
	KindBuild:    2500 * time.Millisecond,
	KindFinalize: 700 * time.Millisecond,
	KindPending:  700 * time.Millisecond,
	KindImport:   1500 * time.Millisecond,
}

var (
	dir     string
	enabled atomic.Bool
	fr      *trace.FlightRecorder

	mu     sync.Mutex
	active = map[uint64]*Op{}
	nextID uint64

	dumpMu       sync.Mutex
	lastTraceAt  time.Time
	lastStacksAt time.Time
)

// Op is one in-flight tracked operation.
type Op struct {
	id      uint64
	kind    string
	num     uint64
	start   time.Time
	stacked bool
	ended   bool
}

func init() {
	setup(os.Getenv("BOR_STALLREC_DIR"), os.Getenv)
}

var watchdogOnce sync.Once

// setup enables recording into dirPath. Thresholds can be overridden with
// BOR_STALLREC_<KIND>_MS, e.g. BOR_STALLREC_FINALIZE_MS (read through
// getenv). An empty dirPath leaves the package disabled.
func setup(dirPath string, getenv func(string) string) {
	if dirPath == "" {
		return
	}
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		log.Error("stallrec: cannot create dir", "dir", dirPath, "err", err)
		return
	}
	mu.Lock() // the watchdog reads dir and thresholds under mu
	dir = dirPath
	for kind := range thresholds {
		if v := getenv("BOR_STALLREC_" + strings.ToUpper(kind) + "_MS"); v != "" {
			if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
				thresholds[kind] = time.Duration(ms) * time.Millisecond
			}
		}
	}
	mu.Unlock()
	if fr == nil {
		fr = trace.NewFlightRecorder(trace.FlightRecorderConfig{
			MinAge:   20 * time.Second,
			MaxBytes: 512 << 20,
		})
		if err := fr.Start(); err != nil {
			log.Error("stallrec: flight recorder start failed", "err", err)
			fr = nil
		}
	}
	enabled.Store(true)
	watchdogOnce.Do(func() { go watchdog() })
}

// announce logs the configuration once, on first use: package init runs
// before bor installs its log handler, so a log line there would be lost.
var announce sync.Once

// Begin registers an in-flight operation. It returns nil when disabled; a nil
// *Op is safe to End.
func Begin(kind string, num uint64) *Op {
	if !enabled.Load() {
		return nil
	}
	announce.Do(func() {
		log.Info("stallrec: enabled", "dir", dir, "flightRecorder", fr != nil, "thresholds", fmt.Sprint(thresholds))
	})
	mu.Lock()
	nextID++
	op := &Op{id: nextID, kind: kind, num: num, start: time.Now()}
	active[op.id] = op
	mu.Unlock()
	return op
}

// End unregisters the operation and, if it exceeded its threshold, writes the
// flight-recorder window. It returns the operation's duration.
func (op *Op) End() time.Duration {
	if op == nil {
		return 0
	}
	d := time.Since(op.start)
	mu.Lock()
	if op.ended {
		mu.Unlock()
		return d
	}
	op.ended = true
	delete(active, op.id)
	slow, outDir := d >= thresholds[op.kind], dir
	mu.Unlock()
	if slow {
		log.Warn("stallrec: slow operation", "kind", op.kind, "number", op.num, "elapsed", d)
		go dumpTrace(outDir, op.kind, op.num, d)
	}
	return d
}

func watchdog() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for range t.C {
		var due []*Op
		mu.Lock()
		outDir := dir
		for _, op := range active {
			if !op.stacked && time.Since(op.start) >= thresholds[op.kind] {
				op.stacked = true
				due = append(due, op)
			}
		}
		mu.Unlock()
		for _, op := range due {
			dumpStacks(outDir, op)
		}
	}
}

func stamp() string { return time.Now().UTC().Format("20060102T150405.000Z") }

func dumpStacks(dir string, op *Op) {
	dumpMu.Lock()
	if time.Since(lastStacksAt) < 2*time.Second {
		dumpMu.Unlock()
		return
	}
	lastStacksAt = time.Now()
	dumpMu.Unlock()

	name := filepath.Join(dir, fmt.Sprintf("%s-%s-%d.midstall.stacks", stamp(), op.kind, op.num))
	err := writeFile(name, func(w io.Writer) error {
		fmt.Fprintf(w, "# kind=%s number=%d running=%s\n", op.kind, op.num, time.Since(op.start))
		mu.Lock()
		for _, o := range active {
			fmt.Fprintf(w, "# active kind=%s number=%d running=%s\n", o.kind, o.num, time.Since(o.start))
		}
		mu.Unlock()
		return pprof.Lookup("goroutine").WriteTo(w, 2)
	})
	if err != nil {
		log.Error("stallrec: write stacks", "err", err)
		return
	}
	log.Warn("stallrec: mid-stall goroutine dump", "kind", op.kind, "number", op.num, "file", name)
}

// dumpTrace writes the flight-recorder window. It runs in its own goroutine so
// the (possibly large) write never delays the operation that stalled.
func dumpTrace(dir string, kind string, num uint64, d time.Duration) {
	if fr == nil {
		return
	}
	dumpMu.Lock()
	// The window is 20s; one dump covers every stall inside it.
	if time.Since(lastTraceAt) < 10*time.Second {
		dumpMu.Unlock()
		return
	}
	lastTraceAt = time.Now()
	dumpMu.Unlock()

	name := filepath.Join(dir, fmt.Sprintf("%s-%s-%d-%dms.trace", stamp(), kind, num, d.Milliseconds()))
	err := writeFile(name, func(w io.Writer) error {
		_, err := fr.WriteTo(w)
		return err
	})
	if err != nil {
		log.Error("stallrec: write trace", "err", err)
		return
	}
	log.Warn("stallrec: flight recorder dumped", "kind", kind, "number", num, "elapsed", d, "file", name)
}

// writeFile writes to a temporary file and renames it to name when complete,
// so a collector never picks up a partial dump.
func writeFile(name string, write func(io.Writer) error) error {
	tmp := name + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, name)
}
