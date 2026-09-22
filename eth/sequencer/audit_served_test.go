package sequencer

import (
	"context"
	"math/big"
	"testing"

	pb "github.com/0xPolygon/sequence-store-proto/sequencestore/v1"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/trie"
)

// servedTxs builds n transactions with distinct hashes, in the order they were
// preconfirmed. The build is deterministic in n, so servedTxs(5)[:3] are the
// same transactions as servedTxs(3) — a canonical block that leads with the
// served set is easy to construct.
func servedTxs(n int) types.Transactions {
	to := common.Address{0x01}
	txs := make(types.Transactions, n)
	for i := range txs {
		txs[i] = types.NewTx(&types.LegacyTx{
			Nonce:    uint64(i + 1),
			GasPrice: big.NewInt(1),
			Gas:      21000,
			To:       &to,
			Value:    big.NewInt(int64(i + 1)),
		})
	}

	return txs
}

// canonicalBlock is the block that became canonical at a height, carrying txs
// in canonical order. Only its number and transactions matter to the audit.
func canonicalBlockHeader(height uint64) *types.Header {
	return testHeader(height, common.Hash{byte(height)})
}

func canonicalBlock(height uint64, txs types.Transactions) *types.Block {
	return types.NewBlock(canonicalBlockHeader(height),
		&types.Body{Transactions: txs}, nil, trie.NewStackTrie(nil))
}

// servedDigest is the commitment a node persists for txs served at a height,
// seeded with that height's canonical execution context — the same way the
// serve path builds it and the audit folds canonical, so a match turns on the
// transactions and their context, not on a seed mismatch.
func servedDigest(height uint64, txs types.Transactions) common.Hash {
	return foldServed(contextSeed(canonicalBlockHeader(height)), txs)
}

// The open-block loss window: the producer served preconfirmations at a height,
// then crashed before sealing, and its successor rebuilt the height without the
// store. The store holds no generation there, so the audit falls back to the
// commitment this node kept — and catches that the canonical block does not
// carry what was preconfirmed. This is the severe bug the fix closes: without
// the commitment the height reads as unheld and the broken preconf is invisible.
func TestAuditReconcilesServedMismatchAtAnUnheldHeight(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)

	delete(sealed, 7) // store holds nothing at 7: NotFound

	served := servedTxs(3)
	// Canonical 7 reordered the first two transactions this node served, so
	// its leading three fold to a different commitment.
	reordered := types.Transactions{served[1], served[0], served[2]}
	chain.blocks[7] = canonicalBlock(7, reordered)

	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// 7 was walked and, unlike a genuinely unheld height, compared against the
	// served commitment: seven held heights plus the one reconciled.
	if summary.walked != 8 || summary.compared != 8 {
		t.Fatalf("walked/compared = %d/%d, want 8/8", summary.walked, summary.compared)
	}
	if summary.mismatch != 1 {
		t.Fatalf("mismatch = %d, want 1", summary.mismatch)
	}
	if summary.unheld != 0 {
		t.Fatalf("unheld = %d, want 0: 7 was reconciled, not skipped", summary.unheld)
	}

	records := rawdb.ReadInvalidPreconfsInRange(db, 5, 12)
	if len(records) != 1 || records[0].Number != 7 || records[0].Reason != servedMismatchReason {
		t.Fatalf("records = %+v, want one %s at 7", records, servedMismatchReason)
	}

	// The commitment is cleared once the height has been judged.
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); ok {
		t.Fatal("served commitment not cleared after judging")
	}
	if got := auditedThrough(t, db); got != 12 {
		t.Fatalf("watermark = %d, want 12", got)
	}
}

// The kept-promise case: the store lost the generation, but the canonical block
// leads with exactly the transactions this node served, so nothing is recorded.
// A commitment must not manufacture a mismatch where the chain agreed.
func TestAuditReconcilesServedMatchAtAnUnheldHeight(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)

	delete(sealed, 7)

	served := servedTxs(3)
	// Canonical 7 leads with the served three, then carries more: the promise
	// was kept.
	chain.blocks[7] = canonicalBlock(7, servedTxs(5))

	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.compared != 8 {
		t.Fatalf("compared = %d, want 8", summary.compared)
	}
	if summary.mismatch != 0 || summary.unheld != 0 {
		t.Fatalf("mismatch/unheld = %d/%d, want 0/0", summary.mismatch, summary.unheld)
	}
	if records := rawdb.ReadInvalidPreconfsInRange(db, 5, 12); len(records) != 0 {
		t.Fatalf("records = %+v, want none", records)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); ok {
		t.Fatal("served commitment not cleared after judging")
	}
	if got := auditedThrough(t, db); got != 12 {
		t.Fatalf("watermark = %d, want 12", got)
	}
}

