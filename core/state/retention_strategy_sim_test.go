// retention_strategy_sim_test.go
//
// Standalone simulation comparing bounded-cache strategies for the reader.go
// retention cache (per-block account/storage read caches retained across
// commits, with write-invalidation). Test-only: touches NO production code.
//
// Workload is synthetic but calibrated to mainnet measurements (~1h40m,
// ~4200 generations): a bimodal hit-age distribution with a young churn mode
// (age <= 4 blocks) and a permanently-hot, write-cold old mode (entries
// inserted near process start and never written since).
//
// Run:
//
//	go test -run TestRetentionStrategySim -v ./core/state/ -count=1 -timeout 20m
package state

import (
	"container/list"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Workload parameters
// ---------------------------------------------------------------------------

const (
	simBlocks       = 20000 // generations
	simReadsPerBlk  = 2000  // R
	simWritesPerBlk = 800   // W

	// Read mix.
	simHotReadFrac   = 0.55 // fixed hot set, Zipfian popularity
	simChurnReadFrac = 0.35 // keys introduced in the last ~4 blocks
	// remainder (~0.10): cold, never-seen-before keys

	// Hot set.
	simHotN  = 60000
	simZipfS = 1.01 // rand.Zipf needs s > 1; approximates s ~= 1.0
	// Top 10% most-popular hot ranks are never written (the write-cold core
	// that produces the measured "old mode near window length"). The
	// remaining 90% receive the rare hot-set invalidations.
	simHotWriteFloor = simHotN / 10

	// Churn set.
	simChurnPerBlk = 250 // new keys introduced per block
	simChurnWindow = 4   // churn reads/writes target the last ~4 blocks

	// Write mix.
	simChurnWriteFrac = 0.90 // writes mostly hit the churn set; hot set is write-cold

	// Key-space layout: category is derivable from the key value.
	simChurnBase = uint64(1) << 32
	simColdBase  = uint64(1) << 40

	simAgeSampleMask = 15 // sample 1-in-16 hit ages
)

const (
	simCatHot = iota
	simCatChurn
	simCatCold
	simNumCats
)

func simCategory(key uint64) int {
	switch {
	case key < simChurnBase:
		return simCatHot
	case key < simColdBase:
		return simCatChurn
	default:
		return simCatCold
	}
}

// ---------------------------------------------------------------------------
// Deterministic trace generation
// ---------------------------------------------------------------------------

type simTrace struct {
	readKeys  []uint64 // simBlocks * simReadsPerBlk, block-major
	writeKeys []uint64 // simBlocks * simWritesPerBlk, block-major
}

// pickChurnAge returns an age in [0, simChurnWindow) with strong recency
// bias, clamped so we never reference a block before the simulation start.
func pickChurnAge(rng *rand.Rand, blk int) int {
	r := rng.Float64()
	age := 3
	switch {
	case r < 0.55:
		age = 0
	case r < 0.80:
		age = 1
	case r < 0.92:
		age = 2
	}
	if age > blk {
		age = blk
	}
	return age
}

func genSimTrace() *simTrace {
	rng := rand.New(rand.NewSource(42))
	zipf := rand.NewZipf(rng, simZipfS, 1, simHotN-1)

	tr := &simTrace{
		readKeys:  make([]uint64, 0, simBlocks*simReadsPerBlk),
		writeKeys: make([]uint64, 0, simBlocks*simWritesPerBlk),
	}
	coldNext := simColdBase

	churnKey := func(blk int) uint64 {
		age := pickChurnAge(rng, blk)
		src := blk - age
		return simChurnBase + uint64(src)*simChurnPerBlk + uint64(rng.Intn(simChurnPerBlk))
	}

	for b := 0; b < simBlocks; b++ {
		for i := 0; i < simReadsPerBlk; i++ {
			p := rng.Float64()
			var k uint64
			switch {
			case p < simHotReadFrac:
				k = zipf.Uint64() // rank == key id, 0 is most popular
			case p < simHotReadFrac+simChurnReadFrac:
				k = churnKey(b)
			default:
				k = coldNext
				coldNext++
			}
			tr.readKeys = append(tr.readKeys, k)
		}
		for i := 0; i < simWritesPerBlk; i++ {
			var k uint64
			if rng.Float64() < simChurnWriteFrac {
				k = churnKey(b)
			} else {
				// Rare hot-set writes; never touch the top-popularity core.
				k = uint64(simHotWriteFloor + rng.Intn(simHotN-simHotWriteFloor))
			}
			tr.writeKeys = append(tr.writeKeys, k)
		}
	}
	return tr
}

// ---------------------------------------------------------------------------
// Cache strategy interface
// ---------------------------------------------------------------------------

// simCache stores key -> insertion generation. lookup may promote / set ref
// bits. insert is only called after a miss (key guaranteed absent).
type simCache interface {
	lookup(key uint64) (gen uint32, ok bool)
	insert(key uint64, gen uint32)
	invalidate(key uint64)
	size() int
}

// --- 1. unbounded -----------------------------------------------------------

type simUnbounded struct{ m map[uint64]uint32 }

func newSimUnbounded() *simUnbounded {
	return &simUnbounded{m: make(map[uint64]uint32, 1<<20)}
}
func (c *simUnbounded) lookup(k uint64) (uint32, bool) { g, ok := c.m[k]; return g, ok }
func (c *simUnbounded) insert(k uint64, g uint32)      { c.m[k] = g }
func (c *simUnbounded) invalidate(k uint64)            { delete(c.m, k) }
func (c *simUnbounded) size() int                      { return len(c.m) }

// --- 2. cap-random ----------------------------------------------------------

type simCapRandom struct {
	cap int
	m   map[uint64]uint32
}

func newSimCapRandom(cap int) *simCapRandom {
	return &simCapRandom{cap: cap, m: make(map[uint64]uint32, cap)}
}
func (c *simCapRandom) lookup(k uint64) (uint32, bool) { g, ok := c.m[k]; return g, ok }
func (c *simCapRandom) insert(k uint64, g uint32) {
	if len(c.m) >= c.cap {
		for victim := range c.m { // Go map iteration order ~ random
			delete(c.m, victim)
			break
		}
	}
	c.m[k] = g
}
func (c *simCapRandom) invalidate(k uint64) { delete(c.m, k) }
func (c *simCapRandom) size() int           { return len(c.m) }

// --- 3/4. rotate-K (optional promotion) --------------------------------------

type simRotate struct {
	capPer  int // per-map insert budget = E/K
	maps    []map[uint64]uint32 // maps[0] is newest
	inserts int // inserts into maps[0] since last rotation
	promote bool
}

func newSimRotate(totalCap, k int, promote bool) *simRotate {
	c := &simRotate{capPer: totalCap / k, promote: promote}
	c.maps = make([]map[uint64]uint32, k)
	for i := range c.maps {
		c.maps[i] = make(map[uint64]uint32, c.capPer)
	}
	return c
}

func (c *simRotate) lookup(k uint64) (uint32, bool) {
	for i, m := range c.maps {
		if g, ok := m[k]; ok {
			if c.promote && i > 0 {
				delete(m, k)
				c.insert(k, g) // keep original insertion gen
			}
			return g, true
		}
	}
	return 0, false
}

func (c *simRotate) insert(k uint64, g uint32) {
	if c.inserts >= c.capPer {
		// Rotate: drop the oldest map, start a fresh newest one.
		for i := len(c.maps) - 1; i > 0; i-- {
			c.maps[i] = c.maps[i-1]
		}
		c.maps[0] = make(map[uint64]uint32, c.capPer)
		c.inserts = 0
	}
	c.maps[0][k] = g
	c.inserts++
}

func (c *simRotate) invalidate(k uint64) {
	for _, m := range c.maps {
		if _, ok := m[k]; ok {
			delete(m, k)
			return
		}
	}
}

func (c *simRotate) size() int {
	n := 0
	for _, m := range c.maps {
		n += len(m)
	}
	return n
}

// --- CLOCK ring (used by 5 and 7) --------------------------------------------

type simClockRing struct {
	cap  int
	m    map[uint64]int32 // key -> slot
	keys []uint64
	gens []uint32
	refs []bool
	free []int32
	hand int32
}

func newSimClockRing(cap int) *simClockRing {
	c := &simClockRing{
		cap:  cap,
		m:    make(map[uint64]int32, cap),
		keys: make([]uint64, cap),
		gens: make([]uint32, cap),
		refs: make([]bool, cap),
		free: make([]int32, 0, cap),
	}
	for i := cap - 1; i >= 0; i-- {
		c.free = append(c.free, int32(i))
	}
	return c
}

func (c *simClockRing) lookup(k uint64) (uint32, bool) {
	if i, ok := c.m[k]; ok {
		c.refs[i] = true // hit: just set the ref bit, no list manipulation
		return c.gens[i], true
	}
	return 0, false
}

func (c *simClockRing) insert(k uint64, g uint32) {
	var slot int32
	if n := len(c.free); n > 0 {
		slot = c.free[n-1]
		c.free = c.free[:n-1]
	} else {
		for { // second-chance sweep
			i := c.hand
			c.hand++
			if c.hand == int32(c.cap) {
				c.hand = 0
			}
			if c.refs[i] {
				c.refs[i] = false
				continue
			}
			delete(c.m, c.keys[i])
			slot = i
			break
		}
	}
	c.keys[slot] = k
	c.gens[slot] = g
	c.refs[slot] = false
	c.m[k] = slot
}

func (c *simClockRing) invalidate(k uint64) {
	if i, ok := c.m[k]; ok {
		delete(c.m, k)
		c.refs[i] = false
		c.free = append(c.free, i)
	}
}

func (c *simClockRing) size() int { return len(c.m) }

// --- 5. twoq-hotcold ----------------------------------------------------------

type simTwoQ struct {
	coldCap int
	cold    map[uint64]uint32 // probation: inserts land here
	hot     *simClockRing     // protected: promoted on a cold hit
}

func newSimTwoQ(totalCap int) *simTwoQ {
	coldCap := totalCap / 4 // ~25% cold / 75% hot
	return &simTwoQ{
		coldCap: coldCap,
		cold:    make(map[uint64]uint32, coldCap),
		hot:     newSimClockRing(totalCap - coldCap),
	}
}

func (c *simTwoQ) lookup(k uint64) (uint32, bool) {
	if g, ok := c.hot.lookup(k); ok {
		return g, true
	}
	if g, ok := c.cold[k]; ok {
		delete(c.cold, k)
		c.hot.insert(k, g) // promote, keep original gen
		return g, true
	}
	return 0, false
}

func (c *simTwoQ) insert(k uint64, g uint32) {
	if len(c.cold) >= c.coldCap {
		for victim := range c.cold {
			delete(c.cold, victim)
			break
		}
	}
	c.cold[k] = g
}

func (c *simTwoQ) invalidate(k uint64) {
	delete(c.cold, k)
	c.hot.invalidate(k)
}

func (c *simTwoQ) size() int { return len(c.cold) + c.hot.size() }

// --- 6. lru -------------------------------------------------------------------

type simLRUEntry struct {
	key uint64
	gen uint32
}

type simLRU struct {
	cap int
	m   map[uint64]*list.Element
	l   *list.List
}

func newSimLRU(cap int) *simLRU {
	return &simLRU{cap: cap, m: make(map[uint64]*list.Element, cap), l: list.New()}
}

func (c *simLRU) lookup(k uint64) (uint32, bool) {
	if e, ok := c.m[k]; ok {
		c.l.MoveToFront(e)
		return e.Value.(*simLRUEntry).gen, true
	}
	return 0, false
}

func (c *simLRU) insert(k uint64, g uint32) {
	if len(c.m) >= c.cap {
		back := c.l.Back()
		c.l.Remove(back)
		delete(c.m, back.Value.(*simLRUEntry).key)
	}
	c.m[k] = c.l.PushFront(&simLRUEntry{key: k, gen: g})
}

func (c *simLRU) invalidate(k uint64) {
	if e, ok := c.m[k]; ok {
		c.l.Remove(e)
		delete(c.m, k)
	}
}

func (c *simLRU) size() int { return len(c.m) }

// ---------------------------------------------------------------------------
// Simulation runner + metrics
// ---------------------------------------------------------------------------

type simResult struct {
	name     string
	capLabel string

	reads [simNumCats]int64
	hits  [simNumCats]int64

	finalSize int
	nsPerOp   float64

	ageP25, ageP50, ageP75, ageP95 uint32

	weightedMissCost float64 // 10x hot misses + 1x others
}

func (r *simResult) totalReads() int64 { return r.reads[0] + r.reads[1] + r.reads[2] }
func (r *simResult) totalHits() int64  { return r.hits[0] + r.hits[1] + r.hits[2] }

func simPctile(sorted []uint32, p float64) uint32 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}

