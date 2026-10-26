package legacypool

import (
	"math/big"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

// promotableQueued calls f, in nonce order, on each queued tx of addr up to
// nonce upTo that is contiguous from the pending nonce. These are the txs the
// next reorg promotes. Gapped txs are not visited.
func (pool *LegacyPool) promotableQueued(addr common.Address, upTo uint64, f func(nonce uint64, tx *types.Transaction)) {
	q, ok := pool.queue.get(addr)
	if !ok {
		return
	}
	for n := pool.pendingNonces.get(addr); n <= upTo; n++ {
		tx := q.txs.Get(n)
		if tx == nil {
			return
		}
		f(n, tx)
	}
}

// existingExpenditure returns what addr already spends in the pool before a tx
// at nonce: its pending txs and the queued txs up to nonce that the next reorg
// promotes. Counting the queued ones stops one batch from admitting more than
// the balance pays for; gap-filling txs are still accepted because gapped txs
// are not counted. A tx at or below the pending ones (a replacement, or a reorg
// reinjecting mined txs) keeps the pending-only rule, so reinjection never drops
// a nonce that higher pending txs depend on.
func (pool *LegacyPool) existingExpenditure(addr common.Address, nonce uint64) *big.Int {
	spent := new(big.Int)
	if list := pool.pending[addr]; list != nil {
		spent = list.totalcost.ToBig()
		// Pending is contiguous, so this is nonce <= highest pending nonce in O(1).
		// LastElement would re-sort the list after every Put, under pool.mu.
		if nonce < list.txs.firstElement().Nonce()+uint64(list.Len()) {
			return spent
		}
	}
	pool.promotableQueued(addr, nonce, func(_ uint64, tx *types.Transaction) {
		spent.Add(spent, tx.Cost())
	})
	return spent
}

// existingCost returns the cost of the tx of addr at nonce that a replacement
// would free up, looking at pending first and then at promotable queued txs.
func (pool *LegacyPool) existingCost(addr common.Address, nonce uint64) *big.Int {
	if list := pool.pending[addr]; list != nil {
		if tx := list.txs.Get(nonce); tx != nil {
			return tx.Cost()
		}
	}
	var cost *big.Int
	pool.promotableQueued(addr, nonce, func(n uint64, tx *types.Transaction) {
		if n == nonce {
			cost = tx.Cost()
		}
	})
	return cost
}

// unminedPendingCost returns the cost of the pending txs of addr that must run
// before nonce: not mined yet and below nonce. On a reset, promotion runs before
// demoteUnexecutables drops the txs the new block included, while the balance
// is already reduced by them, so totalcost would count those txs twice. Pending
// txs above nonce (after a reorg) run later, so they do not hold back lower
// nonces.
func (pool *LegacyPool) unminedPendingCost(addr common.Address, nonce uint64) *uint256.Int {
	total := new(uint256.Int)
	list := pool.pending[addr]
	if list == nil {
		return total
	}
	mined := pool.currentState.GetNonce(addr)
	for n, tx := range list.txs.items {
		if n >= mined && n < nonce {
			total.Add(total, uint256.MustFromBig(tx.Cost()))
		}
	}
	return total
}

// revalidateAfterRelock checks tx against the pool again after add() released
// and re-took pool.mu to evict underpriced txs. Other adds from the same sender
// may have run meanwhile, so the balance check from before is stale. On failure
// the txs picked for eviction go back to the priced list.
//
// Must be called with pool.mu held.
func (pool *LegacyPool) revalidateAfterRelock(tx *types.Transaction, drop types.Transactions, reheapCount uint64) error {
	if err := pool.validateTx(tx); err != nil {
		go pool.priced.PutMany(drop, reheapCount)
		log.Trace("Discarding transaction that no longer fits the balance", "hash", tx.Hash(), "err", err)
		return err
	}
	return nil
}

// keepAffordable returns the prefix of readies whose running cost, on top of
// spent, fits the balance. The rest is put back into list, so it stays queued.
func keepAffordable(list *list, readies types.Transactions, balance, spent *uint256.Int) types.Transactions {
	total := new(uint256.Int).Set(spent)
	for i, tx := range readies {
		if total.Add(total, uint256.MustFromBig(tx.Cost())).Cmp(balance) > 0 {
			for _, back := range readies[i:] {
				list.Add(back, 0)
			}
			return readies[:i]
		}
	}
	return readies
}
