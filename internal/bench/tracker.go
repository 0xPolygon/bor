package bench

import (
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/log"
)

// MinedTracker tracks which submitted transactions have been mined.
// It subscribes to chain head events and marks transactions as mined
// when they appear in new blocks.
type MinedTracker struct {
	eth *eth.Ethereum

	// seen holds hashes of all submitted transactions
	seen map[common.Hash]struct{}
	// mined holds hashes of transactions confirmed in blocks
	mined map[common.Hash]struct{}

	mu         sync.RWMutex
	minedCount atomic.Uint64
}

// NewMinedTracker creates a new tracker for the given Ethereum backend.
func NewMinedTracker(e *eth.Ethereum) *MinedTracker {
	return &MinedTracker{
		eth:   e,
		seen:  make(map[common.Hash]struct{}),
		mined: make(map[common.Hash]struct{}),
	}
}

// MarkSeen records a transaction hash as submitted to the txpool.
// This must be called before Watch() processes blocks containing these transactions.
func (t *MinedTracker) MarkSeen(hash common.Hash) {
	t.mu.Lock()
	t.seen[hash] = struct{}{}
	t.mu.Unlock()
}

// MinedCount returns the current count of mined transactions.
func (t *MinedTracker) MinedCount() uint64 {
	return t.minedCount.Load()
}

// SeenCount returns the number of transactions marked as seen.
func (t *MinedTracker) SeenCount() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return uint64(len(t.seen))
}

// Watch starts watching for chain head events and tracking mined transactions.
// It blocks until the stop channel is closed or an error occurs.
func (t *MinedTracker) Watch(stop <-chan struct{}) {
	headCh := make(chan core.ChainHeadEvent, 128)
	sub := t.eth.BlockChain().SubscribeChainHeadEvent(headCh)
	defer sub.Unsubscribe()

	for {
		select {
		case <-stop:
			return
		case err := <-sub.Err():
			if err != nil {
				log.Error("Chain head subscription error", "err", err)
				return
			}
		case ev := <-headCh:
			block := t.eth.BlockChain().GetBlockByHash(ev.Header.Hash())
			if block == nil {
				log.Warn("Could not fetch block for chain head event",
					"hash", ev.Header.Hash(),
					"number", ev.Header.Number)
				continue
			}
			t.processBlock(block)
		}
	}
}

// processBlock scans a block for transactions we're tracking.
func (t *MinedTracker) processBlock(block *types.Block) {
	t.mu.Lock()
	defer t.mu.Unlock()

	newMined := 0
	for _, tx := range block.Transactions() {
		hash := tx.Hash()

		// Skip if not in our seen set
		if _, ok := t.seen[hash]; !ok {
			continue
		}

		// Skip if already counted as mined
		if _, ok := t.mined[hash]; ok {
			continue
		}

		t.mined[hash] = struct{}{}
		t.minedCount.Add(1)
		newMined++
	}

	if newMined > 0 {
		log.Debug("Processed block for mined txs",
			"block", block.NumberU64(),
			"hash", block.Hash().Hex()[:10],
			"blockTxs", len(block.Transactions()),
			"newMined", newMined,
			"totalMined", t.minedCount.Load())
	}
}

// Stats returns current tracking statistics.
func (t *MinedTracker) Stats() (seen, mined uint64) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return uint64(len(t.seen)), uint64(len(t.mined))
}
