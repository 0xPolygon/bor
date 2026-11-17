package vm

import (
	"sync"
	"sync/atomic"

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
// store only. The legacy 64-byte keccak path is not counted. The ecrecover
// meters cover runEcrecoverWithCache.
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
	// gas grows linearly with input size (6 gas per 32-byte word), so an
	// entry count alone does not bound memory: a block of unique large inputs
	// keeps about 5 bytes per gas at any input size. This budget caps that.
	defaultKeccakMaxBytes = 32 << 20
)

// keccakResultStore caches keccak256(data) -> hash results for one block.
// Implementations key on the exact bytes (and so the length) of data, so two
// inputs of different sizes can never share an entry.
type keccakResultStore interface {
	Load(data []byte) (common.Hash, bool)
	Store(data []byte, h common.Hash)
}

// shardedKeccakStore is a map[string]common.Hash guarded by a sync.RWMutex.
// The map key is string(data), so it is length-aware. It stops inserting once
// the entry or byte budget is reached and never returns a wrong hash. It is
// discarded at the end of the block, so it needs no eviction.
type shardedKeccakStore struct {
	mu         sync.RWMutex
	m          map[string]common.Hash
	maxEntries int64
	maxBytes   int64
	entries    atomic.Int64
	bytes      atomic.Int64
	full       atomic.Bool
	meters     keccakStoreMeters
}

func newKeccakStore(maxEntries, maxBytes int64, scope CacheScope) keccakResultStore {
	if scope >= numCacheScopes {
		scope = CacheScopeImport
	}
	return &shardedKeccakStore{
		m:          make(map[string]common.Hash),
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		meters:     keccakStoreScopeMeters[scope],
	}
}

func (s *shardedKeccakStore) Load(data []byte) (common.Hash, bool) {
	s.mu.RLock()
	h, ok := s.m[string(data)] // no []byte->string allocation on map lookup
	s.mu.RUnlock()
	return h, ok
}

// fits reports whether one more entry of size n stays within both budgets.
// Callers check it once without the lock (to skip a full store cheaply) and
// again under the lock (where it is exact).
func (s *shardedKeccakStore) fits(n int) bool {
	return s.entries.Load() < s.maxEntries && s.bytes.Load()+int64(n) <= s.maxBytes
}

func (s *shardedKeccakStore) Store(data []byte, h common.Hash) {
	// Once the store is full, misses return here without touching the lock.
	if s.full.Load() || !s.fits(len(data)) {
		s.markFullIfExhausted()
		return
	}
	s.mu.Lock()
	if !s.fits(len(data)) {
		s.mu.Unlock()
		s.markFullIfExhausted()
		return
	}
	// One map write. Overwriting an existing key is harmless: the value is
	// keccak256 of the key, so it is the same hash.
	before := len(s.m)
	s.m[string(data)] = h
	inserted := len(s.m) > before
	var entries, bytes int64
	if inserted {
		entries = s.entries.Add(1)
		bytes = s.bytes.Add(int64(len(data)))
	}
	s.mu.Unlock()

	if inserted {
		// Approximate when two blocks of the same scope overlap (last write
		// wins). Good enough for a size gauge.
		s.meters.entries.Update(entries)
		s.meters.bytes.Update(bytes)
	}
}

// markFullIfExhausted sets the full flag once no further entry can fit: the
// entry budget is used, or the remaining byte budget is below the smallest
// cacheable input. A large input that does not fit while smaller ones still
// do leaves the store open.
func (s *shardedKeccakStore) markFullIfExhausted() {
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
	keccakEx  keccakResultStore // nil unless the flag is on
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
		c.keccakEx = newKeccakStore(defaultKeccakMaxEntries, defaultKeccakMaxBytes, scope)
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
		cfg.KeccakStore = c.keccakEx
		cfg.EnablePrecompileCache = true
	}
}
