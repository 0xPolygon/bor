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

func TestWitnessPruner_Reorg_Shallow_CursorBelowNewHead(t *testing.T) {
	db := NewMemoryDatabase()

	const head uint64 = 20
	hashes := buildCanonicalChain(t, db, head)

	// Witness for every block.
	for i := uint64(0); i <= head; i++ {
		WriteWitness(db, hashes[i], []byte{0xAA, 0xBB})
	}

	ws := &WitnessStrategy{retention: 5}
	p := NewPruner(db, ws)

	// First prune: establish cursor and delete old data.
	p.prune()

	cutoff := head - ws.RetentionBlocks() // 20 - 5 = 15

	// Sanity check: cursor is at cutoff.
	if cur := ReadWitnessPruneCursor(db); cur == nil || *cur != cutoff {
		if cur == nil {
			t.Fatalf("expected witness prune cursor to be written after first prune")
		}
		t.Fatalf("unexpected witness prune cursor after first prune: want %d, got %d", cutoff, *cur)
	}

	// Simulate shallow reorg: move head from 20 -> 18.
	newHead := uint64(18)
	WriteHeadHeaderHash(db, hashes[newHead])

	// Run prune again; this should:
	// - detect reorg (latest < lastHead)
	// - delete witnesses in (18..20] = [19..20]
	// - keep cursor at 15 (since 15 <= 18)
	p.prune()

	// Verify witness presence/absence.
	var badDeleted, badRetained []uint64
	for i := uint64(1); i <= head; i++ {
		exists := HasWitness(db, hashes[i])

		switch {
		case i < cutoff:
			// Pruned in first run, must still be absent.
			if exists {
				badDeleted = append(badDeleted, i)
			}
		case i <= newHead:
			// These should be kept (15..18).
			if !exists {
				badRetained = append(badRetained, i)
			}
		default:
			// i > newHead => reverted by reorg; should be pruned now.
			if exists {
				badDeleted = append(badDeleted, i)
			}
		}
	}

	if len(badDeleted) > 0 {
		t.Fatalf("unexpected witnesses present at heights: %v", badDeleted)
	}
	if len(badRetained) > 0 {
		t.Fatalf("expected witnesses to be retained at heights: %v", badRetained)
	}

	// Cursor should remain equal to the original cutoff (15), not new head (18).
	if cur := ReadWitnessPruneCursor(db); cur == nil || *cur != cutoff {
		if cur == nil {
			t.Fatalf("expected witness prune cursor to still be present after reorg")
		}
		t.Fatalf("unexpected witness prune cursor after reorg: want %d, got %d", cutoff, *cur)
	}
}

func TestWitnessPruner_Reorg_Deep_CursorAboveNewHead(t *testing.T) {
	db := NewMemoryDatabase()

	const head uint64 = 60
	hashes := buildCanonicalChain(t, db, head)

	// Witness for every block.
	for i := uint64(0); i <= head; i++ {
		WriteWitness(db, hashes[i], []byte{0xCA, 0xFE})
	}

	ws := &WitnessStrategy{retention: 5}
	p := NewPruner(db, ws)

	// Simulate prior pruning state:
	// - cursor at 40 (meaning [0..39] are considered already processed)
	// - lastHead at 60 (previous head height)
	const simulatedCursor uint64 = 40
	ws.WriteCursor(db, simulatedCursor)
	ws.WritePrunerHead(db, head)

	// Simulate deep reorg: head from 60 -> 20.
	newHead := uint64(20)
	WriteHeadHeaderHash(db, hashes[newHead])

	// Now run prune, which should:
	// - detect reorg (20 < 60)
	// - delete witnesses in [max(cursor, newHead+1) .. oldHead] = [40..60]
	// - roll cursor back to newHead (20)
	p.prune()

	var badDeleted, badRetained []uint64
	for i := uint64(1); i <= head; i++ {
		exists := HasWitness(db, hashes[i])

		switch {
		case i < simulatedCursor:
			// 0..39: should still exist (we never pruned them in this test).
			if !exists {
				badDeleted = append(badDeleted, i)
			}
		case i <= head:
			// 40..60 must be gone after reorg cleanup.
			if i >= simulatedCursor && i > newHead && exists {
				badRetained = append(badRetained, i)
			}
		}
	}

	if len(badDeleted) > 0 {
		t.Fatalf("unexpected witnesses deleted at heights: %v", badDeleted)
	}
	if len(badRetained) > 0 {
		t.Fatalf("expected witnesses to be deleted after reorg at heights: %v", badRetained)
	}

	// Cursor should have been rolled back to newHead.
	cur := ReadWitnessPruneCursor(db)
	if cur == nil {
		t.Fatalf("expected witness prune cursor to be written after deep reorg")
	}
	if *cur != newHead {
		t.Fatalf("unexpected witness prune cursor after deep reorg: want %d, got %d", newHead, *cur)
	}
}

