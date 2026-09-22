package sequencer

import (
	"testing"

	"github.com/ethereum/go-ethereum/core/rawdb"
)

func TestAuditMatchingSealRetainsUnreadableCommitment(t *testing.T) {
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
