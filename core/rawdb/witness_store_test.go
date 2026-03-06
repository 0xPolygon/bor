package rawdb

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// testHash returns a deterministic hash from an integer seed.
func testHash(i int) common.Hash {
	var h common.Hash
	h[0] = byte(i >> 24)
	h[1] = byte(i >> 16)
	h[2] = byte(i >> 8)
	h[3] = byte(i)
	return h
}

// crudTest exercises the full Create / Read / Has / Delete lifecycle on a WitnessStore.
func crudTest(t *testing.T, ws WitnessStore) {
	t.Helper()

	hash := testHash(1)
	data := []byte("witness-payload")

	// Initially empty.
	if ws.HasWitness(hash) {
		t.Fatal("HasWitness should return false for non-existent witness")
	}
	if got := ws.ReadWitness(hash); got != nil {
		t.Fatalf("ReadWitness should return nil, got %x", got)
	}

	// Write and verify.
	ws.WriteWitness(hash, data)
	if !ws.HasWitness(hash) {
		t.Fatal("HasWitness should return true after write")
	}
	got := ws.ReadWitness(hash)
	if string(got) != string(data) {
		t.Fatalf("ReadWitness mismatch: want %q, got %q", data, got)
	}

	// Delete and verify.
	ws.DeleteWitness(hash)
	if ws.HasWitness(hash) {
		t.Fatal("HasWitness should return false after delete")
	}
	if got := ws.ReadWitness(hash); got != nil {
		t.Fatalf("ReadWitness should return nil after delete, got %x", got)
	}
}

func TestDBWitnessStore_CRUD(t *testing.T) {
	db := NewMemoryDatabase()
	ws := NewDBWitnessStore(db)
	crudTest(t, ws)
}

func TestFSWitnessStore_CRUD(t *testing.T) {
	dir := t.TempDir()
	db := NewMemoryDatabase()
	ws := NewFSWitnessStore(dir, db)
	crudTest(t, ws)
}

func TestFSWitnessStore_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	db := NewMemoryDatabase()
	ws := NewFSWitnessStore(dir, db)

	hash := testHash(42)
	data := []byte("some-witness-data")

	ws.WriteWitness(hash, data)

	// Verify the final file exists and no .tmp file remains.
	finalPath := witnessFilePath(dir, hash)
	tmpPath := finalPath + ".tmp"

	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("expected final witness file to exist: %v", err)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("expected no .tmp file after write, got err=%v", err)
	}
}

func TestFSWitnessStore_ShardLayout(t *testing.T) {
	dir := t.TempDir()
	db := NewMemoryDatabase()
	ws := NewFSWitnessStore(dir, db)

	hash := testHash(0xABCD)
	ws.WriteWitness(hash, []byte("payload"))

	// Verify 2-level shard directory structure.
	hex := common.Bytes2Hex(hash[:])
	shard1 := filepath.Join(dir, hex[:2])
	shard2 := filepath.Join(shard1, hex[2:4])

	if _, err := os.Stat(shard2); err != nil {
		t.Fatalf("expected shard directory %s to exist: %v", shard2, err)
	}

	// File should be under the shard directory.
	entries, _ := os.ReadDir(shard2)
	if len(entries) != 1 {
		t.Fatalf("expected 1 file in shard dir, got %d", len(entries))
	}
}

func TestFSWitnessStore_EmptyDirCleanup(t *testing.T) {
	dir := t.TempDir()
	db := NewMemoryDatabase()
	ws := NewFSWitnessStore(dir, db)

	hash := testHash(99)
	ws.WriteWitness(hash, []byte("data"))

	// Delete the witness; shard dirs should be cleaned up if empty.
	ws.DeleteWitness(hash)

	shardDir := witnessDir(dir, hash)
	if _, err := os.Stat(shardDir); !os.IsNotExist(err) {
		t.Fatalf("expected shard dir to be removed after last file deleted, got err=%v", err)
	}
}

func TestFSWitnessStore_ReadNonExistent(t *testing.T) {
	dir := t.TempDir()
	db := NewMemoryDatabase()
	ws := NewFSWitnessStore(dir, db)

	// Reading a non-existent witness should return nil, not panic.
	got := ws.ReadWitness(testHash(999))
	if got != nil {
		t.Fatalf("expected nil for non-existent witness, got %x", got)
	}
}

