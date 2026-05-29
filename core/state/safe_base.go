package state

import (
	"runtime"
	"sync"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
)

// SafeBase provides thread-safe access to StateDB base reads with a
// shared read-through cache. The base state is immutable for the duration
// of a block, so cached values are valid forever within the block.
// Overlay semantics such as FlatDiff and pending system-contract writes stay
// inside StateDB; SafeBase only caches the values returned by StateDB getters.
//
// Architecture:
//   - sync.Map caches sit in front of the pool (lock-free reads for cache hits)
//   - Cache misses acquire a pool copy, read through StateDB, cache result, release
//   - All workers share one SafeBase → cache warms from any worker's reads
type SafeBase struct {
	pool chan *StateDB
	DB   *StateDB // original, for pre-warming only

	// Thread-safe read caches (base state is immutable, so values never change)
	stateCache sync.Map // stateKey{addr,slot} → common.Hash
	codeCache  sync.Map // common.Address → []byte
	nonceCache sync.Map // common.Address → uint64
	balCache   sync.Map // common.Address → uint256.Int (value type, not pointer)
	existCache sync.Map // common.Address → bool
	hashCache  sync.Map // common.Address → common.Hash (code hash)
	rootCache  sync.Map // common.Address → common.Hash (storage root)

	// readDelay is set from TestReadDelay at creation time.
	readDelay time.Duration

	errMu sync.Mutex
	err   error
}

type stateKey struct {
	addr common.Address
	slot common.Hash
}

// SafeBaseDiagnostics is collected only on V2 failure paths. It reports cache
// cardinality rather than per-read counters so normal execution avoids extra
// atomic operations on every base read.
type SafeBaseDiagnostics struct {
	StateEntries       int
	CodeEntries        int
	NonceEntries       int
	BalanceEntries     int
	ExistEntries       int
	CodeHashEntries    int
	StorageRootEntries int
	PoolCapacity       int
	PoolAvailable      int
	ReadErr            error
}

// TestReadDelay is set by benchmarks to simulate production disk I/O.
// SafeBase reads this at creation time. Zero means no delay (default).
var TestReadDelay time.Duration

func NewSafeBase(db *StateDB, poolSize int) *SafeBase {
	sb := &SafeBase{DB: db, readDelay: TestReadDelay}
	if poolSize > 0 {
		sb.pool = make(chan *StateDB, poolSize)
		for i := 0; i < poolSize; i++ {
			c := db.Copy()
			c.SkipTimers() // V2: no per-operation timing needed for workers
			sb.pool <- c
		}
	}
	return sb
}

func (s *SafeBase) simulateReadLatency() {
	if s.readDelay > 0 {
		start := time.Now()
		for time.Since(start) < s.readDelay {
			runtime.Gosched()
		}
	}
}

func (s *SafeBase) acquire() *StateDB {
	if s.pool == nil {
		return s.DB // direct mode: single-threaded, no pool needed
	}
	return <-s.pool
}

func (s *SafeBase) release(db *StateDB) {
	if err := db.Error(); err != nil {
		s.setError(err)
		if s.pool != nil {
			// StateDB keeps read errors internally and would keep returning
			// zero-ish values, so never return a poisoned copy to the pool.
			replacement := s.DB.Copy()
			replacement.SkipTimers()
			s.pool <- replacement
		}
		return
	}
	if s.pool == nil {
		return
	}
	s.pool <- db
}

func (s *SafeBase) setError(err error) {
	if err == nil {
		return
	}
	s.errMu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.errMu.Unlock()
}

// Error returns the first database read failure captured by any pooled base
// reader. StateDB getters return zero-ish values after recording read errors,
// so V2 must discard the block execution result when this is non-nil.
func (s *SafeBase) Error() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.err
}

