package stallrec

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reset puts the package back to its default, disabled state after a test.
// The flight recorder and watchdog keep running; they are harmless when
// nothing is active.
func reset(t *testing.T) {
	t.Helper()
	saved := map[string]time.Duration{}
	for k, v := range thresholds {
		saved[k] = v
	}
	t.Cleanup(func() {
		enabled.Store(false)
		mu.Lock()
		active = map[uint64]*Op{}
		for k, v := range saved {
			thresholds[k] = v
		}
		mu.Unlock()
		dumpMu.Lock()
		lastTraceAt, lastStacksAt = time.Time{}, time.Time{}
		dumpMu.Unlock()
	})
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func waitForFile(t *testing.T, dir, suffix string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), suffix) {
				return filepath.Join(dir, e.Name())
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no *%s file in %s after %v", suffix, dir, timeout)
	return ""
}

func TestDisabledIsNoop(t *testing.T) {
	reset(t)
	enabled.Store(false)

	op := Begin(KindFinalize, 1)
	if op != nil {
		t.Fatal("Begin returned an op while disabled")
	}
	if d := op.End(); d != 0 {
		t.Fatalf("End on nil op = %v, want 0", d)
	}
}

func TestSetupEmptyDirStaysDisabled(t *testing.T) {
	reset(t)
	enabled.Store(false)

	setup("", env(nil))
	if enabled.Load() {
		t.Fatal("setup with empty dir enabled recording")
	}
}

func TestThresholdOverride(t *testing.T) {
	reset(t)
	setup(t.TempDir(), env(map[string]string{
		"BOR_STALLREC_FINALIZE_MS": "123",
		"BOR_STALLREC_IMPORT_MS":   "not-a-number",
		"BOR_STALLREC_BUILD_MS":    "-5",
	}))
	if got := thresholds[KindFinalize]; got != 123*time.Millisecond {
		t.Fatalf("finalize threshold = %v, want 123ms", got)
	}
	if got := thresholds[KindImport]; got != 1500*time.Millisecond {
		t.Fatalf("import threshold = %v, want default 1.5s for an invalid value", got)
	}
	if got := thresholds[KindBuild]; got != 2500*time.Millisecond {
		t.Fatalf("build threshold = %v, want default 2.5s for a negative value", got)
	}
}

func TestFastOperationWritesNothing(t *testing.T) {
	reset(t)
	dir := t.TempDir()
	setup(dir, env(map[string]string{"BOR_STALLREC_FINALIZE_MS": "500"}))

	Begin(KindFinalize, 1).End()
	time.Sleep(250 * time.Millisecond) // > two watchdog ticks

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("fast operation wrote %d files, want 0", len(entries))
	}
}

func TestSlowOperationDumpsStacksAndTrace(t *testing.T) {
	reset(t)
	dir := t.TempDir()
	setup(dir, env(map[string]string{"BOR_STALLREC_FINALIZE_MS": "50"}))

	op := Begin(KindFinalize, 94320763)
	// The watchdog must write the stacks while the operation is still running.
	stacks := waitForFile(t, dir, ".midstall.stacks", 2*time.Second)
	body, err := os.ReadFile(stacks)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "# kind=finalize number=94320763") {
		t.Fatalf("stacks header missing, got:\n%.300s", body)
	}
	if !strings.Contains(string(body), "goroutine ") {
		t.Fatal("stacks file has no goroutine dump")
	}
	if !strings.Contains(filepath.Base(stacks), "-finalize-94320763.") {
		t.Fatalf("unexpected stacks file name %q", filepath.Base(stacks))
	}

	if d := op.End(); d < 50*time.Millisecond {
		t.Fatalf("End = %v, want >= threshold", d)
	}
	if fr == nil {
		t.Skip("flight recorder not available")
	}
	trace := waitForFile(t, dir, "ms.trace", time.Second)
	if info, err := os.Stat(trace); err != nil || info.Size() == 0 {
		t.Fatalf("trace file %s empty or unreadable: %v", trace, err)
	}
}

func TestEndIsIdempotent(t *testing.T) {
	reset(t)
	setup(t.TempDir(), env(nil))

	op := Begin(KindImport, 7)
	op.End()
	op.End()

	mu.Lock()
	n := len(active)
	mu.Unlock()
	if n != 0 {
		t.Fatalf("%d ops still active after End, want 0", n)
	}
}

func TestSetupBadDirStaysDisabled(t *testing.T) {
	reset(t)
	enabled.Store(false)

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	setup(filepath.Join(file, "sub"), env(nil)) // parent is a file: MkdirAll fails
	if enabled.Load() {
		t.Fatal("setup enabled recording although the dir could not be created")
	}
}

func TestStackDumpsAreRateLimited(t *testing.T) {
	reset(t)
	dir := t.TempDir()
	setup(dir, env(nil))

	op := &Op{kind: KindImport, num: 1, start: time.Now()}
	dumpStacks(dir, op)
	dumpStacks(dir, &Op{kind: KindImport, num: 2, start: time.Now()})

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("got %d stack files within 2s, want 1", len(entries))
	}
}

func TestTraceDumpsAreRateLimited(t *testing.T) {
	reset(t)
	dir := t.TempDir()
	setup(dir, env(nil))
	if fr == nil {
		t.Skip("flight recorder not available")
	}
	dumpTrace(dir, KindImport, 1, 2*time.Second)
	dumpTrace(dir, KindImport, 2, 2*time.Second)

	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "-import-1-2000ms.trace") {
		t.Fatalf("got %v, want exactly one trace for block 1", entries)
	}
}

func TestDumpsToMissingDirWriteNothing(t *testing.T) {
	reset(t)
	setup(t.TempDir(), env(nil))
	missing := filepath.Join(t.TempDir(), "gone")

	dumpStacks(missing, &Op{kind: KindBuild, num: 1, start: time.Now()})
	dumpTrace(missing, KindBuild, 1, 3*time.Second)
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("dump created %s: %v", missing, err)
	}
}

func TestWriteFileRemovesTempOnError(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "x.stacks")
	wantErr := os.ErrClosed
	if err := writeFile(name, func(io.Writer) error { return wantErr }); err != wantErr {
		t.Fatalf("writeFile error = %v, want %v", err, wantErr)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("left %d files after a failed write, want 0", len(entries))
	}
}

func TestWriteFileRenamesWhenComplete(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "x.stacks")
	err := writeFile(name, func(w io.Writer) error {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Error("final file visible before the write finished")
		}
		_, err := w.Write([]byte("ok"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(name); string(b) != "ok" {
		t.Fatalf("content = %q, want %q", b, "ok")
	}
	if _, err := os.Stat(name + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
}
