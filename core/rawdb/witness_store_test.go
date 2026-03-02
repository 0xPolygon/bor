package rawdb

import (
	"os"
	"path/filepath"
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
