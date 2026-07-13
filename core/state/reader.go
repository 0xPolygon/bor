// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package state

import (
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/lru"
	"github.com/ethereum/go-ethereum/core/overlay"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/trie/utils"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/ethereum/go-ethereum/triedb/database"
)

// Reader-cache retention metrics (BOR_READER_RETENTION=1).
var (
	retentionServeMeter       = metrics.NewRegisteredMeter("state/retention/serve", nil)
	retentionInvalidatedMeter = metrics.NewRegisteredMeter("state/retention/invalidated", nil)
	retentionResetMeter       = metrics.NewRegisteredMeter("state/retention/reset", nil)

	// Hit-age histograms: generations (≈blocks) between an entry's insertion
	// and a cache hit on it. Age 0 = warmed within the current block; age ≥ 1
	// = served by retention across a commit. Sampled 1/64 to keep the hot
	// path cheap; feeds the layered/generational cache sizing decision.
	retentionAcctHitAgeHist = metrics.NewRegisteredHistogram("state/retention/hitage/account", nil, metrics.NewExpDecaySample(2048, 0.015))
	retentionStorHitAgeHist = metrics.NewRegisteredHistogram("state/retention/hitage/storage", nil, metrics.NewExpDecaySample(2048, 0.015))

	retentionAcctSizeGauge = metrics.NewRegisteredGauge("state/retention/size/accounts", nil)
	retentionStorSizeGauge = metrics.NewRegisteredGauge("state/retention/size/slots", nil)

	// CLOCK eviction observability: evicted = entries removed at cap (ref bit
	// clear); secondchance = entries spared once because a hit set their ref
	// bit since insertion/last sweep. A high secondchance/evicted ratio means
	// the ref bit is doing its job protecting the read-hot, write-cold set.
	retentionEvictedMeter      = metrics.NewRegisteredMeter("state/retention/clock/evicted", nil)
	retentionSecondChanceMeter = metrics.NewRegisteredMeter("state/retention/clock/secondchance", nil)

	// Total live ring slots (incl. stale keys awaiting sweep) across all
	// buckets — the CLOCK bookkeeping's own memory footprint.
	retentionRingLenGauge = metrics.NewRegisteredGauge("state/retention/clock/ringlen", nil)

	acctHitAgeSampleCtr atomic.Uint64
	storHitAgeSampleCtr atomic.Uint64
)

// CLOCK cap (BOR_RETENTION_CLOCK = total combined entry budget; 0/unset = off,
// falling back to the wholesale-reset caps below). The budget splits 1/8 to
// accounts and 7/8 to storage slots (the measured live ratio), then evenly
// across the 16 buckets. Chosen over LRU by simulation on the measured bimodal
// hit-age workload: identical hit rate at ~40% lower hit cost (a hit sets a
// ref bit instead of splicing a list), and unlike insertion-order eviction it
// never rotates out the permanently-hot write-cold set.
var (
	retentionClockCap = func() int {
		n, _ := strconv.Atoi(os.Getenv("BOR_RETENTION_CLOCK"))
		if n < 0 {
			return 0
		}
		return n
	}()
	clockAcctBucketCap = retentionClockCap / 8 / 16
	clockSlotBucketCap = retentionClockCap * 7 / 8 / 16
)

// clockMaxSweep bounds the second-chance scan per eviction so a fully-hot
// bucket cannot stall an insert under the bucket write lock; past the bound
// the next candidate is evicted regardless of its ref bit.
const clockMaxSweep = 64

// ContractCodeReader defines the interface for accessing contract code.
type ContractCodeReader interface {
	// Code retrieves a particular contract's code.
	//
	// - Returns nil code along with nil error if the requested contract code
	//   doesn't exist
	// - Returns an error only if an unexpected issue occurs
	Code(addr common.Address, codeHash common.Hash) ([]byte, error)

	// CodeSize retrieves a particular contracts code's size.
	//
	// - Returns zero code size along with nil error if the requested contract code
	//   doesn't exist
	// - Returns an error only if an unexpected issue occurs
	CodeSize(addr common.Address, codeHash common.Hash) (int, error)
}

// StateReader defines the interface for accessing accounts and storage slots
// associated with a specific state.
//
// StateReader is assumed to be thread-safe and implementation must take care
// of the concurrency issue by themselves.
type StateReader interface {
	// Account retrieves the account associated with a particular address.
	//
	// - Returns a nil account if it does not exist
	// - Returns an error only if an unexpected issue occurs
	// - The returned account is safe to modify after the call
	Account(addr common.Address) (*types.StateAccount, error)

	// Storage retrieves the storage slot associated with a particular account
	// address and slot key.
	//
	// - Returns an empty slot if it does not exist
	// - Returns an error only if an unexpected issue occurs
	// - The returned storage slot is safe to modify after the call
	Storage(addr common.Address, slot common.Hash) (common.Hash, error)
}

