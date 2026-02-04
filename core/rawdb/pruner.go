package rawdb

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

const MaxDeleteRangeSize = uint64(50_000)

type Strategy interface {
	Name() string
	RetentionBlocks() uint64
	Interval() time.Duration

	// Cursor persistence
	ReadCursor(db ethdb.KeyValueReader) *uint64
	WriteCursor(db ethdb.KeyValueWriter, cur uint64)

	// Head Persistency
	ReadPrunerHead(db ethdb.KeyValueReader) *uint64
	WritePrunerHead(db ethdb.KeyValueWriter, head uint64)

	// Find earliest height <= cutoff that has data for this domain.
	FindEarliest(db ethdb.Database, cutoff uint64) (earliest uint64, ok bool)

	// Enumerate all number->hash entries in [from, to] inclusive.
	// You can reuse ReadAllHashesInRange for both strategies.
	ReadNumberHashes(db ethdb.Iteratee, from, to uint64) []*NumberHash

	// Called for each (number, hash) within the range.
	DeletePerHash(batch ethdb.KeyValueWriter, number uint64, hash common.Hash)

	// Called once per height (after DeletePerHash calls for that height).
	DeletePerHeight(batch ethdb.KeyValueWriter, number uint64)
}

type pruner struct {
	db            ethdb.Database
	quit, stopped chan struct{}
	strategy      Strategy
}

func NewPruner(db ethdb.Database, s Strategy) *pruner {
	return &pruner{
		db:       db,
		strategy: s,
		quit:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

func (p *pruner) Start() {
	go func() {
		tick := time.NewTicker(p.strategy.Interval())
		defer func() { tick.Stop(); close(p.stopped) }()
		p.prune()
		for {
			select {
			case <-tick.C:
				p.prune()
			case <-p.quit:
				log.Info(p.strategy.Name() + ": stopping")
				return
			}
		}
	}()
	log.Info(p.strategy.Name()+": started", "retentionBlocks", p.strategy.RetentionBlocks(), "interval", p.strategy.Interval().String())
}

func (p *pruner) Close() error {
	close(p.quit)
	<-p.stopped
	return nil
}

func (p *pruner) prune() {
	head := ReadHeadHeader(p.db)
	if head == nil {
		log.Info(p.strategy.Name() + ": no head; skip")
		return
	}
	latest := head.Number.Uint64()

	prevHeadPtr := p.strategy.ReadPrunerHead(p.db)
	prevHead := latest
	if prevHeadPtr != nil {
		prevHead = *prevHeadPtr
	}

	if latest < prevHead {
		// Reorg between prevHead and latest (could have happened while offline).
		if err := p.handleReorg(latest, prevHead); err != nil {
			log.Error(p.strategy.Name()+": reorg cleanup failed", "newHead", latest, "oldHead", prevHead, "err", err)
			// Do not update stored head; we want to try again next run.
			return
		}
	}

	var cutoff uint64
	if rb := p.strategy.RetentionBlocks(); latest > rb {
		cutoff = latest - rb
	}

	cur := p.strategy.ReadCursor(p.db)

	if cur != nil && *cur > latest {
		log.Warn(p.strategy.Name()+": cursor beyond head; clamping", "cursor", *cur, "head", latest)
		p.strategy.WriteCursor(p.db, latest)
		tmp := latest
		cur = &tmp
	}

	if cur == nil {
		if e, ok := p.strategy.FindEarliest(p.db, cutoff); ok {
			log.Info(p.strategy.Name()+": no cursor stored", "earliestFound", e)
			cur = &e
		} else {
			tmp := cutoff
			cur = &tmp
			log.Info(p.strategy.Name()+": no data ≤ cutoff; starting at cutoff", "cutoff", cutoff)
		}
	}

	if *cur >= cutoff {
		p.strategy.WritePrunerHead(p.db, latest)
		return
	}

	// Normal pruning: delete [cur .. cutoff-1] inclusive, then move cursor to cutoff.
	from := *cur
	to := cutoff - 1

	if err := p.deleteRange(from, to); err != nil {
		log.Error(p.strategy.Name()+": batch write error during prune", "from", from, "to", to, "err", err)
		return
	}

	p.strategy.WriteCursor(p.db, cutoff)
	log.Info(p.strategy.Name()+": successfully pruned", "count", cutoff-from, "from", from, "to", to)
	p.strategy.WritePrunerHead(p.db, latest)
}

func (p *pruner) handleReorg(newHead, oldHead uint64) error {
	log.Warn(p.strategy.Name()+": reorg detected", "newHead", newHead, "oldHead", oldHead)

	cur := p.strategy.ReadCursor(p.db)
	if cur == nil {
		cur = new(uint64)
		*cur = 0 // meaning no pruned data yet
	}

	// Range of non-canonical heights
	deleteFrom := max(*cur, newHead+1)
	deleteTo := oldHead

	if deleteFrom <= deleteTo {
		log.Warn(p.strategy.Name()+": deleting non-canonical data",
			"from", deleteFrom, "to", deleteTo)

		if err := p.deleteRange(deleteFrom, deleteTo); err != nil {
			log.Error(p.strategy.Name()+": reorg cleanup failed; keeping cursor unchanged",
				"from", deleteFrom, "to", deleteTo, "err", err)
			return err
		}
	}

	if *cur > newHead {
		p.strategy.WriteCursor(p.db, newHead)
		log.Warn(p.strategy.Name()+": cursor rolled back", "oldCursor", *cur, "newCursor", newHead)
	}

	return nil
}

func max(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func (p *pruner) deleteRange(from, to uint64) error {
	if from > to {
		return nil
	}

	const step = MaxDeleteRangeSize
	start := from

	for start <= to {
		end := start + step - 1
		if end > to {
			end = to
		}

		nhs := p.strategy.ReadNumberHashes(p.db, start, end)

		batch := p.db.NewBatch()
		var lastNum = ^uint64(0)

		for _, nh := range nhs {
			p.strategy.DeletePerHash(batch, nh.Number, nh.Hash)

			if nh.Number != lastNum {
				if lastNum != ^uint64(0) {
					p.strategy.DeletePerHeight(batch, lastNum)
				}
				lastNum = nh.Number
			}
		}

		if lastNum != ^uint64(0) {
			p.strategy.DeletePerHeight(batch, lastNum)
		}

		if err := batch.Write(); err != nil {
			log.Error(p.strategy.Name()+": batch write error during reorg cleanup",
				"from", start, "to", end, "err", err)
			return err
		}

		log.Info(p.strategy.Name()+": removed reverted data", "from", start, "to", end)

		start = end + 1
	}
	return nil
}
