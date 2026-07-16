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
	"sync"
	"sync/atomic"

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
	code, _ := r.codeCache.Get(codeHash)
	if len(code) > 0 {
		return code, nil
	}
	code = rawdb.ReadCode(r.db, codeHash)
	if len(code) > 0 {
		r.codeCache.Add(codeHash, code)
		r.codeSizeCache.Add(codeHash, len(code))
	}
	return code, nil
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
	reader    database.StateReader
	addrCache sync.Map // common.Address → common.Hash (keccak256 cache)
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
	var addrHash common.Hash
	if v, ok := r.addrCache.Load(addr); ok {
		addrHash = v.(common.Hash)
	} else {
		addrHash = crypto.Keccak256Hash(addr.Bytes())
		r.addrCache.Store(addr, addrHash)
	}
	account, err := r.reader.Account(addrHash)
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
	var addrHash common.Hash
	if v, ok := r.addrCache.Load(addr); ok {
		addrHash = v.(common.Hash)
	} else {
		addrHash = crypto.Keccak256Hash(addr.Bytes())
		r.addrCache.Store(addr, addrHash)
	}
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

	subRoots          sync.Map   // common.Address → common.Hash (storage roots)
	subTries          sync.Map   // common.Address → Trie (storage tries)
	lock              sync.Mutex // Lock for protecting concurrent read
	accountCache      sync.Map   // addr → *types.StateAccount (concurrent-safe cache)
	storageCache      sync.Map   // addrSlot → common.Hash (concurrent-safe cache)
	concurrentEnabled bool       // if true, skip r.lock (trie uses sync.Map resolve cache)
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
	}, nil
}

// account is the inner version of Account and assumes the r.lock is already held.
func (r *trieReader) account(addr common.Address) (*types.StateAccount, error) {
	account, err := r.mainTrie.GetAccount(addr)
	if err != nil {
		return nil, err
	}
	if account == nil {
		r.subRoots.Store(addr, types.EmptyRootHash)
	} else {
		r.subRoots.Store(addr, account.Root)
	}
	return account, nil
}

// Account implements StateReader, retrieving the account specified by the address.
//
// An error will be returned if the trie state is corrupted. An nil account
// will be returned if it's not existent in the trie.
func (r *trieReader) Account(addr common.Address) (*types.StateAccount, error) {
	// Fast path: check concurrent-safe cache before acquiring lock.
	if cached, ok := r.accountCache.Load(addr); ok {
		return cached.(*types.StateAccount), nil
	}

	if r.concurrentEnabled {
		// Trie has sync.Map resolve cache — no lock needed.
		acct, err := r.account(addr)
		if err == nil {
			r.accountCache.Store(addr, acct)
		}
		return acct, err
	}

	r.lock.Lock()
	acct, err := r.account(addr)
	r.lock.Unlock()

	if err == nil {
		r.accountCache.Store(addr, acct)
	}
	return acct, err
}

// EnableConcurrentReads enables concurrent trie reads by switching the
// trie to use a sync.Map resolve cache instead of in-place mutation.
// After calling this, Account() and Storage() can be called concurrently
// without the r.lock (using only the sync.Map caches).
func (r *trieReader) EnableConcurrentReads() {
	if st, ok := r.mainTrie.(*trie.StateTrie); ok {
		st.EnableConcurrentReads()
	}
	r.concurrentEnabled = true
}

// CollectStateWitness adds every trie node that has been read through this
// reader into the supplied witness. Called by V2 BlockSTM at end-of-block
// because finalDB.IntermediateRoot's per-stateObject witness loop only
// covers addresses present in finalDB.stateObjects — addresses that were
// only READ (never written) by V2 workers don't reach finalDB at all.
//
// The reader's mainTrie and per-address sub-tries are SHARED with all
// pool copies and finalDB (StateDB.Copy() shares reader by reference), so
// every worker read accumulates in the same set of trie tracers. Walking
// reader.subTries here picks up exactly the worker-only-read tries that
// finalDB doesn't know about.
func (r *trieReader) CollectStateWitness(addState func(map[string][]byte)) {
	if r.mainTrie != nil {
		addState(r.mainTrie.Witness())
	}
	r.subTries.Range(func(_, v any) bool {
		if t, ok := v.(interface{ Witness() map[string][]byte }); ok {
			addState(t.Witness())
		}
		return true
	})
}

// Storage implements StateReader, retrieving the storage slot specified by the
// address and slot key.
//
// An error will be returned if the trie state is corrupted. An empty storage
// slot will be returned if it's not existent in the trie.
// storageCacheKey is a composite key for the trieReader storage cache.
// Uses the same layout as stateKey so the maps are compatible.
type storageCacheKey = stateKey

