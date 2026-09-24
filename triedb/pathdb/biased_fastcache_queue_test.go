package pathdb

import (
	"bytes"
	stdcontext "context"
	"math/rand"
	"runtime"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/VictoriaMetrics/fastcache"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
)

func TestPreloadQueue_Empty(t *testing.T) {
	var q preloadQueue
	if q.len() != 0 {
		t.Fatalf("expected empty queue, got len %d", q.len())
	}
	if _, ok := q.pop(); ok {
		t.Fatal("pop on empty queue returned ok")
	}

	// Drain after use and check it behaves as empty again.
	q.push(preloadQueueItem{depth: 1})
	if _, ok := q.pop(); !ok {
		t.Fatal("pop on non-empty queue returned !ok")
	}
	if _, ok := q.pop(); ok {
		t.Fatal("pop on drained queue returned ok")
	}
	if q.len() != 0 {
		t.Fatalf("expected drained queue, got len %d", q.len())
	}
}

// TestPreloadQueue_FIFOOrder checks that items come out in push order across
// segment boundaries, including when pushes and pops are interleaved like in
// the preload BFS.
func TestPreloadQueue_FIFOOrder(t *testing.T) {
	var (
		q    preloadQueue
		ref  []int
		next int
	)
	rng := rand.New(rand.NewSource(1))
	p := &boundedPusher{t: t, q: &q}

	// Push a random number of items (0..16, like a branch node) for each pop,
	// until enough items went through to cross several segments.
	push := func() {
		p.push(preloadQueueItem{path: []byte{byte(next)}, depth: next})
		ref = append(ref, next)
		next++
	}
	push()
	for popped := 0; popped < 5*preloadQueueSegmentSize+123; popped++ {
		if q.len() != len(ref) {
			t.Fatalf("len mismatch: have %d, want %d", q.len(), len(ref))
		}
		if len(ref) == 0 {
			push()
		}
		item, ok := q.pop()
		if !ok {
			t.Fatal("unexpected empty queue")
		}
		if item.depth != ref[0] || !bytes.Equal(item.path, []byte{byte(ref[0])}) {
			t.Fatalf("pop %d: have depth %d, want %d", popped, item.depth, ref[0])
		}
		ref = ref[1:]
		// Keep the queue size bounded but far above one segment.
		n := rng.Intn(17)
		if len(ref) > 3*preloadQueueSegmentSize {
			n = rng.Intn(2)
		}
		for i := 0; i < n; i++ {
			push()
		}
	}
	for len(ref) > 0 {
		item, ok := q.pop()
		if !ok || item.depth != ref[0] {
			t.Fatalf("drain: have (%d, %v), want %d", item.depth, ok, ref[0])
		}
		ref = ref[1:]
	}
	if _, ok := q.pop(); ok {
		t.Fatal("queue not empty after draining")
	}
}

// TestPreloadQueue_SegmentRelease checks that a fully consumed segment is
// unlinked from the queue and that popped slots no longer reference paths.
func TestPreloadQueue_SegmentRelease(t *testing.T) {
	var q preloadQueue
	total := 2*preloadQueueSegmentSize + 1
	for i := 0; i < total; i++ {
		q.push(preloadQueueItem{path: []byte{1, 2, 3}, depth: i})
	}
	first := q.head
	second := first.next
	if second == nil || second.next == nil || second.next != q.tail {
		t.Fatal("expected three linked segments")
	}

	// Pop all but the last item of the first segment: still the head.
	for i := 0; i < preloadQueueSegmentSize-1; i++ {
		q.pop()
	}
	if q.head != first {
		t.Fatal("head segment dropped too early")
	}
	for i := 0; i < preloadQueueSegmentSize-1; i++ {
		if first.items[i].path != nil {
			t.Fatalf("popped slot %d still references its path", i)
		}
	}

	// Pop the last item: the first segment must be unlinked.
	if item, _ := q.pop(); item.depth != preloadQueueSegmentSize-1 {
		t.Fatalf("unexpected item depth %d", item.depth)
	}
	if q.head != second {
		t.Fatal("consumed head segment not dropped")
	}
	if first.next != nil {
		t.Fatal("dropped segment still links to the queue")
	}
	if q.len() != total-preloadQueueSegmentSize {
		t.Fatalf("unexpected len %d", q.len())
	}

	// Drain the rest; the last segment is rewound and reused.
	for q.len() > 0 {
		q.pop()
	}
	last := q.tail
	if q.head != last || q.headIdx != 0 || q.tailIdx != 0 {
		t.Fatal("drained queue not rewound to a single empty segment")
	}
	q.push(preloadQueueItem{depth: 42})
	if q.tail != last {
		t.Fatal("drained segment not reused")
	}
	if item, ok := q.pop(); !ok || item.depth != 42 {
		t.Fatalf("unexpected item after reuse: %d, %v", item.depth, ok)
	}
}

