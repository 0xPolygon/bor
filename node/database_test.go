package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/ethdb/memorydb"
	"github.com/ethereum/go-ethereum/p2p"
)

// newTestNode creates a Node with a temporary DataDir for database tests.
func newTestNode(t *testing.T) *Node {
	t.Helper()
	cfg := &Config{
		Name:    "test",
		DataDir: t.TempDir(),
		P2P:     p2p.Config{MaxPeers: 0},
	}
	n, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create test node: %v", err)
	}
	t.Cleanup(func() { n.Close() })
	return n
}

// TestOpenDatabase_WitnessFileStore_ResolvesDir verifies that when
// WitnessFileStore is true, openDatabase resolves the witness store
// directory to <datadir>/<dbname>/witnesses and the FS backend is used.
func TestOpenDatabase_WitnessFileStore_ResolvesDir(t *testing.T) {
	n := newTestNode(t)

	db, err := n.OpenDatabaseWithOptions("chaindata", DatabaseOptions{
		DisableFreeze:    true,
		WitnessFileStore: true,
	})
	if err != nil {
		t.Fatalf("OpenDatabaseWithOptions failed: %v", err)
	}
	defer db.Close()

	// The witness store should be an FS backend. Verify by writing
	// through it and checking the file lands on disk.
	ws := rawdb.GetWitnessStore(db)

	hash := [32]byte{0xab, 0xcd}
	ws.WriteWitness(hash, []byte("test-witness"))

	// The witness directory should be at <datadir>/chaindata/witnesses.
	witnessDir := filepath.Join(n.ResolvePath("chaindata"), "witnesses")
	if _, err := os.Stat(witnessDir); err != nil {
		t.Fatalf("expected witness directory to exist at %s: %v", witnessDir, err)
	}
}

// TestOpenDatabase_WitnessFileStore_Disabled verifies that when
// WitnessFileStore is false, witnessStoreDir is empty and the DB
// backend is used.
func TestOpenDatabase_WitnessFileStore_Disabled(t *testing.T) {
	n := newTestNode(t)

	db, err := n.OpenDatabaseWithOptions("chaindata", DatabaseOptions{
		DisableFreeze:    true,
		WitnessFileStore: false,
	})
	if err != nil {
		t.Fatalf("OpenDatabaseWithOptions failed: %v", err)
	}
	defer db.Close()

	// Write through the witness store and verify it goes to the DB,
	// not the filesystem.
	ws := rawdb.GetWitnessStore(db)

	hash := [32]byte{0xab, 0xcd}
	ws.WriteWitness(hash, []byte("db-witness"))

	// No witnesses directory should be created.
	witnessDir := filepath.Join(n.ResolvePath("chaindata"), "witnesses")
	if _, err := os.Stat(witnessDir); !os.IsNotExist(err) {
		t.Fatalf("expected no witness directory when filestore is disabled, got err=%v", err)
	}

	// Witness should be readable from the DB.
	got := ws.ReadWitness(hash)
	if string(got) != "db-witness" {
		t.Fatalf("expected DB witness, got %q", got)
	}
}

// TestOpenDatabase_InMemory_NoWitnessFileStore verifies that when DataDir
// is empty (in-memory mode), WitnessFileStore is not used regardless of
// the flag, because there is no directory to resolve.
func TestOpenDatabase_InMemory_NoWitnessFileStore(t *testing.T) {
	cfg := &Config{
		Name:    "test",
		DataDir: "", // in-memory mode
		P2P:     p2p.Config{MaxPeers: 0},
	}
	n, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create test node: %v", err)
	}
	defer n.Close()

	db, err := n.OpenDatabaseWithOptions("chaindata", DatabaseOptions{
		WitnessFileStore: true, // should be ignored in-memory
	})
	if err != nil {
		t.Fatalf("OpenDatabaseWithOptions failed: %v", err)
	}
	defer db.Close()

	// Should still work with DB backend.
	ws := rawdb.GetWitnessStore(db)

	hash := [32]byte{0x01}
	ws.WriteWitness(hash, []byte("mem-witness"))
	if !ws.HasWitness(hash) {
		t.Fatal("expected witness to exist in memory DB")
	}
}

// TestCloseTrackingDB_WitnessStore_NilForNonFreezerDB verifies that the
// closeTrackingDB.WitnessStore() method returns nil when the inner database
// does not implement the witnessStoreDB interface (e.g., nofreezedb).
func TestCloseTrackingDB_WitnessStore_NilForNonFreezerDB(t *testing.T) {
	n, err := New(&Config{
		Name:    "test",
		DataDir: t.TempDir(),
		P2P:     p2p.Config{MaxPeers: 0},
	})
	if err != nil {
		t.Fatalf("failed to create test node: %v", err)
	}
	defer n.Close()

	// nofreezedb does not implement WitnessStore().
	inner := rawdb.NewDatabase(memorydb.New())
	wrapper := &closeTrackingDB{Database: inner, n: n}

	if ws := wrapper.WitnessStore(); ws != nil {
		t.Fatal("expected nil WitnessStore for nofreezedb wrapped in closeTrackingDB")
	}
}
