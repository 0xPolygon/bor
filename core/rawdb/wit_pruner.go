package rawdb

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

const (
	WitnessRetentionBlocks = 64000             // Minimum necessary distance between local header and latest non pruned witness
	WitnessPruneInterval   = 120 * time.Second // The time interval between each witness prune routine
)

type witPruner struct {
	database        ethdb.Database
	quitPrune       chan struct{}
	stopped         chan struct{}
	retentionBlocks uint64
	pruneInterval   time.Duration
}

func NewWitPruner(
	db ethdb.Database,
) *witPruner {
	return &witPruner{
		database:        db,
		quitPrune:       make(chan struct{}),
		stopped:         make(chan struct{}),
		retentionBlocks: WitnessRetentionBlocks,
		pruneInterval:   WitnessPruneInterval,
	}
}

// pruneWitnessLoop starts a background goroutine that prunes old witnesses every WitnessPruneInterval
// Close quitPrune to stop it.
func (wp *witPruner) Start() {
	go func() {
		ticker := time.NewTicker(wp.pruneInterval)
		defer func() {
			ticker.Stop()
			close(wp.stopped)
		}()
		wp.pruneWitness()
		for {
			select {
			case <-ticker.C:
				wp.pruneWitness()
			case <-wp.quitPrune:
				log.Info("witness pruner: stopping")
				return
			}
		}
	}()
}

// Stop terminates the background loop.
func (wp *witPruner) Close() error {
	select {
	case <-wp.stopped:
		return nil // already stopped
	default:
	}
	close(wp.quitPrune)
	<-wp.stopped
	return nil
}

func (wp *witPruner) pruneWitness() {
	cursor := ReadWitnessPruneCursor(wp.database)
	head := ReadHeadHeader(wp.database)
	if head == nil {
		log.Debug("witness pruner: no head header yet; skipping")
		return
	}
	latest := head.Number.Uint64()
	var cutoff uint64
	if latest > wp.retentionBlocks {
		cutoff = latest - wp.retentionBlocks
	}

	if cursor == nil {
		if earliest, ok := findEarliestWitness(wp.database, cutoff); ok {
			cursor = &earliest
		} else {
			tmp := cutoff
			cursor = &tmp
			log.Debug("witness pruner: no witnesses ≤ cutoff; starting at cutoff", "cutoff", cutoff)
		}
	}

	batch := wp.database.NewBatch()
	if *cursor < cutoff {
		allHashes := ReadAllHashesInRange(wp.database, *cursor, cutoff-1)

		for _, hash := range allHashes {
			DeleteWitness(batch, hash.Hash)
		}
		*cursor = cutoff
	}

	WriteWitnessPruneCursor(batch, *cursor)

	if err := batch.Write(); err != nil {
		log.Error("error while pruning old witnesses", "writeErr", err)
	}
}

// findEarliestWitness returns the smallest block number h in [0, hi] that has a witness.
// If none exists in the range, it returns (hi, false).
func findEarliestWitness(db ethdb.Database, hi uint64) (uint64, bool) {
	var (
		lo    uint64 = 0
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
