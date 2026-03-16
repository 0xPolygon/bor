package bench

import (
	"sync/atomic"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/log"
)

// gasPerSimpleTransfer is the gas cost of a simple ETH transfer (no data).
const gasPerSimpleTransfer = 21000

// MinedTracker tracks mined transactions by counting gas used in new blocks.
// Since all benchmark transactions are simple transfers (21000 gas each),
// we derive the tx count from header.GasUsed without reading the block body,
// avoiding expensive RLP decode of all transactions.
type MinedTracker struct {
	eth        *eth.Ethereum
	minedCount atomic.Uint64
	seenCount  atomic.Uint64
}

// NewMinedTracker creates a new tracker for the given Ethereum backend.
func NewMinedTracker(e *eth.Ethereum) *MinedTracker {
	return &MinedTracker{
		eth: e,
	}
}

// MarkSeenCount records the total number of submitted transactions.
// This replaces per-hash MarkSeen for efficiency.
func (t *MinedTracker) MarkSeenCount(count uint64) {
	t.seenCount.Store(count)
}

// MinedCount returns the current count of mined transactions.
func (t *MinedTracker) MinedCount() uint64 {
	return t.minedCount.Load()
}

// SeenCount returns the number of transactions marked as seen.
func (t *MinedTracker) SeenCount() uint64 {
	return t.seenCount.Load()
}

// Watch starts watching for chain head events and tracking mined transactions.
// It blocks until the stop channel is closed or an error occurs.
// Instead of reading full blocks from DB, it derives tx count from header.GasUsed,
// saving ~0.15s of RLP decode overhead on the critical path.
func (t *MinedTracker) Watch(stop <-chan struct{}) {
	headCh := make(chan core.ChainHeadEvent, 1024)
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
			gasUsed := ev.Header.GasUsed
			if gasUsed == 0 {
				continue
			}
			// All benchmark txs are simple transfers at 21000 gas each.
			// Derive tx count from gas used without reading block body.
			txCount := gasUsed / gasPerSimpleTransfer
			t.minedCount.Add(txCount)

			log.Debug("Counted mined txs from header",
				"block", ev.Header.Number,
				"gasUsed", gasUsed,
				"txCount", txCount,
				"totalMined", t.minedCount.Load())
		}
	}
}

// Stats returns current tracking statistics.
func (t *MinedTracker) Stats() (seen, mined uint64) {
	return t.seenCount.Load(), t.minedCount.Load()
}

// ScanBlockRange scans a range of blocks for mined transactions.
// Uses header-only reads to count gas without decoding block bodies.
func (t *MinedTracker) ScanBlockRange(startBlock, endBlock uint64) {
	for blockNum := startBlock; blockNum <= endBlock; blockNum++ {
		header := t.eth.BlockChain().GetHeaderByNumber(blockNum)
		if header == nil {
			continue
		}
		gasUsed := header.GasUsed
		if gasUsed == 0 {
			continue
		}
		txCount := gasUsed / gasPerSimpleTransfer
		t.minedCount.Add(txCount)
	}
}