func (r *trieReader) Storage(addr common.Address, key common.Hash) (common.Hash, error) {
	cacheKey := storageCacheKey{addr, key}
	if cached, ok := r.storageCache.Load(cacheKey); ok {
		return cached.(common.Hash), nil
	}
	tr, err := r.subTrieFor(addr)
	if err != nil {
		return common.Hash{}, err
	}
	ret, err := tr.GetStorage(addr, key.Bytes())
	if err != nil {
		return common.Hash{}, err
	}
	var value common.Hash
	value.SetBytes(ret)
	r.storageCache.Store(cacheKey, value)
	return value, nil
}

// subTrieFor returns a Trie suitable for reading storage of addr. It
// dispatches to the concurrent or locked path based on configuration.
func (r *trieReader) subTrieFor(addr common.Address) (Trie, error) {
	if r.concurrentEnabled {
		return r.subTrieConcurrent(addr)
	}
	r.lock.Lock()
	defer r.lock.Unlock()
	return r.subTrieLocked(addr)
}

// subTrieConcurrent is the V2 lock-free path: uses sync.Map for the trie
// cache and EnableConcurrentReads() on freshly opened tries so multiple
// goroutines can read different addresses without cross-address contention.
func (r *trieReader) subTrieConcurrent(addr common.Address) (Trie, error) {
	if v, ok := r.subTries.Load(addr); ok {
		return v.(Trie), nil
	}
	root, err := r.resolveSubRoot(addr)
	if err != nil {
		return nil, err
	}
	newTr, err := trie.NewStateTrie(trie.StorageTrieID(r.root, crypto.Keccak256Hash(addr.Bytes()), root), r.db)
	if err != nil {
		return nil, err
	}
	newTr.EnableConcurrentReads()
	// First-writer-wins: another goroutine may have created it.
	if existing, loaded := r.subTries.LoadOrStore(addr, newTr); loaded {
		return existing.(Trie), nil
	}
	return newTr, nil
}

// subTrieLocked is the legacy mutex-protected path. Verkle uses the
// merged main trie; MPT uses per-address sub tries cached in subTries.
func (r *trieReader) subTrieLocked(addr common.Address) (Trie, error) {
	if r.db.IsVerkle() {
		return r.mainTrie, nil
	}
	if v, ok := r.subTries.Load(addr); ok {
		return v.(Trie), nil
	}
	root, err := r.resolveSubRoot(addr)
	if err != nil {
		return nil, err
	}
	tr, err := trie.NewStateTrie(trie.StorageTrieID(r.root, crypto.Keccak256Hash(addr.Bytes()), root), r.db)
	if err != nil {
		return nil, err
	}
	r.subTries.Store(addr, tr)
	return tr, nil
}

// resolveSubRoot returns the storage root for addr, resolving the account
// first if necessary so the cache is populated.
func (r *trieReader) resolveSubRoot(addr common.Address) (common.Hash, error) {
	if v, ok := r.subRoots.Load(addr); ok {
		return v.(common.Hash), nil
	}
	if _, err := r.account(addr); err != nil {
		return common.Hash{}, err
	}
	if v, ok := r.subRoots.Load(addr); ok {
		return v.(common.Hash), nil
	}
	return common.Hash{}, nil
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
}

// storageCacheEntry is the cached storage slot plus attribution metadata.
type storageCacheEntry struct {
	value  common.Hash
	origin readerRole
}

// storageKey is the composite key for the readerWithCache storage sync.Map.
// Uses the same layout as stateKey so SafeBase can share the map.
type storageKey = stateKey

// Reader-cache retention metrics (BOR_READER_RETENTION=1).
var (
	retentionServeMeter       = metrics.NewRegisteredMeter("state/retention/serve", nil)
	retentionInvalidatedMeter = metrics.NewRegisteredMeter("state/retention/invalidated", nil)
	retentionResetMeter       = metrics.NewRegisteredMeter("state/retention/reset", nil)
)

// Retention caps: past these the retained cache is dropped wholesale rather than
// trimmed, keeping the worst case simple and the memory bounded. These are the
// primary memory-vs-hit-rate tuning knob for the on-node A/B (see the W5 design
// doc); start at the campaign's 512k/4M and cut toward ~1M slots as measured.
const (
	retentionMaxAccounts = 512 * 1024
	retentionMaxSlots    = 4 * 1024 * 1024
)

