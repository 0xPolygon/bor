package rawdb

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

// TestDeleteStateHistoryIndex_WithoutBorReceipts tests if the deletion of all
// history indexing data works and bor receipts aren't deleted despite having same
// prefix for deletion.
func TestDeleteStateHistoryIndex_WithoutBorReceipts(t *testing.T) {
	db := NewMemoryDatabase()

	// Insert some mock data into state history indexing related tables
	db.Put([]byte("a"), []byte{})
	db.Put([]byte("l"), []byte{})
	db.Put([]byte("m"), []byte{})
	key := accountHistoryIndexKey(common.Hash{})
	db.Put(key, []byte{}) // "ma" prefix
	key = storageHistoryIndexKey(common.Hash{}, common.Hash{})
	db.Put(key, []byte{}) // "ms" prefix
	key = accountHistoryIndexBlockKey(common.Hash{}, 1)
	db.Put(key, []byte{}) // "mba" prefix
	key = storageHistoryIndexBlockKey(common.Hash{}, common.Hash{}, 1)
	db.Put(key, []byte{}) // "mbs" prefix

	// Insert bor receipt and tx lookup entry
	key = types.BorReceiptKey(1, common.Hash{})
	db.Put(key, []byte{})
	key = borTxLookupKey(common.Hash{})
	db.Put(key, []byte{})

	// Insert in the next key to "matic-bor"
	db.Put([]byte("matic-bos"), []byte{})

	// Again insert some mock data
	db.Put([]byte("n"), []byte{})
	db.Put([]byte("z"), []byte{})

	// Call the state history deletor
	DeleteStateHistoryIndex(db)

	type Testcase struct {
		name  string
		key   []byte
		exist bool
	}
	tests := []Testcase{
		// Misc data
		{"checking 'a' key", []byte("a"), true},
		{"checking 'l' key", []byte("l"), true},

		// "m" prefixed keys for state history indexing related data
		{"checking 'm' key", []byte("m"), false},
		{"checking 'ma' key", accountHistoryIndexKey(common.Hash{}), false},
		{"checking 'ms' key", storageHistoryIndexKey(common.Hash{}, common.Hash{}), false},
		{"checking 'mba' key", accountHistoryIndexBlockKey(common.Hash{}, 1), false},
		{"checking 'mbs' key", storageHistoryIndexBlockKey(common.Hash{}, common.Hash{}, 1), false},

		// Bor receipt related data
		{"checking 'matic-bor-receipts' key", types.BorReceiptKey(1, common.Hash{}), true},
		{"checking 'matic-bor-tx-lookup' key", borTxLookupKey(common.Hash{}), true},
		{"checking 'matic-bos' key", []byte("matic-bos"), false}, // this one should be deleted

		// Misc data
		{"checking 'n' key", []byte("n"), true},
		{"checking 'z' key", []byte("z"), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := db.Get(tc.key)
			if tc.exist {
				require.NoError(t, err, "expected key to be present", string(tc.key))
				require.NotNil(t, data, "expected key to be present", string(tc.key))
			} else {
				// else "not found" db error would be thrown and data would be empty
				require.Nil(t, data, "expected key to be absent", string(tc.key))
			}
		})
	}
}
