package vm

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// --- Candidate 1: sync.Map keyed by string(data). ---

type syncMapStore struct {
	m sync.Map // string(data) -> common.Hash
}

func newSyncMapStore() keccakResultStore { return &syncMapStore{} }

func (s *syncMapStore) Load(data []byte) (common.Hash, bool) {
	v, ok := s.m.Load(string(data))
	if !ok {
		return common.Hash{}, false
	}
	return v.(common.Hash), true
}

func (s *syncMapStore) Store(data []byte, h common.Hash) {
	s.m.Store(string(data), h)
}

// --- Candidate 2: sharded map[string]common.Hash + sync.RWMutex. ---

type shardedMapStore struct {
	mu sync.RWMutex
	m  map[string]common.Hash
}

func newShardedMapStore() keccakResultStore {
	return &shardedMapStore{m: make(map[string]common.Hash)}
}

func (s *shardedMapStore) Load(data []byte) (common.Hash, bool) {
	s.mu.RLock()
	h, ok := s.m[string(data)] // compiler avoids the []byte->string alloc on map lookup
	s.mu.RUnlock()
	return h, ok
}

func (s *shardedMapStore) Store(data []byte, h common.Hash) {
	s.mu.Lock()
	s.m[string(data)] = h
	s.mu.Unlock()
}

// --- Candidate 3: exact-size fixed-array buckets, keyed by exact length. ---
//
// Only the measured hot sizes get a dedicated fixed-array bucket; anything
// else falls back to a string-keyed bucket so the candidate stays correct
// (just not maximally fast) for sizes outside the benchmarked set.

type fixedBucketStore struct {
	mu       sync.RWMutex
	b64      map[[64]byte]common.Hash
	b88      map[[88]byte]common.Hash
	b128     map[[128]byte]common.Hash
	b349     map[[349]byte]common.Hash
	fallback map[string]common.Hash
}

func newFixedBucketStore() keccakResultStore {
	return &fixedBucketStore{
		b64:      make(map[[64]byte]common.Hash),
		b88:      make(map[[88]byte]common.Hash),
		b128:     make(map[[128]byte]common.Hash),
		b349:     make(map[[349]byte]common.Hash),
		fallback: make(map[string]common.Hash),
	}
}

func (s *fixedBucketStore) Load(data []byte) (common.Hash, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch len(data) {
	case 64:
		var k [64]byte
		copy(k[:], data)
		h, ok := s.b64[k]
		return h, ok
	case 88:
		var k [88]byte
		copy(k[:], data)
		h, ok := s.b88[k]
		return h, ok
	case 128:
		var k [128]byte
		copy(k[:], data)
		h, ok := s.b128[k]
		return h, ok
	case 349:
		var k [349]byte
		copy(k[:], data)
		h, ok := s.b349[k]
		return h, ok
	default:
		h, ok := s.fallback[string(data)]
		return h, ok
	}
}

func (s *fixedBucketStore) Store(data []byte, h common.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch len(data) {
	case 64:
		var k [64]byte
		copy(k[:], data)
		s.b64[k] = h
	case 88:
		var k [88]byte
		copy(k[:], data)
		s.b88[k] = h
	case 128:
		var k [128]byte
		copy(k[:], data)
		s.b128[k] = h
	case 349:
		var k [349]byte
		copy(k[:], data)
		s.b349[k] = h
	default:
		s.fallback[string(data)] = h
	}
}

// --- Benchmark harness. ---

func BenchmarkKeccakStore_SyncMapString(b *testing.B) { benchKeccakStore(b, newSyncMapStore()) }
func BenchmarkKeccakStore_ShardedMap(b *testing.B)    { benchKeccakStore(b, newShardedMapStore()) }
func BenchmarkKeccakStore_FixedBuckets(b *testing.B)  { benchKeccakStore(b, newFixedBucketStore()) }

func benchKeccakStore(b *testing.B, s keccakResultStore) {
	sizes := []int{64, 88, 128, 349}
	inputs := make([][]byte, len(sizes))
	for i, n := range sizes {
		inputs[i] = bytes.Repeat([]byte{byte(i + 1)}, n)
		s.Store(inputs[i], crypto.Keccak256Hash(inputs[i]))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = s.Load(inputs[i%len(inputs)])
	}
}

// BenchmarkKeccakStore_*_LargeUniqueMix models the adversarial-block shape
// from spec §3.4: a stream of unique 8192B inputs (each Store'd exactly once,
// never re-read) interleaved with repeated lookups of the hot small sizes.
// This exercises Store-path allocation behavior, not just Load.
func BenchmarkKeccakStore_SyncMapString_LargeUniqueMix(b *testing.B) {
	benchKeccakStoreLargeMix(b, newSyncMapStore())
}
func BenchmarkKeccakStore_ShardedMap_LargeUniqueMix(b *testing.B) {
	benchKeccakStoreLargeMix(b, newShardedMapStore())
}
func BenchmarkKeccakStore_FixedBuckets_LargeUniqueMix(b *testing.B) {
	benchKeccakStoreLargeMix(b, newFixedBucketStore())
}

func benchKeccakStoreLargeMix(b *testing.B, s keccakResultStore) {
	sizes := []int{64, 88, 128, 349}
	hot := make([][]byte, len(sizes))
	for i, n := range sizes {
		hot[i] = bytes.Repeat([]byte{byte(i + 1)}, n)
		s.Store(hot[i], crypto.Keccak256Hash(hot[i]))
	}
	large := make([]byte, 8192)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%4 == 3 {
			// unique large input, inserted once, never looked up again
			binary.PutUvarint(large, uint64(i))
			s.Store(large, crypto.Keccak256Hash(large))
			continue
		}
		_, _ = s.Load(hot[i%len(hot)])
	}
}
