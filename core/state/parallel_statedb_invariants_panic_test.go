//go:build invariants

package state

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/triedb"
)

// TestInvariant_SettleNotPanicked verifies the runtime assertion under
// `-tags invariants` actually fires when SettleTo is invoked on a
// panicked PDB. This pins the assertion as a working safety net rather
// than a comment that drifts out of sync with the code it's protecting.
//
// Production builds (no tag) compile this file out entirely.
func TestInvariant_SettleNotPanicked(t *testing.T) {
	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	sdb, err := New(types.EmptyRootHash, NewDatabase(tdb, nil))
	if err != nil {
		t.Fatal(err)
	}
	pdb, _, _ := newTestPDB(t, 0)
	pdb.Panicked = true

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected SettleTo to panic on a panicked PDB under -tags invariants")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "panicked ParallelStateDB") {
			t.Fatalf("unexpected panic payload: %v", r)
		}
	}()
	pdb.SettleTo(sdb)
}
