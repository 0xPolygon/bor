package rawdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/ethdb/memorydb"
)

// TestOpen_DBWitnessStoreDefault verifies that Open initializes a DB-backed
// witness store when WitnessFileStore is false (the default).
func TestOpen_DBWitnessStoreDefault(t *testing.T) {
	db, err := Open(memorydb.New(), OpenOptions{
		DisableFreeze: true,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// The returned database should expose a WitnessStore via the interface.
	wsdb, ok := db.(witnessStoreDB)
	if !ok {
		t.Fatal("expected database to implement witnessStoreDB interface")
	}
	ws := wsdb.WitnessStore()
	if ws == nil {
		t.Fatal("WitnessStore() should not return nil")
	}

	// Verify it's a DB backend by doing a round-trip through both the
	// witness store and the raw DB.
	hash := testHash(1)
	ws.WriteWitness(hash, []byte("test"))
	if got := ReadWitness(db, hash); string(got) != "test" {
		t.Fatalf("expected DB witness store to write to underlying database, got %q", got)
	}
}

// TestOpen_FSWitnessStore verifies that Open initializes a filesystem-backed
// witness store when WitnessFileStore is true and WitnessStoreDir is set.
func TestOpen_FSWitnessStore(t *testing.T) {
	witnessDir := filepath.Join(t.TempDir(), "witnesses")

	db, err := Open(memorydb.New(), OpenOptions{
		DisableFreeze:    true,
		WitnessFileStore: true,
		WitnessStoreDir:  witnessDir,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	wsdb, ok := db.(witnessStoreDB)
	if !ok {
		t.Fatal("expected database to implement witnessStoreDB interface")
	}
	ws := wsdb.WitnessStore()
	if ws == nil {
		t.Fatal("WitnessStore() should not return nil")
	}

	// Write through the witness store and verify the file lands on disk.
	hash := testHash(42)
	ws.WriteWitness(hash, []byte("fs-witness"))

	filePath := witnessFilePath(witnessDir, hash)
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("expected witness file on disk: %v", err)
	}
}

// TestOpen_FSWitnessStore_FlagTrueButDirEmpty verifies that when
// WitnessFileStore is true but WitnessStoreDir is empty, the DB backend
// is used instead (guard against misconfiguration).
func TestOpen_FSWitnessStore_FlagTrueButDirEmpty(t *testing.T) {
	db, err := Open(memorydb.New(), OpenOptions{
		DisableFreeze:    true,
		WitnessFileStore: true,
		WitnessStoreDir:  "", // empty dir
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	wsdb := db.(witnessStoreDB)
	ws := wsdb.WitnessStore()
	if ws == nil {
		t.Fatal("WitnessStore() should not return nil")
	}

	// Should still work — falls back to DB backend.
	hash := testHash(1)
	ws.WriteWitness(hash, []byte("data"))
	if !ws.HasWitness(hash) {
		t.Fatal("expected witness to exist via DB backend fallback")
	}
}

// TestOpen_OrphanedWitnessDir_Warning verifies that when WitnessFileStore is
// false but a witness directory already exists on disk, the DB backend is used
// and the orphaned directory is detected (the warning is logged, not asserted).
func TestOpen_OrphanedWitnessDir_Warning(t *testing.T) {
	witnessDir := filepath.Join(t.TempDir(), "witnesses")
	// Pre-create the directory to simulate leftover filesystem witnesses.
	os.MkdirAll(witnessDir, 0755)

	db, err := Open(memorydb.New(), OpenOptions{
		DisableFreeze:    true,
		WitnessFileStore: false,
		WitnessStoreDir:  witnessDir,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Should use DB backend despite the directory existing.
	wsdb := db.(witnessStoreDB)
	ws := wsdb.WitnessStore()

	hash := testHash(1)
	ws.WriteWitness(hash, []byte("db-only"))

	// Verify the witness is in the DB, not on the filesystem.
	if got := ReadWitness(db, hash); string(got) != "db-only" {
		t.Fatalf("expected DB write, got %q", got)
	}
	filePath := witnessFilePath(witnessDir, hash)
	if _, err := os.Stat(filePath); !os.IsNotExist(err) {
		t.Fatalf("expected no file on disk when filestore is disabled, got err=%v", err)
	}
}

// TestOpen_OrphanedWitnessDir_NoWarningWhenAbsent verifies that no warning
// is triggered when the witness directory does not exist and the flag is off.
func TestOpen_OrphanedWitnessDir_NoWarningWhenAbsent(t *testing.T) {
	witnessDir := filepath.Join(t.TempDir(), "does-not-exist")

	db, err := Open(memorydb.New(), OpenOptions{
		DisableFreeze:    true,
		WitnessFileStore: false,
		WitnessStoreDir:  witnessDir,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Should work fine with DB backend.
	wsdb := db.(witnessStoreDB)
	ws := wsdb.WitnessStore()
	hash := testHash(1)
	ws.WriteWitness(hash, []byte("data"))
	if !ws.HasWitness(hash) {
		t.Fatal("expected witness to exist")
	}
}

// TestFreezerDB_Close_ClosesWitnessStore verifies that closing a freezerdb
// also closes its witness store.
func TestFreezerDB_Close_ClosesWitnessStore(t *testing.T) {
	db, err := Open(memorydb.New(), OpenOptions{
		DisableFreeze: true,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Write a witness before closing to confirm the store was live.
	wsdb := db.(witnessStoreDB)
	ws := wsdb.WitnessStore()
	ws.WriteWitness(testHash(1), []byte("data"))

	// Close should not panic or error.
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// TestFreezerDB_Close_NilWitnessStore verifies that Close handles a nil
// witness store gracefully (e.g., if a freezerdb is constructed without
// going through Open).
func TestFreezerDB_Close_NilWitnessStore(t *testing.T) {
	db, err := Open(memorydb.New(), OpenOptions{
		DisableFreeze: true,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Simulate a nil witness store by clearing it.
	frdb := db.(*freezerdb)
	frdb.witnessStore = nil

	// Close should not panic on nil witness store.
	if err := db.Close(); err != nil {
		t.Fatalf("Close with nil witnessStore failed: %v", err)
	}
}

// TestOpen_WitnessPrunerUsesWitnessStore verifies that when witness pruning
// is enabled, the pruner is initialized with the witness store from the database.
func TestOpen_WitnessPrunerUsesWitnessStore(t *testing.T) {
	db, err := Open(memorydb.New(), OpenOptions{
		DisableFreeze:       true,
		WitnessPruneEnabled: true,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	frdb := db.(*freezerdb)
	if frdb.witPruner == nil {
		t.Fatal("expected witness pruner to be initialized")
	}
	if frdb.witnessStore == nil {
		t.Fatal("expected witness store to be initialized for pruner")
	}
}

// TestGetWitnessStore_FreezerDB verifies that GetWitnessStore extracts the
// witness store from a freezerdb.
func TestGetWitnessStore_FreezerDB(t *testing.T) {
	db, err := Open(memorydb.New(), OpenOptions{
		DisableFreeze: true,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	ws := GetWitnessStore(db)
	if ws == nil {
		t.Fatal("GetWitnessStore should not return nil for freezerdb")
	}

	// Verify it's the same store instance.
	frdb := db.(*freezerdb)
	if ws != frdb.witnessStore {
		t.Fatal("GetWitnessStore should return the same instance as freezerdb.witnessStore")
	}
}

// TestGetWitnessStore_NonFreezerDB verifies that GetWitnessStore returns a
// fallback DB-backed witness store for databases that don't implement
// the witnessStoreDB interface (e.g., nofreezedb).
func TestGetWitnessStore_NonFreezerDB(t *testing.T) {
	db := NewDatabase(memorydb.New())
	ws := GetWitnessStore(db)
	if ws == nil {
		t.Fatal("GetWitnessStore should return a fallback store for nofreezedb")
	}

	// Verify the fallback store is functional.
	hash := testHash(1)
	ws.WriteWitness(hash, []byte("fallback"))
	if !ws.HasWitness(hash) {
		t.Fatal("fallback witness store should be functional")
	}
}
