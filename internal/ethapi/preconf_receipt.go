package ethapi

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type preconfBackend interface {
	GetPreconfTransaction(common.Hash) (*types.Transaction, *types.Receipt, bool)
}

func getPreconfTransaction(backend Backend, hash common.Hash) (*types.Transaction, *types.Receipt, bool) {
	provider, ok := backend.(preconfBackend)
	if !ok {
		return nil, nil, false
	}
	return provider.GetPreconfTransaction(hash)
}

func preconfTransactionReceipt(backend Backend, hash common.Hash) map[string]interface{} {
	tx, receipt, ok := getPreconfTransaction(backend, hash)
	if !ok || tx == nil || receipt == nil || receipt.BlockNumber == nil {
		return nil
	}
	copyReceipt := *receipt
	copyReceipt.Logs = make([]*types.Log, len(receipt.Logs))
	for i, entry := range receipt.Logs {
		if entry == nil {
			continue
		}
		copyLog := *entry
		copyLog.BlockHash = common.Hash{}
		copyReceipt.Logs[i] = &copyLog
	}
	blockNumber := receipt.BlockNumber.Uint64()
	signer := types.LatestSigner(backend.ChainConfig())
	if block := pendingBlock(backend); block != nil && block.Number().Cmp(receipt.BlockNumber) == 0 {
		signer = types.MakeSigner(backend.ChainConfig(), receipt.BlockNumber, block.Time())
	}
	result := marshalReceipt(&copyReceipt, common.Hash{}, blockNumber, signer, tx, int(receipt.TransactionIndex), false)
	result["blockHash"] = nil
	return result
}
