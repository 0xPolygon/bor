package sequencer

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
)

// Heights below the watermark are never walked again and the live path never
// clears a commitment once the head has passed it; every pass must judge
// what is left below the mark.
func TestAuditJudgesServedCommitmentBelowTheWatermark(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)

	// 7 is below the mark; its canonical body differs from what was served and
	// the store's seal is that block, so only the sweep can catch it.
	served := servedTxs(3)
	block := canonicalBlock(7, types.Transactions{served[1], served[0], served[2]})
	chain.blocks[7], chain.hashes[7], sealed[7] = block, block.Hash(), block.Header()

	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 10); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.mismatch != 1 {
		t.Fatalf("mismatch = %d, want 1: the commitment below the watermark was not judged", summary.mismatch)
	}
	records := rawdb.ReadInvalidPreconfsInRange(db, 1, 12)
	if len(records) != 1 || records[0].Number != 7 || records[0].Reason != servedMismatchReason {
		t.Fatalf("records = %+v, want one %s at 7", records, servedMismatchReason)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); ok {
		t.Fatal("served commitment not cleared after judging")
	}
}

// The sweep runs even when rangeToAudit finds nothing to walk; a commitment
// below the mark can sit through many such passes.
func TestAuditJudgesServedCommitmentBelowTheWatermarkWithNoRangeToWalk(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)

	// Mark at the head: nothing to walk forward.
	if err := rawdb.WritePreconfAuditedThrough(db, 12); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}
	served := servedTxs(3)
	reordered := types.Transactions{served[1], served[0], served[2]}
	chain.blocks[7] = canonicalBlock(7, reordered)
	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.mismatch != 1 {
		t.Fatalf("mismatch = %d, want 1: no forward range, and the commitment below the mark was not judged", summary.mismatch)
	}
	records := rawdb.ReadInvalidPreconfsInRange(db, 1, 12)
	if len(records) != 1 || records[0].Number != 7 || records[0].Reason != servedMismatchReason {
		t.Fatalf("records = %+v, want one %s at 7", records, servedMismatchReason)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); ok {
		t.Fatal("served commitment not cleared after judging")
	}
	if got := auditedThrough(t, db); got != 12 {
		t.Fatalf("watermark = %d, want 12: the sweep must not move the mark", got)
	}
}

// A commitment left unjudged because its body was not local is judged once
// the body arrives, even after the mark has moved past it.
func TestAuditRevisitsUnjudgeableCommitmentOnALaterPass(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)

	// The store holds nothing at 7 and the body is not local yet: pass one
	// falls back to the commitment and cannot judge it.
	delete(sealed, 7)
	served := servedTxs(3)
	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	first, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.unknown != 1 || first.mismatch != 0 {
		t.Fatalf("first pass unknown = %d mismatch = %d, want 1 and 0", first.unknown, first.mismatch)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); !ok {
		t.Fatal("unjudgeable commitment was dropped instead of kept for a later pass")
	}
	if got := auditedThrough(t, db); got != 12 {
		t.Fatalf("watermark = %d, want 12: the mark moved past the unjudged height", got)
	}

	// The body arrives, and it is not what was served. 7 is below the mark and
	// there is nothing new to walk; the sweep alone must judge it.
	reordered := types.Transactions{served[1], served[0], served[2]}
	chain.blocks[7] = canonicalBlock(7, reordered)

	second, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.mismatch != 1 {
		t.Fatalf("second pass mismatch = %d, want 1: the later pass never revisited 7", second.mismatch)
	}
	records := rawdb.ReadInvalidPreconfsInRange(db, 1, 14)
	if len(records) != 1 || records[0].Number != 7 || records[0].Reason != servedMismatchReason {
		t.Fatalf("records = %+v, want one %s at 7", records, servedMismatchReason)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); ok {
		t.Fatal("served commitment not cleared after judging")
	}
}

// A canceled pass judges nothing; the commitment waits for the next one.
func TestAuditSweepStopsOnCancel(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)
	served := servedTxs(3)
	chain.blocks[7] = canonicalBlock(7, types.Transactions{served[1], served[0], served[2]})
	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 12); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	if _, err := audit.run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); !ok {
		t.Fatal("a canceled pass judged the commitment")
	}
	if records := rawdb.ReadInvalidPreconfsInRange(db, 1, 12); len(records) != 0 {
		t.Fatalf("records = %+v, want none", records)
	}
}

// The sweep is bounded by finality like the walk; a commitment above it waits.
func TestAuditSweepStopsAtFinality(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)
	served := servedTxs(3)
	chain.blocks[7] = canonicalBlock(7, types.Transactions{served[1], served[0], served[2]})
	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 12); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	final := uint64(5)
	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed), finalized: func() (uint64, bool) { return final, true }}
	if summary, err := audit.run(context.Background()); err != nil || summary.mismatch != 0 {
		t.Fatalf("run = %d mismatches, %v; want 0 with 7 above finality", summary.mismatch, err)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); !ok {
		t.Fatal("commitment above finality was judged")
	}

	final = 8
	if summary, err := audit.run(context.Background()); err != nil || summary.mismatch != 1 {
		t.Fatalf("run = %d mismatches, %v; want 1 once 7 is final", summary.mismatch, err)
	}
	records := rawdb.ReadInvalidPreconfsInRange(db, 1, 12)
	if len(records) != 1 || records[0].Number != 7 || records[0].Reason != servedMismatchReason {
		t.Fatalf("records = %+v, want one %s at 7", records, servedMismatchReason)
	}
}

// A first run only seeds the watermark; the pass after it sweeps.
func TestAuditFirstRunSeedsBeforeSweeping(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)
	served := servedTxs(3)
	chain.blocks[7] = canonicalBlock(7, types.Transactions{served[1], served[0], served[2]})
	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	if _, err := audit.run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if got := auditedThrough(t, db); got != 12 {
		t.Fatalf("watermark = %d, want seeded at 12", got)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); !ok {
		t.Fatal("the seeding pass judged the commitment")
	}

	if summary, err := audit.run(context.Background()); err != nil || summary.mismatch != 1 {
		t.Fatalf("second run = %d mismatches, %v; want 1", summary.mismatch, err)
	}
}
