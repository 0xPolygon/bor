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
	"container/heap"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core/types"
)

// identifyStuckTransactions orders transactions by their last attempt or admission.
// Selection is read-only; publication records attempts independently of queue acceptance.
// Must be called with pool.mu.RLock held.
func (pool *LegacyPool) identifyStuckTransactions() []*types.Transaction {
	head := pool.currentHead.Load()
	if head == nil || pool.config.RebroadcastBatchSize <= 0 {
		return nil
	}
	var baseFee *big.Int
	if pool.chainconfig.IsLondon(head.Number) && head.BaseFee != nil {
		baseFee = eip1559.CalcBaseFee(pool.chainconfig, head)
	}
	now, minTip := time.Now(), pool.gasTip.Load().ToBig()
	candidates := make(rebroadcastCandidates, 0, min(pool.config.RebroadcastBatchSize, pool.all.Count()))
	for _, list := range pool.pending {
		for _, tx := range list.txs.items {
			last := pool.rebroadcastLastActivity(tx.Hash())
			if !pool.rebroadcastEligible(tx, last, now, baseFee, minTip) {
				continue
			}
			candidates.consider(rebroadcastCandidate{tx: tx, last: last}, pool.config.RebroadcastBatchSize)
		}
	}
	return candidates.popTransactions()
}

func (pool *LegacyPool) rebroadcastEligible(tx *types.Transaction, last, now time.Time, baseFee, minTip *big.Int) bool {
	age := now.Sub(tx.Time())
	if age < pool.config.RebroadcastInterval {
		return false
	}
	if !last.IsZero() {
		if now.Sub(last) < pool.config.RebroadcastInterval {
			return false
		}
		// Allow one first attempt after a late fee drop. Further attempts obey
		// the age limit even when no peer has accepted the transaction.
		if pool.config.RebroadcastMaxAge > 0 && age > pool.config.RebroadcastMaxAge {
			return false
		}
	}
	return (baseFee == nil || tx.GasFeeCapIntCmp(baseFee) >= 0) &&
		tx.GasTipCapIntCmp(minTip) >= 0 && pool.canRebroadcast(tx)
}

type rebroadcastCandidate struct {
	tx   *types.Transaction
	last time.Time
}

func (c rebroadcastCandidate) before(other rebroadcastCandidate) bool {
	// First attempts join at admission time so later arrivals cannot indefinitely delay retries.
	priority, otherPriority := c.last, other.last
	if priority.IsZero() {
		priority = c.tx.Time()
	}
	if otherPriority.IsZero() {
		otherPriority = other.tx.Time()
	}
	if !priority.Equal(otherPriority) {
		return priority.Before(otherPriority)
	}
	if !c.tx.Time().Equal(other.tx.Time()) {
		return c.tx.Time().Before(other.tx.Time())
	}
	return c.tx.Nonce() < other.tx.Nonce()
}

// Keep the least preferred candidate at the root so selection retains only
// one batch while scanning the pool, rather than sorting every pending transaction.
type rebroadcastCandidates []rebroadcastCandidate

func (h *rebroadcastCandidates) consider(candidate rebroadcastCandidate, limit int) {
	if len(*h) < limit {
		heap.Push(h, candidate)
	} else if candidate.before((*h)[0]) {
		(*h)[0] = candidate
		heap.Fix(h, 0)
	}
}

func (h rebroadcastCandidates) Len() int           { return len(h) }
func (h rebroadcastCandidates) Less(i, j int) bool { return h[j].before(h[i]) }
func (h rebroadcastCandidates) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *rebroadcastCandidates) Push(x any)        { *h = append(*h, x.(rebroadcastCandidate)) }
func (h *rebroadcastCandidates) Pop() any {
	last := len(*h) - 1
	x := (*h)[last]
	(*h)[last] = rebroadcastCandidate{}
	*h = (*h)[:last]
	return x
}

func (pool *LegacyPool) rebroadcastLastActivity(hash common.Hash) time.Time {
	last := pool.lastRebroadcastAttempt[hash]
	if accepted := pool.lastRebroadcast[hash]; accepted.After(last) {
		return accepted
	}
	return last
}

func (h *rebroadcastCandidates) popTransactions() []*types.Transaction {
	txs := make([]*types.Transaction, len(*h))
	for i := len(txs) - 1; i >= 0; i-- {
		txs[i] = heap.Pop(h).(rebroadcastCandidate).tx
	}
	return txs
}