// boundedPusher pushes into a queue and fails the test as soon as the queue
// holds more segments than its item count needs, so a broken segment
// rollover fails fast instead of allocating one segment per push.
type boundedPusher struct {
	t      testing.TB
	q      *preloadQueue
	pushes int
	segs   int
	last   *preloadQueueSegment
}

func (b *boundedPusher) push(item preloadQueueItem) {
	b.t.Helper()
	b.q.push(item)
	b.pushes++
	if b.q.tail != b.last {
		b.segs++
		b.last = b.q.tail
	}
	if limit := (b.pushes+preloadQueueSegmentSize-1)/preloadQueueSegmentSize + 1; b.segs > limit {
		b.t.Fatalf("after %d pushes the queue made %d segments, want <= %d", b.pushes, b.segs, limit)
	}
}

// TestPreloadQueue_LargeN pushes many items across many segments and checks
// order.
func TestPreloadQueue_LargeN(t *testing.T) {
	n := 200_000 // ~49 segments
	var q preloadQueue
	p := &boundedPusher{t: t, q: &q}
	for i := 0; i < n; i++ {
		p.push(preloadQueueItem{depth: i})
	}
	if q.len() != n {
		t.Fatalf("len: have %d, want %d", q.len(), n)
	}
	for i := 0; i < n; i++ {
		item, ok := q.pop()
		if !ok || item.depth != i {
			t.Fatalf("pop %d: have (%d, %v)", i, item.depth, ok)
		}
	}
	if _, ok := q.pop(); ok {
		t.Fatal("queue not empty")
	}
}

// TestPreloadQueue_AllocationBounded checks that the queue never makes an
// allocation larger than one segment: filling a whole segment costs exactly
// one allocation of the segment size, independent of queue length.
func TestPreloadQueue_AllocationBounded(t *testing.T) {
	const maxAlloc = 256 << 10
	if size := unsafe.Sizeof(preloadQueueSegment{}); size > maxAlloc {
		t.Fatalf("segment is %d bytes, want <= %d", size, maxAlloc)
	}

	var q preloadQueue
	// Pre-grow the queue so the measured pushes happen on a long queue.
	p := &boundedPusher{t: t, q: &q}
	for i := 0; i < 10*preloadQueueSegmentSize; i++ {
		p.push(preloadQueueItem{depth: i})
	}
	allocs := testing.AllocsPerRun(20, func() {
		for i := 0; i < preloadQueueSegmentSize; i++ {
			q.push(preloadQueueItem{depth: i})
		}
	})
	if allocs != 1 {
		t.Fatalf("filling one segment made %v allocations, want 1", allocs)
	}
}

// recordingDatabase records the keys of all Get calls, in order.
type recordingDatabase struct {
	ethdb.Database
	mu   sync.Mutex
	keys [][]byte
}

func (db *recordingDatabase) Get(key []byte) ([]byte, error) {
	db.mu.Lock()
	db.keys = append(db.keys, common.CopyBytes(key))
	db.mu.Unlock()
	return db.Database.Get(key)
}

