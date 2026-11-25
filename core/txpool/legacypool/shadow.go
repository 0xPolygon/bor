package legacypool

import "github.com/ethereum/go-ethereum/common"

// ShadowRemove drops one transaction from the pool. Used only by the LLS
// shadow-producer experiment to retire injected transactions after use.
func (pool *LegacyPool) ShadowRemove(hash common.Hash) bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	if pool.all.Get(hash) == nil {
		return false
	}

	pool.removeTx(hash, true, true)

	return true
}
