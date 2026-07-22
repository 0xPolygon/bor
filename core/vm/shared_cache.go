package vm

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/metrics"
)

// Result-cache observability meters. Distinct namespace from the
// state-reader caches at core/blockchain.go:84-99 / miner/worker.go
// ("chain/cache/*", "worker/chain/*") — those track a different cache
// (state-read results), not these VM opcode/precompile result caches.
//
// keccakCacheHit/Miss and keccakCacheEntries/Bytes track the widened
// (Task 3) keccak store only — the legacy 64B path is unmetered by design
// (kept out of scope; see task-7 brief). ecrecoverCacheHit/Miss track the
// always-on legacy ecrecover cache in runEcrecoverWithCache. All increments
// are cheap atomic ops and only occur on paths that already run when the
// corresponding cache is wired, so they add ~zero overhead when unused.
var (
	keccakCacheHit      = metrics.GetOrRegisterMeter("vm/cache/keccak/hit", nil)
	keccakCacheMiss     = metrics.GetOrRegisterMeter("vm/cache/keccak/miss", nil)
	keccakCacheEntries  = metrics.GetOrRegisterGauge("vm/cache/keccak/entries", nil)
	// keccakCacheBytesTotal is a cumulative counter: cumulative bytes
	// inserted (lifetime), not current retained. It is never decremented
	// when a per-block store is discarded, so it must not be read as
	// "current retained bytes" on a dashboard.
	keccakCacheBytesTotal = metrics.GetOrRegisterCounter("vm/cache/keccak/bytes_total", nil)
	keccakCacheLockWait   = metrics.GetOrRegisterTimer("vm/cache/keccak/lock_wait", nil)

	ecrecoverCacheHit  = metrics.GetOrRegisterMeter("vm/cache/ecrecover/hit", nil)
	ecrecoverCacheMiss = metrics.GetOrRegisterMeter("vm/cache/ecrecover/miss", nil)
)

// Keccak backing-store microbenchmark (BenchmarkKeccakStore_*, 3 runs,
// -benchmem, Apple M4 Pro) compared three candidates over the measured hot
// sizes {64, 88, 128, 349} plus a large-unique-8192 adversarial mix:
//
//	sync.Map (string key)  ~38 ns/op   160 B/op   1 allocs/op  (Load)
//	sharded map+RWMutex    ~12.5 ns/op   0 B/op   0 allocs/op  (Load)
//	fixed-array buckets    ~16.3 ns/op   0 B/op   0 allocs/op  (Load)
//
// All three converge on the large-unique-8192 mix (Store-dominated, ~3.8-3.9
// us/op) since that path is bound by the one-time map insert + Keccak256,
// not the backing structure. The sharded map is both the fastest and the
// only zero-allocation candidate on the hot Load path (fixed-buckets is also
// zero-alloc but ~30% slower due to its per-size type switch), so it wins
// outright per the decision rule (lowest allocation; not decisively beaten
// on speed by anything with equal or lower allocations). sync.Map is
// disqualified: >0 B/op on Load.
//
// See core/vm/keccak_store_bench_test.go for the benchmark source and
// candidate implementations (the two runners-up live only in that file).

// defaultKeccakCap bounds retained widened-keccak entries per block so an
// adversarial block of unique large inputs (<=8192B) cannot amplify retained
// memory beyond what is otherwise transient. Chosen to cover the measured hot
// working set (~60k widened calls/block) with margin; tune via the memory test.
const defaultKeccakCap = 1 << 16

// keccakResultStore caches keccak256(data) -> hash results for one block.
// Implementations MUST be length-aware: they key on the exact bytes (and thus
// length) of data, so no two differently-sized inputs can ever alias the same
// entry.
type keccakResultStore interface {
	Load(data []byte) (common.Hash, bool)
	Store(data []byte, h common.Hash)
}

