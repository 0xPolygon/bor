package legacypool

import (
	"math/big"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/metrics"
)

// strandedEvictedCacheSize bounds how many evicted underpriced transactions the
// pool remembers to refuse re-admission from peers.
const strandedEvictedCacheSize = 8192

var (
	strandedEvictionMeter = metrics.NewRegisteredMeter("txpool/pending/stranded/eviction", nil)
	strandedRejectMeter   = metrics.NewRegisteredMeter("txpool/pending/stranded/reject", nil)
)

// strandedHead records since when the lowest-nonce pending transaction of an
// account has been unminable.
type strandedHead struct {
	nonce uint64
	since time.Time
}

// strandedState tracks accounts whose pending chain is blocked by a head that
// no block producer would include at the current base fee. Pending
// transactions are otherwise never expired: Lifetime only applies to the
// queue, so a gapless chain behind such a head stays in the pool until the
// base fee drops enough for the head to be mined.
type strandedState struct {
	heads   map[common.Address]strandedHead
	evicted lru.BasicLRU[common.Hash, time.Time] // evicted txs that were unminable
}

func newStrandedState() strandedState {
	return strandedState{
		heads:   make(map[common.Address]strandedHead),
		evicted: lru.NewBasicLRU[common.Hash, time.Time](strandedEvictedCacheSize),
	}
}

// unminable reports whether a block producer would never include tx at the
// given base fee: either its fee cap is below the base fee, or its effective tip
// is below the minimum tip producers enforce (see the MinTip filter in Pending).
// The fee cap check is explicit because EffectiveGasTipIntCmp falls back to the
// tip cap when the fee cap is below the base fee.
func unminable(tx *types.Transaction, baseFee *big.Int, minTip *uint256.Int) bool {
	if tx.GasFeeCap().Cmp(baseFee) < 0 {
		return true
	}
	return tx.EffectiveGasTipIntCmp(minTip, uint256.MustFromBig(baseFee)) < 0
}

// strandedBaseFee returns the base fee of the next block, the same one the
// priced list and producers use, or nil before London.
func (pool *LegacyPool) strandedBaseFee() *big.Int {
	head := pool.currentHead.Load()
	if head == nil || head.BaseFee == nil || !pool.chainconfig.IsLondon(head.Number) {
		return nil
	}
	return eip1559.CalcBaseFee(pool.chainconfig, head)
}

// evictStranded drops the whole pending chain of every account whose head has
// been unminable at the current base fee for longer than the pool lifetime.
// On a chain whose base fee is held near a target, such a head will not become
// payable again, and everything behind it can never be mined.
//
// Must be called with pool.mu held.
func (pool *LegacyPool) evictStranded(now time.Time) {
	baseFee := pool.strandedBaseFee()
	if baseFee == nil {
		clear(pool.stranded.heads)
		return
	}
	minTip := pool.gasTip.Load()

	for addr, list := range pool.pending {
		first := list.txs.firstElement()
		if first == nil || !unminable(first, baseFee, minTip) {
			delete(pool.stranded.heads, addr)
			continue
		}
		stranded, ok := pool.stranded.heads[addr]
		if !ok || stranded.nonce != first.Nonce() {
			pool.stranded.heads[addr] = strandedHead{nonce: first.Nonce(), since: now}
			continue
		}
		if now.Sub(stranded.since) <= pool.config.Lifetime {
			continue
		}
		for _, tx := range list.txs.flatten() {
			if unminable(tx, baseFee, minTip) {
				pool.stranded.evicted.Add(tx.Hash(), now)
			}
		}
		pool.dropPendingAccount(addr, list)
		delete(pool.stranded.heads, addr)
	}
	for addr := range pool.stranded.heads {
		if _, ok := pool.pending[addr]; !ok {
			delete(pool.stranded.heads, addr)
		}
	}
}

// dropPendingAccount removes every pending transaction of addr in one pass.
// Removing them one by one via removeTx would demote the tail into the queue
// on the first removal and scan the nonce heap on each one.
//
// Must be called with pool.mu held.
func (pool *LegacyPool) dropPendingAccount(addr common.Address, list *list) {
	first := list.txs.firstElement().Nonce()
	drops := list.Cap(0)
	for _, tx := range drops {
		hash := tx.Hash()
		pool.all.Remove(hash)
		delete(pool.lastRebroadcast, hash)
	}
	delete(pool.pending, addr)
	pool.pendingNonces.setIfLower(addr, first)
	pool.priced.Removed(len(drops))
	pendingGauge.Dec(int64(len(drops)))
	strandedEvictionMeter.Mark(int64(len(drops)))

	if _, hasQueued := pool.queue.get(addr); !hasQueued {
		pool.reserver.Release(addr)
	}
}

// isEvictedStranded reports whether tx was evicted as stranded within the
// lifetime and is still unminable at the current base fee. Rejecting it as
// underpriced keeps peers from pushing it straight back, and lets the tx
// fetcher remember it as underpriced too.
//
// Must be called with pool.mu held.
func (pool *LegacyPool) isEvictedStranded(tx *types.Transaction) bool {
	evictedAt, ok := pool.stranded.evicted.Peek(tx.Hash())
	if !ok {
		return false
	}
	if time.Since(evictedAt) > pool.config.Lifetime {
		pool.stranded.evicted.Remove(tx.Hash())
		return false
	}
	baseFee := pool.strandedBaseFee()
	if baseFee == nil || !unminable(tx, baseFee, pool.gasTip.Load()) {
		return false
	}
	strandedRejectMeter.Mark(1)
	return true
}