// Same transactions, different execution context. A producer handover rebuilds
// the height with a different context (succession-based difficulty, and often
// time or parent), so a preconfirmation served against the old context is a
// broken promise even though its transactions are unchanged. Folding the
// context into the commitment is what catches it.
func TestAuditReconcilesServedMismatchOnDifferentContext(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)

	delete(sealed, 7)

	served := servedTxs(3)
	// The node served these transactions against a context with a different
	// difficulty than the one that became canonical at 7.
	servedHeader := canonicalBlockHeader(7)
	servedHeader.Difficulty = big.NewInt(2)
	chain.blocks[7] = canonicalBlock(7, served) // canonical difficulty stays 1

	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), foldServed(contextSeed(servedHeader), served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.mismatch != 1 {
		t.Fatalf("mismatch = %d, want 1: same txs but a different context is a broken promise", summary.mismatch)
	}
	records := rawdb.ReadInvalidPreconfsInRange(db, 5, 12)
	if len(records) != 1 || records[0].Number != 7 || records[0].Reason != servedMismatchReason {
		t.Fatalf("records = %+v, want one %s at 7", records, servedMismatchReason)
	}
}

// A canonical block whose body is not available locally (pruned, or a snap-sync
// gap) cannot be judged. The audit must count it uncomparable and leave the
// commitment in place — never record a spurious served_mismatch, which would be
// a false entry in the ledger.
func TestReconcileServedLeavesUnjudgeableCommitment(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain := &stubAuditChain{blocks: map[uint64]*types.Block{}} // no block at 7

	served := servedTxs(3)
	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	audit := &auditor{db: db, chain: chain}
	summary := auditSummary{}
	audit.reconcileServed(7, &summary)

	if summary.mismatch != 0 {
		t.Fatalf("mismatch = %d, want 0: a missing body must not read as a broken promise", summary.mismatch)
	}
	if summary.unknown != 1 {
		t.Fatalf("unknown = %d, want 1: an unjudgeable height is uncomparable", summary.unknown)
	}
	if records := rawdb.ReadInvalidPreconfsInRange(db, 7, 7); len(records) != 0 {
		t.Fatalf("records = %+v, want none", records)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); !ok {
		t.Fatal("commitment was cleared; it should be left for a later pass")
	}
}

// reconcileServed judges an unheld height against the node's own commitment.
// The verdict turns on what the commitment says and how it compares to the
// canonical block; the surrounding walk is not needed to exercise it.
func TestReconcileServed(t *testing.T) {
	const height = 7

	served := servedTxs(3)
	digest := servedDigest(height, served)

	cases := []struct {
		name    string
		present bool // whether a commitment is stored for the height
		count   uint64
		digest  common.Hash
		block   *types.Block // canonical block at the height, or nil for none

		wantCompared uint64
		wantMismatch uint64
		wantUnheld   uint64
		wantRecord   bool
	}{
		{
			name:       "no commitment is unheld",
			block:      canonicalBlock(height, served),
			wantUnheld: 1,
		},
		{
			name:         "a kept promise is compared and cleared",
			present:      true,
			count:        3,
			digest:       digest,
			block:        canonicalBlock(height, servedTxs(5)),
			wantCompared: 1,
		},
		{
			name:         "a broken promise is recorded",
			present:      true,
			count:        3,
			digest:       digest,
			block:        canonicalBlock(height, servedTxs(2)), // fewer txs than served
			wantCompared: 1,
			wantMismatch: 1,
			wantRecord:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backing := rawdb.NewMemoryDatabase()
			chain := &stubAuditChain{blocks: map[uint64]*types.Block{}}
			if tc.block != nil {
				chain.blocks[height] = tc.block
			}
			if tc.present {
				if err := rawdb.WritePreconfServed(backing, height, tc.count, tc.digest); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}

			audit := &auditor{db: backing, chain: chain}
			summary := auditSummary{}
			audit.reconcileServed(height, &summary)

			if summary.compared != tc.wantCompared {
				t.Fatalf("compared = %d, want %d", summary.compared, tc.wantCompared)
			}
			if summary.mismatch != tc.wantMismatch {
				t.Fatalf("mismatch = %d, want %d", summary.mismatch, tc.wantMismatch)
			}
			if summary.unheld != tc.wantUnheld {
				t.Fatalf("unheld = %d, want %d", summary.unheld, tc.wantUnheld)
			}

			records := rawdb.ReadInvalidPreconfsInRange(backing, height, height)
			if tc.wantRecord && (len(records) != 1 || records[0].Reason != servedMismatchReason) {
				t.Fatalf("records = %+v, want one %s", records, servedMismatchReason)
			}
			if !tc.wantRecord && len(records) != 0 {
				t.Fatalf("records = %+v, want none", records)
			}

			// Every commitment the audit reads is cleared once judged.
			if _, _, stillHeld, _ := rawdb.ReadPreconfServed(backing, height); stillHeld {
				t.Fatal("commitment not cleared")
			}
		})
	}
}

