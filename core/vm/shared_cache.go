package vm

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/metrics"
)

// CacheConsumer identifies which executor reads the shared VM result caches,
// so hit/miss metrics can be split per consumer. When several executors run
// the same block, the first one to reach an input misses and the others hit,
// so a single global hit rate says little about real reuse. The zero value
// is CacheConsumerUnknown.
type CacheConsumer uint8

const (
	CacheConsumerUnknown CacheConsumer = iota
	CacheConsumerImportPrefetch
	CacheConsumerImportParallel
	CacheConsumerImportSerial
	CacheConsumerBuildPrefetch
	CacheConsumerBuildSeal
	numCacheConsumers
)

var cacheConsumerNames = [numCacheConsumers]string{
	"unknown", "import_prefetch", "import_parallel", "import_serial", "build_prefetch", "build_seal",
}

func (c CacheConsumer) String() string {
	if c >= numCacheConsumers {
		return cacheConsumerNames[CacheConsumerUnknown]
	}
	return cacheConsumerNames[c]
}

// CacheScope names the owner of a per-block result cache set: block import or
// block building. Each scope has its own widened keccak store, so their size
// gauges are reported separately.
type CacheScope uint8

const (
	CacheScopeImport CacheScope = iota
	CacheScopeBuild
	numCacheScopes
)

var cacheScopeNames = [numCacheScopes]string{"import", "build"}

type hitMissMeters struct {
	hit, miss *metrics.Meter
}

func newHitMissMeters(kind string) [numCacheConsumers]hitMissMeters {
	var m [numCacheConsumers]hitMissMeters
	for i := range m {
		prefix := "vm/cache/" + kind + "/" + cacheConsumerNames[i]
		m[i] = hitMissMeters{
			hit:  metrics.GetOrRegisterMeter(prefix+"/hit", nil),
			miss: metrics.GetOrRegisterMeter(prefix+"/miss", nil),
		}
	}
	return m
}

type keccakStoreMeters struct {
	entries *metrics.Gauge
	bytes   *metrics.Gauge
	full    *metrics.Meter
}

// Result-cache meters. The widened keccak meters cover the variable-length
// cache only and are sampled (see keccakMeterSampling). The legacy 64-byte
// keccak path is not counted. The ecrecover meters cover
// runEcrecoverWithCache.
var (
	keccakCacheMeters    = newHitMissMeters("keccak")
	ecrecoverCacheMeters = newHitMissMeters("ecrecover")

	keccakStoreScopeMeters = func() [numCacheScopes]keccakStoreMeters {
		var m [numCacheScopes]keccakStoreMeters
		for i := range m {
			prefix := "vm/cache/keccak/store/" + cacheScopeNames[i]
			m[i] = keccakStoreMeters{
				entries: metrics.GetOrRegisterGauge(prefix+"/entries", nil),
				bytes:   metrics.GetOrRegisterGauge(prefix+"/bytes", nil),
				full:    metrics.GetOrRegisterMeter(prefix+"/full", nil),
			}
		}
		return m
	}()
)

// keccakMeterSampling is the 1-in-N sampling rate of the widened keccak
// hit/miss meters. The widened cache sees tens of thousands of lookups per
// block from many goroutines at once, and Meter.Mark is an atomic add on one
// shared counter, so marking every lookup made the meter cost more than the
// cache hit. A sampled event adds N, so the meter count stays an unbiased
// estimate. The ecrecover meters are not sampled: there is about one call per
// transaction, next to a much more expensive signature check.
var keccakMeterSampling atomic.Uint32

func init() { keccakMeterSampling.Store(64) }

// SetKeccakMeterSampling sets the 1-in-n sampling rate of the widened keccak
// hit/miss meters (n <= 1 counts every event) and returns the previous rate.
// For tests that need exact counts.
func SetKeccakMeterSampling(n uint32) uint32 {
	if n == 0 {
		n = 1
	}
	return keccakMeterSampling.Swap(n)
}

// markSampled marks m for one event, sampled 1-in-keccakMeterSampling. The
// random draw uses the runtime's per-thread generator, so it adds no shared
// state between goroutines.
func markSampled(m *metrics.Meter) {
	n := keccakMeterSampling.Load()
	if n <= 1 {
		m.Mark(1)
		return
	}
	if rand.Uint32N(n) == 0 {
		m.Mark(int64(n))
	}
}

func (c CacheConsumer) keccakMeters() hitMissMeters {
	if c >= numCacheConsumers {
		c = CacheConsumerUnknown
	}
	return keccakCacheMeters[c]
}

func (c CacheConsumer) ecrecoverMeters() hitMissMeters {
	if c >= numCacheConsumers {
		c = CacheConsumerUnknown
	}
	return ecrecoverCacheMeters[c]
}

const (
	// defaultKeccakMaxEntries bounds the number of widened keccak entries kept
	// per block. It covers the measured working set (about 60k widened calls
	// per block) with margin.
	defaultKeccakMaxEntries = 1 << 16

	// defaultKeccakMaxBytes bounds the preimage bytes kept per block. Keccak
	// gas is 30 + 6 per 32-byte word, so it grows with input size and an
	// entry count alone does not bound memory. A block of unique inputs keeps
	// about 5.2 bytes per gas at 8192 bytes, 4.1 at 512 and 3.6 at 349
	// (before loop and memory costs). This budget caps the total. Map
	// overhead and the 32-byte hashes come on top of it.
	defaultKeccakMaxBytes = 32 << 20
)

