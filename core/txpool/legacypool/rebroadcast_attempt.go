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
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// RebroadcastAttempt records actual queue offers independently of acceptance.
// The batch owns one attempt per hash, even when several peers refuse it.
func (pool *LegacyPool) RebroadcastAttempt(txs []*types.Transaction) func([]common.Hash) {
	remaining := make(map[common.Hash]*types.Transaction, len(txs))
	for _, tx := range txs {
		remaining[tx.Hash()] = tx
	}
	return func(hashes []common.Hash) {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		now := pool.rebroadcastTime()
		for _, hash := range hashes {
			tx := remaining[hash]
			if tx == nil {
				continue
			}
			delete(remaining, hash)
			if pool.all.Get(hash) == tx {
				pool.lastRebroadcastAttempt[hash] = now
			}
		}
	}
}