func runSimStrategy(tr *simTrace, name, capLabel string, c simCache) simResult {
	res := simResult{name: name, capLabel: capLabel}
	ages := make([]uint32, 0, simBlocks*simReadsPerBlk/(simAgeSampleMask+1))

	ri, wi := 0, 0
	var hitCount int64

	start := time.Now()
	for b := 0; b < simBlocks; b++ {
		gen := uint32(b)
		for i := 0; i < simReadsPerBlk; i++ {
			k := tr.readKeys[ri]
			ri++
			cat := simCategory(k)
			res.reads[cat]++
			if g, ok := c.lookup(k); ok {
				res.hits[cat]++
				hitCount++
				if hitCount&simAgeSampleMask == 0 {
					ages = append(ages, gen-g)
				}
			} else {
				c.insert(k, gen)
			}
		}
		// Commit: write-invalidations for this block.
		for i := 0; i < simWritesPerBlk; i++ {
			c.invalidate(tr.writeKeys[wi])
			wi++
		}
	}
	elapsed := time.Since(start)

	totalOps := int64(simBlocks) * int64(simReadsPerBlk+simWritesPerBlk)
	res.nsPerOp = float64(elapsed.Nanoseconds()) / float64(totalOps)
	res.finalSize = c.size()

	sort.Slice(ages, func(i, j int) bool { return ages[i] < ages[j] })
	res.ageP25 = simPctile(ages, 0.25)
	res.ageP50 = simPctile(ages, 0.50)
	res.ageP75 = simPctile(ages, 0.75)
	res.ageP95 = simPctile(ages, 0.95)

	hotMiss := res.reads[simCatHot] - res.hits[simCatHot]
	churnMiss := res.reads[simCatChurn] - res.hits[simCatChurn]
	coldMiss := res.reads[simCatCold] - res.hits[simCatCold]
	res.weightedMissCost = 10*float64(hotMiss) + float64(churnMiss) + float64(coldMiss)

	return res
}

