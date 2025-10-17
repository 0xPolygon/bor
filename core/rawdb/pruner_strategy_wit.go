package rawdb

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
)

const (
	WitnessRetentionBlocks = 6400              // Minimum necessary distance between local header and latest non pruned witness
	WitnessPruneInterval   = 120 * time.Second // The time interval between each witness prune routine
)

type WitnessStrategy struct {
	retention uint64
	interval  time.Duration
}

func NewWitnessStrategy() *WitnessStrategy {
	return &WitnessStrategy{
		retention: WitnessRetentionBlocks,
		interval:  WitnessPruneInterval,
	}
}

func (w *WitnessStrategy) Name() string            { return "witness pruner" }
func (w *WitnessStrategy) RetentionBlocks() uint64 { return w.retention }
func (w *WitnessStrategy) Interval() time.Duration { return w.interval }

func (w *WitnessStrategy) ReadCursor(db ethdb.KeyValueReader) *uint64 {
	return ReadWitnessPruneCursor(db)
}
func (w *WitnessStrategy) WriteCursor(db ethdb.KeyValueWriter, cur uint64) {
	WriteWitnessPruneCursor(db, cur)
}

func (w *WitnessStrategy) FindEarliest(db ethdb.Database, cutoff uint64) (uint64, bool) {
	// same as your findEarliestWitness
	return findEarliestWitness(db, cutoff)
}
func (w *WitnessStrategy) ReadNumberHashes(db ethdb.Iteratee, from, to uint64) []*NumberHash {
	return ReadAllHashesInRange(db, from, to)
}
func (w *WitnessStrategy) DeletePerHash(batch ethdb.KeyValueWriter, number uint64, hash common.Hash) {
	DeleteWitness(batch, hash)
}
func (w *WitnessStrategy) DeletePerHeight(batch ethdb.KeyValueWriter, number uint64) {
	// nothing per height for witnesses
}

// findEarliestWitness returns the smallest block number h in [0, hi] that has a witness.
// If none exists in the range, it returns (hi, false).
func findEarliestWitness(db ethdb.Database, hi uint64) (uint64, bool) {
	var (
		lo    uint64 = 1
		res   uint64
		found bool
	)
	originalHi := hi

	for lo <= hi {
		mid := lo + (hi-lo)/2

		hash := ReadCanonicalHash(db, mid)
		if (hash == common.Hash{}) || !HasWitness(db, hash) {
			// No witness at mid, earliest (if any) must be to the right.
			lo = mid + 1
			continue
		}

		// Witness exists at mid: record and move left to find earliest.
		res = mid
		found = true
		if mid == 0 {
			break
		}
		hi = mid - 1
	}
	if !found {
		return originalHi, found
	}
	return res, found
}
