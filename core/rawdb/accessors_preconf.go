package rawdb

import (
	"encoding/binary"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
)

type InvalidPreconfRecord struct {
	Number uint64 `json:"number"`
	Reason string `json:"reason"`
}

func invalidPreconfKey(number uint64) []byte {
	key := make([]byte, len(invalidPreconfPrefix)+8)
	copy(key, invalidPreconfPrefix)
	binary.BigEndian.PutUint64(key[len(invalidPreconfPrefix):], ^number)
	return key
}

// PrepareInvalidPreconf adds an invalidation to batch.
func PrepareInvalidPreconf(batch ethdb.KeyValueWriter, number uint64, reason string) error {
	return batch.Put(invalidPreconfKey(number), []byte(reason))
}

// WriteInvalidPreconf atomically writes an invalidation.
func WriteInvalidPreconf(db ethdb.Database, number uint64, reason string) error {
	batch := db.NewBatch()
	if err := PrepareInvalidPreconf(batch, number, reason); err != nil {
		return err
	}
	return batch.Write()
}

// WriteInvalidPreconfIfAbsent writes an invalidation only where the height
// carries none, and reports whether it wrote. The audit backfills heights the
// live path may already have judged; a live record means a preconfirmation
// reached callers, which is a stronger claim than any after-the-fact verdict,
// so it must not be overwritten by one.
//
// The check and the write are not atomic. A live record landing between them
// is still overwritten, which needs the live path to judge the exact height a
// pass is judging, in that window — the two only overlap at a height a dropped
// session left behind the watermark. Narrowing it further would need a
// compare-and-set the key-value layer does not offer.
func WriteInvalidPreconfIfAbsent(db ethdb.Database, number uint64, reason string) (bool, error) {
	key := invalidPreconfKey(number)

	present, err := db.Has(key)
	if err != nil {
		return false, fmt.Errorf("read invalid preconf %d: %w", number, err)
	}
	if present {
		return false, nil
	}

	if err := WriteInvalidPreconf(db, number, reason); err != nil {
		return false, err
	}

	return true, nil
}

func ReadInvalidPreconfs(db ethdb.Iteratee, limit uint64) []InvalidPreconfRecord {
	if limit == 0 {
		return []InvalidPreconfRecord{}
	}

	iterator := db.NewIterator(invalidPreconfPrefix, nil)
	defer iterator.Release()

	records := make([]InvalidPreconfRecord, 0, limit)
	for iterator.Next() {
		key := iterator.Key()
		if len(key) != len(invalidPreconfPrefix)+8 {
			continue
		}
		records = append(records, InvalidPreconfRecord{
			Number: ^binary.BigEndian.Uint64(key[len(invalidPreconfPrefix):]),
			Reason: string(iterator.Value()),
		})
		if uint64(len(records)) == limit {
			break
		}
	}
	return records
}

// ReadInvalidPreconfsInRange returns the invalid-preconfirmation records whose
// block number falls within [from, to] inclusive, newest block first. There is
// one record per height at most, so bounding the range bounds the response;
// callers cap the range rather than having results silently truncated here.
func ReadInvalidPreconfsInRange(db ethdb.Iteratee, from, to uint64) []InvalidPreconfRecord {
	if from > to {
		return []InvalidPreconfRecord{}
	}

	// Keys are stored as ^number (see invalidPreconfKey), so ascending key
	// iteration yields descending block numbers. Seek to ^to — the smallest
	// key in the window — and walk upward until the decoded number drops
	// below `from`, rather than scanning the whole prefix.
	seek := make([]byte, 8)
	binary.BigEndian.PutUint64(seek, ^to)
	iterator := db.NewIterator(invalidPreconfPrefix, seek)
	defer iterator.Release()

	records := make([]InvalidPreconfRecord, 0)
	for iterator.Next() {
		key := iterator.Key()
		if len(key) != len(invalidPreconfPrefix)+8 {
			continue
		}
		number := ^binary.BigEndian.Uint64(key[len(invalidPreconfPrefix):])
		if number < from {
			break
		}
		records = append(records, InvalidPreconfRecord{
			Number: number,
			Reason: string(iterator.Value()),
		})
	}
	return records
}

