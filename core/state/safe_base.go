package state

import (
	"runtime"
	"sync"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// SafeBase provides thread-safe access to StateDB base reads with a
// shared read-through cache. The base state is immutable for the duration
// of a block, so cached values are valid forever within the block.
//
// Architecture:
//   - sync.Map caches sit in front of the pool (lock-free reads for cache hits)
//   - Cache misses acquire a pool copy, read from trie, cache result, release
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

	// SharedStorageCache points to the readerWithCache's storageCache (if set).
	// The prefetcher populates this cache as it warms storage slots.
	// V2 workers check it BEFORE going through the pool, getting instant hits
	// for slots already warmed by the prefetcher.
	SharedStorageCache *sync.Map // storageCacheKey{addr,slot} → common.Hash

	// OverlayStorageCache holds pre-block system-call writes (EIP-4788/2935).
	// GetState consults it ahead of SharedStorageCache so the prefetcher's
	// trieReader Load→read→Store cannot clobber the overlay value.
	OverlayStorageCache *sync.Map // stateKey{addr,slot} → common.Hash

	// readDelay is set from TestReadDelay at creation time.
	readDelay time.Duration
}

type stateKey struct {
	addr common.Address
	slot common.Hash
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
	if s.pool == nil {
		return
	}
	s.pool <- db
}

// flatAccount returns account-level FlatDiff coverage for pipelined import.
// In flatdiff mode SafeBase reads root N-1 while the FlatDiff represents N,
// so scalar account reads must check this overlay before pooled StateDB copies
// that may already hold stale stateObjects.
func (s *SafeBase) flatAccount(addr common.Address) (types.StateAccount, bool, bool) {
	if s.DB == nil {
		return types.StateAccount{}, false, false
	}
	return s.DB.flatDiffRef.accountOverlay(addr)
}

func (s *SafeBase) GetBalance(addr common.Address) *uint256.Int {
	if v, ok := s.balCache.Load(addr); ok {
		bal := v.(uint256.Int)
		return &bal
	}
	if acct, exists, covered := s.flatAccount(addr); covered {
		result := new(uint256.Int)
		if exists && acct.Balance != nil {
			result.Set(acct.Balance)
		}
		s.balCache.Store(addr, *result)
		return result
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.GetBalance(addr)
	s.balCache.Store(addr, *result) // store by value
	return result
}

func (s *SafeBase) GetNonce(addr common.Address) uint64 {
	if v, ok := s.nonceCache.Load(addr); ok {
		return v.(uint64)
	}
	if acct, exists, covered := s.flatAccount(addr); covered {
		var result uint64
		if exists {
			result = acct.Nonce
		}
		s.nonceCache.Store(addr, result)
		return result
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.GetNonce(addr)
	s.nonceCache.Store(addr, result)
	return result
}

func (s *SafeBase) GetState(addr common.Address, key common.Hash) common.Hash {
	sk := stateKey{addr: addr, slot: key}
	if v, ok := s.stateCache.Load(sk); ok {
		return v.(common.Hash)
	}
	// Check the V2-owned overlay first. The prefetcher writes only to
	// SharedStorageCache (its trieReader.storageCache), so it cannot race
	// against this map; the post-system-call value always wins.
	if s.OverlayStorageCache != nil {
		if v, ok := s.OverlayStorageCache.Load(sk); ok {
			result := v.(common.Hash)
			s.stateCache.Store(sk, result)
			return result
		}
	}
	// FlatDiff is part of the logical base when pipelined import executes
	// block N+1 on top of committed root N-1. It must beat shared trie caches,
	// which are keyed by the committed root and can contain pre-FlatDiff values.
	if value, ok, _ := s.DB.flatDiffRef.storageOverlay(addr, key); ok {
		s.stateCache.Store(sk, value)
		return value
	}
	// Check the shared readerWithCache storageCache — populated by the
	// prefetcher running concurrently. This gives V2 workers instant hits
	// for slots the prefetcher has already warmed, bypassing the pool entirely.
	if s.SharedStorageCache != nil {
		if v, ok := s.SharedStorageCache.Load(sk); ok {
			result := v.(common.Hash)
			s.stateCache.Store(sk, result)
			return result
		}
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.GetState(addr, key)
	s.stateCache.Store(sk, result)
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
	if acct, exists, covered := s.flatAccount(addr); covered {
		if !exists || len(acct.CodeHash) == 0 || common.BytesToHash(acct.CodeHash) == types.EmptyCodeHash {
			s.codeCache.Store(addr, []byte(nil))
			return nil
		}
		if code, ok := s.DB.flatDiffRef.Code[common.BytesToHash(acct.CodeHash)]; ok {
			s.codeCache.Store(addr, code)
			return code
		}
		// Account metadata can be covered even when code bytes are unchanged
		// and therefore absent from the FlatDiff code map. In that case the
		// committed trie still has the correct code for this code hash.
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.GetCode(addr)
	s.codeCache.Store(addr, result)
	return result
}

func (s *SafeBase) GetCodeHash(addr common.Address) common.Hash {
	if v, ok := s.hashCache.Load(addr); ok {
		return v.(common.Hash)
	}
	if acct, exists, covered := s.flatAccount(addr); covered {
		var result common.Hash
		if exists {
			if len(acct.CodeHash) == 0 {
				result = types.EmptyCodeHash
			} else {
				result = common.BytesToHash(acct.CodeHash)
			}
		}
		s.hashCache.Store(addr, result)
		return result
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.GetCodeHash(addr)
	s.hashCache.Store(addr, result)
	return result
}

func (s *SafeBase) GetCodeSize(addr common.Address) int {
	return len(s.GetCode(addr))
}

func (s *SafeBase) Exist(addr common.Address) bool {
	if v, ok := s.existCache.Load(addr); ok {
		return v.(bool)
	}
	if _, exists, covered := s.flatAccount(addr); covered {
		s.existCache.Store(addr, exists)
		return exists
	}
	s.simulateReadLatency()
	db := s.acquire()
	defer s.release(db)
	result := db.Exist(addr)
	s.existCache.Store(addr, result)
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
	if acct, exists, covered := s.flatAccount(addr); covered {
		var result common.Hash
		if exists {
			result = acct.Root
		}
		s.rootCache.Store(addr, result)
		return result
	}
	db := s.acquire()
	defer s.release(db)
	result := db.GetStorageRoot(addr)
	s.rootCache.Store(addr, result)
	return result
}
