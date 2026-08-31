package filters

import (
	"context"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

func (api *FilterAPI) subscribeLogs(crit FilterCriteria, logs chan []*types.Log) (*Subscription, error) {
	if crit.Pending || isPendingRange(crit) {
		if !onlyPendingRange(crit) {
			return nil, errInvalidBlockRange
		}
		query := crit.query()
		query.FromBlock = nil
		query.ToBlock = nil
		return api.events.SubscribePendingLogs(query, logs)
	}
	return api.events.SubscribeLogs(crit.query(), logs)
}

func (es *EventSystem) validateLogCriteria(crit ethereum.FilterQuery) error {
	if len(crit.Topics) > maxTopics {
		return errExceedMaxTopics
	}
	limit := es.sys.cfg.LogQueryLimit
	if limit == 0 {
		return nil
	}
	if len(crit.Addresses) > limit {
		return errExceedLogQueryLimit
	}
	for _, topics := range crit.Topics {
		if len(topics) > limit {
			return errExceedLogQueryLimit
		}
	}
	return nil
}

func (es *EventSystem) SubscribePendingLogs(crit ethereum.FilterQuery, logs chan []*types.Log) (*Subscription, error) {
	if err := es.validateLogCriteria(crit); err != nil {
		return nil, err
	}
	if crit.BlockHash != nil || crit.FromBlock != nil || crit.ToBlock != nil {
		return nil, errInvalidBlockRange
	}
	sub := &subscription{
		id: rpc.NewID(), typ: PendingLogsSubscription, logsCrit: crit, created: time.Now(), logs: logs,
		txs: make(chan []*types.Transaction), headers: make(chan *types.Header),
		receipts: make(chan []*ReceiptWithTx), installed: make(chan struct{}), err: make(chan error),
	}
	return es.subscribe(sub), nil
}

func isPendingRange(crit FilterCriteria) bool {
	return crit.FromBlock != nil && crit.FromBlock.Int64() == rpc.PendingBlockNumber.Int64() ||
		crit.ToBlock != nil && crit.ToBlock.Int64() == rpc.PendingBlockNumber.Int64()
}

type pendingLogsBackend interface {
	PendingBlockAndReceipts() (*types.Block, types.Receipts)
}

func (api *FilterAPI) getPendingLogs(ctx context.Context, crit FilterCriteria) ([]*types.Log, error) {
	if !onlyPendingRange(crit) {
		return nil, errInvalidBlockRange
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	block, receipts, err := api.pendingBlockReceipts(ctx)
	if err != nil {
		return nil, err
	}
	if err := validatePendingReceipts(block, receipts); err != nil {
		return nil, err
	}
	return returnLogs(filterLogs(receiptLogs(receipts), nil, nil, crit.Addresses, crit.Topics)), nil
}

func (api *FilterAPI) pendingBlockReceipts(ctx context.Context) (*types.Block, types.Receipts, error) {
	if backend, ok := api.sys.backend.(pendingLogsBackend); ok {
		block, receipts := backend.PendingBlockAndReceipts()
		if block == nil {
			return nil, nil, errPendingLogsUnsupported
		}
		return block, receipts, nil
	}
	header, err := api.sys.backend.HeaderByNumber(ctx, rpc.PendingBlockNumber)
	if err != nil {
		return nil, nil, err
	}
	if header == nil {
		return nil, nil, errPendingLogsUnsupported
	}
	receipts, err := api.sys.backend.GetReceipts(ctx, header.Hash())
	return nil, receipts, err
}

func validatePendingReceipts(block *types.Block, receipts types.Receipts) error {
	for _, receipt := range receipts {
		if receipt == nil {
			return errPendingLogsIncomplete
		}
	}
	if block == nil {
		return nil
	}
	txs := block.Transactions()
	if len(receipts) != len(txs) {
		return errPendingLogsIncomplete
	}
	for i, receipt := range receipts {
		if receipt.TxHash != txs[i].Hash() {
			return errPendingLogsIncomplete
		}
	}
	return nil
}

func receiptLogs(receipts types.Receipts) []*types.Log {
	var logs []*types.Log
	for _, receipt := range receipts {
		logs = append(logs, receipt.Logs...)
	}
	return logs
}

func onlyPendingRange(crit FilterCriteria) bool {
	if crit.BlockHash != nil {
		return false
	}
	if crit.FromBlock == nil {
		return crit.Pending && crit.ToBlock == nil
	}
	if crit.FromBlock != nil && crit.FromBlock.Int64() != rpc.PendingBlockNumber.Int64() {
		return false
	}
	return crit.ToBlock == nil || crit.ToBlock.Int64() == rpc.PendingBlockNumber.Int64()
}