// Reader defines the interface for accessing accounts, storage slots and contract
// code associated with a specific state.
//
// Reader is assumed to be thread-safe and implementation must take care of the
// concurrency issue by themselves.
type Reader interface {
	ContractCodeReader
	StateReader
}

// ReaderStats wraps the statistics of reader.
type ReaderStats struct {
	AccountHit  int64
	AccountMiss int64
	StorageHit  int64
	StorageMiss int64
}

// PrefetchStats exposes additional attribution stats for evaluating prefetch effectiveness.
type PrefetchStats struct {
	// Hits in PROCESS that came from PREFETCH-origin entries.
	AccountHitFromPrefetch int64
	StorageHitFromPrefetch int64
	// Unique keys PREFETCH inserted into the shared local cache.
	AccountInsert int64
	StorageInsert int64
	// Unique prefetched account keys that PROCESS actually used.
	AccountHitFromPrefetchUnique int64
}

// ReaderWithStats wraps the additional method to retrieve the reader statistics from.
type ReaderWithStats interface {
	Reader
	GetStats() ReaderStats
	GetPrefetchStats() PrefetchStats
}

// cachingCodeReader implements ContractCodeReader, accessing contract code either in
// local key-value store or the shared code cache.
//
// cachingCodeReader is safe for concurrent access.
type cachingCodeReader struct {
	db ethdb.KeyValueReader

	// These caches could be shared by multiple code reader instances,
	// they are natively thread-safe.
	codeCache     *lru.SizeConstrainedCache[common.Hash, []byte]
	codeSizeCache *lru.Cache[common.Hash, int]
}

// newCachingCodeReader constructs the code reader.
func newCachingCodeReader(db ethdb.KeyValueReader, codeCache *lru.SizeConstrainedCache[common.Hash, []byte], codeSizeCache *lru.Cache[common.Hash, int]) *cachingCodeReader {
	return &cachingCodeReader{
		db:            db,
		codeCache:     codeCache,
		codeSizeCache: codeSizeCache,
	}
}

// Code implements ContractCodeReader, retrieving a particular contract's code.
// If the contract code doesn't exist, no error will be returned.
func (r *cachingCodeReader) Code(addr common.Address, codeHash common.Hash) ([]byte, error) {
	code, _, err := r.codeWithHit(codeHash)
	return code, err
}

// codeWithHit is Code plus a flag reporting whether the shared code LRU
// served the read (lab instrumentation support).
func (r *cachingCodeReader) codeWithHit(codeHash common.Hash) ([]byte, bool, error) {
	code, _ := r.codeCache.Get(codeHash)
	if len(code) > 0 {
		return code, true, nil
	}
	code = rawdb.ReadCode(r.db, codeHash)
	if len(code) > 0 {
		r.codeCache.Add(codeHash, code)
		r.codeSizeCache.Add(codeHash, len(code))
	}
	return code, false, nil
}