func TestWitnessPruner_Reorg_Offline(t *testing.T) {
	db := NewMemoryDatabase()

	const oldHead uint64 = 60
	hashes := buildCanonicalChain(t, db, oldHead)

	// Witness for every block [0..oldHead].
	for i := uint64(0); i <= oldHead; i++ {
		WriteWitness(db, hashes[i], []byte{0xBE, 0xEF})
	}

	ws := &WitnessStrategy{retention: 5}

	// --- First run: normal pruning at oldHead ---
	p1 := NewPruner(db, ws)
	p1.prune()

	// After this:
	// - cursor should be oldHead - retention = 55
	// - witnesses for [0..54] should have been pruned
	// - pruner head should be persisted as oldHead

	wantCursor := oldHead - ws.RetentionBlocks()
	cur := ReadWitnessPruneCursor(db)
	if cur == nil || *cur != wantCursor {
		if cur == nil {
			t.Fatalf("expected witness prune cursor after first prune")
		}
		t.Fatalf("unexpected cursor after first prune: want %d, got %d", wantCursor, *cur)
	}

	// Optional: assert pruner head if you expose a reader for it
	if headPtr := ws.ReadPrunerHead(db); headPtr == nil || *headPtr != oldHead {
		if headPtr == nil {
			t.Fatalf("expected persisted pruner head after first prune")
		}
		t.Fatalf("unexpected persisted pruner head after first prune: want %d, got %d", oldHead, *headPtr)
	}

	// Sanity: everything < cursor should now be pruned (this enforces the invariant).
	for i := uint64(1); i < wantCursor; i++ {
		if HasWitness(db, hashes[i]) {
			t.Fatalf("expected witnesses below cursor to be pruned after first run; found at height %d", i)
		}
	}

	// --- Simulate OFFLINE reorg: canonical head moves from 60 -> 50 while pruner is not running ---
	const newHead uint64 = 50
	WriteHeadHeaderHash(db, hashes[newHead])

	// --- Second run: new pruner instance after restart ---
	p2 := NewPruner(db, ws)
	p2.prune()

	// After offline reorg handling, we expect:
	// - witnesses in (newHead..oldHead] = [51..60] deleted as non-canonical
	// - witnesses <= newHead kept (subject to any retention from second run, but
	//   since newHead=50 and retention=5, cutoff=45, and cursor is rolled back to 50,
	//   normal prune will early-exit with cur>=cutoff)
	// - cursor rolled back to newHead (50)
	// - pruner head updated to newHead in DB

	cur = ReadWitnessPruneCursor(db)
	if cur == nil {
		t.Fatalf("expected witness prune cursor to be written after offline reorg")
	}
	if *cur != newHead {
		t.Fatalf("unexpected witness prune cursor after offline reorg: want %d, got %d", newHead, *cur)
	}

	if headPtr := ws.ReadPrunerHead(db); headPtr == nil || *headPtr != newHead {
		if headPtr == nil {
			t.Fatalf("expected persisted pruner head after offline reorg")
		}
		t.Fatalf("unexpected persisted pruner head after offline reorg: want %d, got %d", newHead, *headPtr)
	}

	// Verify witness presence/absence:
	// - [0..cutoffFirst] (0..54) were pruned in first run and must still be absent
	// - (cutoffFirst..newHead] = [55..50] is empty range, so nothing special
	// - (newHead..oldHead] = [51..60] should be deleted by reorg cleanup
	//   (51..54 already gone; 55..60 removed during reorg cleanup)
	// - [<= newHead] should still be present at least for [45..50], depending on retention logic
	var badDeleted, badRetained []uint64

	for i := uint64(1); i <= oldHead; i++ {
		exists := HasWitness(db, hashes[i])

		switch {
		case i < wantCursor:
			// Pruned in first run; must remain absent.
			if exists {
				badRetained = append(badRetained, i)
			}
		case i <= newHead:
			// These should still be present (assuming retention kept them at newHead=50).
			// If your retention semantics change this, adjust accordingly.
			if !exists {
				badDeleted = append(badDeleted, i)
			}
		default: // i > newHead
			// Non-canonical tail; must be gone.
			if exists {
				badRetained = append(badRetained, i)
			}
		}
	}

	if len(badDeleted) > 0 {
		t.Fatalf("unexpected witnesses deleted after offline reorg at heights: %v", badDeleted)
	}
	if len(badRetained) > 0 {
		t.Fatalf("expected witnesses to be pruned (or stay pruned) after offline reorg at heights: %v", badRetained)
	}
}

