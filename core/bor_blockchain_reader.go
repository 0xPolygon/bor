package core

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

// GetBorReceiptByHash retrieves the bor block receipt in a given block.
func (bc *BlockChain) GetBorReceiptByHash(hash common.Hash) *types.Receipt {
	if receipt, ok := bc.borReceiptsCache.Get(hash); ok {
		return receipt
	}

	// read header from hash
	number := rawdb.ReadHeaderNumber(bc.db, hash)
	if number == nil {
		return nil
	}

	// read bor receipt by hash and number
	receipt := rawdb.ReadBorReceipt(bc.db, hash, *number, bc.chainConfig)
	if receipt == nil {
		return nil
	}

	// add into bor receipt cache
	bc.borReceiptsCache.Add(hash, receipt)

	return receipt
}

// GetBorReceiptRLPByHash retrieves the bor block receipt RLP in a given block.
func (bc *BlockChain) GetBorReceiptRLPByHash(hash common.Hash) rlp.RawValue {
	if receiptRLP, ok := bc.borReceiptsRLPCache.Get(hash); ok {
		return receiptRLP
	}

	// read header from hash
	number := rawdb.ReadHeaderNumber(bc.db, hash)
	if number == nil {
		return nil
	}

	// read bor receipt RLP by hash and number
	receiptRLP := rawdb.ReadBorReceiptRLP(bc.db, hash, *number)
	if receiptRLP == nil {
		return nil
	}

	// add into bor receipt RLP cache
	bc.borReceiptsRLPCache.Add(hash, receiptRLP)

	return receiptRLP
}

// GetAllReceiptsByHash retrieves all receipts (normal + state-sync) for
// a given block hash. The p2p message handler should use this for serving
// all receipts to the peer.
func (bc *BlockChain) GetAllReceiptsByHash(hash common.Hash) types.Receipts {
	number := rawdb.ReadHeaderNumber(bc.db, hash)
	if number == nil {
		return nil
	}

	receipts, _ := bc.receiptsCache.Get(hash)
	if receipts == nil {
		header := bc.GetHeader(hash, *number)
		if header == nil {
			return nil
		}
		receipts = rawdb.ReadReceipts(bc.db, hash, *number, header.Time, bc.chainConfig)
		bc.receiptsCache.Add(hash, receipts)
	}

	// Exit early if we are not at sprint start block (as they
	// don't have state-sync events).
	if !isSprintStart(*number, bc.chainConfig) {
		return receipts
	}

	borReceipt, _ := bc.borReceiptsCache.Get(hash)
	if borReceipt != nil {
		receipts = append(receipts, borReceipt)
		return receipts
	}

	// We're deriving many fields from the block body, retrieve beside the receipt
	borReceipt = rawdb.ReadRawBorReceipt(bc.db, hash, *number)
	if borReceipt == nil {
		return receipts
	}
	if err := types.DeriveFieldsForBorReceipt(borReceipt, hash, *number, receipts); err != nil {
		log.Error("Failed to derive bor receipt fields", "hash", hash, "number", number, "err", err)
		return receipts
	}

	bc.borReceiptsCache.Add(hash, borReceipt)

	// Return all receipts
	receipts = append(receipts, borReceipt)
	return receipts
}

func isSprintStart(number uint64, config *params.ChainConfig) bool {
	if config != nil && config.Bor != nil && config.Bor.Sprint != nil && config.Bor.IsSprintStart(number) {
		return true
	}
	return false
}
