package blockstm

import (
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
)

// ---------------------------------------------------------------------------
// MVStore — shared concurrent multi-version store for direct values
// ---------------------------------------------------------------------------

type versionedEntry struct {
	txIdx       int
	incarnation int
	value       any  // uint64, common.Hash, []byte, bool
	estimate    bool // true = writer is being re-executed, value is stale
}

// MVStore is a sharded concurrent map storing versioned values per key.
// Workers write their tx's values; readers find the latest prior tx's value.
// A lock-free bloom filter gates reads: if a key was never written, the read
// skips the shard lock entirely.
//
// ESTIMATE markers: when a tx fails validation and is re-executed, its entries
// are marked as ESTIMATE (not deleted). Reads encountering an ESTIMATE entry
// spin-wait until the writer re-writes (DONE) or the entry is cleaned up.
// This enables per-key pipelining: readers wait only for the specific SSTORE,
// not the writer's full tx completion.
const (
	mvStoreShards = 64
	// initialMVStoreShardCap is the per-shard map capacity hint. Distinct
	// from mvStoreShards (which sets the *number* of shards): this sets
	// the *initial size* of each shard's underlying map. 64 is a
	// conservative over-allocation — typical blocks touch a few hundred
	// keys, distributed across mvStoreShards shards.
	initialMVStoreShardCap = 64
)

type mvStoreShard struct {
	mu   sync.RWMutex
	data map[Key][]versionedEntry
}

type MVStore struct {
	shards [mvStoreShards]mvStoreShard
	bloom  writeBloom // lock-free bloom filter for fast read misses
}

func NewMVStore() *MVStore {
	s := &MVStore{}
	for i := range s.shards {
		s.shards[i].data = make(map[Key][]versionedEntry, initialMVStoreShardCap)
	}
	return s
}

func (s *MVStore) shard(k Key) *mvStoreShard {
	return &s.shards[addrShardIndex(k[:common.AddressLength])%mvStoreShards]
}

func (s *MVStore) WriteInc(k Key, txIdx, incarnation int, val any) {
	s.bloom.add(k)
	sh := s.shard(k)
	sh.mu.Lock()
	entries := sh.data[k]
	pos := sort.Search(len(entries), func(i int) bool { return entries[i].txIdx >= txIdx })
	if pos < len(entries) && entries[pos].txIdx == txIdx {
		entries[pos].value = val
		entries[pos].incarnation = incarnation
		entries[pos].estimate = false
	} else {
		entries = append(entries, versionedEntry{})
		copy(entries[pos+1:], entries[pos:])
		entries[pos] = versionedEntry{txIdx: txIdx, incarnation: incarnation, value: val}
		sh.data[k] = entries
	}
	sh.mu.Unlock()
}

// Delete removes the entry for a specific txIdx from the store.
// Used when reverting writes — ensures subsequent txs don't see reverted values.
// Note: we don't remove from bloom (false positives are OK, false negatives are not).
func (s *MVStore) Delete(k Key, txIdx int) {
	sh := s.shard(k)
	sh.mu.Lock()
	entries := sh.data[k]
	pos := sort.Search(len(entries), func(i int) bool { return entries[i].txIdx >= txIdx })
	if pos < len(entries) && entries[pos].txIdx == txIdx {
		entries = append(entries[:pos], entries[pos+1:]...)
		sh.data[k] = entries
	}
	sh.mu.Unlock()
}

// ReadVersionFull returns the latest value, writer info, AND the estimate
// flag in a single atomic read (one lock acquisition). Returning the flag
// alongside the value rules out the race where the entry's estimate state
// transitions between a separate read and a follow-up IsEstimate query.
func (s *MVStore) ReadVersionFull(k Key, txIdx int) (val any, writerIdx int, writerInc int, found bool, estimate bool) {
	if !s.bloom.mayContain(k) {
		return nil, -1, 0, false, false
	}
	sh := s.shard(k)
	sh.mu.RLock()
	entries := sh.data[k]
	pos := sort.Search(len(entries), func(i int) bool { return entries[i].txIdx >= txIdx })
	if pos > 0 {
		e := entries[pos-1]
		sh.mu.RUnlock()
		return e.value, e.txIdx, e.incarnation, true, e.estimate
	}
	sh.mu.RUnlock()
	return nil, -1, 0, false, false
}

// IsEstimate checks if the entry for key k from writerIdx is marked as ESTIMATE.
func (s *MVStore) IsEstimate(k Key, writerIdx int) bool {
	sh := s.shard(k)
	sh.mu.RLock()
	entries := sh.data[k]
	pos := sort.Search(len(entries), func(i int) bool { return entries[i].txIdx >= writerIdx })
	if pos < len(entries) && entries[pos].txIdx == writerIdx {
		est := entries[pos].estimate
		sh.mu.RUnlock()
		return est
	}
	sh.mu.RUnlock()
	return false
}

// MarkEstimate sets the estimate flag on all entries for txIdx.
// Called when a tx fails validation and is about to be re-executed.
// Entries stay in MVStore as dependency markers — readers that encounter
// ESTIMATE entries spin-wait for the re-execution to write new values.
func (s *MVStore) MarkEstimate(txIdx int, keys []Key) {
	for _, k := range keys {
		sh := s.shard(k)
		sh.mu.Lock()
		entries := sh.data[k]
		pos := sort.Search(len(entries), func(i int) bool { return entries[i].txIdx >= txIdx })
		if pos < len(entries) && entries[pos].txIdx == txIdx {
			entries[pos].estimate = true
		}
		sh.mu.Unlock()
	}
}

// CleanupEstimate removes entries still marked as ESTIMATE for txIdx.
// Called after re-execution: entries that the new incarnation wrote are
// DONE (estimate=false from WriteInc). Entries NOT re-written are stale
// and must be removed.
func (s *MVStore) CleanupEstimate(txIdx int, keys []Key) {
	for _, k := range keys {
		sh := s.shard(k)
		sh.mu.Lock()
		entries := sh.data[k]
		pos := sort.Search(len(entries), func(i int) bool { return entries[i].txIdx >= txIdx })
		if pos < len(entries) && entries[pos].txIdx == txIdx && entries[pos].estimate {
			entries = append(entries[:pos], entries[pos+1:]...)
			sh.data[k] = entries
		}
		sh.mu.Unlock()
	}
}
