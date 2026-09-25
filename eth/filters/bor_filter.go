// Copyright 2014 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package filters

import (
	"context"
	"fmt"
	big "math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rpc"
)

// BorBlockLogsFilter can be used to retrieve and filter logs.
type BorBlockLogsFilter struct {
	backend   Backend
	borConfig *params.BorConfig

	db        ethdb.Database
	addresses []common.Address
	topics    [][]common.Hash

	block      common.Hash // Block hash if filtering a single block
	begin, end int64       // Range interval if filtering multiple blocks
}

// NewBorBlockLogsRangeFilter creates a new filter which uses a bloom filter on blocks to
// figure out whether a particular block is interesting or not.
func NewBorBlockLogsRangeFilter(backend Backend, borConfig *params.BorConfig, begin, end int64, addresses []common.Address, topics [][]common.Hash) *BorBlockLogsFilter {
	// Create a generic filter and convert it into a range filter
	f := newBorBlockLogsFilter(backend, borConfig, addresses, topics)
	f.begin = begin
	f.end = end

	return f
}

// NewBorBlockLogsFilter creates a new filter which directly inspects the contents of
// a block to figure out whether it is interesting or not.
func NewBorBlockLogsFilter(backend Backend, borConfig *params.BorConfig, block common.Hash, addresses []common.Address, topics [][]common.Hash) *BorBlockLogsFilter {
	// Create a generic filter and convert it into a block filter
	f := newBorBlockLogsFilter(backend, borConfig, addresses, topics)
	f.block = block

	return f
}

// newBorBlockLogsFilter creates a generic filter that can either filter based on a block hash,
// or based on range queries. The search criteria needs to be explicitly set.
func newBorBlockLogsFilter(backend Backend, borConfig *params.BorConfig, addresses []common.Address, topics [][]common.Hash) *BorBlockLogsFilter {
	return &BorBlockLogsFilter{
		backend:   backend,
		borConfig: borConfig,
		addresses: addresses,
		topics:    topics,
		db:        backend.ChainDb(),
	}
}

// Logs searches the blockchain for matching log entries, returning all from the
// first block that contains matches, updating the start of the filter accordingly.
func (f *BorBlockLogsFilter) Logs(ctx context.Context) ([]*types.Log, error) {
	// If we're doing singleton block filtering, execute and return
	if f.block != (common.Hash{}) {
		receipt, _ := f.backend.GetBorBlockReceipt(ctx, f.block)
		if receipt == nil {
			return nil, nil
		}

		return f.borBlockLogs(ctx, receipt)
	}

	// Figure out the limits of the filter range
	header, _ := f.backend.HeaderByNumber(ctx, rpc.LatestBlockNumber)
	if header == nil {
		return nil, nil
	}

	head := header.Number.Uint64()

	// Resolve the rpc.BlockNumber sentinels before doing any arithmetic on the
	// bounds: the API hands us rpc.LatestBlockNumber (-2, not -1) when the
	// caller omits fromBlock/toBlock, and uint64(-2) would otherwise end the
	// range scan before its first iteration.
	begin, err := f.resolveBlockNumber(ctx, f.begin, head)
	if err != nil {
		return nil, err
	}

	end, err := f.resolveBlockNumber(ctx, f.end, head)
	if err != nil {
		return nil, err
	}

	// adjust begin for sprint
	f.begin = currentSprintEnd(f.borConfig.CalculateSprint(uint64(begin)), begin)

	// begin already on PIP-74, no more need for bor logs
	if f.borConfig != nil && f.borConfig.IsMadhugiri(big.NewInt(f.begin)) {
		return nil, nil
	}

	// end on PIP-74, reduce to fit just on preHF blocks
	if f.borConfig != nil && f.borConfig.IsMadhugiri(big.NewInt(end)) {
		end = f.borConfig.MadhugiriBlock.Int64() - 1
	}

	// Gather all indexed logs, and finish with non indexed ones
	return f.unindexedLogs(ctx, uint64(end))
}

// resolveBlockNumber maps the negative rpc.BlockNumber sentinels onto concrete
// block numbers, mirroring resolveSpecial in filter.go: latest and pending
// resolve to the current head, finalized and safe to the corresponding
// headers, and earliest to genesis.
func (f *BorBlockLogsFilter) resolveBlockNumber(ctx context.Context, number int64, head uint64) (int64, error) {
	switch number {
	case rpc.LatestBlockNumber.Int64(), rpc.PendingBlockNumber.Int64():
		return int64(head), nil
	case rpc.FinalizedBlockNumber.Int64(), rpc.SafeBlockNumber.Int64():
		hdr, _ := f.backend.HeaderByNumber(ctx, rpc.BlockNumber(number))
		if hdr == nil {
			return 0, fmt.Errorf("%s header not found", rpc.BlockNumber(number).String())
		}
		return hdr.Number.Int64(), nil
	case rpc.EarliestBlockNumber.Int64():
		return 0, nil
	default:
		if number < 0 {
			return 0, fmt.Errorf("invalid block number %d", number)
		}
		return number, nil
	}
}

// unindexedLogs returns the logs matching the filter criteria based on raw block
// iteration and bloom matching.
func (f *BorBlockLogsFilter) unindexedLogs(ctx context.Context, end uint64) ([]*types.Log, error) {
	var logs []*types.Log

	sprintLength := f.borConfig.CalculateSprint(uint64(f.begin))

	for ; f.begin <= int64(end); f.begin = f.begin + int64(sprintLength) {
		// Honor context cancellation (client disconnect or deadline) so a large
		// range scan stops promptly instead of holding an RPC worker busy with
		// work whose result the caller will never read. Mirrors the check in
		// upstream go-ethereum eth/filters/filter.go's unindexedLogs.
		select {
		case <-ctx.Done():
			return logs, ctx.Err()
		default:
		}

		header, err := f.backend.HeaderByNumber(ctx, rpc.BlockNumber(f.begin))
		if header == nil || err != nil {
			return logs, err
		}

		// get bor block receipt
		receipt, err := f.backend.GetBorBlockReceipt(ctx, header.Hash())
		if receipt == nil || err != nil {
			continue
		}

		// filter bor block logs
		found, err := f.borBlockLogs(ctx, receipt)
		if err != nil {
			return logs, err
		}

		logs = append(logs, found...)
		sprintLength = f.borConfig.CalculateSprint(uint64(f.begin))
	}

	return logs, nil
}

// borBlockLogs returns the logs matching the filter criteria within a single block.
func (f *BorBlockLogsFilter) borBlockLogs(_ context.Context, receipt *types.Receipt) (logs []*types.Log, err error) {
	// no bloom filter applied since bor logs has no effect on bloom
	logs = filterLogs(receipt.Logs, nil, nil, f.addresses, f.topics)

	return logs, nil
}

func currentSprintEnd(sprint uint64, n int64) int64 {
	m := n % int64(sprint)
	if m == 0 {
		return n
	}

	return n + int64(sprint) - m
}
