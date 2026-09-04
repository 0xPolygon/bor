package rawdb

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/ethdb"
)

const InvalidPreconfQueryLimit = 1024

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

func ReadInvalidPreconfs(db ethdb.Iteratee, limit uint64) []InvalidPreconfRecord {
	// A zero limit means zero records. Callers that want the default cap pass
	// InvalidPreconfQueryLimit explicitly, and the RPC layer maps an omitted
	// parameter to it. Returning early is also what keeps the loop's break
	// reachable: with limit 0 the len(records) == limit test never fires after
	// the first append, so the query would return the whole keyspace.
	if limit == 0 {
		return []InvalidPreconfRecord{}
	}

	if limit > InvalidPreconfQueryLimit {
		limit = InvalidPreconfQueryLimit
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
