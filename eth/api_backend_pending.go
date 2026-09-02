package eth

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
)

func (b *EthAPIBackend) sequencerPendingBlock() *types.Block {
	if b.eth.seqConsumer == nil {
		return nil
	}
	return b.eth.seqConsumer.PendingBlock()
}

func (b *EthAPIBackend) sequencerPendingBlockAndReceipts() (*types.Block, types.Receipts) {
	if b.eth.seqConsumer == nil {
		return nil, nil
	}
	return b.eth.seqConsumer.PendingBlockAndReceipts()
}

func (b *EthAPIBackend) PendingBlock() *types.Block {
	if block := b.sequencerPendingBlock(); block != nil {
		return block
	}
	if b.eth.miner == nil {
		return nil
	}
	return b.eth.miner.PendingBlock()
}

func (b *EthAPIBackend) PendingBlockAndReceipts() (*types.Block, types.Receipts) {
	if block, receipts := b.sequencerPendingBlockAndReceipts(); block != nil {
		return block, receipts
	}
	if b.eth.miner == nil {
		return nil, nil
	}
	block, receipts, _ := b.eth.miner.Pending()
	return block, receipts
}

func (b *EthAPIBackend) PendingSnapshot(ctx context.Context) (*types.Block, types.Receipts, *state.StateDB, error) {
	if b.eth.seqConsumer != nil {
		block, receipts, statedb, err := b.eth.seqConsumer.PendingSnapshot(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		if block != nil && statedb != nil {
			return block, receipts, statedb, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	if b.eth.miner != nil {
		block, receipts, statedb := b.eth.miner.Pending()
		if block != nil && statedb != nil {
			return block, receipts, statedb, nil
		}
	}
	return nil, nil, nil, errors.New("pending state is not available")
}

func (b *EthAPIBackend) PendingParentState(ctx context.Context, block *types.Block) (*state.StateDB, error) {
	if b.eth.seqConsumer == nil {
		return nil, nil
	}
	return b.eth.seqConsumer.PendingParentState(ctx, block)
}

func (b *EthAPIBackend) pendingStateAndHeader(ctx context.Context) (*state.StateDB, *types.Header, error) {
	block, _, statedb, err := b.PendingSnapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	return statedb, block.Header(), nil
}
