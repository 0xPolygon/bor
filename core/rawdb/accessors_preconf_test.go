package rawdb

import "testing"

func TestInvalidPreconfRecords(t *testing.T) {
	db := NewMemoryDatabase()
	for number, reason := range map[uint64]string{7: "skipped", 9: "canonical_mismatch", 8: "reorged"} {
		if err := WriteInvalidPreconf(db, number, reason); err != nil {
			t.Fatalf("write %d: %v", number, err)
		}
	}
	if err := WriteInvalidPreconf(db, 8, "superseded"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	records := ReadInvalidPreconfs(db, 2)
	if len(records) != 2 || records[0].Number != 9 || records[1].Number != 8 || records[1].Reason != "superseded" {
		t.Fatalf("records = %+v", records)
	}
	for number := uint64(10); number < InvalidPreconfQueryLimit+20; number++ {
		if err := WriteInvalidPreconf(db, number, "skipped"); err != nil {
			t.Fatalf("write %d: %v", number, err)
		}
	}
	iterator := db.NewIterator(invalidPreconfPrefix, nil)
	defer iterator.Release()
	stored := 0
	for iterator.Next() {
		stored++
	}
	if stored != InvalidPreconfQueryLimit+13 {
		t.Fatalf("stored records = %d", stored)
	}
	records = ReadInvalidPreconfs(db, InvalidPreconfQueryLimit+1)
	if len(records) != InvalidPreconfQueryLimit || records[0].Number != InvalidPreconfQueryLimit+19 {
		t.Fatalf("bounded query = %d records, newest %d", len(records), records[0].Number)
	}
	if err := WriteInvalidPreconf(db, 1, "late_invalidation"); err != nil {
		t.Fatalf("write old record: %v", err)
	}
	if retained, err := db.Has(invalidPreconfKey(1)); err != nil || !retained {
		t.Fatalf("late invalidation retained = %t, err = %v", retained, err)
	}
	if records = ReadInvalidPreconfs(db, InvalidPreconfQueryLimit); len(records) != InvalidPreconfQueryLimit || records[len(records)-1].Number == 1 {
		t.Fatalf("bounded query includes late old record: %+v", records[len(records)-1])
	}
}