func TestFSWitnessStore_SizeMetadataInPebble(t *testing.T) {
	dir := t.TempDir()
	db := NewMemoryDatabase()
	ws := NewFSWitnessStore(dir, db)

	hash := testHash(7)
	data := make([]byte, 1234)
	ws.WriteWitness(hash, data)

	// Size metadata should be in the database.
	sizePtr := ReadWitnessSize(db, hash)
	if sizePtr == nil {
		t.Fatal("expected witness size metadata in database")
	}
	if *sizePtr != 1234 {
		t.Fatalf("expected witness size 1234, got %d", *sizePtr)
	}
}

func TestFSWitnessStore_FallbackToPebble(t *testing.T) {
	dir := t.TempDir()
	db := NewMemoryDatabase()

	hash := testHash(10)
	data := []byte("legacy-pebble-witness")

	// Write directly to Pebble (simulates pre-migration data).
	WriteWitness(db, hash, data)

	// Create FS store; it should fall back to Pebble on read.
	ws := NewFSWitnessStore(dir, db)

	if !ws.HasWitness(hash) {
		t.Fatal("HasWitness should return true for Pebble-stored witness")
	}
	got := ws.ReadWitness(hash)
	if string(got) != string(data) {
		t.Fatalf("ReadWitness fallback mismatch: want %q, got %q", data, got)
	}

	// Delete should clean both FS and Pebble.
	ws.DeleteWitness(hash)
	if HasWitness(db, hash) {
		t.Fatal("Pebble witness data should be deleted")
	}
}

func TestFSWitnessStore_ConcurrentReadWrite(t *testing.T) {
	dir := t.TempDir()
	db := NewMemoryDatabase()
	ws := NewFSWitnessStore(dir, db)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n * 2)

	// Concurrent writers.
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			hash := testHash(i)
			ws.WriteWitness(hash, []byte{byte(i)})
		}(i)
	}

	// Concurrent readers (may read before write; that's OK).
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			hash := testHash(i)
			ws.ReadWitness(hash)
			ws.HasWitness(hash)
		}(i)
	}

	wg.Wait()

	// Verify all writes succeeded.
	for i := 0; i < n; i++ {
		hash := testHash(i)
		if !ws.HasWitness(hash) {
			t.Fatalf("expected witness %d to exist after concurrent write", i)
		}
	}
}

func TestFSWitnessStore_TempFileCleanup(t *testing.T) {
	dir := t.TempDir()
	db := NewMemoryDatabase()

	// Create an orphaned .tmp file before initializing the store.
	hash := testHash(1)
	shardDir := witnessDir(dir, hash)
	os.MkdirAll(shardDir, 0755)
	tmpPath := witnessFilePath(dir, hash) + ".tmp"
	os.WriteFile(tmpPath, []byte("orphan"), 0644)

	// NewFSWitnessStore should clean it up.
	_ = NewFSWitnessStore(dir, db)

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("expected orphaned .tmp file to be cleaned up, got err=%v", err)
	}
}

func TestFSWitnessStore_Close(t *testing.T) {
	dir := t.TempDir()
	db := NewMemoryDatabase()
	ws := NewFSWitnessStore(dir, db)

	if err := ws.Close(); err != nil {
		t.Fatalf("Close should return nil, got %v", err)
	}

	// Close should be idempotent.
	if err := ws.Close(); err != nil {
		t.Fatalf("second Close should return nil, got %v", err)
	}
}

// TestFSWitnessStore_DeletePermissionError exercises the os.Remove error path
// in DeleteWitness where the error is NOT os.IsNotExist (e.g., permission denied).
// The store should log the error but not panic.
func TestFSWitnessStore_DeletePermissionError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based test not reliable on Windows")
	}

	dir := t.TempDir()
	db := NewMemoryDatabase()
	ws := NewFSWitnessStore(dir, db)

	hash := testHash(77)
	ws.WriteWitness(hash, []byte("data"))

	// Make the shard directory read-only so os.Remove gets EACCES, not ENOENT.
	shardDir := witnessDir(dir, hash)
	os.Chmod(shardDir, 0555)
	t.Cleanup(func() { os.Chmod(shardDir, 0755) })

	// DeleteWitness should not panic; it logs the permission error and continues.
	ws.DeleteWitness(hash)

	// The file should still exist on disk because removal failed.
	filePath := witnessFilePath(dir, hash)
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file should still exist after failed delete, got err=%v", err)
	}

	// But Pebble data should still be cleaned up.
	if HasWitness(db, hash) {
		t.Fatal("Pebble witness data should be deleted even when fs delete fails")
	}
}

