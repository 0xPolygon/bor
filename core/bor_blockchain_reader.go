package core

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rlp"
)

var (
	borReceiptsCacheHit     = metrics.NewRegisteredGauge("bor/receipts/cache/hit", nil)
	borReceiptsCacheMiss    = metrics.NewRegisteredGauge("bor/receipts/cache/miss", nil)
	borReceiptsRLPCacheHit  = metrics.NewRegisteredGauge("bor/rlpreceipts/cache/hit", nil)
	borReceiptsRLPCacheMiss = metrics.NewRegisteredGauge("bor/rlpreceipts/cache/miss", nil)
)

// GetBorReceiptByHash retrieves the bor block receipt in a given block.
func (bc *BlockChain) GetBorReceiptByHash(hash common.Hash) *types.Receipt {
	if receipt, ok := bc.borReceiptsCache.Get(hash); ok {
		borReceiptsCacheHit.Update(1)
		return receipt
	}
	borReceiptsCacheMiss.Update(1)

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
		borReceiptsRLPCacheHit.Update(1)
		return receiptRLP
	}
	borReceiptsRLPCacheMiss.Update(1)

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

// GetRawBorReceipt retrieves the raw state-sync transaction receipt of the given block
// without any derived fields (to be sent over p2p).
func (bc *BlockChain) GetRawBorReceipt(hash common.Hash, number uint64) *types.Receipt {
	return rawdb.ReadRawBorReceipt(bc.db, hash, number)
}
