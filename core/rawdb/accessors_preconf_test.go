package rawdb

import (
	"errors"
	"testing"
)

// manyRecords is enough invalidations to tell a bounded read from an
// unbounded one; the accessors have no ceiling of their own.
const manyRecords = 1024

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
	for number := uint64(10); number < manyRecords+20; number++ {
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
	if stored != manyRecords+13 {
		t.Fatalf("stored records = %d", stored)
	}
	records = ReadInvalidPreconfs(db, manyRecords+1)
	if len(records) != manyRecords+1 || records[0].Number != manyRecords+19 {
		t.Fatalf("bounded query = %d records, newest %d", len(records), records[0].Number)
	}
	if err := WriteInvalidPreconf(db, 1, "late_invalidation"); err != nil {
		t.Fatalf("write old record: %v", err)
	}
	if retained, err := db.Has(invalidPreconfKey(1)); err != nil || !retained {
		t.Fatalf("late invalidation retained = %t, err = %v", retained, err)
	}
	if records = ReadInvalidPreconfs(db, manyRecords); len(records) != manyRecords || records[len(records)-1].Number == 1 {
		t.Fatalf("bounded query includes late old record: %+v", records[len(records)-1])
	}
}

func TestInvalidPreconfZeroLimit(t *testing.T) {
	db := NewMemoryDatabase()
	if err := WriteInvalidPreconf(db, 7, "skipped"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if records := ReadInvalidPreconfs(db, 1); len(records) != 1 {
		t.Fatalf("sanity read = %+v", records)
	}

	records := ReadInvalidPreconfs(db, 0)
	if records == nil {
		t.Fatal("zero limit returned a nil slice, which marshals as null rather than []")
	}
	if len(records) != 0 {
		t.Fatalf("zero limit = %+v, want no records", records)
	}
}

func TestInvalidPreconfsInRange(t *testing.T) {
	db := NewMemoryDatabase()
	for number, reason := range map[uint64]string{7: "skipped", 9: "canonical_mismatch", 8: "reorged"} {
		if err := WriteInvalidPreconf(db, number, reason); err != nil {
			t.Fatalf("write %d: %v", number, err)
		}
	}
	if err := WriteInvalidPreconf(db, 8, "superseded"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}

	// [8,9] returns 9 then 8, newest first, with the overwritten reason; 7 is
	// below the range and excluded.
	records := ReadInvalidPreconfsInRange(db, 8, 9)
	if len(records) != 2 || records[0].Number != 9 || records[1].Number != 8 || records[1].Reason != "superseded" {
		t.Fatalf("range [8,9] = %+v", records)
	}

	// A bound below the stored set still works; 7 is the only record in [1,7].
	if records = ReadInvalidPreconfsInRange(db, 1, 7); len(records) != 1 || records[0].Number != 7 {
		t.Fatalf("range [1,7] = %+v", records)
	}

	// A single-block range hits exactly one record.
	if records = ReadInvalidPreconfsInRange(db, 9, 9); len(records) != 1 || records[0].Number != 9 {
		t.Fatalf("range [9,9] = %+v", records)
	}

	// A range with no invalidations returns a non-nil empty slice so it marshals
	// as [] rather than null.
	if records = ReadInvalidPreconfsInRange(db, 10, 20); records == nil || len(records) != 0 {
		t.Fatalf("empty range = %+v (nil=%t)", records, records == nil)
	}

	// from > to is treated as an empty (non-nil) range.
	if records = ReadInvalidPreconfsInRange(db, 9, 3); records == nil || len(records) != 0 {
		t.Fatalf("inverted range = %+v (nil=%t)", records, records == nil)
	}

	// A wide range returns every record in it, newest first: there is one
	// record per height, so the caller capping the range caps the response.
	for number := uint64(10); number < manyRecords+20; number++ {
		if err := WriteInvalidPreconf(db, number, "skipped"); err != nil {
			t.Fatalf("write %d: %v", number, err)
		}
	}
	records = ReadInvalidPreconfsInRange(db, 0, manyRecords+100)
	if len(records) != manyRecords+13 || records[0].Number != manyRecords+19 {
		t.Fatalf("wide range = %d records, newest %d", len(records), records[0].Number)
	}
}

func TestPreconfAuditWatermarks(t *testing.T) {
	db := NewMemoryDatabase()

	// A node that never audited has no watermark, which is not the same as
	// having audited through block zero.
	if _, ok, _ := ReadPreconfAuditedThrough(db); ok {
		t.Fatal("watermark present on a fresh database")
	}

	if err := WritePreconfAuditedThrough(db, 0); err != nil {
		t.Fatalf("write zero: %v", err)
	}
	if number, ok, _ := ReadPreconfAuditedThrough(db); !ok || number != 0 {
		t.Fatalf("watermark = (%d, %v), want (0, true)", number, ok)
	}

	if err := WritePreconfAuditedThrough(db, 4_200_000_000_000); err != nil {
		t.Fatalf("write: %v", err)
	}
	if number, ok, _ := ReadPreconfAuditedThrough(db); !ok || number != 4_200_000_000_000 {
		t.Fatalf("watermark = (%d, %v)", number, ok)
	}
}

// A stored height has three answers, and the caller acts differently on each:
// present, absent, and unavailable. A malformed value or a failing read must
// not read as absence, because absence seeds the audit watermark at the head.
func TestPreconfHeightSeparatesAbsenceFromFailure(t *testing.T) {
	t.Run("truncated value is an error, not absence", func(t *testing.T) {
		db := NewMemoryDatabase()
		if err := db.Put(preconfAuditedThroughKey, []byte{0x01}); err != nil {
			t.Fatalf("put: %v", err)
		}

		number, ok, err := ReadPreconfAuditedThrough(db)
		if err == nil {
			t.Fatal("a truncated value read back without an error")
		}
		if ok || number != 0 {
			t.Fatalf("read = (%d, %v), want (0, false)", number, ok)
		}
	})

	t.Run("missing key is absence, not an error", func(t *testing.T) {
		number, ok, err := ReadPreconfAuditedThrough(NewMemoryDatabase())
		if err != nil {
			t.Fatalf("missing key returned an error: %v", err)
		}
		if ok || number != 0 {
			t.Fatalf("read = (%d, %v), want (0, false)", number, ok)
		}
	})

	t.Run("failing presence check is an error", func(t *testing.T) {
		number, ok, err := ReadPreconfAuditedThrough(failingReader{})
		if err == nil {
			t.Fatal("a failing read reported absence")
		}
		if ok || number != 0 {
			t.Fatalf("read = (%d, %v), want (0, false)", number, ok)
		}
	})

	t.Run("failing value read is an error", func(t *testing.T) {
		number, ok, err := ReadPreconfAuditedThrough(presentButUnreadable{})
		if err == nil {
			t.Fatal("a failing value read reported absence")
		}
		if ok || number != 0 {
			t.Fatalf("read = (%d, %v), want (0, false)", number, ok)
		}
	})
}

var errReadRefused = errors.New("read refused")

type failingReader struct{}

func (failingReader) Has([]byte) (bool, error)   { return false, errReadRefused }
func (failingReader) Get([]byte) ([]byte, error) { return nil, errReadRefused }

// presentButUnreadable reports the key exists and then fails to hand it over,
// which is the shape of a corrupt or racing backend.
type presentButUnreadable struct{}

func (presentButUnreadable) Has([]byte) (bool, error)   { return true, nil }
func (presentButUnreadable) Get([]byte) ([]byte, error) { return nil, errReadRefused }

func TestWriteInvalidPreconfIfAbsent(t *testing.T) {
	db := NewMemoryDatabase()

	wrote, err := WriteInvalidPreconfIfAbsent(db, 9, "unobserved_mismatch")
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if !wrote {
		t.Fatal("first write reported no write")
	}

	wrote, err = WriteInvalidPreconfIfAbsent(db, 9, "reorged")
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if wrote {
		t.Fatal("second write replaced an existing record")
	}

	records := ReadInvalidPreconfsInRange(db, 9, 9)
	if len(records) != 1 || records[0].Reason != "unobserved_mismatch" {
		t.Fatalf("records = %+v, want the first reason kept", records)
	}
}