// readerWithCache is a wrapper around Reader that maintains additional state caches
// to support concurrent state access. Both caches use sync.Map for lock-free reads —
// the dominant access pattern is many concurrent readers with infrequent first-writes.
type readerWithCache struct {
	Reader // safe for concurrent read; also the initial miss-resolution backing

	// Previously resolved account entries. Key: common.Address, Value: *accountCacheEntry.
	accounts sync.Map

	// Previously resolved storage entries. Key: storageKey, Value: *storageCacheEntry.
	storageCache sync.Map

	// --- Reader-cache retention (BOR_READER_RETENTION=1) ---
	// Retention advances this cache past a block commit and serves it to the next
	// block at the committed root. The fields below are inert unless retention is
	// enabled (the miss path takes the original develop branch when it is off).

	// backing, when set, overrides the embedded Reader for account/storage miss
	// resolution. advance() swaps it to a reader at the newly committed root while
	// the entries for keys the block wrote are evicted, so every surviving entry
	// still holds the correct value at the new root. Contract-code reads stay on
	// the embedded Reader (code is keyed by hash and root-independent).
	backing atomic.Pointer[Reader]

	// gen guards in-flight inserts across an advance: a fetch that started against
	// the previous backing must not populate the cache after the invalidation pass
	// ran, or a key written by the just-committed block could resurface with its
	// stale pre-block value. See storeAccountGuarded / advance and the W5 design.
	gen atomic.Uint64

	// advanceMu makes a miss-insert's [gen-recheck + LoadOrStore] atomic with
	// respect to advance()'s eviction pass. Inserts take RLock (concurrent among
	// themselves, never held across the slow backing fetch); advance takes Lock.
	// Hits never touch it — the lock-free hit path is preserved.
	advanceMu sync.RWMutex

	// Approximate entry counts, used to bound retained memory. Incremented on a
	// successful insert, decremented on eviction; drift under stragglers is
	// harmless because the caps are soft limits.
	accountCount atomic.Int64
	storageCount atomic.Int64
}

