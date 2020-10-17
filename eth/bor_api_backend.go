package eth

import (
	"context"

	ethereum "github.com/maticnetwork/bor"
	"github.com/maticnetwork/bor/common"
	"github.com/maticnetwork/bor/consensus/bor"
	"github.com/maticnetwork/bor/core/types"
)

func (b *EthAPIBackend) GetRootHash(ctx context.Context, starBlockNr uint64, endBlockNr uint64) (string, error) {
	var api *bor.API
	for _, _api := range b.eth.Engine().APIs(b.eth.BlockChain()) {
		if _api.Namespace == "bor" {
			api = _api.Service.(*bor.API)
		}
	}

	root, err := api.GetRootHash(starBlockNr, endBlockNr)
	if err != nil {
		return "", err
	}
	return root, nil
}

func (b *EthAPIBackend) GetBorBlockReceipt(ctx context.Context, hash common.Hash) (*types.BorReceipt, error) {
	receipt := b.eth.blockchain.GetBorReceiptByHash(hash)
	if receipt == nil {
		return nil, ethereum.NotFound
	}

	return receipt, nil
}

// func (b *EthAPIBackend) GetBorBlockLogs(ctx context.Context, hash common.Hash) ([][]*types.Log, error) {
// 	receipts := b.eth.blockchain.GetReceiptsByHash(hash)
// 	if receipts == nil {
// 		return nil, nil
// 	}
// 	logs := make([][]*types.Log, len(receipts))
// 	for i, receipt := range receipts {
// 		logs[i] = receipt.Logs
// 	}
// 	return logs, nil
// }
