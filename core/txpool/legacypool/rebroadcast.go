// Copyright 2026 The go-ethereum Authors
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

package legacypool

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// RebroadcastAcknowledgement creates a batch-scoped callback for gossip queue
// acceptance. Selecting candidates or creating the callback does not record a
// send. Duplicate acknowledgments within a batch are ignored, including those
// from multiple peers. This is separate from the public StuckTxsEvent shape.
func (pool *LegacyPool) RebroadcastAcknowledgement(txs []*types.Transaction) func([]common.Hash) {
	return pool.rebroadcastAcknowledgement(txs, true)
}

func (pool *LegacyPool) rebroadcastAcknowledgement(txs []*types.Transaction, countMetrics bool) func([]common.Hash) {
	remaining := make(map[common.Hash]*types.Transaction, len(txs))
	for _, tx := range txs {
		remaining[tx.Hash()] = tx
	}
	return func(hashes []common.Hash) {
		pool.mu.Lock()
		defer pool.mu.Unlock()

		now := time.Now()
		var count int64
		for _, hash := range hashes {
			tx := remaining[hash]
			if tx == nil {
				continue
			}
			delete(remaining, hash)
			// Removal or replacement may happen between selection and gossip.
			if pool.all.Get(hash) == tx {
				pool.lastRebroadcast[hash] = now
				count++
			}
		}
		rebroadcastTrackingGauge.Update(int64(len(pool.lastRebroadcast)))
		if countMetrics {
			rebroadcastTxMeter.Mark(count)
		}
	}
}