// writeSyntheticStorageTrie writes a pseudo-random storage trie with branch,
// extension and leaf nodes of varying size, and returns all node paths.
func writeSyntheticStorageTrie(t *testing.T, db ethdb.Database, owner common.Hash, seed int64) [][]byte {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	hash := bytes.Repeat([]byte{0x22}, 32)

	var paths [][]byte
	var build func(path []byte, depth int)
	build = func(path []byte, depth int) {
		paths = append(paths, path)
		switch {
		case depth >= 5 || (depth > 1 && rng.Intn(8) == 0):
			// Leaf with a value of random size
			value := bytes.Repeat([]byte{byte(depth)}, 1+rng.Intn(64))
			rawdb.WriteStorageTrieNode(db, owner, path,
				encodeShortNode(t, nibblesToCompact([]byte{0xa}, true), value))

		case depth > 0 && rng.Intn(6) == 0:
			// Extension of 1-3 nibbles
			ext := make([]byte, 1+rng.Intn(3))
			for i := range ext {
				ext[i] = byte(rng.Intn(16))
			}
			rawdb.WriteStorageTrieNode(db, owner, path,
				encodeShortNode(t, nibblesToCompact(ext, false), hash))
			build(append(common.CopyBytes(path), ext...), depth+1)

		default:
			// Branch with a random, non-empty set of children
			var slots []byte
			for i := byte(0); i < 16; i++ {
				if rng.Intn(2) == 0 {
					slots = append(slots, i)
				}
			}
			if len(slots) == 0 {
				slots = []byte{byte(rng.Intn(16))}
			}
			rawdb.WriteStorageTrieNode(db, owner, path, encodeBranchNode(t, slots, hash))
			for _, s := range slots {
				build(append(common.CopyBytes(path), s), depth+1)
			}
		}
	}
	build(nil, 0)
	return paths
}

// legacyPreloadKeys runs the original slice-queue preload BFS (without rate
// limiting and logging) and returns the cache keys it loads.
func legacyPreloadKeys(db ethdb.Database, accountHash common.Hash, cacheSize int) map[string]struct{} {
	type queueItem struct {
		path  []byte
		depth int
	}
	loaded := make(map[string]struct{})
	var totalBytesLoaded uint64

	queue := []queueItem{{path: nil, depth: 0}}
	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		nodeData := rawdb.ReadStorageTrieNode(db, accountHash, item.path)
		if len(nodeData) == 0 {
			continue
		}
		nodeSize := uint64(common.HashLength + len(item.path) + len(nodeData))
		if totalBytesLoaded+nodeSize > uint64(cacheSize*2/3) {
			break
		}
		key := append(accountHash.Bytes(), item.path...)
		if _, ok := loaded[string(key)]; ok {
			continue
		}
		loaded[string(key)] = struct{}{}
		totalBytesLoaded += nodeSize

		for _, childPath := range decodeChildPaths(nodeData, item.path) {
			queue = append(queue, queueItem{path: childPath, depth: item.depth + 1})
		}
	}
	return loaded
}

// runPreload runs preloadAddressAsync synchronously on a bare cache. It does
// not go through NewAddressBiasedCache/Close so it stays independent of their
// signatures.
func runPreload(db ethdb.Database, addr common.Address, cacheSize int) *AddressBiasedCache {
	ctx, cancel := stdcontext.WithCancel(stdcontext.Background())
	c := &AddressBiasedCache{commonCache: fastcache.New(1024), ctx: ctx, cancel: cancel}
	c.initAddressCache(addr, cacheSize)
	c.wg.Add(1)
	c.preloadAddressAsync(db, addr, cacheSize)
	cancel()
	return c
}