// TestFSWitnessStore_CleanupSkipsUnreadableEntries exercises the WalkFunc error
// path where an entry in the witness directory is unreadable. The cleanup should
// skip it without panicking.
func TestFSWitnessStore_CleanupSkipsUnreadableEntries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based test not reliable on Windows")
	}

	dir := t.TempDir()
	db := NewMemoryDatabase()

	// Create a valid .tmp file that should be cleaned up.
	hash1 := testHash(1)
	shard1 := witnessDir(dir, hash1)
	os.MkdirAll(shard1, 0755)
	tmpPath1 := witnessFilePath(dir, hash1) + ".tmp"
	os.WriteFile(tmpPath1, []byte("orphan1"), 0644)

	// Create an unreadable subdirectory. Walk will get a permission error
	// when trying to read its contents and should skip it.
	unreadableDir := filepath.Join(dir, "00", "ff")
	os.MkdirAll(unreadableDir, 0755)
	os.WriteFile(filepath.Join(unreadableDir, "test.tmp"), []byte("hidden"), 0644)
	os.Chmod(unreadableDir, 0000)
	t.Cleanup(func() { os.Chmod(unreadableDir, 0755) })

	// NewFSWitnessStore calls cleanupTempFiles; should not panic.
	_ = NewFSWitnessStore(dir, db)

	// The accessible .tmp file should still be cleaned up.
	if _, err := os.Stat(tmpPath1); !os.IsNotExist(err) {
		t.Fatalf("expected accessible .tmp to be cleaned up, got err=%v", err)
	}
}

// TestFSWitnessStore_CleanupNonRemovableTmpFile exercises the path where
// os.Remove fails on a .tmp file during cleanup (e.g., the file became
// read-only between Walk's Lstat and the Remove call).
func TestFSWitnessStore_CleanupNonRemovableTmpFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based test not reliable on Windows")
	}

	dir := t.TempDir()
	db := NewMemoryDatabase()

	// Create a .tmp file, then make its parent directory read-only
	// so os.Remove will fail with EACCES.
	hash := testHash(50)
	shard := witnessDir(dir, hash)
	os.MkdirAll(shard, 0755)
	tmpPath := witnessFilePath(dir, hash) + ".tmp"
	os.WriteFile(tmpPath, []byte("stuck"), 0644)
	os.Chmod(shard, 0555)
	t.Cleanup(func() { os.Chmod(shard, 0755) })

	// NewFSWitnessStore calls cleanupTempFiles; should log a warning
	// but not panic.
	_ = NewFSWitnessStore(dir, db)

	// Restore permissions and verify the .tmp file is still there
	// (removal failed as expected).
	os.Chmod(shard, 0755)
	if _, err := os.Stat(tmpPath); err != nil {
		t.Fatalf("expected .tmp file to survive failed cleanup, got err=%v", err)
	}
}

// TestFSWitnessStore_CleanupNonExistentDir exercises cleanupTempFiles when
// the witness directory does not exist yet. Walk receives an error for the
// root path and the WalkFunc returns nil to skip it.
func TestFSWitnessStore_CleanupNonExistentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	db := NewMemoryDatabase()

	// Should not panic or error; the directory simply doesn't exist yet.
	ws := NewFSWitnessStore(dir, db)

	// The store should still be functional once a write creates the directory.
	hash := testHash(1)
	ws.WriteWitness(hash, []byte("data"))
	if !ws.HasWitness(hash) {
		t.Fatal("HasWitness should return true after write to initially non-existent dir")
	}
}

// --- Subprocess tests for log.Crit paths ---
//
// log.Crit calls os.Exit(1), so these tests spawn a subprocess that runs
// the crashing code path and verify it exits non-zero.

// runCrashTest re-executes the current test binary in a subprocess with
// the given test function name and WITNESS_CRASH_TEST=1 set.
// Returns true if the subprocess exited with a non-zero status.
func runCrashTest(t *testing.T, testName string) bool {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+testName+"$")
	cmd.Env = append(os.Environ(), "WITNESS_CRASH_TEST=1")
	err := cmd.Run()
	if err == nil {
		return false // process exited 0 — no crash
	}
	if _, ok := err.(*exec.ExitError); ok {
		return true // non-zero exit — expected crash
	}
	t.Fatalf("unexpected error running subprocess: %v", err)
	return false
}

