package sequencer

import (
	"testing"

	"github.com/ethereum/go-ethereum/core/rawdb"
)

func TestAuditSealedVerdictRetainsUnreadableCommitment(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mismatch bool
	}{
		{name: "matching seal"},
		{name: "mismatching seal", mismatch: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := rawdb.NewMemoryDatabase()
			if err := rawdb.WritePreconfServed(db, 7, 3, servedDigest(7, servedTxs(3))); err != nil {
				t.Fatal(err)
			}
			it := db.NewIterator(nil, nil)
			if !it.Next() {
				t.Fatal("missing commitment")
			}
			key := append([]byte(nil), it.Key()...)
			it.Release()
			if err := db.Put(key, []byte{1}); err != nil {
				t.Fatal(err)
			}
			chain, sealed := auditFixture(t, 12)
			block := canonicalBlock(7, servedTxs(3))
			chain.blocks[7], chain.hashes[7] = block, block.Hash()
			if !tc.mismatch {
				sealed[7] = block.Header()
			}
			a := &auditor{db: db, chain: chain, fetch: fetchFrom(t, sealed)}
			var summary auditSummary
			if err := a.auditHeightInto(t.Context(), 7, &summary); err != nil {
				t.Fatal(err)
			}
			if present, err := db.Has(key); err != nil || !present {
				t.Fatalf("unreadable evidence removed: present=%v err=%v", present, err)
			}
			if summary.unheld != 1 {
				t.Fatalf("unheld = %d, want 1", summary.unheld)
			}
			records := rawdb.ReadInvalidPreconfsInRange(db, 7, 7)
			if tc.mismatch {
				if len(records) != 1 || records[0].Reason != unobservedMismatchReason {
					t.Fatalf("mismatch was not recorded: %v", records)
				}
			} else if len(records) != 0 {
				t.Fatalf("matching seal recorded as invalid: %v", records)
			}
		})
	}
}

func TestAuditRetainsCommitmentWhenVerdictWriteFails(t *testing.T) {
	for _, storeMismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "served mismatch", true: "store mismatch"}[storeMismatch], func(t *testing.T) {
			db := rawdb.NewMemoryDatabase()
			if err := rawdb.WritePreconfServed(db, 7, 3, servedDigest(7, servedTxs(3))); err != nil {
				t.Fatal(err)
			}
			chain, sealed := auditFixture(t, 12)
			chain.blocks[7] = canonicalBlock(7, servedTxs(2))
			if storeMismatch {
				chain.hashes[7] = chain.blocks[7].Hash()
			} else {
				delete(sealed, 7)
			}
			a := &auditor{db: failingWriteDB{db}, chain: chain, fetch: fetchFrom(t, sealed)}
			var summary auditSummary
			if err := a.auditHeightInto(t.Context(), 7, &summary); err != nil {
				t.Fatal(err)
			}
			if _, _, present, err := rawdb.ReadPreconfServed(db, 7); err != nil || !present {
				t.Fatalf("evidence lost after failed verdict write: present=%v err=%v", present, err)
			}
		})
	}
}
