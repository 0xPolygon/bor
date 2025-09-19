package core

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
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
