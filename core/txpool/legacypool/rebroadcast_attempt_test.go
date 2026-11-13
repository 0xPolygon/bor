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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestRebroadcastSuppressedOldBatch(t *testing.T) {
	pool, _, from, tx := setupRebroadcastTest(t, time.Hour, 24*time.Hour, 200)
	defer pool.Close()
	ch := make(chan core.StuckTxsEvent, 1)
	sub := pool.SubscribeRebroadcastTransactionsWithAcknowledgement(ch)
	defer sub.Unsubscribe()
	pool.mu.Lock()
	setTxAge(pool, from, 48*time.Hour)
	batch := pool.identifyStuckTransactions()
	pool.mu.Unlock()
	require.Len(t, batch, 1)
	pool.publishRebroadcastTransactions(batch)
	<-ch
	pool.mu.RLock()
	require.Empty(t, pool.lastRebroadcastAttempt)
	require.Len(t, pool.identifyStuckTransactions(), 1)
	pool.mu.RUnlock()
	attempt := pool.RebroadcastAttempt(batch)
	attempt([]common.Hash{{99}, tx.Hash()})
	pool.mu.RLock()
	first := pool.lastRebroadcastAttempt[tx.Hash()]
	require.False(t, first.IsZero())
	require.Empty(t, pool.lastRebroadcast)
	require.Empty(t, pool.identifyStuckTransactions())
	pool.mu.RUnlock()
	attempt([]common.Hash{tx.Hash()})
	pool.mu.RLock()
	require.Equal(t, first, pool.lastRebroadcastAttempt[tx.Hash()])
	pool.mu.RUnlock()
	late := pool.RebroadcastAttempt([]*types.Transaction{tx})
	pool.mu.Lock()
	pool.removeTx(tx.Hash(), true, false)
	pool.mu.Unlock()
	late([]common.Hash{tx.Hash()})
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	require.Empty(t, pool.lastRebroadcastAttempt)
}

func TestRebroadcastOldRefusedOffersRetryUntilAccepted(t *testing.T) {
	pool, _, from, tx := setupRebroadcastTest(t, time.Minute, 10*time.Minute, 200)
	defer pool.Close()
	pool.mu.Lock()
	setTxAge(pool, from, 31*time.Minute)
	pool.mu.Unlock()
	for range 3 {
		pool.mu.RLock()
		batch := pool.identifyStuckTransactions()
		pool.mu.RUnlock()
		require.Equal(t, []*types.Transaction{tx}, batch)
		pool.RebroadcastAttempt(batch)([]common.Hash{tx.Hash()})
		pool.RebroadcastAcknowledgement(batch)(nil)
		pool.mu.RLock()
		require.Empty(t, pool.identifyStuckTransactions(), "refusal must respect the retry interval")
		require.Empty(t, pool.lastRebroadcast)
		pool.mu.RUnlock()
		advanceRebroadcastTime(pool, from, 2*time.Minute)
	}
	pool.RebroadcastAcknowledgement([]*types.Transaction{tx})([]common.Hash{tx.Hash()})
	advanceRebroadcastTime(pool, from, 2*time.Minute)
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	require.NotZero(t, pool.lastRebroadcast[tx.Hash()])
	require.Empty(t, pool.identifyStuckTransactions(), "accepted old transactions must obey the maximum age")
	require.Same(t, tx, pool.all.Get(tx.Hash()))
}