// keccakResultCache caches keccak256(data) -> hash results for one block.
//
// Reads take no lock: the prefetcher, the parallel (V2) workers and the
// serial processor read the same entries at the same time, and one shared
// RWMutex made its reader count a contended cache line. The map is a
// sync.Map, like the legacy 64-byte cache. Keys are string(data), so inputs of
// different sizes never share an entry.
//
// Inserts reserve entry and byte budget with compare-and-swap, so both caps
// are exact under concurrency. The cache stops inserting once a budget is
// used, never returns a wrong hash, and is discarded at the end of the block,
// so it needs no eviction.
type keccakResultCache struct {
	m          sync.Map // string(data) -> common.Hash
	maxEntries int64
	maxBytes   int64
	entries    atomic.Int64
	bytes      atomic.Int64
	full       atomic.Bool
	meters     keccakStoreMeters
}

func newKeccakResultCache(maxEntries, maxBytes int64, scope CacheScope) *keccakResultCache {
	if scope >= numCacheScopes {
		scope = CacheScopeImport
	}
	return &keccakResultCache{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		meters:     keccakStoreScopeMeters[scope],
	}
}

// Load returns the cached hash of data, if present.
func (s *keccakResultCache) Load(data []byte) (common.Hash, bool) {
	// The key is a string view of data, built without a copy. It is safe
	// because sync.Map.Load only compares it and does not keep it, and data
	// is not changed during the call. Stored keys are always real copies
	// (see Store).
	v, ok := s.m.Load(unsafe.String(unsafe.SliceData(data), len(data)))
	if !ok {
		return common.Hash{}, false
	}
	return v.(common.Hash), true
}

// reserve takes n from counter if that keeps it at or below max.
func reserve(counter *atomic.Int64, n, max int64) bool {
	for {
		cur := counter.Load()
		if cur+n > max {
			return false
		}
		if counter.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

// Store caches h as the hash of data, unless a budget is used.
func (s *keccakResultCache) Store(data []byte, h common.Hash) {
	if s.full.Load() {
		return
	}
	n := int64(len(data))
	if !reserve(&s.entries, 1, s.maxEntries) {
		s.markFullIfExhausted()
		return
	}
	if !reserve(&s.bytes, n, s.maxBytes) {
		s.entries.Add(-1)
		s.markFullIfExhausted()
		return
	}
	// string(data) copies the input, so the stored key never aliases EVM
	// memory. If another executor stored the same input first, keep its
	// entry (same hash) and give the reservation back.
	if _, loaded := s.m.LoadOrStore(string(data), h); loaded {
		s.entries.Add(-1)
		s.bytes.Add(-n)
		return
	}
	// Approximate when two blocks of the same scope overlap (last write
	// wins). Good enough for a size gauge.
	s.meters.entries.Update(s.entries.Load())
	s.meters.bytes.Update(s.bytes.Load())
}

// markFullIfExhausted sets the full flag once no further entry can fit: the
// entry budget is used, or the byte budget is used exactly. A large input
// that does not fit while smaller ones still do leaves the cache open.
func (s *keccakResultCache) markFullIfExhausted() {
	if s.full.Load() {
		return
	}
	if s.entries.Load() >= s.maxEntries || s.bytes.Load() >= s.maxBytes {
		if s.full.CompareAndSwap(false, true) {
			s.meters.full.Mark(1)
		}
	}
}

// SharedResultCaches owns the VM-level result caches shared by the executors
// of one block. The legacy caches (jumpDests, 64-byte keccak, ecrecover) are
// always present and always wired, as before this flag existed. The widened
// keccak store exists only when the precompile cache flag is on.
type SharedResultCaches struct {
	jumpDests JumpDestCache
	keccak    *Keccak256Cache
	ecrecover *EcrecoverCache
	keccakEx  *keccakResultCache // nil unless the flag is on
}

// NewSharedResultCaches constructs the caches for one block. enableExtended
// creates the widened keccak store, which ApplyTo then wires together with
// EnablePrecompileCache. scope selects the size gauges for that store.
func NewSharedResultCaches(enableExtended bool, scope CacheScope) *SharedResultCaches {
	c := &SharedResultCaches{
		jumpDests: NewSyncJumpDestCache(),
		keccak:    NewKeccak256Cache(),
		ecrecover: NewEcrecoverCache(),
	}
	if enableExtended {
		c.keccakEx = newKeccakResultCache(defaultKeccakMaxEntries, defaultKeccakMaxBytes, scope)
	}
	return c
}

// ApplyTo wires the caches into cfg and tags cfg with the consumer, for the
// per-consumer metrics. The legacy caches are always wired. The widened
// keccak store and EnablePrecompileCache are wired only when it exists.
func (c *SharedResultCaches) ApplyTo(cfg *Config, consumer CacheConsumer) {
	cfg.SharedJumpDestCache = c.jumpDests
	cfg.Keccak256Cache = c.keccak
	cfg.EcrecoverCache = c.ecrecover
	cfg.ResultCacheConsumer = consumer
	if c.keccakEx != nil {
		cfg.keccakCache = c.keccakEx
		cfg.EnablePrecompileCache = true
	}
}