// The other half of the loss window: the producer streamed the block open to
// the store, then died before sealing. The store holds an unsealed generation,
// which confirms nothing canonical, so the audit must still judge the served
// commitment against the canonical block — or the broken promise it left goes
// unrecorded, the exact severe bug in a different disguise.
func TestAuditReconcilesServedMismatchAtAnUnsealedHeight(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)

	delete(sealed, 7) // the fetch below returns 7 open-but-unsealed

	served := servedTxs(3)
	reordered := types.Transactions{served[1], served[0], served[2]}
	chain.blocks[7] = canonicalBlock(7, reordered)

	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: func(ctx context.Context, height uint64) ([]*pb.Entry, error) {
		if height == 7 {
			return []*pb.Entry{{Kind: &pb.Entry_BlockOpen{BlockOpen: &pb.BlockOpen{BlockNumber: 7}}}}, nil
		}

		return fetchFrom(t, sealed)(ctx, height)
	}}

	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.mismatch != 1 {
		t.Fatalf("mismatch = %d, want 1", summary.mismatch)
	}
	records := rawdb.ReadInvalidPreconfsInRange(db, 5, 12)
	if len(records) != 1 || records[0].Number != 7 || records[0].Reason != servedMismatchReason {
		t.Fatalf("records = %+v, want one %s at 7", records, servedMismatchReason)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); ok {
		t.Fatal("served commitment not cleared after judging")
	}
}

// A corrupt or malicious store must not be able to bury a served_mismatch by
// returning an unusable seal. When the store holds a seal that does not decode
// (or names the wrong height), the audit cannot confirm the height canonical,
// so it judges the served commitment against canonical rather than dropping it.
func TestAuditReconcilesServedMismatchDespiteUnusableSeal(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)

	served := servedTxs(3)
	reordered := types.Transactions{served[1], served[0], served[2]}
	chain.blocks[7] = canonicalBlock(7, reordered)

	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	// Height 7 comes back with a seal whose header bytes do not decode; every
	// other height is a normal sealed generation that matches canonical.
	audit := &auditor{db: db, chain: chain, fetch: func(ctx context.Context, height uint64) ([]*pb.Entry, error) {
		if height == 7 {
			return []*pb.Entry{{Kind: &pb.Entry_BlockSeal{BlockSeal: &pb.BlockSeal{Header: []byte{0xde, 0xad}}}}}, nil
		}

		return fetchFrom(t, sealed)(ctx, height)
	}}

	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.unknown != 1 {
		t.Fatalf("unknown = %d, want 1: the unusable seal is still counted as a store fault", summary.unknown)
	}
	if summary.mismatch != 1 {
		t.Fatalf("mismatch = %d, want 1: the served commitment was judged despite the garbage seal", summary.mismatch)
	}
	records := rawdb.ReadInvalidPreconfsInRange(db, 5, 12)
	if len(records) != 1 || records[0].Number != 7 || records[0].Reason != servedMismatchReason {
		t.Fatalf("records = %+v, want one %s at 7", records, servedMismatchReason)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); ok {
		t.Fatal("served commitment not cleared after judging")
	}
}

// A store seal matching canonical proves nothing about what this node served;
// the commitment is judged, not dropped.
func TestAuditJudgesServedCommitmentDespiteMatchingSeal(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 12)

	// The seal at 7 matches canonical; the body is not what was served.
	served := servedTxs(3)
	reordered := types.Transactions{served[1], served[0], served[2]}
	chain.blocks[7] = canonicalBlock(7, reordered)

	if err := rawdb.WritePreconfServed(db, 7, uint64(len(served)), servedDigest(7, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}

	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.mismatch != 1 {
		t.Fatalf("mismatch = %d, want 1: a matching store seal must not skip judging the served commitment", summary.mismatch)
	}
	records := rawdb.ReadInvalidPreconfsInRange(db, 5, 12)
	if len(records) != 1 || records[0].Number != 7 || records[0].Reason != servedMismatchReason {
		t.Fatalf("records = %+v, want one %s at 7", records, servedMismatchReason)
	}
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 7); ok {
		t.Fatal("served commitment not cleared after judging")
	}
}