// TestPreloadBFS_MatchesLegacySliceQueue checks that the preload reads nodes
// in the same BFS order and loads the same entries as the original slice
// queue, both when the whole trie fits and when the size limit stops the
// preload in the middle of a level.
func TestPreloadBFS_MatchesLegacySliceQueue(t *testing.T) {
	addr := common.HexToAddress("0x5eed5eed5eed5eed5eed5eed5eed5eed5eed5eed")
	accountHash := crypto.Keccak256Hash(addr.Bytes())
	base := rawdb.NewMemoryDatabase()
	paths := writeSyntheticStorageTrie(t, base, accountHash, 7)
	if len(paths) < 2*preloadQueueSegmentSize {
		t.Fatalf("synthetic trie too small to cross segments: %d nodes", len(paths))
	}

	for _, cacheSize := range []int{300, 5_000, 60_000, 1_000_000, 1_800_000, 2_500_000, 64 << 20} {
		legacyDB := &recordingDatabase{Database: base}
		want := legacyPreloadKeys(legacyDB, accountHash, cacheSize)

		db := &recordingDatabase{Database: base}
		c := runPreload(db, addr, cacheSize)

		if len(db.keys) != len(legacyDB.keys) {
			t.Fatalf("cache %d: read %d nodes, legacy read %d", cacheSize, len(db.keys), len(legacyDB.keys))
		}
		for i := range db.keys {
			if !bytes.Equal(db.keys[i], legacyDB.keys[i]) {
				t.Fatalf("cache %d: read %d differs from legacy order", cacheSize, i)
			}
		}
		var have int
		for _, p := range paths {
			key := append(accountHash.Bytes(), p...)
			_, inWant := want[string(key)]
			if c.Has(key) != inWant {
				t.Fatalf("cache %d: path %x cached=%v, legacy=%v", cacheSize, p, !inWant, inWant)
			}
			if inWant {
				have++
			}
		}
		if have != len(want) {
			t.Fatalf("cache %d: have %d entries, legacy loaded %d", cacheSize, have, len(want))
		}
		t.Logf("cache %d: %d/%d nodes loaded, %d reads", cacheSize, len(want), len(paths), len(db.keys))
	}
}

// benchmarkBFSQueue simulates the preload BFS frontier: for every pop, 16
// children are pushed (the XEN preload pops ~10.8M nodes and queues ~160M).
// It reports the peak live heap, the largest single allocation made by the
// queue and the slowest single push (a growslice copy shows up here).
func benchmarkBFSQueue(b *testing.B, pushes int, slice bool) {
	b.ReportAllocs()
	for iter := 0; iter < b.N; iter++ {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		baseHeap := ms.HeapAlloc
		var peakHeap uint64
		sample := func() {
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peakHeap {
				peakHeap = ms.HeapAlloc
			}
		}
		var (
			maxPush  time.Duration
			maxAlloc uintptr
			sq       []preloadQueueItem
			cq       preloadQueue
		)
		for i := 0; i < pushes; i++ {
			item := preloadQueueItem{depth: i}
			start := time.Now()
			if slice {
				oldCap := cap(sq)
				sq = append(sq, item)
				if cap(sq) != oldCap {
					maxAlloc = uintptr(cap(sq)) * unsafe.Sizeof(item)
				}
			} else {
				cq.push(item)
			}
			if d := time.Since(start); d > maxPush {
				maxPush = d
			}
			if i%16 == 15 {
				if slice {
					sq = sq[1:]
				} else {
					cq.pop()
				}
			}
			if i%(1<<20) == 0 {
				sample()
			}
		}
		sample()
		if !slice {
			maxAlloc = unsafe.Sizeof(preloadQueueSegment{})
		}
		b.ReportMetric(float64(peakHeap-baseHeap)/(1<<20), "peak-heap-MiB")
		b.ReportMetric(float64(maxAlloc)/(1<<20), "max-alloc-MiB")
		b.ReportMetric(float64(maxPush.Microseconds()), "max-push-µs")
		runtime.KeepAlive(sq)
		runtime.KeepAlive(&cq)
	}
}

func BenchmarkPreloadQueue_Slice20M(b *testing.B)   { benchmarkBFSQueue(b, 20_000_000, true) }
func BenchmarkPreloadQueue_Chunked20M(b *testing.B) { benchmarkBFSQueue(b, 20_000_000, false) }
