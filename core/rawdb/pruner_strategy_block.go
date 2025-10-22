package rawdb

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
)

const (
	BlockRetention     = 250000            // Minimum necessary distance between local header and latest non pruned Block
	BlockPruneInterval = 120 * time.Second // The time interval between each Block prune routine
)

type BlockStrategy struct {
	retention uint64
	interval  time.Duration
}

func NewBlockStrategy() *BlockStrategy {
	return &BlockStrategy{
		retention: BlockRetention,
		interval:  BlockPruneInterval,
	}
}

func (b *BlockStrategy) Name() string            { return "block pruner" }
func (b *BlockStrategy) RetentionBlocks() uint64 { return b.retention }
func (b *BlockStrategy) Interval() time.Duration { return b.interval }

func (b *BlockStrategy) ReadCursor(db ethdb.KeyValueReader) *uint64 { return ReadBlockPruneCursor(db) }
func (b *BlockStrategy) WriteCursor(db ethdb.KeyValueWriter, cur uint64) {
	WriteBlockPruneCursor(db, cur)
}

func (b *BlockStrategy) FindEarliest(db ethdb.Database, cutoff uint64) (uint64, bool) {
	// earliest canonical header with data
	return findEarliestBlockWithData(db, cutoff)
}
func (b *BlockStrategy) ReadNumberHashes(db ethdb.Iteratee, from, to uint64) []*NumberHash {
	return ReadAllHashesInRange(db, from, to)
}
func (b *BlockStrategy) DeletePerHash(batch ethdb.KeyValueWriter, number uint64, hash common.Hash) {
	// do not prune genesis
	if number == 0 {
		return
	}
	DeleteBlockWithoutNumber(batch, hash, number)
}
func (b *BlockStrategy) DeletePerHeight(batch ethdb.KeyValueWriter, number uint64) {
	// do not prune genesis
	if number == 0 {
		return
	}
	DeleteCanonicalHash(batch, number)
}

// findEarliestBlockWithData returns the smallest block number h in [0, hi]
// that has a canonical header stored. If none exists in the range,
// it returns (hi, false).
func findEarliestBlockWithData(db ethdb.Database, hi uint64) (uint64, bool) {
	var (
		lo    uint64 = 1
		res   uint64
		found bool
	)
	originalHi := hi

	for lo <= hi {
		mid := lo + (hi-lo)/2

		hash := ReadCanonicalHash(db, mid)
		// Treat zero hash as "no canonical block at this height".
		// If hash exists, confirm we indeed have header data for it.
		if (hash == common.Hash{}) || !HasHeader(db, hash, mid) {
			lo = mid + 1
			continue
		}
		// Block data exists at mid; move left to find earliest.
		res = mid
		found = true
		if mid == 0 {
			break
		}
		hi = mid - 1
	}
	if !found {
		return originalHi, false
	}
	return res, true
}