// shardedKeccakStore is the benchmark winner from Task 1: a single
// map[string]common.Hash guarded by a sync.RWMutex. Length-aware by
// construction (the map key is string(data), which encodes length). Stops
// inserting once the entry cap is hit; never returns a wrong/aliased hash.
// It is a per-block, throwaway store: no eviction churn is needed because the
// whole store is discarded at the end of the block.
type shardedKeccakStore struct {
	mu      sync.RWMutex
	m       map[string]common.Hash
	cap     int
	entries atomic.Int64
}

func newKeccakStore(cap int) keccakResultStore {
	return &shardedKeccakStore{m: make(map[string]common.Hash), cap: cap}
}

func (s *shardedKeccakStore) Load(data []byte) (common.Hash, bool) {
	s.mu.RLock()
	h, ok := s.m[string(data)] // compiler avoids the []byte->string alloc on map lookup
	s.mu.RUnlock()
	return h, ok
}

func (s *shardedKeccakStore) Store(data []byte, h common.Hash) {
	start := time.Now()
	s.mu.Lock()
	// waited measures lock acquisition wait; captured under the lock (right
	// after acquiring it) so it reflects only the wait, not the work below.
	waited := time.Since(start)
	if int(s.entries.Load()) >= s.cap {
		s.mu.Unlock()
		keccakCacheLockWait.Update(waited)
		return // stop inserting; per-block store, discarded after the block
	}
	var inserted bool
	var n int64
	if _, exists := s.m[string(data)]; !exists {
		s.m[string(data)] = h
		n = s.entries.Add(1)
		inserted = true
	}
	s.mu.Unlock()

	// Meter updates run after unlocking: they are not part of the mutual
	// exclusion this lock protects (the map/entries state), and keeping them
	// out of the critical section avoids widening what lock_wait measures
	// and keeps this hot path (~60k calls/block) from paying meter overhead
	// while holding the lock.
	keccakCacheLockWait.Update(waited)
	if inserted {
		// Best-effort process-wide sample: with one store per in-flight
		// block, concurrent blocks' updates race harmlessly (last write
		// wins), giving an approximate current size rather than an exact
		// global total. Cheap and race-free per the entries counter itself.
		keccakCacheEntries.Update(n)
		keccakCacheBytesTotal.Inc(int64(len(data)))
	}
}

// SharedResultCaches owns the VM-level result caches shared across the
// prefetcher goroutine and the V2 BlockSTM workers for a single block. The
// legacy caches (jumpDests, keccak, ecrecover) are always populated — this
// preserves today's import prefetch↔parallel sharing regardless of the
// EnablePrecompileCache flag. The widened keccak store is populated only
// when constructed with enableExtended == true.
type SharedResultCaches struct {
	jumpDests JumpDestCache
	keccak    *sync.Map // legacy [64]byte→common.Hash, always present
	ecrecover *sync.Map // [128]byte→[]byte, always present
	keccakEx  keccakResultStore // widened store; nil unless extended
	extended  bool
}

// NewSharedResultCaches constructs the owner. enableExtended gates the
// widened keccak store and cfg.EnablePrecompileCache in ApplyTo; the legacy
// caches are always constructed and wired regardless.
func NewSharedResultCaches(enableExtended bool) *SharedResultCaches {
	c := &SharedResultCaches{
		jumpDests: NewSyncJumpDestCache(),
		keccak:    &sync.Map{},
		ecrecover: &sync.Map{},
		extended:  enableExtended,
	}
	if enableExtended {
		c.keccakEx = newKeccakStore(defaultKeccakCap)
	}
	return c
}

// ApplyTo wires the caches into cfg. The legacy caches are always wired (this
// preserves today's import prefetch↔parallel behavior regardless of the
// flag). The widened keccak store and the flag are wired only when extended.
func (c *SharedResultCaches) ApplyTo(cfg *Config) {
	cfg.SharedJumpDestCache = c.jumpDests
	cfg.Keccak256Cache = c.keccak
	cfg.EcrecoverCache = c.ecrecover
	if c.extended {
		cfg.KeccakStore = c.keccakEx
		cfg.EnablePrecompileCache = true
	}
}