// A commitment in a range the audit skips for store retention must still be
// judged. The store aged those heights out, but the commitment reconciles
// against the canonical chain, and skipping it silently would both drop a broken
// promise and leak the key.
func TestAuditReconcilesServedMismatchInTheSkippedRange(t *testing.T) {
	db := rawdb.NewMemoryDatabase()
	chain, sealed := auditFixture(t, 20)

	// The store retains only from height 15; 5..14 aged out and are skipped.
	for height := uint64(1); height < 15; height++ {
		delete(sealed, height)
	}

	served := servedTxs(3)
	reordered := types.Transactions{served[1], served[0], served[2]}
	chain.blocks[8] = canonicalBlock(8, reordered) // 8 sits inside the skipped range

	if err := rawdb.WritePreconfServed(db, 8, uint64(len(served)), servedDigest(8, served)); err != nil {
		t.Fatalf("seed served commitment: %v", err)
	}
	if err := rawdb.WritePreconfAuditedThrough(db, 4); err != nil {
		t.Fatalf("seed watermark: %v", err)
	}

	audit := &auditor{
		db: db, chain: chain, fetch: fetchFrom(t, sealed),
		oldest: oldestFrom(new(int), servedWindow(15)),
	}
	summary, err := audit.run(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.from != 15 || summary.skipped != 10 {
		t.Fatalf("from/skipped = %d/%d, want 15/10", summary.from, summary.skipped)
	}
	if summary.mismatch != 1 {
		t.Fatalf("mismatch = %d, want 1: the skipped-range commitment was reconciled", summary.mismatch)
	}
	records := rawdb.ReadInvalidPreconfsInRange(db, 0, 20)
	if len(records) != 1 || records[0].Number != 8 || records[0].Reason != servedMismatchReason {
		t.Fatalf("records = %+v, want one %s at 8", records, servedMismatchReason)
	}
	// Cleared, so the skip path does not leak it.
	if _, _, ok, _ := rawdb.ReadPreconfServed(db, 8); ok {
		t.Fatal("served commitment in the skipped range was not cleared")
	}
}

// An unreadable commitment must count as unheld, not silently pass: a corrupt
// record is the one case where the audit cannot tell whether a preconf was
// served, and skipping it would hide exactly what the commitment exists to catch.
func TestReconcileServedCountsUnheldWhenTheCommitmentIsUnreadable(t *testing.T) {
	audit := &auditor{db: failingReadDB{rawdb.NewMemoryDatabase()}}

	summary := auditSummary{}
	audit.reconcileServed(7, &summary)

	if summary.unheld != 1 || summary.compared != 0 || summary.mismatch != 0 {
		t.Fatalf("summary = %+v, want one unheld", summary)
	}
}

// judgeServedAgainstCanonical is verified only when the served transactions are
// the canonical block's leading prefix, in order and against the same execution
// context; diverged when they are not (or the block is shorter); unjudgeable
// when the canonical block is not available locally.
func TestJudgeServedAgainstCanonical(t *testing.T) {
	served := servedTxs(3)
	digest := servedDigest(7, served)

	// A block at height 7 whose leading transactions match but whose execution
	// context differs (succession-based difficulty after a producer handover).
	diffContext := canonicalBlockHeader(7)
	diffContext.Difficulty = big.NewInt(2)
	diffContextBlock := types.NewBlock(diffContext, &types.Body{Transactions: served}, nil, trie.NewStackTrie(nil))

	cases := []struct {
		name  string
		block *types.Block
		want  servedVerdict
	}{
		{name: "leading txs match", block: canonicalBlock(7, servedTxs(5)), want: servedVerified},
		{name: "exact txs match", block: canonicalBlock(7, servedTxs(3)), want: servedVerified},
		{name: "reordered leading txs", block: canonicalBlock(7, types.Transactions{served[1], served[0], served[2]}), want: servedDiverged},
		{name: "fewer txs than served", block: canonicalBlock(7, servedTxs(2)), want: servedDiverged},
		{name: "same txs, different context", block: diffContextBlock, want: servedDiverged},
		{name: "no canonical block available", want: servedUnjudgeable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain := &stubAuditChain{blocks: map[uint64]*types.Block{}}
			if tc.block != nil {
				chain.blocks[7] = tc.block
			}

			audit := &auditor{chain: chain}
			if got := audit.judgeServedAgainstCanonical(7, uint64(len(served)), digest); got != tc.want {
				t.Fatalf("judgeServedAgainstCanonical = %v, want %v", got, tc.want)
			}
		})
	}
}
