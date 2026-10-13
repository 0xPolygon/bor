// statescan reads the state history freezer of a bor node (state.scheme=path)
// and dumps every account and storage-slot modification recorded there, in a
// form suited for downstream analysis with pandas/R/duckdb.
//
// The state history is a per-block reverse diff: every value stored is the
// PRE-state value of a key the block changed. present=false on an account row
// (or an empty slot value) therefore means "did not exist before this block",
// i.e. created by it.
//
// The tool reads the freezer files directly (no flock, no key-value store),
// so it can run next to a live node. Only records frozen at start-up are
// visible; the most recent diff layers still live in memory/the journal.
//
// Usage:
//
//	statescan --ancient /var/lib/bor/data/bor/chaindata/ancient/state \
//	          --out /tmp/state --format parquet --blocks 43200 --summary
package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
)

const (
	tableMeta         = "history.meta" // uncompressed
	tableAccountIndex = "account.index"
	tableStorageIndex = "storage.index"
	tableAccountData  = "account.data"
	tableStorageData  = "storage.data"
)

type options struct {
	ancient string
	out     string
	blocks  uint64
	format  string
	summary bool
	quiet   bool
	workers int
	chunk   uint64
}

func main() {
	var o options
	flag.StringVar(&o.ancient, "ancient", "", "path to chaindata/ancient/state (required)")
	flag.StringVar(&o.out, "out", "state", "output file prefix; writes <prefix>.records|accounts|slots.<ext>")
	flag.Uint64Var(&o.blocks, "blocks", 0, "number of most recent blocks to include (0 = everything frozen)")
	flag.StringVar(&o.format, "format", "tsv", "output format: tsv, json or parquet")
	flag.BoolVar(&o.summary, "summary", false, "print aggregate statistics to stderr")
	flag.BoolVar(&o.quiet, "quiet", false, "suppress progress output")
	flag.IntVar(&o.workers, "workers", runtime.NumCPU(), "parallel readers")
	flag.Uint64Var(&o.chunk, "chunk", 256, "records per read")
	flag.Parse()
	if o.ancient == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(o, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// aggregate is touched only by the single emitter goroutine, so no locking.
type aggregate struct {
	records    uint64
	minBlock   uint64
	maxBlock   uint64
	accRows    uint64
	slotRows   uint64
	created    uint64 // accounts with present=false (created by a block)
	decodeErrs uint64

	// Per-address counters, only populated when --summary is set. Bounded by
	// the number of distinct touched addresses, not slots.
	addrTouches   map[common.Address]uint64
	addrSlotWrite map[common.Address]uint64
}

func newAggregate(summary bool) *aggregate {
	a := &aggregate{minBlock: ^uint64(0)}
	if summary {
		a.addrTouches = make(map[common.Address]uint64)
		a.addrSlotWrite = make(map[common.Address]uint64)
	}
	return a
}

type topEntry struct {
	addr           common.Address
	slotWrites     uint64
	accountTouches uint64
}

// topAddrs returns the top n (address, slotWrites, accountTouches) entries.
func (a *aggregate) topAddrs(n int) []topEntry {
	if len(a.addrSlotWrite) == 0 {
		return nil
	}
	type kv struct {
		addr common.Address
		n    uint64
	}
	slots := make([]kv, 0, len(a.addrSlotWrite))
	for addr, cnt := range a.addrSlotWrite {
		slots = append(slots, kv{addr, cnt})
	}
	sort.Slice(slots, func(i, j int) bool {
		if slots[i].n != slots[j].n {
			return slots[i].n > slots[j].n
		}
		return bytesLess(slots[i].addr[:], slots[j].addr[:])
	})
	out := make([]topEntry, 0, min(n, len(slots)))
	for _, e := range slots[:min(n, len(slots))] {
		out = append(out, topEntry{addr: e.addr, slotWrites: e.n, accountTouches: a.addrTouches[e.addr]})
	}
	return out
}

func bytesLess(a, b []byte) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func run(o options, logw *os.File) error {
	// Open the five state history tables. Only the meta table is uncompressed.
	tables := []struct {
		name       string
		compressed bool
	}{
		{tableMeta, false},
		{tableAccountIndex, true},
		{tableStorageIndex, true},
		{tableAccountData, true},
		{tableStorageData, true},
	}
	tabs := make(map[string]*table, len(tables))
	defer func() {
		for _, t := range tabs {
			t.Close()
		}
	}()
	for _, spec := range tables {
		t, err := openTable(o.ancient, spec.name, spec.compressed)
		if err != nil {
			return fmt.Errorf("opening state history freezer: %w", err)
		}
		tabs[spec.name] = t
	}
	meta := tabs[tableMeta]

	// Determine the readable range [firstID, lastID] of state history IDs.
	// Freezer position p holds ID p+1 (IDs start at 1).
	if meta.hidden >= meta.items {
		return fmt.Errorf("no state history records available (retention disabled or fully pruned?)")
	}
	firstID := meta.hidden + 1
	count := meta.items - meta.hidden

	// Read all metadata records (73 bytes each) to resolve the block range.
	metas, err := meta.Retrieve(meta.hidden, count)
	if err != nil {
		return err
	}
	var maxBlock uint64
	for _, m := range metas {
		hm, err := decodeHistoryMeta(m)
		if err != nil {
			return fmt.Errorf("meta %d: %w", firstID, err)
		}
		if hm.block > maxBlock {
			maxBlock = hm.block
		}
	}

	// Narrow to the most recent N blocks if requested.
	if o.blocks > 0 && o.blocks < maxBlock {
		threshold := maxBlock - o.blocks + 1
		for i, m := range metas {
			hm, err := decodeHistoryMeta(m)
			if err != nil {
				return fmt.Errorf("meta %d: %w", firstID+uint64(i), err)
			}
			if hm.block >= threshold {
				firstID += uint64(i)
				count -= uint64(i)
				metas = metas[i:]
				break
			}
		}
	}
	if !o.quiet {
		fmt.Fprintf(logw, "scanning state history ids [%d,%d] (%d records) workers=%d format=%s\n",
			firstID, firstID+count-1, count, o.workers, o.format)
	}

	writers, err := openWriters(o.out, o.format)
	if err != nil {
		return err
	}
	agg := newAggregate(o.summary)

	// Producer workers decode chunks in parallel; the emitter writes serially
	// and consumes per-chunk futures in order so rows land in ID order.
	nchunks := (count + o.chunk - 1) / o.chunk
	futures := make([]chan chunk, nchunks)
	for i := range futures {
		futures[i] = make(chan chunk, 1)
	}
	prodJobs := make(chan job, o.workers*2)
	var (
		prodWg   sync.WaitGroup
		firstErr atomic.Pointer[error]
		done     = make(chan struct{})
	)
	fail := func(err error) { firstErr.CompareAndSwap(nil, &err) }

	// Emitter: the only goroutine touching writers and agg. Consumes the
	// futures in index order so rows land in state-ID order regardless of
	// which decode worker finishes first.
	go func() {
		defer close(done)
		for i := range futures {
			j := <-futures[i]
			for _, rec := range j.recs {
				if rec.meta.block < agg.minBlock {
					agg.minBlock = rec.meta.block
				}
				if rec.meta.block > agg.maxBlock {
					agg.maxBlock = rec.meta.block
				}
				agg.records++
				var nSlots int
				for i := range rec.accounts {
					nSlots += len(rec.accounts[i].slots)
				}
				agg.accRows += uint64(len(rec.accounts))
				agg.slotRows += uint64(nSlots)
				writers.writeRecord(recordRow{
					StateID:    rec.id,
					Block:      rec.meta.block,
					ParentRoot: hashHex(rec.meta.parent),
					Root:       hashHex(rec.meta.root),
					NumAcc:     len(rec.accounts),
					NumSlots:   nSlots,
					DataBytes:  rec.dataBytes,
				})
				for i := range rec.accounts {
					e := &rec.accounts[i]
					ar := toAccountRow(rec, e)
					if ar.DecodeFailed {
						agg.decodeErrs++
					}
					if !ar.Present {
						agg.created++
					}
					if o.summary {
						agg.addrTouches[e.address]++
						agg.addrSlotWrite[e.address] += uint64(len(e.slots))
					}
					writers.writeAccount(ar)
					for _, s := range e.slots {
						writers.writeSlot(slotRow{
							StateID:  rec.id,
							Block:    rec.meta.block,
							Address:  addrHex(e.address),
							Slot:     hashHex(s.key),
							Value:    bytesHex(s.value),
							ValueLen: len(s.value),
						})
					}
				}
			}
		}
	}()

	// Producer workers fetch and decode chunks, one future per chunk.
	for w := 0; w < o.workers; w++ {
		prodWg.Add(1)
		go func() {
			defer prodWg.Done()
			for j := range prodJobs {
				// produce always sends exactly one chunk (possibly empty on
				// error) so the emitter can never block on a missing future.
				futures[j.idx] <- produce(tabs, metas, o, firstID, count, j.startID, fail)
			}
		}()
	}
	for id := firstID; id < firstID+count; id += o.chunk {
		prodJobs <- job{idx: int((id - firstID) / o.chunk), startID: id}
	}
	close(prodJobs)
	prodWg.Wait()
	<-done
	if e := firstErr.Load(); e != nil {
		return *e
	}
	if err := writers.close(); err != nil {
		return err
	}

	if !o.quiet {
		printSummary(logw, agg, count)
	}
	return nil
}

// produce fetches and decodes one chunk of records [startID, startID+n).
// On error it records the failure via fail and returns a possibly empty
// chunk; the emitter never blocks.
func produce(tabs map[string]*table, metas [][]byte, o options, firstID, count, startID uint64, fail func(error)) chunk {
	n := min(o.chunk, firstID+count-startID)
	id0 := startID - firstID // index into metas
	pos := startID - 1       // freezer position of ID startID
	aIdx, err := tabs[tableAccountIndex].Retrieve(pos, n)
	if err != nil {
		fail(err)
		return chunk{}
	}
	sIdx, err := tabs[tableStorageIndex].Retrieve(pos, n)
	if err != nil {
		fail(err)
		return chunk{}
	}
	aData, err := tabs[tableAccountData].Retrieve(pos, n)
	if err != nil {
		fail(err)
		return chunk{}
	}
	sData, err := tabs[tableStorageData].Retrieve(pos, n)
	if err != nil {
		fail(err)
		return chunk{}
	}
	recs := make([]*record, 0, n)
	for i := uint64(0); i < n; i++ {
		rec, err := decodeRecord(startID+i, metas[id0+i], aIdx[i], sIdx[i], aData[i], sData[i])
		if err != nil {
			fail(err)
			break
		}
		recs = append(recs, rec)
	}
	return chunk{recs: recs}
}

func printSummary(logw *os.File, agg *aggregate, scanned uint64) {
	fmt.Fprintf(logw, "\n--- summary ---\n")
	fmt.Fprintf(logw, "records scanned : %d (blocks %d..%d)\n", scanned, agg.minBlock, agg.maxBlock)
	fmt.Fprintf(logw, "account rows    : %d (%d distinct addresses)\n", agg.accRows, len(agg.addrTouches))
	fmt.Fprintf(logw, "slot rows       : %d\n", agg.slotRows)
	fmt.Fprintf(logw, "accounts created: %d\n", agg.created)
	if agg.decodeErrs > 0 {
		fmt.Fprintf(logw, "decode failures : %d\n", agg.decodeErrs)
	}
	if top := agg.topAddrs(10); len(top) > 0 {
		fmt.Fprintf(logw, "\ntop addresses by slot writes:\n")
		fmt.Fprintf(logw, "  %-44s %12s %15s\n", "address", "slot-writes", "account-writes")
		for _, e := range top {
			fmt.Fprintf(logw, "  %-44s %12d %15d\n", e.addr.Hex(), e.slotWrites, e.accountTouches)
		}
	}
}