// newReaderWithCache constructs the reader with local cache.
func newReaderWithCache(reader Reader) *readerWithCache {
	return &readerWithCache{
		Reader: reader,
	}
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
// method is promoted through the readerWithCacheStats wrapper handed to StateDB.
func (r *readerWithCache) sharedReaderCache() *readerWithCache {
	return r
}

// storeAccountGuarded stores newEnt for addr iff the cache has not advanced since
// gen0 was sampled (the caller sampled it before the backing fetch). It returns
// the entry now in the cache and whether this call inserted it. The advanceMu
// RLock makes the gen-recheck and the map write atomic with respect to advance()'s
// eviction, so a stale in-flight insert of a written key can never survive an
// advance (advance either has not run — recheck passes, entry later evicted — or
// has run — recheck fails, store skipped).
func (r *readerWithCache) storeAccountGuarded(addr common.Address, newEnt *accountCacheEntry, gen0 uint64) (*accountCacheEntry, bool) {
	r.advanceMu.RLock()
	defer r.advanceMu.RUnlock()
	if r.gen.Load() != gen0 {
		return newEnt, false
	}
	if existing, loaded := r.accounts.LoadOrStore(addr, newEnt); loaded {
		return existing.(*accountCacheEntry), false
	}
	r.accountCount.Add(1)
	return newEnt, true
}

// storeStorageGuarded is the storage counterpart of storeAccountGuarded.
func (r *readerWithCache) storeStorageGuarded(key storageKey, newEnt *storageCacheEntry, gen0 uint64) (*storageCacheEntry, bool) {
	r.advanceMu.RLock()
	defer r.advanceMu.RUnlock()
	if r.gen.Load() != gen0 {
		return newEnt, false
	}
	if existing, loaded := r.storageCache.LoadOrStore(key, newEnt); loaded {
		return existing.(*storageCacheEntry), false
	}
	r.storageCount.Add(1)
	return newEnt, true
}

// advance carries the cache across a block commit: it swaps miss resolution to a
// reader at the newly committed root and evicts every key the block wrote. Any
// surviving entry holds the same value at the new root as at the root it was read
// from, so serving it stays correct. Safe to run while straggling prefetch
// workers still use the cache: advanceMu excludes their guarded stores while the
// eviction pass runs, and the generation bump neutralises any store whose fetch
// straddled the advance.
//
// The new backing is stored BEFORE the generation is bumped: sync/atomic is
// sequentially consistent, so any straggler that observes the new generation is
// guaranteed to also observe the new backing and therefore fetches a new-root
// value (this closes a window the reference implementation left open by bumping
// the generation first).
func (r *readerWithCache) advance(backing Reader, upd *stateUpdate) {
	r.advanceMu.Lock()
	defer r.advanceMu.Unlock()

	r.backing.Store(&backing)
	r.gen.Add(1)

	dropAddrs := storageDropSet(upd)
	invalidated := r.evictWrittenAccounts(upd)
	invalidated += r.evictWrittenSlots(upd)
	invalidated += r.dropAccountsStorage(dropAddrs)
	retentionInvalidatedMeter.Mark(invalidated)

	r.enforceRetentionCap()
}

// storageDropSet returns the accounts whose entire cached storage must be
// dropped on this advance: deletions (their storage is implicitly wiped) and,
// in hashed-key mode, every account with slot writes (hashed keys cannot be
// matched against the raw-keyed cache). Empty on the live post-Cancun raw-key,
// no-deletion path. Pure computation — no cache mutation.
func storageDropSet(upd *stateUpdate) map[common.Address]struct{} {
	var drops map[common.Address]struct{}
	add := func(addr common.Address) {
		if drops == nil {
			drops = make(map[common.Address]struct{})
		}
		drops[addr] = struct{}{}
	}
	// Hashed-key mode (pre-Cancun / noStorageWiping=false): drop wholesale.
	if !upd.rawStorageKey {
		for addr := range upd.storagesOrigin {
			add(addr)
		}
	}
	// Deletions implicitly wipe storage (rare post-Cancun; see deletedAddrs).
	for addr := range deletedAddrs(upd) {
		add(addr)
	}
	return drops
}

// deletedAddrs returns the accounts the block deleted (nil slim-RLP payload
// under their hash), or nil if none. It first scans for any deletion so the
// per-account keccak (address -> hash, to match the hash-keyed upd.accounts) is
// paid only when a deletion actually exists — essentially never on the live
// post-Cancun path.
func deletedAddrs(upd *stateUpdate) map[common.Address]struct{} {
	if !hasAccountDeletion(upd) {
		return nil
	}
	var deleted map[common.Address]struct{}
	for addr := range upd.accountsOrigin {
		if data, ok := upd.accounts[crypto.Keccak256Hash(addr.Bytes())]; ok && len(data) == 0 {
			if deleted == nil {
				deleted = make(map[common.Address]struct{})
			}
			deleted[addr] = struct{}{}
		}
	}
	return deleted
}

// hasAccountDeletion reports whether the block deleted any account (a nil
// slim-RLP payload under its hash). Cheap scan, no keccak.
func hasAccountDeletion(upd *stateUpdate) bool {
	for _, data := range upd.accounts {
		if len(data) == 0 {
			return true
		}
	}
	return false
}

// evictWrittenAccounts deletes every account the block mutated (all of
// accountsOrigin) from the cache, returning the number removed.
func (r *readerWithCache) evictWrittenAccounts(upd *stateUpdate) int64 {
	invalidated := int64(0)
	for addr := range upd.accountsOrigin {
		if _, ok := r.accounts.LoadAndDelete(addr); ok {
			r.accountCount.Add(-1)
			invalidated++
		}
	}
	return invalidated
}

// evictWrittenSlots deletes the exact slots the block wrote, on the raw-key
// (live post-Cancun) path. Hashed-key accounts are handled by dropAccountsStorage.
func (r *readerWithCache) evictWrittenSlots(upd *stateUpdate) int64 {
	if !upd.rawStorageKey {
		return 0
	}
	invalidated := int64(0)
	for addr, slots := range upd.storagesOrigin {
		for slot := range slots {
			if _, ok := r.storageCache.LoadAndDelete(storageKey{addr: addr, slot: slot}); ok {
				r.storageCount.Add(-1)
				invalidated++
			}
		}
	}
	return invalidated
}

// dropAccountsStorage removes every cached slot of each account in drops via a
// single Range over the flat storage cache. Runs only when drops is non-empty —
// essentially never on the live path (see storageDropSet).
func (r *readerWithCache) dropAccountsStorage(drops map[common.Address]struct{}) int64 {
	if len(drops) == 0 {
		return 0
	}
	invalidated := int64(0)
	r.storageCache.Range(func(k, _ any) bool {
		if _, drop := drops[k.(storageKey).addr]; drop {
			if _, ok := r.storageCache.LoadAndDelete(k); ok {
				r.storageCount.Add(-1)
				invalidated++
			}
		}
		return true
	})
	return invalidated
}

// enforceRetentionCap resets the cache wholesale once it exceeds the soft size
// caps, bounding retained memory. Counters are approximate (straggler inserts
// may race), which is harmless because the caps are soft.
func (r *readerWithCache) enforceRetentionCap() {
	if r.accountCount.Load() > retentionMaxAccounts || r.storageCount.Load() > retentionMaxSlots {
		r.accounts.Clear()
		r.storageCache.Clear()
		r.accountCount.Store(0)
		r.storageCount.Store(0)
		retentionResetMeter.Mark(1)
	}
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
	// Try to resolve the requested account in the local cache (lock-free read).
	if v, ok := r.accounts.Load(addr); ok {
		ent := v.(*accountCacheEntry)
		return ent.acct, true, ent, false, nil
	}
	if !readerRetentionEnabled {
		// Original develop path, untouched: retention adds zero overhead when off.
		acct, err := r.Reader.Account(addr)
		if err != nil {
			return nil, false, nil, false, err
		}
		// First-writer-wins: LoadOrStore inserts only if key is absent.
		newEnt := &accountCacheEntry{acct: acct, origin: caller}
		if existing, loaded := r.accounts.LoadOrStore(addr, newEnt); loaded {
			ent := existing.(*accountCacheEntry)
			// Another goroutine inserted while we fetched from the backing reader.
			// Report incache=false so miss counters reflect backing-read cost.
			return ent.acct, false, ent, false, nil
		}
		return acct, false, newEnt, true, nil
	}
	// Retention path: sample the generation before the fetch, resolve against the
	// current backing (construction reader, or the advanced reader post-commit),
	// then store under the generation guard so a fetch that straddles an advance
	// cannot cache a stale value for a written key.
	gen0 := r.gen.Load()
	acct, err := r.currentBacking().Account(addr)
	if err != nil {
		return nil, false, nil, false, err
	}
	newEnt := &accountCacheEntry{acct: acct, origin: caller}
	ent, inserted := r.storeAccountGuarded(addr, newEnt, gen0)
	// Report incache=false either way so miss counters reflect backing-read cost.
	return ent.acct, false, ent, inserted, nil
}

// Account implements StateReader, retrieving the account specified by the address.
// The returned account might be nil if it's not existent.
//
// An error will be returned if the state is corrupted in the underlying reader.
func (r *readerWithCache) Account(addr common.Address) (*types.StateAccount, error) {
	account, _, _, _, err := r.account(addr, roleUnknown)
	return account, err
}

// storage retrieves the storage slot specified by the address and slot key, along
// with a flag indicating whether it's found in the cache or not. The returned
// storage slot might be empty if it's not existent.
//
// It also returns the cache entry (for provenance/unique-usage accounting)
// and whether this call inserted a new entry (first-writer-wins).
func (r *readerWithCache) storage(addr common.Address, slot common.Hash, caller readerRole) (common.Hash, bool, *storageCacheEntry, bool, error) {
	key := storageKey{addr: addr, slot: slot}

	// Try to resolve the requested storage slot in the local cache (lock-free read).
	if v, ok := r.storageCache.Load(key); ok {
		ent := v.(*storageCacheEntry)
		return ent.value, true, ent, false, nil
	}
	if !readerRetentionEnabled {
		// Original develop path, untouched: retention adds zero overhead when off.
		value, err := r.Reader.Storage(addr, slot)
		if err != nil {
			return common.Hash{}, false, nil, false, err
		}
		// First-writer-wins: LoadOrStore inserts only if key is absent.
		newEnt := &storageCacheEntry{value: value, origin: caller}
		if existing, loaded := r.storageCache.LoadOrStore(key, newEnt); loaded {
			ent := existing.(*storageCacheEntry)
			// Another goroutine inserted while we fetched from the backing reader.
			// Report incache=false so miss counters reflect backing-read cost.
			return ent.value, false, ent, false, nil
		}
		return value, false, newEnt, true, nil
	}
	// Retention path: generation-guarded resolve + store (see account()).
	gen0 := r.gen.Load()
	value, err := r.currentBacking().Storage(addr, slot)
	if err != nil {
		return common.Hash{}, false, nil, false, err
	}
	newEnt := &storageCacheEntry{value: value, origin: caller}
	ent, inserted := r.storeStorageGuarded(key, newEnt, gen0)
	return ent.value, false, ent, inserted, nil
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
	account, incache, ent, inserted, err := r.readerWithCache.account(addr, r.role)
	if err != nil {
		return nil, err
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
	value, incache, entCopy, inserted, err := r.readerWithCache.storage(addr, slot, r.role)
	if err != nil {
		return common.Hash{}, err
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