// ReadPreconfAuditedThrough returns the highest block the sequence-store audit
// has compared against the canonical chain, and whether a watermark is stored
// at all. A node that has never audited has no watermark, which is not the same
// as having audited through block zero — and neither is the same as a database
// that could not answer, which is why a read failure is an error rather than a
// third spelling of absence. Collapsing the last into the second would let a
// read failure read as "never audited", which seeds the watermark at the
// current head and so reports an uncompared range as audited.
func ReadPreconfAuditedThrough(db ethdb.KeyValueReader) (uint64, bool, error) {
	present, err := db.Has(preconfAuditedThroughKey)
	if err != nil {
		return 0, false, fmt.Errorf("read preconf audit watermark: %w", err)
	}
	if !present {
		return 0, false, nil
	}

	value, err := db.Get(preconfAuditedThroughKey)
	if err != nil {
		return 0, false, fmt.Errorf("read preconf audit watermark: %w", err)
	}
	if len(value) != 8 {
		return 0, false, fmt.Errorf("preconf audit watermark is %d bytes, want 8", len(value))
	}

	return binary.BigEndian.Uint64(value), true, nil
}

// WritePreconfAuditedThrough stores the audit watermark.
func WritePreconfAuditedThrough(db ethdb.KeyValueWriter, number uint64) error {
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, number)

	return db.Put(preconfAuditedThroughKey, value)
}

const servedPreconfValueLen = 8 + common.HashLength

// servedPreconfKey = preconfServedPrefix + num (big endian). Forward order, so
// ascending key iteration yields ascending heights.
func servedPreconfKey(number uint64) []byte {
	return append(preconfServedPrefix, encodeBlockNumber(number)...)
}

// WritePreconfServed records what this node served at a height: count is how
// many transactions it preconfirmed there, digest a fold over their hashes in
// served order. It is the node's own promise, kept so the audit can judge the
// height even after the store stops holding that generation. A later
// generation at the same height overwrites the earlier promise, which is what
// the audit should compare against — the live path records the superseded one.
func WritePreconfServed(db ethdb.KeyValueWriter, number, count uint64, digest common.Hash) error {
	value := make([]byte, servedPreconfValueLen)
	binary.BigEndian.PutUint64(value, count)
	copy(value[8:], digest[:])

	return db.Put(servedPreconfKey(number), value)
}

// ReadPreconfServed returns the served-preconf commitment at a height, and
// whether one is stored. A malformed value is reported as an error rather than
// absence, so a corrupt record cannot read as "nothing was served".
func ReadPreconfServed(db ethdb.KeyValueReader, number uint64) (count uint64, digest common.Hash, ok bool, err error) {
	key := servedPreconfKey(number)

	present, err := db.Has(key)
	if err != nil {
		return 0, common.Hash{}, false, fmt.Errorf("read served preconf %d: %w", number, err)
	}
	if !present {
		return 0, common.Hash{}, false, nil
	}

	value, err := db.Get(key)
	if err != nil {
		return 0, common.Hash{}, false, fmt.Errorf("read served preconf %d: %w", number, err)
	}
	if len(value) != servedPreconfValueLen {
		return 0, common.Hash{}, false, fmt.Errorf("served preconf %d is %d bytes, want %d", number, len(value), servedPreconfValueLen)
	}

	return binary.BigEndian.Uint64(value[:8]), common.BytesToHash(value[8:]), true, nil
}

// DeletePreconfServed drops the commitment once the height has been judged
// (audited, or reconciled on the live path); it is only needed until then.
func DeletePreconfServed(db ethdb.KeyValueWriter, number uint64) error {
	return db.Delete(servedPreconfKey(number))
}

// ReadServedPreconfHeightsInRange returns the heights in [from, to] inclusive
// that carry a served-preconf commitment, ascending. The audit uses it to
// reconcile commitments in a range it advanced past without walking (store
// retention had aged those heights out), so a promise there is still judged and
// its commitment cleared rather than leaked.
func ReadServedPreconfHeightsInRange(db ethdb.Iteratee, from, to uint64) []uint64 {
	if from > to {
		return nil
	}

	// Keys are prefix + big-endian number in forward order, so seeking to
	// `from` and walking upward yields ascending heights until one exceeds to.
	iterator := db.NewIterator(preconfServedPrefix, encodeBlockNumber(from))
	defer iterator.Release()

	var heights []uint64
	for iterator.Next() {
		key := iterator.Key()
		if len(key) != len(preconfServedPrefix)+8 {
			continue
		}
		number := binary.BigEndian.Uint64(key[len(preconfServedPrefix):])
		if number > to {
			break
		}
		heights = append(heights, number)
	}

	return heights
}
