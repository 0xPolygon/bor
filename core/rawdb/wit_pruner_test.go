package rawdb

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
)

// buildCanonicalChain writes a canonical header chain [0..n] into db and returns the hashes by number.
func buildCanonicalChain(t *testing.T, db ethdb.Database, n uint64) []common.Hash {
	t.Helper()

	hashes := make([]common.Hash, n+1)

	var parent common.Hash
	for i := uint64(0); i <= n; i++ {
		h := &types.Header{
			Number:     new(big.Int).SetUint64(i),
			ParentHash: parent,
			Time:       uint64(time.Now().Unix()),
		}
		// Compute hash and write header + number mapping.
		WriteHeader(db, h)
		// Mark canonical number -> hash.
		WriteCanonicalHash(db, h.Hash(), i)
		hashes[i] = h.Hash()
		parent = h.Hash()
	}

	// Set head to the tip (n)
	WriteHeadHeaderHash(db, hashes[n])
	return hashes
}

func TestWitPruner_HappyPath_DeletesOldWitnesses(t *testing.T) {
	db := NewMemoryDatabase()

	// Build a small canonical chain 0..20 and set head=20.
	const head uint64 = 20
	hashes := buildCanonicalChain(t, db, head)

	// Write witnesses for blocks 0..20 (all of them).
	for i := uint64(0); i <= head; i++ {
		WriteWitness(db, hashes[i], []byte{0xAB, 0xCD})
	}

	// Instantiate pruner with small retention to get a non-zero cutoff.
	// cutoff = head - retentionBlocks = 20 - 5 = 15
	wp := NewWitPruner(db)
	wp.retentionBlocks = 5       // override for the test (set before Start)
	wp.pruneInterval = time.Hour // irrelevant; we call prune once directly

	// Sanity: cursor should be nil before first prune.
	if cur := ReadWitnessPruneCursor(db); cur != nil {
		t.Fatalf("expected nil prune cursor before first run, got %v", *cur)
	}

	// Run a single prune cycle synchronously.
	wp.pruneWitness()

	// Expect: witnesses [0..14] deleted, [15..20] kept.
	var (
		cutoff   = head - wp.retentionBlocks // 15
		deleted  []uint64
		retained []uint64
	)
	for i := uint64(0); i <= head; i++ {
		exists := HasWitness(db, hashes[i])
		if i < cutoff && exists {
			deleted = append(deleted, i)
		}
		if i >= cutoff && !exists {
			retained = append(retained, i)
		}
	}
	if len(deleted) > 0 {
		t.Fatalf("expected witnesses < cutoff to be deleted; still present for heights: %v", deleted)
	}
	if len(retained) > 0 {
		t.Fatalf("expected witnesses >= cutoff to be retained; missing for heights: %v", retained)
	}

	// Cursor should be written to cutoff.
	cur := ReadWitnessPruneCursor(db)
	if cur == nil {
		t.Fatalf("expected prune cursor to be written")
	}
	if *cur != cutoff {
		t.Fatalf("unexpected prune cursor: want %d, got %d", cutoff, *cur)
	}
}

// This test sets up witnesses that only start at some Hs > 0 (e.g., 7),
// and sets head & retention so that cutoff >> Hs. That way, when cursor is nil,
// pruneWitness() must binary-search [0..cutoff] to discover Hs, then prune
// witnesses in [Hs..cutoff-1] and keep [cutoff..head].
func TestWitPruner_BinarySearch_EarliestWitnessNotZero(t *testing.T) {
	db := NewMemoryDatabase()

	// Chain: 0..60, head=60
	const head uint64 = 60
	hashes := buildCanonicalChain(t, db, head)

	// Earliest witness height (not zero).
	const earliest uint64 = 7

	// Write witnesses from 'earliest' up to head (0..earliest-1 have NO witness).
	for i := earliest; i <= head; i++ {
		WriteWitness(db, hashes[i], []byte{0xDE, 0xAD})
	}

	// retention=10 => cutoff = head - 10 = 50
	wp := NewWitPruner(db)
	wp.retentionBlocks = 10
	wp.pruneInterval = time.Hour // irrelevant; we'll run once directly

	// Sanity: cursor not set yet
	if cur := ReadWitnessPruneCursor(db); cur != nil {
		t.Fatalf("expected nil prune cursor before first run, got %v", *cur)
	}

	// Run a single prune cycle; this must:
	// - binary-search earliest witness (7) within [0..50]
	// - delete witnesses in [7..49]
	// - keep witnesses in [50..60]
	wp.pruneWitness()

	cutoff := head - wp.retentionBlocks // 50

	// Check deletion/retention
	var badDeleted []uint64
	var badRetained []uint64

	for i := uint64(0); i <= head; i++ {
		exists := HasWitness(db, hashes[i])

		switch {
		case i < earliest:
			// These never had witnesses; should still be absent.
			if exists {
				badRetained = append(badRetained, i)
			}
		case i < cutoff:
			// These had witnesses (from earliest..head); should be deleted now.
			if exists {
				badDeleted = append(badDeleted, i)
			}
		default: // i >= cutoff
			// Should be retained.
			if !exists {
				badRetained = append(badRetained, i)
			}
		}
	}

	if len(badDeleted) > 0 {
		t.Fatalf("expected witnesses < cutoff to be deleted; still present for heights: %v", badDeleted)
	}
	if len(badRetained) > 0 {
		t.Fatalf("expected witnesses >= cutoff (or never-existing < earliest) to be correct; bad heights: %v", badRetained)
	}

	// Cursor should be written to cutoff.
	cur := ReadWitnessPruneCursor(db)
	if cur == nil {
		t.Fatalf("expected prune cursor to be written")
	}
	if *cur != cutoff {
		t.Fatalf("unexpected prune cursor: want %d, got %d", cutoff, *cur)
	}
}