func TestWitnessPruner_CursorBeyondHead_Clamping(t *testing.T) {
	db := NewMemoryDatabase()

	const head uint64 = 50
	hashes := buildCanonicalChain(t, db, head)

	// Write witnesses for every block.
	for i := uint64(0); i <= head; i++ {
		WriteWitness(db, hashes[i], []byte{0xDE, 0xAD})
	}

	ws := &WitnessStrategy{retention: 10}

	// Manually set cursor to a value beyond the head (simulates corrupted state or edge case).
	invalidCursor := head + 20
	ws.WriteCursor(db, invalidCursor)

	// Verify cursor is set beyond head.
	cur := ReadWitnessPruneCursor(db)
	if cur == nil || *cur != invalidCursor {
		t.Fatalf("expected cursor to be set to %d, got %v", invalidCursor, cur)
	}

	// Create pruner and run prune cycle.
	p := NewPruner(db, ws)
	p.prune()

	// After prune, cursor should be clamped to head.
	cur = ReadWitnessPruneCursor(db)
	if cur == nil {
		t.Fatalf("expected cursor to be written after clamping")
	}
	if *cur != head {
		t.Fatalf("expected cursor to be clamped to head %d, got %d", head, *cur)
	}

	// Pruner should have updated the head.
	if headPtr := ws.ReadPrunerHead(db); headPtr == nil || *headPtr != head {
		if headPtr == nil {
			t.Fatalf("expected pruner head to be written")
		}
		t.Fatalf("expected pruner head to be %d, got %d", head, *headPtr)
	}
}

func TestBlockPruner_CursorBeyondHead_Clamping(t *testing.T) {
	db := NewMemoryDatabase()

	const head uint64 = 50
	hashes := buildCanonicalChain(t, db, head)

	bs := &BlockStrategy{retention: 10}

	// Manually set cursor to a value beyond the head (simulates corrupted state or edge case).
	invalidCursor := head + 20
	bs.WriteCursor(db, invalidCursor)

	// Verify cursor is set beyond head.
	cur := ReadBlockPruneCursor(db)
	if cur == nil || *cur != invalidCursor {
		t.Fatalf("expected cursor to be set to %d, got %v", invalidCursor, cur)
	}

	// Create pruner and run prune cycle.
	p := NewPruner(db, bs)
	p.prune()

	// After prune, cursor should be clamped to head.
	cur = ReadBlockPruneCursor(db)
	if cur == nil {
		t.Fatalf("expected cursor to be written after clamping")
	}
	if *cur != head {
		t.Fatalf("expected cursor to be clamped to head %d, got %d", head, *cur)
	}

	// Pruner should have updated the head.
	if headPtr := bs.ReadPrunerHead(db); headPtr == nil || *headPtr != head {
		if headPtr == nil {
			t.Fatalf("expected pruner head to be written")
		}
		t.Fatalf("expected pruner head to be %d, got %d", head, *headPtr)
	}

	// All blocks should still be present since no actual pruning should have occurred.
	for i := uint64(1); i <= head; i++ {
		if !HasHeader(db, hashes[i], i) {
			t.Fatalf("expected block %d to still exist after cursor clamping", i)
		}
		if canon := ReadCanonicalHash(db, i); canon != hashes[i] {
			t.Fatalf("expected canonical hash at %d to be preserved", i)
		}
	}
}