// ---------------------------------------------------------------------------
// Test
// ---------------------------------------------------------------------------

func TestRetentionStrategySim(t *testing.T) {
	if testing.Short() {
		t.Skip("retention strategy simulation is heavy; skipped in -short mode")
	}

	fmt.Printf("generating trace: %d blocks x %d reads + %d writes ...\n",
		simBlocks, simReadsPerBlk, simWritesPerBlk)
	tr := genSimTrace()

	type strategyDef struct {
		name     string
		capLabel string
		build    func() simCache
	}

	caps := []struct {
		label string
		n     int
	}{
		{"256k", 256 << 10},
		{"512k", 512 << 10},
		{"1M", 1 << 20},
	}

	defs := []strategyDef{
		{"unbounded", "-", func() simCache { return newSimUnbounded() }},
	}
	for _, cp := range caps {
		n := cp.n
		defs = append(defs,
			strategyDef{"cap-random", cp.label, func() simCache { return newSimCapRandom(n) }},
			strategyDef{"rotate-4", cp.label, func() simCache { return newSimRotate(n, 4, false) }},
			strategyDef{"rotate-4-promote", cp.label, func() simCache { return newSimRotate(n, 4, true) }},
			strategyDef{"twoq-hotcold", cp.label, func() simCache { return newSimTwoQ(n) }},
			strategyDef{"lru", cp.label, func() simCache { return newSimLRU(n) }},
			strategyDef{"clock", cp.label, func() simCache { return newSimClockRing(n) }},
		)
	}

	results := make([]simResult, 0, len(defs))
	for _, d := range defs {
		fmt.Printf("running %-16s cap=%-5s ...\n", d.name, d.capLabel)
		results = append(results, runSimStrategy(tr, d.name, d.capLabel, d.build()))
	}

	unbounded := results[0]

	// Sanity check: unbounded hit-age distribution should be bimodal
	// (young mode <= ~4 from the churn set, old mode reaching toward the
	// window length from the write-cold hot core), qualitatively matching
	// the mainnet measurement (accounts p50=4 p75=4143 p95=4220 over a
	// ~4231-generation window).
	fmt.Printf("\n=== unbounded hit-age sanity check (window = %d generations) ===\n", simBlocks)
	fmt.Printf("hit-age percentiles: p25=%d p50=%d p75=%d p95=%d\n",
		unbounded.ageP25, unbounded.ageP50, unbounded.ageP75, unbounded.ageP95)
	fmt.Printf("bimodality: young mode if p25 <= ~4 (got %d); old mode if p95 near window length %d (got %d)\n",
		unbounded.ageP25, simBlocks, unbounded.ageP95)

	fmt.Printf("\n=== strategy comparison (N=%d blocks, R=%d reads/blk, W=%d writes/blk, hot=%d zipf s~1.0, churn=%d/blk window=%d) ===\n",
		simBlocks, simReadsPerBlk, simWritesPerBlk, simHotN, simChurnPerBlk, simChurnWindow)
	fmt.Printf("%-17s %-5s %8s %8s %8s %10s %8s %9s %10s\n",
		"strategy", "cap", "hit%", "hot-hit%", "chn-hit%", "retained", "ns/op", "hitageP50", "wmiss-rel")
	fmt.Println("--------------------------------------------------------------------------------------------")
	for _, r := range results {
		hitPct := 100 * float64(r.totalHits()) / float64(r.totalReads())
		hotPct := 100 * float64(r.hits[simCatHot]) / float64(r.reads[simCatHot])
		chnPct := 100 * float64(r.hits[simCatChurn]) / float64(r.reads[simCatChurn])
		fmt.Printf("%-17s %-5s %8.2f %8.2f %8.2f %10d %8.1f %9d %10.3f\n",
			r.name, r.capLabel, hitPct, hotPct, chnPct, r.finalSize, r.nsPerOp,
			r.ageP50, r.weightedMissCost/unbounded.weightedMissCost)
	}

	// Basic invariants so the test actually asserts something.
	if unbounded.ageP25 > 100 {
		t.Errorf("unbounded young mode missing: p25=%d, expected <= ~4", unbounded.ageP25)
	}
	if unbounded.ageP95 < uint32(simBlocks/2) {
		t.Errorf("unbounded old mode missing: p95=%d, expected near window length %d", unbounded.ageP95, simBlocks)
	}
	for _, r := range results[1:] {
		if r.finalSize > (1<<20)+1 {
			t.Errorf("%s/%s exceeded its cap: retained %d", r.name, r.capLabel, r.finalSize)
		}
	}
}
