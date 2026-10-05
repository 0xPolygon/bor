package sequencer

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
)

// openOnDisk opens a real on-disk pebble-backed chaindb at dir. Reopening the
// same dir is a faithful stand-in for a process restart: everything durably
// written is replayed from the WAL and still there, exactly as after a crash.
func openOnDisk(t *testing.T, dir string) (ethdb.Database, func()) {
	t.Helper()

	pdb, err := pebble.New(dir, 256, 16, "", false)
	if err != nil {
		t.Fatalf("open pebble at %s: %v", dir, err)
	}

	return rawdb.NewDatabase(pdb), func() { pdb.Close() }
}

// TestServedCommitmentSurvivesCrashAndIsAudited reproduces the severe bug end to
// end and shows the fix stops it, deterministically, across a real crash.
//
// The scenario is the open-block loss window: a node serves a preconfirmation
// for a height whose canonical block later diverges (its transactions are
// reordered), the node crashes, and the store no longer holds a sealed
// generation for that height. The post-restart audit is all that can catch the
// broken promise.
//
// Both arms run identically — same divergence, same crash, same audit — and
// differ only in whether the durable served commitment was written, which is
// exactly the fix. Pre-fix: nothing survives the crash, the audit finds the
// height unheld, and the invalid preconfirmation is absent from the ledger (the
// severe bug). Fixed: the commitment survives the crash, the audit reconciles it
// against canonical, and records served_mismatch.
func TestServedCommitmentSurvivesCrashAndIsAudited(t *testing.T) {
	const atRisk = 282

	served := servedTxs(3)
	// Canonical 282 reordered the transactions this node preconfirmed: a real
	// served-versus-canonical divergence, the same signature the live oracle saw.
	canonical := types.Transactions{served[1], served[0], served[2]}

	run := func(t *testing.T, writeCommitment bool) []rawdb.InvalidPreconfRecord {
		dir := t.TempDir()

		// Before the crash: the node has audited up to just below the at-risk
		// height and (with the fix) recorded what it served there.
		db, closeDB := openOnDisk(t, dir)
		if err := rawdb.WritePreconfAuditedThrough(db, atRisk-3); err != nil {
			t.Fatalf("seed watermark: %v", err)
		}
		if writeCommitment {
			// Exactly what persistServed writes on the serve path.
			if err := rawdb.WritePreconfServed(db, atRisk, uint64(len(served)), servedDigest(atRisk, served)); err != nil {
				t.Fatalf("persist served: %v", err)
			}
		}
		closeDB() // crash

		// After the crash: reopen the same on-disk database.
		db2, closeDB2 := openOnDisk(t, dir)
		defer closeDB2()

		if writeCommitment {
			if _, _, ok, _ := rawdb.ReadPreconfServed(db2, atRisk); !ok {
				t.Fatal("served commitment did not survive the crash")
			}
		}

		// The audit walks the window. The store holds nothing for the at-risk
		// height (the successor rebuilt it store-blind); every other height is
		// sealed and matches canonical.
		chain, sealed := auditFixture(t, atRisk+5)
		delete(sealed, atRisk)
		chain.blocks[atRisk] = canonicalBlock(atRisk, canonical)

		audit := &auditor{db: db2, chain: chain, fetch: fetchFrom(t, sealed)}
		if _, err := audit.run(context.Background()); err != nil {
			t.Fatalf("audit run: %v", err)
		}

		return rawdb.ReadInvalidPreconfsInRange(db2, atRisk, atRisk)
	}

	t.Run("pre-fix: no durable commitment, divergence unrecorded", func(t *testing.T) {
		if recs := run(t, false); len(recs) != 0 {
			t.Fatalf("records = %+v, want none — this is the severe bug: an invalid preconfirmation absent from the ledger", recs)
		}
	})

	t.Run("fixed: durable commitment survives the crash and is recorded", func(t *testing.T) {
		recs := run(t, true)
		if len(recs) != 1 || recs[0].Number != atRisk || recs[0].Reason != servedMismatchReason {
			t.Fatalf("records = %+v, want one %s at %d", recs, servedMismatchReason, atRisk)
		}
	})
}
