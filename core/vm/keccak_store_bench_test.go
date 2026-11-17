package vm

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// Benchmarks for the production widened keccak cache (keccakResultCache).
// Hot input sizes come from mainnet measurements: 88, 128 and 349 bytes
// (64-byte inputs use the legacy cache and are not stored here).

var keccakBenchHotSizes = []int{88, 128, 349}

// keccakBenchHotKeys returns n distinct inputs cycling over the hot sizes.
func keccakBenchHotKeys(n int) [][]byte {
	keys := make([][]byte, n)
	for i := range keys {
		k := make([]byte, keccakBenchHotSizes[i%len(keccakBenchHotSizes)])
		binary.BigEndian.PutUint64(k, uint64(i))
		keys[i] = k
	}
	return keys
}

func newFilledKeccakCache(keys [][]byte) *keccakResultCache {
	c := newKeccakResultCache(defaultKeccakMaxEntries, defaultKeccakMaxBytes, CacheScopeImport)
	for _, k := range keys {
		c.Store(k, crypto.Keccak256Hash(k))
	}
	return c
}

// BenchmarkKeccakCache_LoadHit measures a cache hit on one goroutine.
func BenchmarkKeccakCache_LoadHit(b *testing.B) {
	keys := keccakBenchHotKeys(256)
	c := newFilledKeccakCache(keys)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := c.Load(keys[i%len(keys)]); !ok {
			b.Fatal("miss")
		}
	}
}

// BenchmarkKeccakCache_LoadHitParallel measures cache hits from GOMAXPROCS
// goroutines, as the prefetcher, V2 workers and the serial processor do.
func BenchmarkKeccakCache_LoadHitParallel(b *testing.B) {
	keys := keccakBenchHotKeys(256)
	c := newFilledKeccakCache(keys)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if _, ok := c.Load(keys[i%len(keys)]); !ok {
				b.Error("miss")
				return
			}
			i++
		}
	})
}

// BenchmarkKeccakCache_FillBlockParallel models one block on a fresh cache:
// 8 goroutines each store 512 unique inputs and read 9 hot entries per store
// (about 90% hits). One op is one whole block. The ns/insert metric divides
// by the 4096 inserts.
func BenchmarkKeccakCache_FillBlockParallel(b *testing.B) {
	const (
		workers   = 8
		perWorker = 512
		readsPer  = 9
	)
	hot := keccakBenchHotKeys(64)
	unique := make([][][]byte, workers)
	for w := range unique {
		unique[w] = make([][]byte, perWorker)
		for i := range unique[w] {
			k := make([]byte, keccakBenchHotSizes[i%len(keccakBenchHotSizes)])
			binary.BigEndian.PutUint64(k, uint64(w)<<32|uint64(i)|1<<63)
			unique[w][i] = k
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		c := newFilledKeccakCache(hot)
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func(keys [][]byte) {
				defer wg.Done()
				for i, k := range keys {
					c.Store(k, keccakBenchFakeHash(i))
					for r := 0; r < readsPer; r++ {
						c.Load(hot[(i+r)%len(hot)])
					}
				}
			}(unique[w])
		}
		wg.Wait()
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*workers*perWorker), "ns/insert")
}

// BenchmarkKeccakCache_StoreWhenFullParallel measures Store of new inputs
// once the entry budget is used: the early return without the lock.
func BenchmarkKeccakCache_StoreWhenFullParallel(b *testing.B) {
	c := newKeccakResultCache(1024, defaultKeccakMaxBytes, CacheScopeImport)
	for _, k := range keccakBenchHotKeys(1024) {
		c.Store(k, crypto.Keccak256Hash(k))
	}
	c.Store([]byte{1}, keccakBenchFakeHash(0)) // the first rejected Store marks the cache full
	if !c.full.Load() {
		b.Fatal("cache not full")
	}
	var seq atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		k := make([]byte, 88)
		// Per-goroutine counter, so the benchmark does not add its own
		// shared atomic to the measured path.
		next := seq.Add(1) << 40
		for pb.Next() {
			next++
			binary.BigEndian.PutUint64(k, next|1<<63)
			c.Store(k, keccakBenchFakeHash(0))
		}
	})
}

// keccakBenchFakeHash returns a cheap stand-in hash so the store benchmarks
// measure the cache, not keccak itself.
func keccakBenchFakeHash(i int) (h [32]byte) {
	binary.BigEndian.PutUint64(h[:], uint64(i))
	return h
}

// benchOpKeccak256Parallel runs opKeccak256 over an 88-byte input from
// GOMAXPROCS goroutines, each with its own EVM. With a cache, all EVMs share
// it (a warm hit, including the hit meter). Without, every call hashes.
func benchOpKeccak256Parallel(b *testing.B, cache *keccakResultCache) {
	input := make([]byte, 88)
	for i := range input {
		input[i] = byte(i)
	}
	if cache != nil {
		cache.Store(input, crypto.Keccak256Hash(input))
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
		cfg := Config{ResultCacheConsumer: CacheConsumerImportParallel}
		if cache != nil {
			cfg.EnablePrecompileCache = true
			cfg.keccakCache = cache
		}
		evm := NewEVM(BlockContext{}, statedb, params.TestChainConfig, cfg)
		stack := newstack()
		mem := NewMemory()
		mem.Resize(uint64(len(input)))
		mem.Set(0, uint64(len(input)), input)
		scope := &ScopeContext{mem, stack, nil}
		pc := uint64(0)
		for pb.Next() {
			stack.push(uint256.NewInt(uint64(len(input))))
			stack.push(uint256.NewInt(0))
			if _, err := opKeccak256(&pc, evm, scope); err != nil {
				b.Error(err)
				return
			}
			stack.pop()
		}
	})
}

func BenchmarkOpKeccak256_88B_NoCacheParallel(b *testing.B) {
	benchOpKeccak256Parallel(b, nil)
}

func BenchmarkOpKeccak256_88B_CacheHitParallel(b *testing.B) {
	benchOpKeccak256Parallel(b, newKeccakResultCache(defaultKeccakMaxEntries, defaultKeccakMaxBytes, CacheScopeImport))
}
