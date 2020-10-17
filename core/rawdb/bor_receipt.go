package rawdb

import (
	"fmt"

	"github.com/maticnetwork/bor/common"
	"github.com/maticnetwork/bor/core/types"
	"github.com/maticnetwork/bor/ethdb"
	"github.com/maticnetwork/bor/log"
	"github.com/maticnetwork/bor/rlp"
)

var (
	borReceiptPrefix = []byte("bor-receipt-") // borReceiptPrefix + number + block hash -> bor block receipt

	// freezerReceiptTable indicates the name of the freezer bor receipts table.
	freezerBorReceiptTable = "bor-receipts"
)

// borReceiptKey = borReceiptPrefix + num (uint64 big endian) + hash
func borReceiptKey(number uint64, hash common.Hash) []byte {
	return append(append(borReceiptPrefix, encodeBlockNumber(number)...), hash.Bytes()...)
}

// HasBorReceipt verifies the existence of all block receipt belonging
// to a block.
func HasBorReceipt(db ethdb.Reader, hash common.Hash, number uint64) bool {
	if has, err := db.Ancient(freezerHashTable, number); err == nil && common.BytesToHash(has) == hash {
		return true
	}

	if has, err := db.Has(borReceiptKey(number, hash)); !has || err != nil {
		return false
	}

	return true
}

// ReadBorReceiptRLP retrieves all the transaction receipts belonging to a block in RLP encoding.
func ReadBorReceiptRLP(db ethdb.Reader, hash common.Hash, number uint64) rlp.RawValue {
	// First try to look up the data in ancient database. Extra hash
	// comparison is necessary since ancient database only maintains
	// the canonical data.
	data, _ := db.Ancient(freezerBorReceiptTable, number)
	if len(data) > 0 {
		h, _ := db.Ancient(freezerHashTable, number)
		if common.BytesToHash(h) == hash {
			return data
		}
	}
	// Then try to look up the data in leveldb.
	data, _ = db.Get(borReceiptKey(number, hash))
	if len(data) > 0 {
		fmt.Println("==> RAWDB IN ReadBorReceiptRLP", common.Bytes2Hex(data))
		return data
	}
	// In the background freezer is moving data from leveldb to flatten files.
	// So during the first check for ancient db, the data is not yet in there,
	// but when we reach into leveldb, the data was already moved. That would
	// result in a not found error.
	data, _ = db.Ancient(freezerReceiptTable, number)
	if len(data) > 0 {
		h, _ := db.Ancient(freezerHashTable, number)
		if common.BytesToHash(h) == hash {
			return data
		}
	}
	return nil // Can't find the data anywhere.
}

// ReadRawBorReceipt retrieves the block receipt belonging to a block.
// The receipt metadata fields are not guaranteed to be populated, so they
// should not be used. Use ReadBorReceipt instead if the metadata is needed.
func ReadRawBorReceipt(db ethdb.Reader, hash common.Hash, number uint64) *types.BorReceipt {
	// Retrieve the flattened receipt slice
	data := ReadBorReceiptRLP(db, hash, number)
	if data == nil || len(data) == 0 {
		return nil
	}

	// Convert the receipts from their storage form to their internal representation
	var storageReceipt types.BorReceiptForStorage
	if err := rlp.DecodeBytes(data, &storageReceipt); err != nil {
		log.Error("Invalid receipt array RLP", "hash", hash, "err", err)
		return nil
	}

	return (*types.BorReceipt)(&storageReceipt)
}

// ReadBorReceipt retrieves all the bor block receipts belonging to a block, including
// its correspoinding metadata fields. If it is unable to populate these metadata
// fields then nil is returned.
func ReadBorReceipt(db ethdb.Reader, hash common.Hash, number uint64) *types.BorReceipt {
	// We're deriving many fields from the block body, retrieve beside the receipt
	borReceipt := ReadRawBorReceipt(db, hash, number)
	if borReceipt == nil {
		return nil
	}

	body := ReadBody(db, hash, number)
	if body == nil {
		log.Error("Missing body but have bor receipt", "hash", hash, "number", number)
		return nil
	}

	if err := borReceipt.DeriveFields(hash, number); err != nil {
		log.Error("Failed to derive bor receipt fields", "hash", hash, "number", number, "err", err)
		return nil
	}
	return borReceipt
}

// WriteBorReceipt stores all the bor receipt belonging to a block.
func WriteBorReceipt(db ethdb.KeyValueWriter, hash common.Hash, number uint64, borReceipt *types.BorReceiptForStorage) {
	// Convert the bor receipt into their storage form and serialize them
	bytes, err := rlp.EncodeToBytes(borReceipt)
	if err != nil {
		log.Crit("Failed to encode bor receipt", "err", err)
	}

	// Store the flattened receipt slice
	if err := db.Put(borReceiptKey(number, hash), bytes); err != nil {
		log.Crit("Failed to store bor receipt", "err", err)
	}
}

// DeleteBorReceipt removes receipt data associated with a block hash.
func DeleteBorReceipt(db ethdb.KeyValueWriter, hash common.Hash, number uint64) {
	if err := db.Delete(borReceiptKey(number, hash)); err != nil {
		log.Crit("Failed to delete bor receipt", "err", err)
	}
}
