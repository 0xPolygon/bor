package rawdb

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/log"
)

type Strategy interface {
	Name() string
	RetentionBlocks() uint64
	Interval() time.Duration

	// Cursor persistence
	ReadCursor(db ethdb.KeyValueReader) *uint64
	WriteCursor(db ethdb.KeyValueWriter, cur uint64)

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

// This pruner implements an online solution of pruning data. It demands the data to be pruned
// to be on the KV database. That's why, for example, if you prune blocks you need to disable ancient db
func NewPruner(db ethdb.Database, s Strategy) *pruner {
	return &pruner{db: db, strategy: s, quit: make(chan struct{}), stopped: make(chan struct{})}
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

	var cutoff uint64
	if rb := p.strategy.RetentionBlocks(); latest > rb {
		cutoff = latest - rb
	}

	cur := p.strategy.ReadCursor(p.db)
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
		batch := p.db.NewBatch()
		p.strategy.WriteCursor(batch, *cur)
		_ = batch.Write()
		log.Info(p.strategy.Name()+": no data to prune", "cursor", *cur, "cutoff", cutoff)
		return
	}

	// Chunk to keep batches reasonable.
	const step = uint64(50_000)
	from := *cur
	for from < cutoff {
		to := from + step
		if to > cutoff {
			to = cutoff
		}
		nhs := p.strategy.ReadNumberHashes(p.db, from, to-1)

		batch := p.db.NewBatch()
		var lastNum = ^uint64(0) // sentinel
		for _, nh := range nhs {
			p.strategy.DeletePerHash(batch, nh.Number, nh.Hash)
			if nh.Number != lastNum {
				if lastNum != ^uint64(0) { // avoid multiple deletes per same height in case of multiple hashs in same height
					p.strategy.DeletePerHeight(batch, lastNum)
				}
				lastNum = nh.Number
			}
		}
		// flush last height if any rows existed
		if lastNum != ^uint64(0) {
			p.strategy.DeletePerHeight(batch, lastNum)
		}
		p.strategy.WriteCursor(batch, to)
		if err := batch.Write(); err != nil {
			log.Error(p.strategy.Name()+": batch write error", "from", from, "to", to-1, "err", err)
			return
		}
		log.Info(p.strategy.Name()+": successfully pruned", "count", (to-1)-from, "from", from, "to", to-1)
		from = to
	}
}