// CodeSize implements ContractCodeReader, retrieving a particular contracts code's size.
// If the contract code doesn't exist, no error will be returned.
func (r *cachingCodeReader) CodeSize(addr common.Address, codeHash common.Hash) (int, error) {
	if cached, ok := r.codeSizeCache.Get(codeHash); ok {
		return cached, nil
	}
	code, err := r.Code(addr, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

// flatReader wraps a database state reader and is safe for concurrent access.
type flatReader struct {
	reader database.StateReader
}

// newFlatReader constructs a state reader with on the given state root.
func newFlatReader(reader database.StateReader) *flatReader {
	return &flatReader{reader: reader}
}

// Account implements StateReader, retrieving the account specified by the address.
//
// An error will be returned if the associated snapshot is already stale or
// the requested account is not yet covered by the snapshot.
//
// The returned account might be nil if it's not existent.
func (r *flatReader) Account(addr common.Address) (*types.StateAccount, error) {
	account, err := r.reader.Account(crypto.Keccak256Hash(addr.Bytes()))
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, nil
	}
	acct := &types.StateAccount{
		Nonce:    account.Nonce,
		Balance:  account.Balance,
		CodeHash: account.CodeHash,
		Root:     common.BytesToHash(account.Root),
	}
	if len(acct.CodeHash) == 0 {
		acct.CodeHash = types.EmptyCodeHash.Bytes()
	}
	if acct.Root == (common.Hash{}) {
		acct.Root = types.EmptyRootHash
	}
	return acct, nil
}

// Storage implements StateReader, retrieving the storage slot specified by the
// address and slot key.
//
// An error will be returned if the associated snapshot is already stale or
// the requested storage slot is not yet covered by the snapshot.
//
// The returned storage slot might be empty if it's not existent.
func (r *flatReader) Storage(addr common.Address, key common.Hash) (common.Hash, error) {
	addrHash := crypto.Keccak256Hash(addr.Bytes())
	slotHash := crypto.Keccak256Hash(key.Bytes())
	ret, err := r.reader.Storage(addrHash, slotHash)
	if err != nil {
		return common.Hash{}, err
	}
	if len(ret) == 0 {
		return common.Hash{}, nil
	}
	// Perform the rlp-decode as the slot value is RLP-encoded in the state
	// snapshot.
	_, content, _, err := rlp.Split(ret)
	if err != nil {
		return common.Hash{}, err
	}
	var value common.Hash
	value.SetBytes(content)
	return value, nil
}

// trieReader implements the StateReader interface, providing functions to access
// state from the referenced trie.
//
// trieReader is safe for concurrent read.
type trieReader struct {
	root common.Hash      // State root which uniquely represent a state
	db   *triedb.Database // Database for loading trie

	// Main trie, resolved in constructor. Note either the Merkle-Patricia-tree
	// or Verkle-tree is not safe for concurrent read.
	mainTrie Trie

	subRoots   map[common.Address]common.Hash // Set of storage roots, cached when the account is resolved
	subTries   map[common.Address]Trie        // Group of storage tries, cached when it's resolved
	muSubRoot  sync.Mutex
	muSubTries sync.Mutex
	lock       sync.Mutex // Lock for protecting concurrent read
}

// newTrieReader constructs a trie reader of the specific state. An error will be
// returned if the associated trie specified by root is not existent.
func newTrieReader(root common.Hash, db *triedb.Database, cache *utils.PointCache) (*trieReader, error) {
	var (
		tr  Trie
		err error
	)
	if !db.IsVerkle() {
		tr, err = trie.NewStateTrie(trie.StateTrieID(root), db)
	} else {
		tr, err = trie.NewVerkleTrie(root, db, cache)

		// Based on the transition status, determine if the overlay
		// tree needs to be created, or if a single, target tree is
		// to be picked.
		ts := overlay.LoadTransitionState(db.Disk(), root, true)
		if ts.InTransition() {
			mpt, err := trie.NewStateTrie(trie.StateTrieID(ts.BaseRoot), db)
			if err != nil {
				return nil, err
			}
			tr = trie.NewTransitionTrie(mpt, tr.(*trie.VerkleTrie), false)
		}
	}
	if err != nil {
		return nil, err
	}
	return &trieReader{
		root:     root,
		db:       db,
		mainTrie: tr,
		subRoots: make(map[common.Address]common.Hash),
		subTries: make(map[common.Address]Trie),
	}, nil
}

// account is the inner version of Account and assumes the r.lock is already held.
func (r *trieReader) account(addr common.Address) (*types.StateAccount, error) {
	account, err := r.mainTrie.GetAccount(addr)
	if err != nil {
		return nil, err
	}
	r.muSubRoot.Lock()
	if account == nil {
		r.subRoots[addr] = types.EmptyRootHash
	} else {
		r.subRoots[addr] = account.Root
	}
	r.muSubRoot.Unlock()

	return account, nil
}

// Account implements StateReader, retrieving the account specified by the address.
//
// An error will be returned if the trie state is corrupted. An nil account
// will be returned if it's not existent in the trie.
func (r *trieReader) Account(addr common.Address) (*types.StateAccount, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	return r.account(addr)
}

// Storage implements StateReader, retrieving the storage slot specified by the
// address and slot key.
//
// An error will be returned if the trie state is corrupted. An empty storage
// slot will be returned if it's not existent in the trie.
func (r *trieReader) Storage(addr common.Address, key common.Hash) (common.Hash, error) {
	r.lock.Lock()
	defer r.lock.Unlock()

	var (
		tr    Trie
		found bool
		value common.Hash
	)
	if r.db.IsVerkle() {
		tr = r.mainTrie
	} else {
		tr, found = r.subTries[addr]
		if !found {
			root, ok := r.subRoots[addr]

			// The storage slot is accessed without account caching. It's unexpected
			// behavior but try to resolve the account first anyway.
			if !ok {
				_, err := r.account(addr)
				if err != nil {
					return common.Hash{}, err
				}
				root = r.subRoots[addr]
			}
			var err error
			tr, err = trie.NewStateTrie(trie.StorageTrieID(r.root, crypto.Keccak256Hash(addr.Bytes()), root), r.db)
			if err != nil {
				return common.Hash{}, err
			}
			r.muSubTries.Lock()
			r.subTries[addr] = tr
			r.muSubTries.Unlock()
		}
	}
	ret, err := tr.GetStorage(addr, key.Bytes())
	if err != nil {
		return common.Hash{}, err
	}
	value.SetBytes(ret)
	return value, nil
}

// multiStateReader is the aggregation of a list of StateReader interface,
// providing state access by leveraging all readers. The checking priority
// is determined by the position in the reader list.
//
// multiStateReader is safe for concurrent read and assumes all underlying
// readers are thread-safe as well.
type multiStateReader struct {
	readers []StateReader // List of state readers, sorted by checking priority
}

// newMultiStateReader constructs a multiStateReader instance with the given
// readers. The priority among readers is assumed to be sorted. Note, it must
// contain at least one reader for constructing a multiStateReader.
func newMultiStateReader(readers ...StateReader) (*multiStateReader, error) {
	if len(readers) == 0 {
		return nil, errors.New("empty reader set")
	}
	return &multiStateReader{
		readers: readers,
	}, nil
}

// Account implementing StateReader interface, retrieving the account associated
// with a particular address.
//
// - Returns a nil account if it does not exist
// - Returns an error only if an unexpected issue occurs
// - The returned account is safe to modify after the call
func (r *multiStateReader) Account(addr common.Address) (*types.StateAccount, error) {
	var errs []error
	for _, reader := range r.readers {
		acct, err := reader.Account(addr)
		if err == nil {
			return acct, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// Storage implementing StateReader interface, retrieving the storage slot
// associated with a particular account address and slot key.
//
// - Returns an empty slot if it does not exist
// - Returns an error only if an unexpected issue occurs
// - The returned storage slot is safe to modify after the call
func (r *multiStateReader) Storage(addr common.Address, slot common.Hash) (common.Hash, error) {
	var errs []error
	for _, reader := range r.readers {
		slot, err := reader.Storage(addr, slot)
		if err == nil {
			return slot, nil
		}
		errs = append(errs, err)
	}
	return common.Hash{}, errors.Join(errs...)
}

// reader is the wrapper of ContractCodeReader and StateReader interface.
type reader struct {
	ContractCodeReader
	StateReader
}

// newReader constructs a reader with the supplied code reader and state reader.
func newReader(codeReader ContractCodeReader, stateReader StateReader) *reader {
	return &reader{
		ContractCodeReader: codeReader,
		StateReader:        stateReader,
	}
}

// readerRole identifies the "writer" responsible for warming the shared local cache.
// It is used purely for attribution in metrics (prefetch vs process).
type readerRole uint8

const (
	roleUnknown  readerRole = 0
	rolePrefetch readerRole = 1
	roleProcess  readerRole = 2
)

// accountCacheEntry is the cached account plus attribution metadata.
type accountCacheEntry struct {
	acct *types.StateAccount
	// origin is who first inserted this entry into the local cache (prefetch/process).
	origin readerRole
	// usedByProcess is flipped exactly once when the PROCESS reader consumes an entry
	// that was prefetched. Used to compute unique-usage/precision.
	usedByProcess uint32
	// gen is the retention generation at insertion time; hit age = current gen - gen.
	gen uint32
	// ref is the CLOCK reference bit (atomic): set on every cache hit, cleared
	// by the eviction sweep. An entry with ref=1 gets a second chance.
	ref uint32
}

// storageCacheEntry is the cached storage slot plus attribution metadata.
// Stored by pointer so cache hits can set the CLOCK ref bit without holding
// the bucket write lock.
type storageCacheEntry struct {
	value  common.Hash
	origin readerRole
	// gen is the retention generation at insertion time; hit age = current gen - gen.
	gen uint32
	// ref is the CLOCK reference bit (atomic): set on every cache hit, cleared
	// by the eviction sweep. An entry with ref=1 gets a second chance.
	ref uint32
}

// storageRingKey addresses one cached slot in a storage bucket's CLOCK ring.
type storageRingKey struct {
	addr common.Address
	slot common.Hash
}

// readerWithCache is a wrapper around Reader that maintains additional state caches
// to support concurrent state access.
type readerWithCache struct {
	Reader // safe for concurrent read; also the initial backing

	// backing, when set, overrides the embedded Reader for account/storage
	// miss resolution. Retention advances it to a reader at the newly
	// committed root while the cached entries are carried across blocks
	// (entries for keys the block wrote are invalidated first, so every
	// surviving entry still holds the correct value at the new root).
	// Contract-code reads stay on the embedded Reader: code is keyed by
	// hash and root-independent.
	backing atomic.Pointer[Reader]

	// gen guards in-flight inserts across an advance: a fetch that started
	// against the previous backing must not populate the cache after the
	// invalidation pass ran, or a key written by the just-committed block
	// could resurface with its stale pre-block value.
	gen atomic.Uint64

	// Approximate entry counts, used to bound retained memory.
	accountCount atomic.Int64
	storageCount atomic.Int64

	// List of account buckets, each of which is thread-safe. Sharded like
	// storageBuckets: a single map+RWMutex serializes the prefetch workers
	// and the exec goroutine on one cache line under tip-cadence load.
	//
	// ring/head implement CLOCK (second-chance FIFO) eviction when
	// retentionClockCap is set: inserts append their key; when the bucket is
	// over cap the sweep pops from head, sparing (and re-appending) entries
	// whose ref bit was set by a hit since the last pass. Keys invalidated by
	// advance() linger in the ring and are skipped (and freed) on sweep.
	accountBuckets [16]accountBucket

	// List of storage buckets, each of which is thread-safe.
	// This reader is typically used in scenarios requiring concurrent
	// access to storage. Using multiple buckets helps mitigate
	// the overhead caused by locking.
	storageBuckets [16]storageBucket
}

// accountBucket is one shard of the account cache plus its CLOCK ring.
type accountBucket struct {
	lock     sync.RWMutex
	accounts map[common.Address]*accountCacheEntry
	ring     []common.Address
	head     int
}

// storageBucket is one shard of the storage cache plus its CLOCK ring.
// count tracks the bucket's total slots (nested maps make len() a walk).
type storageBucket struct {
	lock     sync.RWMutex
	storages map[common.Address]map[common.Hash]*storageCacheEntry
	count    int
	ring     []storageRingKey
	head     int
}

// newReaderWithCache constructs the reader with local cache.
func newReaderWithCache(reader Reader) *readerWithCache {
	r := &readerWithCache{
		Reader: reader,
	}
	for i := range r.accountBuckets {
		r.accountBuckets[i].accounts = make(map[common.Address]*accountCacheEntry)
	}
	for i := range r.storageBuckets {
		r.storageBuckets[i].storages = make(map[common.Address]map[common.Hash]*storageCacheEntry)
	}
	return r
}

// account retrieves the account specified by the address along with a flag
// indicating whether it's found in the cache or not. The returned account
// might be nil if it's not existent.
//
// An error will be returned if the state is corrupted in the underlying reader.
//
// It also returns the cache entry (for provenance/unique-usage accounting)
// and whether this call inserted a new entry (first-writer-wins).
func (r *readerWithCache) account(addr common.Address, caller readerRole) (*types.StateAccount, bool, *accountCacheEntry, bool, error) {
	bucket := &r.accountBuckets[addr[0]&0x0f]

	// Try to resolve the requested account in the local cache
	bucket.lock.RLock()
	ent, ok := bucket.accounts[addr]
	bucket.lock.RUnlock()
	if ok {
		atomic.StoreUint32(&ent.ref, 1)
		if acctHitAgeSampleCtr.Add(1)&63 == 0 {
			retentionAcctHitAgeHist.Update(int64(uint32(r.gen.Load()) - ent.gen))
		}
		return ent.acct, true, ent, false, nil
	}
	// Try to resolve the requested account from the underlying reader
	gen := r.gen.Load()
	acct, err := r.currentBacking().Account(addr)
	if err != nil {
		return nil, false, nil, false, err
	}
	bucket.lock.Lock()
	// First-writer-wins: avoid clobbering if another goroutine inserted meanwhile.
	if existing, ok := bucket.accounts[addr]; ok {
		bucket.lock.Unlock()
		// This was a MISS originally (we didn't find it under RLock),
		// but another goroutine inserted it while we fetched from the backing reader.
		// Report incache=false so miss counters reflect backing-read cost.
		return existing.acct, false, existing, false, nil
	}
	if r.gen.Load() != gen {
		// The cache advanced to a new root while we fetched; the value may be
		// stale for the new generation, so serve it without caching.
		bucket.lock.Unlock()
		return acct, false, &accountCacheEntry{acct: acct, origin: caller}, false, nil
	}
	newEnt := &accountCacheEntry{acct: acct, origin: caller, gen: uint32(gen)}
	bucket.accounts[addr] = newEnt
	if clockAcctBucketCap > 0 {
		bucket.ring = append(bucket.ring, addr)
		r.evictAccountsClock(bucket)
	}
	bucket.lock.Unlock()
	r.accountCount.Add(1)
	return acct, false, newEnt, true, nil
}

// evictAccountsClock brings the bucket back under its CLOCK cap. Caller holds
// the bucket write lock. Entries whose ref bit was set since the last sweep
// are spared once (bit cleared, key re-appended); after clockMaxSweep spares
// the next candidate is evicted unconditionally so the sweep stays bounded.
func (r *readerWithCache) evictAccountsClock(bucket *accountBucket) {
	spared := 0
	for len(bucket.accounts) > clockAcctBucketCap && bucket.head < len(bucket.ring) {
		addr := bucket.ring[bucket.head]
		bucket.head++
		ent, ok := bucket.accounts[addr]
		if !ok {
			continue // stale ring slot: key invalidated by advance()
		}
		if spared < clockMaxSweep && atomic.SwapUint32(&ent.ref, 0) == 1 {
			bucket.ring = append(bucket.ring, addr)
			spared++
			retentionSecondChanceMeter.Mark(1)
			continue
		}
		delete(bucket.accounts, addr)
		r.accountCount.Add(-1)
		retentionEvictedMeter.Mark(1)
	}
	// Compact the consumed prefix once it dominates the ring.
	if bucket.head > 4096 && bucket.head > len(bucket.ring)/2 {
		bucket.ring = append(bucket.ring[:0:0], bucket.ring[bucket.head:]...)
		bucket.head = 0
	}
}

// Account implements StateReader, retrieving the account specified by the address.
// The returned account might be nil if it's not existent.
//
// An error will be returned if the state is corrupted in the underlying reader.
func (r *readerWithCache) Account(addr common.Address) (*types.StateAccount, error) {
	account, _, _, _, err := r.account(addr, roleUnknown)
	return account, err
}

// currentBacking returns the reader misses resolve against: the retention
// backing when the cache has been advanced past a commit, the construction
// reader otherwise.
func (r *readerWithCache) currentBacking() Reader {
	if p := r.backing.Load(); p != nil {
		return *p
	}
	return r.Reader
}

// sharedReaderCache exposes the cache to the commit-time retention hook; the
// method is promoted through the stats wrappers handed to StateDB.
func (r *readerWithCache) sharedReaderCache() *readerWithCache {
	return r
}

// Retention caps: past these the retained cache is dropped wholesale rather
// than trimmed, keeping the worst case simple and the memory bounded.
const (
	retentionMaxAccounts = 512 * 1024
	retentionMaxSlots    = 4 * 1024 * 1024
)

// advance carries the cache across a block commit: it swaps miss resolution
// to a reader at the newly committed root and evicts every key the block
// wrote. Any surviving entry holds the same value at the new root as at the
// root it was read from, so serving it stays correct. Safe to run while
// straggling prefetch workers still use the cache: the generation bump makes
// their in-flight inserts no-ops, and completed stale inserts are covered by
// the eviction pass below.
func (r *readerWithCache) advance(backing Reader, upd *stateUpdate) {
	r.gen.Add(1)
	r.backing.Store(&backing)

	invalidated := int64(0)
	for addr := range upd.accountsOrigin {
		bucket := &r.accountBuckets[addr[0]&0x0f]
		bucket.lock.Lock()
		if _, ok := bucket.accounts[addr]; ok {
			delete(bucket.accounts, addr)
			r.accountCount.Add(-1)
			invalidated++
		}
		bucket.lock.Unlock()

		// A deleted account implicitly wipes its storage; deletion shows as a
		// nil slim-RLP payload under the account hash.
		if data, ok := upd.accounts[crypto.Keccak256Hash(addr.Bytes())]; ok && len(data) == 0 {
			invalidated += r.dropStorage(addr)
		}
	}
	for addr, slots := range upd.storagesOrigin {
		if !upd.rawStorageKey {
			// Slot keys are hashed and cannot be matched against the
			// plain-keyed cache; drop the whole per-account map.
			invalidated += r.dropStorage(addr)
			continue
		}
		bucket := &r.storageBuckets[addr[0]&0x0f]
		bucket.lock.Lock()
		if cached, ok := bucket.storages[addr]; ok {
			for key := range slots {
				if _, ok := cached[key]; ok {
					delete(cached, key)
					bucket.count--
					r.storageCount.Add(-1)
					invalidated++
				}
			}
		}
		bucket.lock.Unlock()
	}
	retentionInvalidatedMeter.Mark(invalidated)

	// Bound retained memory: reset wholesale when over cap. The counters are
	// approximate (straggler inserts may race the reset); drift is harmless
	// because the caps are soft limits.
	if r.accountCount.Load() > retentionMaxAccounts || r.storageCount.Load() > retentionMaxSlots {
		for i := range r.accountBuckets {
			bucket := &r.accountBuckets[i]
			bucket.lock.Lock()
			bucket.accounts = make(map[common.Address]*accountCacheEntry)
			bucket.ring, bucket.head = nil, 0
			bucket.lock.Unlock()
		}
		for i := range r.storageBuckets {
			bucket := &r.storageBuckets[i]
			bucket.lock.Lock()
			bucket.storages = make(map[common.Address]map[common.Hash]*storageCacheEntry)
			bucket.count = 0
			bucket.ring, bucket.head = nil, 0
			bucket.lock.Unlock()
		}
		r.accountCount.Store(0)
		r.storageCount.Store(0)
		retentionResetMeter.Mark(1)
	}

	retentionAcctSizeGauge.Update(r.accountCount.Load())
	retentionStorSizeGauge.Update(r.storageCount.Load())
	if retentionClockCap > 0 {
		ringLen := 0
		for i := range r.accountBuckets {
			bucket := &r.accountBuckets[i]
			bucket.lock.RLock()
			ringLen += len(bucket.ring) - bucket.head
			bucket.lock.RUnlock()
		}
		for i := range r.storageBuckets {
			bucket := &r.storageBuckets[i]
			bucket.lock.RLock()
			ringLen += len(bucket.ring) - bucket.head
			bucket.lock.RUnlock()
		}
		retentionRingLenGauge.Update(int64(ringLen))
	}
}

// dropStorage removes every cached slot of one account, returning the count.
func (r *readerWithCache) dropStorage(addr common.Address) int64 {
	bucket := &r.storageBuckets[addr[0]&0x0f]
	bucket.lock.Lock()
	dropped := int64(len(bucket.storages[addr]))
	delete(bucket.storages, addr)
	bucket.count -= int(dropped)
	bucket.lock.Unlock()
	r.storageCount.Add(-dropped)
	return dropped
}

// storage retrieves the storage slot specified by the address and slot key, along
// with a flag indicating whether it's found in the cache or not. The returned
// storage slot might be empty if it's not existent.
//
// It also returns the cache entry (for provenance/unique-usage accounting)
// and whether this call inserted a new entry (first-writer-wins).
func (r *readerWithCache) storage(addr common.Address, slot common.Hash, caller readerRole) (common.Hash, bool, *storageCacheEntry, bool, error) {
	var (
		ok     bool
		bucket = &r.storageBuckets[addr[0]&0x0f]
	)
	// Try to resolve the requested storage slot in the local cache
	bucket.lock.RLock()
	slots, ok := bucket.storages[addr]
	if ok {
		ent, ok := slots[slot]
		if ok {
			bucket.lock.RUnlock()
			atomic.StoreUint32(&ent.ref, 1)
			if storHitAgeSampleCtr.Add(1)&63 == 0 {
				retentionStorHitAgeHist.Update(int64(uint32(r.gen.Load()) - ent.gen))
			}
			return ent.value, true, ent, false, nil
		}
	}
	bucket.lock.RUnlock()

	// Try to resolve the requested storage slot from the underlying reader
	gen := r.gen.Load()
	value, err := r.currentBacking().Storage(addr, slot)
	if err != nil {
		return common.Hash{}, false, nil, false, err
	}

	bucket.lock.Lock()
	slots, ok = bucket.storages[addr]
	if !ok {
		slots = make(map[common.Hash]*storageCacheEntry)
		bucket.storages[addr] = slots
	}
	// First-writer-wins: avoid clobbering if another goroutine inserted meanwhile.
	if existing, ok := slots[slot]; ok {
		bucket.lock.Unlock()
		// This was a MISS originally (we didn't find it under RLock),
		// but another goroutine inserted it while we fetched from the backing reader.
		// Report incache=false so miss counters reflect backing-read cost.
		return existing.value, false, existing, false, nil
	}
	newEnt := &storageCacheEntry{value: value, origin: caller, gen: uint32(gen)}
	if r.gen.Load() != gen {
		// The cache advanced to a new root while we fetched; the value may be
		// stale for the new generation, so serve it without caching.
		bucket.lock.Unlock()
		return value, false, newEnt, false, nil
	}
	slots[slot] = newEnt
	bucket.count++
	if clockSlotBucketCap > 0 {
		bucket.ring = append(bucket.ring, storageRingKey{addr: addr, slot: slot})
		r.evictStorageClock(bucket)
	}
	bucket.lock.Unlock()
	r.storageCount.Add(1)

	return value, false, newEnt, true, nil
}

// evictStorageClock brings the bucket back under its CLOCK cap. Caller holds
// the bucket write lock. Same second-chance sweep as evictAccountsClock.
func (r *readerWithCache) evictStorageClock(bucket *storageBucket) {
	spared := 0
	for bucket.count > clockSlotBucketCap && bucket.head < len(bucket.ring) {
		key := bucket.ring[bucket.head]
		bucket.head++
		slots, ok := bucket.storages[key.addr]
		if !ok {
			continue // stale ring slot: account dropped by advance()
		}
		ent, ok := slots[key.slot]
		if !ok {
			continue // stale ring slot: slot invalidated by advance()
		}
		if spared < clockMaxSweep && atomic.SwapUint32(&ent.ref, 0) == 1 {
			bucket.ring = append(bucket.ring, key)
			spared++
			retentionSecondChanceMeter.Mark(1)
			continue
		}
		delete(slots, key.slot)
		if len(slots) == 0 {
			delete(bucket.storages, key.addr)
		}
		bucket.count--
		r.storageCount.Add(-1)
		retentionEvictedMeter.Mark(1)
	}
	if bucket.head > 4096 && bucket.head > len(bucket.ring)/2 {
		bucket.ring = append(bucket.ring[:0:0], bucket.ring[bucket.head:]...)
		bucket.head = 0
	}
}

// Storage implements StateReader, retrieving the storage slot specified by the
// address and slot key. The returned storage slot might be empty if it's not
// existent.
//
// An error will be returned if the state is corrupted in the underlying reader.
func (r *readerWithCache) Storage(addr common.Address, slot common.Hash) (common.Hash, error) {
	value, _, _, _, err := r.storage(addr, slot, roleUnknown)
	return value, err
}

type readerWithCacheStats struct {
	*readerWithCache
	role readerRole

	accountHit  atomic.Int64
	accountMiss atomic.Int64
	storageHit  atomic.Int64
	storageMiss atomic.Int64

	// attribute PROCESS hits that were served by PREFETCH-origin entries.
	accountHitFromPrefetch atomic.Int64
	storageHitFromPrefetch atomic.Int64

	// count unique inserts by PREFETCH (how much it warmed).
	accountInsert atomic.Int64
	storageInsert atomic.Int64

	// count unique prefetched keys that PROCESS actually used (precision) for accounts only.
	accountHitFromPrefetchUnique atomic.Int64

	// Optional lab instrumentation: when set, reads are timed and misses logged.
	detail atomic.Pointer[ReadDetail]
}

// newReaderWithCacheStats constructs the reader with additional statistics tracked.
func newReaderWithCacheStats(reader *readerWithCache, role readerRole) *readerWithCacheStats {
	return &readerWithCacheStats{
		readerWithCache: reader,
		role:            role,
	}
}

// Account implements StateReader, retrieving the account specified by the address.
// The returned account might be nil if it's not existent.
//
// An error will be returned if the state is corrupted in the underlying reader.
func (r *readerWithCacheStats) Account(addr common.Address) (*types.StateAccount, error) {
	var start time.Time
	detail := r.detail.Load()
	if detail != nil {
		start = time.Now()
	}
	account, incache, ent, inserted, err := r.readerWithCache.account(addr, r.role)
	if err != nil {
		return nil, err
	}
	if detail != nil {
		detail.recordAccount(addr, incache, time.Since(start))
	}
	if incache {
		r.accountHit.Add(1)
		// Attribute hits in PROCESS that came from PREFETCH-origin entries.
		if r.role == roleProcess && ent != nil && ent.origin == rolePrefetch {
			r.accountHitFromPrefetch.Add(1)
			// Flip usedByProcess only once per entry.
			if atomic.CompareAndSwapUint32(&ent.usedByProcess, 0, 1) {
				r.accountHitFromPrefetchUnique.Add(1)
			}
		}
	} else {
		r.accountMiss.Add(1)
		// Count unique inserts done by PREFETCH (first-writer-wins).
		if r.role == rolePrefetch && inserted {
			r.accountInsert.Add(1)
		}
	}
	return account, nil
}

// Storage implements StateReader, retrieving the storage slot specified by the
// address and slot key. The returned storage slot might be empty if it's not
// existent.
//
// An error will be returned if the state is corrupted in the underlying reader.
func (r *readerWithCacheStats) Storage(addr common.Address, slot common.Hash) (common.Hash, error) {
	var start time.Time
	detail := r.detail.Load()
	if detail != nil {
		start = time.Now()
	}
	value, incache, entCopy, inserted, err := r.readerWithCache.storage(addr, slot, r.role)
	if err != nil {
		return common.Hash{}, err
	}
	if detail != nil {
		detail.recordStorage(addr, slot, incache, time.Since(start))
	}
	if incache {
		r.storageHit.Add(1)
		// Attribute hits in PROCESS that came from PREFETCH-origin entries.
		// NOTE: No write-lock marking (Option C). We only track hit attribution.
		if r.role == roleProcess && entCopy != nil && entCopy.origin == rolePrefetch {
			r.storageHitFromPrefetch.Add(1)
		}
	} else {
		r.storageMiss.Add(1)
		// Count unique inserts done by PREFETCH (first-writer-wins).
		// This comes "for free" on the miss/insert path (no extra locking).
		if r.role == rolePrefetch && inserted {
			r.storageInsert.Add(1)
		}
	}
	return value, nil
}

// GetStats implements ReaderWithStats, returning the statistics of state reader.
func (r *readerWithCacheStats) GetStats() ReaderStats {
	return ReaderStats{
		AccountHit:  r.accountHit.Load(),
		AccountMiss: r.accountMiss.Load(),
		StorageHit:  r.storageHit.Load(),
		StorageMiss: r.storageMiss.Load(),
	}
}

// GetPrefetchStats returns attribution statistics for evaluating prefetch effectiveness.
func (r *readerWithCacheStats) GetPrefetchStats() PrefetchStats {
	return PrefetchStats{
		AccountHitFromPrefetch:       r.accountHitFromPrefetch.Load(),
		StorageHitFromPrefetch:       r.storageHitFromPrefetch.Load(),
		AccountInsert:                r.accountInsert.Load(),
		StorageInsert:                r.storageInsert.Load(),
		AccountHitFromPrefetchUnique: r.accountHitFromPrefetchUnique.Load(),
	}
}