func (s *SafeBase) Diagnostics() SafeBaseDiagnostics {
	var diag SafeBaseDiagnostics
	diag.StateEntries = syncMapLen(&s.stateCache)
	diag.CodeEntries = syncMapLen(&s.codeCache)
	diag.NonceEntries = syncMapLen(&s.nonceCache)
	diag.BalanceEntries = syncMapLen(&s.balCache)
	diag.ExistEntries = syncMapLen(&s.existCache)
	diag.CodeHashEntries = syncMapLen(&s.hashCache)
	diag.StorageRootEntries = syncMapLen(&s.rootCache)
	if s.pool != nil {
		diag.PoolCapacity = cap(s.pool)
		diag.PoolAvailable = len(s.pool)
	}
	diag.ReadErr = s.Error()
	return diag
}

func syncMapLen(m *sync.Map) int {
	count := 0
	m.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

func (s *SafeBase) cleanRead(db *StateDB) bool {
	if err := db.Error(); err != nil {
		s.setError(err)
		return false
	}
	return true
}

func (s *SafeBase) GetBalance(addr common.Address) *uint256.Int {
	if v, ok := s.balCache.Load(addr); ok {
		bal := v.(uint256.Int)
		return &bal
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.GetBalance(addr)
	if s.cleanRead(db) {
		s.balCache.Store(addr, *result) // store by value
	}
	return result
}

func (s *SafeBase) GetNonce(addr common.Address) uint64 {
	if v, ok := s.nonceCache.Load(addr); ok {
		return v.(uint64)
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.GetNonce(addr)
	if s.cleanRead(db) {
		s.nonceCache.Store(addr, result)
	}
	return result
}

func (s *SafeBase) GetState(addr common.Address, key common.Hash) common.Hash {
	sk := stateKey{addr: addr, slot: key}
	if v, ok := s.stateCache.Load(sk); ok {
		return v.(common.Hash)
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.GetState(addr, key)
	if s.cleanRead(db) {
		s.stateCache.Store(sk, result)
	}
	return result
}

func (s *SafeBase) GetCommittedState(addr common.Address, key common.Hash) common.Hash {
	// For the base state (pre-block), GetState == GetCommittedState
	return s.GetState(addr, key)
}

func (s *SafeBase) GetCode(addr common.Address) []byte {
	if v, ok := s.codeCache.Load(addr); ok {
		return v.([]byte)
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.GetCode(addr)
	if s.cleanRead(db) {
		s.codeCache.Store(addr, result)
	}
	return result
}

func (s *SafeBase) GetCodeHash(addr common.Address) common.Hash {
	if v, ok := s.hashCache.Load(addr); ok {
		return v.(common.Hash)
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.GetCodeHash(addr)
	if s.cleanRead(db) {
		s.hashCache.Store(addr, result)
	}
	return result
}

func (s *SafeBase) GetCodeSize(addr common.Address) int {
	return len(s.GetCode(addr))
}

func (s *SafeBase) Exist(addr common.Address) bool {
	if v, ok := s.existCache.Load(addr); ok {
		return v.(bool)
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.Exist(addr)
	if s.cleanRead(db) {
		s.existCache.Store(addr, result)
	}
	return result
}

// CollectCodeWitness adds every code blob loaded by V2 workers via
// SafeBase.GetCode into the supplied addCode callback. Workers' code
// reads never reach finalDB.stateObjects (they live on per-worker pool
// copies that are discarded after settle), so finalDB.IntermediateRoot's
// witness loop misses them. The codeCache is populated whenever a
// worker resolves contract code; walking it here captures every blob
// V2 needed to execute the block.
func (s *SafeBase) CollectCodeWitness(addCode func([]byte)) {
	s.codeCache.Range(func(_, v any) bool {
		if code, ok := v.([]byte); ok {
			addCode(code)
		}
		return true
	})
}

func (s *SafeBase) GetStorageRoot(addr common.Address) common.Hash {
	if v, ok := s.rootCache.Load(addr); ok {
		return v.(common.Hash)
	}
	db := s.acquire()
	defer s.release(db)
	result := db.GetStorageRoot(addr)
	if s.cleanRead(db) {
		s.rootCache.Store(addr, result)
	}
	return result
}
