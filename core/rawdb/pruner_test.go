package rawdb

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
)

// --- helpers ---

// buildCanonicalChain writes a canonical header chain [0..n] and returns hashes by number.
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
		WriteHeader(db, h)
		WriteCanonicalHash(db, h.Hash(), i)
		hashes[i] = h.Hash()
		parent = h.Hash()
	}
	WriteHeadHeaderHash(db, hashes[n])
	return hashes
}

// --- test ---

func TestWitnessPruner_HappyPath_GenericPruner(t *testing.T) {
	db := NewMemoryDatabase()

	// Chain 0..20 (head=20) with witnesses on every block.
	const head uint64 = 20
	hashes := buildCanonicalChain(t, db, head)
	for i := uint64(0); i <= head; i++ {
		WriteWitness(db, hashes[i], []byte{0xAB, 0xCD})
	}

	// Strategy: keep only last 5 blocks -> cutoff = 20 - 5 = 15
	ws := &WitnessStrategy{retention: 5}

	// Generic pruner; interval irrelevant since we invoke runOnce directly.
	p := NewPruner(db, ws)

	// Sanity: cursor should be nil before first prune.
	if cur := ReadWitnessPruneCursor(db); cur != nil {
		t.Fatalf("expected nil witness prune cursor before first run, got %v", *cur)
	}

	// Run one prune cycle synchronously.
	p.prune()

	// Expect: witnesses [0..14] deleted, [15..20] kept.
	cutoff := head - ws.RetentionBlocks()

	var badDeleted []uint64
	var badRetained []uint64

	for i := uint64(1); i <= head; i++ {
		exists := HasWitness(db, hashes[i])
		if i < cutoff && exists {
			badDeleted = append(badDeleted, i)
		}
		if i >= cutoff && !exists {
			badRetained = append(badRetained, i)
		}
	}

	if len(badDeleted) > 0 {
		t.Fatalf("expected witnesses < cutoff to be deleted; still present for heights: %v", badDeleted)
	}
	if len(badRetained) > 0 {
		t.Fatalf("expected witnesses >= cutoff to be retained; missing for heights: %v", badRetained)
	}

	// Cursor should be written to cutoff.
	cur := ReadWitnessPruneCursor(db)
	if cur == nil {
		t.Fatalf("expected witness prune cursor to be written")
	}
	if *cur != cutoff {
		t.Fatalf("unexpected witness prune cursor: want %d, got %d", cutoff, *cur)
	}
}

func TestWitnessPruner_FindCursor_FirstWitnessBeforeCutoff(t *testing.T) {
	db := NewMemoryDatabase()

	// Chain 0..60 (head=60).
	const head uint64 = 60
	hashes := buildCanonicalChain(t, db, head)

	// Earliest witness height is NOT zero.
	const earliest uint64 = 7

	// Write witnesses only from 'earliest'..head.
	for i := earliest; i <= head; i++ {
		WriteWitness(db, hashes[i], []byte{0xDE, 0xAD})
	}

	// Keep only last 10 blocks -> cutoff = 60 - 10 = 50.
	ws := &WitnessStrategy{retention: 10}
	p := NewPruner(db, ws)

	// Sanity: no prior cursor.
	if cur := ReadWitnessPruneCursor(db); cur != nil {
		t.Fatalf("expected nil witness prune cursor before first run, got %v", *cur)
	}

	// Trigger one prune cycle; must:
	// - binary search earliest witness (7) in [0..50]
	// - delete witnesses in [7..49]
	// - keep witnesses in [50..60]
	p.prune()

	cutoff := head - ws.RetentionBlocks()

	var badDeleted []uint64
	var badRetained []uint64

	for i := uint64(1); i <= head; i++ {
		exists := HasWitness(db, hashes[i])

		switch {
		case i < earliest:
			// These never had witnesses; must still be absent.
			if exists {
				badRetained = append(badRetained, i)
			}
		case i < cutoff:
			// These had witnesses originally; must be deleted now.
			if exists {
				badDeleted = append(badDeleted, i)
			}
		default: // i >= cutoff
			// Recent witnesses must be retained.
			if !exists {
				badRetained = append(badRetained, i)
			}
		}
	}

	if len(badDeleted) > 0 {
		t.Fatalf("expected witnesses < cutoff to be deleted; still present for heights: %v", badDeleted)
	}
	if len(badRetained) > 0 {
		t.Fatalf("unexpected witness presence/absence at heights: %v", badRetained)
	}

	// Cursor should be written to cutoff.
	if cur := ReadWitnessPruneCursor(db); cur == nil || *cur != cutoff {
		if cur == nil {
			t.Fatalf("expected witness prune cursor to be written")
		}
		t.Fatalf("unexpected witness prune cursor: want %d, got %d", cutoff, *cur)
	}
}

// --- Block pruner tests ---

