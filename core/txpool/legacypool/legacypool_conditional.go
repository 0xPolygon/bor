package legacypool

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

// conditionalAccountSlots is the share of the conditional budget one sender may
// use: one tx at the KnownAccounts length limit. It keeps a single account from
// filling the pool-wide budget and locking everyone else out.
const conditionalAccountSlots = 1000

var (
	conditionalRejectMeter   = metrics.NewRegisteredMeter("txpool/conditional/reject", nil)
	conditionalOverflowMeter = metrics.NewRegisteredMeter("txpool/conditional/overflow", nil)
	conditionalGappedMeter   = metrics.NewRegisteredMeter("txpool/conditional/gapped", nil)
	conditionalEvictionMeter = metrics.NewRegisteredMeter("txpool/conditional/eviction", nil)
	conditionalSlotsGauge    = metrics.NewRegisteredGauge("txpool/conditional/slots", nil)
)

// conditionalWeight returns how much of the conditional budget tx uses: its
// known accounts and slots, and at least 1 for any conditional transaction.
// Plain transactions weigh 0.
func conditionalWeight(tx *types.Transaction) int {
	options := tx.GetOptions()
	if options == nil {
		return 0
	}
	return max(1, options.KnownAccounts.Length())
}

// checkConditional rejects a PIP-15 conditional transaction when the pool does
// not accept them, when its nonce is not executable, or when it would push the
// pool or its sender over the conditional budget. Every reset re-validates all
// pending conditional transactions under pool.mu, so the budget bounds how long
// that takes.
//
// Must be called with pool.mu held.
func (pool *LegacyPool) checkConditional(tx *types.Transaction) error {
	weight := conditionalWeight(tx)
	if weight == 0 {
		return nil
	}
	if !pool.config.ConditionalTxs {
		conditionalRejectMeter.Mark(1)
		return txpool.ErrConditionalTxsDisabled
	}
	from, _ := types.Sender(pool.signer, tx) // already validated
	if tx.Nonce() > pool.executableNonce(from) {
		conditionalGappedMeter.Mark(1)
		return txpool.ErrConditionalNonceGap
	}
	account, replaced := pool.conditionalAccountWeight(from, tx.Nonce())
	if uint64(pool.all.ConditionalSlots()-replaced+weight) > pool.config.ConditionalSlots {
		conditionalOverflowMeter.Mark(1)
		return txpool.ErrConditionalPoolFull
	}
	if account-replaced+weight > conditionalAccountSlots {
		conditionalOverflowMeter.Mark(1)
		return txpool.ErrConditionalAccountLimit
	}
	return nil
}

// executableNonce returns the highest nonce of addr that can run without a gap:
// the pending nonce plus the queued txs the next reorg promotes.
func (pool *LegacyPool) executableNonce(addr common.Address) uint64 {
	nonce := pool.pendingNonces.get(addr)
	if q, ok := pool.queue.get(addr); ok {
		for q.txs.Get(nonce) != nil {
			nonce++
		}
	}
	return nonce
}

// conditionalAccountWeight returns the conditional weight of all pooled txs of
// addr, and the weight of its tx at nonce, which a new tx would replace.
func (pool *LegacyPool) conditionalAccountWeight(addr common.Address, nonce uint64) (total int, replaced int) {
	lists := []*list{pool.pending[addr]}
	if q, ok := pool.queue.get(addr); ok {
		lists = append(lists, q)
	}
	for _, l := range lists {
		if l == nil {
			continue
		}
		for n, tx := range l.txs.items {
			w := conditionalWeight(tx)
			total += w
			if n == nonce {
				replaced = w
			}
		}
	}
	return total, replaced
}

// evictConditional drops every conditional transaction, pending or queued, that
// has been in the pool for longer than the conditional lifetime. Non-producing
// nodes never include them, so without this they would stay until restart.
//
// Must be called with pool.mu held.
func (pool *LegacyPool) evictConditional(now time.Time) {
	if pool.all.ConditionalSlots() == 0 {
		return
	}
	var expired []common.Hash
	pool.all.Range(func(hash common.Hash, tx *types.Transaction) bool {
		if tx.GetOptions() != nil && now.Sub(tx.Time()) > pool.config.ConditionalLifetime {
			expired = append(expired, hash)
		}
		return true
	})
	for _, hash := range expired {
		pool.removeTx(hash, true, true)
	}
	if len(expired) > 0 {
		conditionalEvictionMeter.Mark(int64(len(expired)))
		log.Debug("Evicted expired conditional transactions", "count", len(expired))
	}
}