// TestFSWitnessStore_CritOnMkdirAllFailure verifies that WriteWitness
// calls log.Crit (exits non-zero) when os.MkdirAll fails.
func TestFSWitnessStore_CritOnMkdirAllFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based test not reliable on Windows")
	}
	if os.Getenv("WITNESS_CRASH_TEST") == "1" {
		// Subprocess: write to a path where MkdirAll will fail.
		db := NewMemoryDatabase()
		// Use a regular file as the base dir so MkdirAll fails with ENOTDIR.
		f, _ := os.CreateTemp("", "witness-crash-*") //nolint:usetesting // subprocess exits via log.Crit; t.TempDir cleanup won't run
		f.Close()
		defer os.Remove(f.Name())
		ws := NewFSWitnessStore(f.Name(), db)
		ws.WriteWitness(testHash(1), []byte("data"))
		return
	}
	if !runCrashTest(t, "TestFSWitnessStore_CritOnMkdirAllFailure") {
		t.Fatal("expected subprocess to exit non-zero on MkdirAll failure")
	}
}

// TestFSWitnessStore_CritOnWriteFileFailure verifies that WriteWitness
// calls log.Crit when os.WriteFile fails.
func TestFSWitnessStore_CritOnWriteFileFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based test not reliable on Windows")
	}
	if os.Getenv("WITNESS_CRASH_TEST") == "1" {
		// Subprocess: create the shard dir as read-only so WriteFile fails.
		dir, _ := os.MkdirTemp("", "witness-crash-*") //nolint:usetesting // subprocess exits via log.Crit; t.TempDir cleanup won't run
		defer os.RemoveAll(dir)
		db := NewMemoryDatabase()
		ws := NewFSWitnessStore(dir, db)
		hash := testHash(1)
		shard := witnessDir(dir, hash)
		os.MkdirAll(shard, 0755)
		os.Chmod(shard, 0555)
		ws.WriteWitness(hash, []byte("data"))
		return
	}
	if !runCrashTest(t, "TestFSWitnessStore_CritOnWriteFileFailure") {
		t.Fatal("expected subprocess to exit non-zero on WriteFile failure")
	}
}

// TestFSWitnessStore_CritOnRenameFailure verifies that WriteWitness
// calls log.Crit when os.Rename fails.
func TestFSWitnessStore_CritOnRenameFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-based test not reliable on Windows")
	}
	if os.Getenv("WITNESS_CRASH_TEST") == "1" {
		// Subprocess: write the temp file, then remove the shard dir before
		// rename can happen. We achieve this by making the shard dir read-only
		// after the temp file is written. Since WriteWitness does MkdirAll,
		// WriteFile, then Rename atomically, we use a file as the final path
		// to force the rename to fail: create a subdirectory at the final path.
		dir, _ := os.MkdirTemp("", "witness-crash-*") //nolint:usetesting // subprocess exits via log.Crit; t.TempDir cleanup won't run
		defer os.RemoveAll(dir)
		db := NewMemoryDatabase()
		hash := testHash(1)
		shard := witnessDir(dir, hash)
		os.MkdirAll(shard, 0755)
		// Create a directory at the final path — os.Rename of file to dir fails.
		finalPath := witnessFilePath(dir, hash)
		os.MkdirAll(finalPath, 0755)
		// Place a file inside to prevent removal.
		os.WriteFile(filepath.Join(finalPath, "blocker"), []byte("x"), 0644)
		ws := NewFSWitnessStore(dir, db)
		ws.WriteWitness(hash, []byte("data"))
		return
	}
	if !runCrashTest(t, "TestFSWitnessStore_CritOnRenameFailure") {
		t.Fatal("expected subprocess to exit non-zero on Rename failure")
	}
}

// TestFSWitnessStore_CritOnDBPutFailure verifies that WriteWitness
// calls log.Crit when the database Put for size metadata fails.
func TestFSWitnessStore_CritOnDBPutFailure(t *testing.T) {
	if os.Getenv("WITNESS_CRASH_TEST") == "1" {
		// Subprocess: use a database that has been closed so Put fails.
		dir, _ := os.MkdirTemp("", "witness-crash-*") //nolint:usetesting // subprocess exits via log.Crit; t.TempDir cleanup won't run
		defer os.RemoveAll(dir)
		db := NewMemoryDatabase()
		ws := NewFSWitnessStore(dir, db)
		db.Close()
		ws.WriteWitness(testHash(1), []byte("data"))
		return
	}
	if !runCrashTest(t, "TestFSWitnessStore_CritOnDBPutFailure") {
		t.Fatal("expected subprocess to exit non-zero on DB Put failure")
	}
}