func TestBlockPruner_HappyPath_GenericPruner(t *testing.T) {
	db := NewMemoryDatabase()

	// Chain 0..20 (head=20). buildCanonicalChain writes headers + canonical mapping and sets head.
	const head uint64 = 20
	hashes := buildCanonicalChain(t, db, head)

	// Strategy: keep only last 5 blocks -> cutoff = 20 - 5 = 15
	bs := &BlockStrategy{retention: 5}
	p := NewPruner(db, bs)

	// Sanity: cursor should be nil before first prune.
	if cur := ReadBlockPruneCursor(db); cur != nil {
		t.Fatalf("expected nil block prune cursor before first run, got %v", *cur)
	}

	// Run a single prune.
	p.prune()

	cutoff := head - bs.RetentionBlocks()

	var badDeleted, badRetainedNumMap []uint64
	for i := uint64(1); i <= head; i++ {
		// Per-hash data presence: header should be gone for pruned heights.
		haveHeader := HasHeader(db, hashes[i], i)
		// Canonical number->hash presence
		canon := ReadCanonicalHash(db, i)

		if i < cutoff {
			if haveHeader {
				badDeleted = append(badDeleted, i)
			}
			if canon != (hashes[i]) && canon != (common.Hash{}) {
				// If something weird is mapped, also flag it;
				// primary check is that mapping is removed (zero hash).
				badRetainedNumMap = append(badRetainedNumMap, i)
			}
			if canon != (common.Hash{}) {
				badRetainedNumMap = append(badRetainedNumMap, i)
			}
		} else {
			// These must be retained
			if !haveHeader {
				badDeleted = append(badDeleted, i)
			}
			if canon != hashes[i] {
				badRetainedNumMap = append(badRetainedNumMap, i)
			}
		}
	}

	if len(badDeleted) > 0 {
		t.Fatalf("expected blocks < cutoff to be deleted (no header); still present for heights: %v", badDeleted)
	}
	if len(badRetainedNumMap) > 0 {
		t.Fatalf("unexpected canonical mapping state at heights: %v", badRetainedNumMap)
	}

	// Cursor should be written to cutoff.
	cur := ReadBlockPruneCursor(db)
	if cur == nil {
		t.Fatalf("expected block prune cursor to be written")
	}
	if *cur != cutoff {
		t.Fatalf("unexpected block prune cursor: want %d, got %d", cutoff, *cur)
	}
}

func TestBlockPruner_FindCursor_FirstBlockBeforeCutoff(t *testing.T) {
	db := NewMemoryDatabase()

	// Chain 0..60 (head=60).
	const head uint64 = 60
	hashes := buildCanonicalChain(t, db, head)

	// Make earliest existing block-data height > 0 by removing [0..6] pre-state,
	// so the pruner must binary-search to find earliest=7.
	const earliest uint64 = 7
	for i := uint64(1); i < earliest; i++ {
		DeleteBlockWithoutNumber(db, hashes[i], i)
		DeleteCanonicalHash(db, i)
	}

	// Keep only last 10 blocks -> cutoff = 60 - 10 = 50.
	bs := &BlockStrategy{retention: 10}
	p := NewPruner(db, bs)

	// Sanity: no prior cursor.
	if cur := ReadBlockPruneCursor(db); cur != nil {
		t.Fatalf("expected nil block prune cursor before first run, got %v", *cur)
	}

	// Run a single prune cycle; must:
	// - binary search earliest block with data (7) in [0..50]
	// - delete blocks in [7..49]
	// - keep blocks in [50..60]
	p.prune()

	cutoff := head - bs.RetentionBlocks()

	var badDeleted, badMapping []uint64
	for i := uint64(1); i <= head; i++ {
		haveHeader := HasHeader(db, hashes[i], i)
		canon := ReadCanonicalHash(db, i)

		switch {
		case i < earliest:
			// These were manually deleted before pruning; must remain absent.
			if haveHeader || canon != (common.Hash{}) {
				badDeleted = append(badDeleted, i)
			}
		case i < cutoff:
			// These existed initially; must be deleted by pruner now.
			if haveHeader {
				badDeleted = append(badDeleted, i)
			}
			if canon != (common.Hash{}) {
				badMapping = append(badMapping, i)
			}
		default: // i >= cutoff
			// Must be retained.
			if !haveHeader {
				badDeleted = append(badDeleted, i)
			}
			if canon != hashes[i] {
				badMapping = append(badMapping, i)
			}
		}
	}

	if len(badDeleted) > 0 {
		t.Fatalf("unexpected header presence/absence at heights: %v", badDeleted)
	}
	if len(badMapping) > 0 {
		t.Fatalf("unexpected canonical mapping at heights: %v", badMapping)
	}

	// Cursor should be written to cutoff.
	if cur := ReadBlockPruneCursor(db); cur == nil || *cur != cutoff {
		if cur == nil {
			t.Fatalf("expected block prune cursor to be written")
		}
		t.Fatalf("unexpected block prune cursor: want %d, got %d", cutoff, *cur)
	}
}
