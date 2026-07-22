package vm

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func newKeccakStoreForTest() keccakResultStore { return newKeccakStore(defaultKeccakCap) }

func TestSharedResultCaches_ApplyTo(t *testing.T) {
	// Extended off: legacy caches wired, no widened store.
	base := NewSharedResultCaches(false)
	var cfg Config
	base.ApplyTo(&cfg)
	if cfg.Keccak256Cache == nil || cfg.EcrecoverCache == nil || cfg.SharedJumpDestCache == nil {
		t.Fatal("legacy caches must always be wired by ApplyTo")
	}
	if cfg.KeccakStore != nil {
		t.Fatal("widened store must be nil when extended is off")
	}
	// Extended on: widened store present too.
	ext := NewSharedResultCaches(true)
	var cfg2 Config
	ext.ApplyTo(&cfg2)
	if cfg2.KeccakStore == nil {
		t.Fatal("widened store must be wired when extended is on")
	}
}

func TestKeccakStore_LengthAwareNoCollision(t *testing.T) {
	s := newKeccakStoreForTest()
	short := bytes.Repeat([]byte{0xAB}, 60)                  // 60 bytes
	padded := append(append([]byte{}, short...), 0, 0, 0, 0) // same 60 bytes ‖ 4×0x00 = a real 64B input
	if len(padded) != 64 {
		t.Fatalf("setup: want 64, got %d", len(padded))
	}
	s.Store(short, crypto.Keccak256Hash(short))
	// A different-length input that shares a prefix MUST NOT read the short entry.
	if got, ok := s.Load(padded); ok && got == crypto.Keccak256Hash(short) {
		t.Fatal("length collision: padded input aliased the shorter entry")
	}
	// Correct roundtrip for each length independently.
	s.Store(padded, crypto.Keccak256Hash(padded))
	if got, ok := s.Load(short); !ok || got != crypto.Keccak256Hash(short) {
		t.Fatalf("short roundtrip failed: ok=%v got=%x", ok, got)
	}
	if got, ok := s.Load(padded); !ok || got != crypto.Keccak256Hash(padded) {
		t.Fatalf("padded roundtrip failed: ok=%v got=%x", ok, got)
	}
}

func TestKeccakStore_MemoryCapBounded(t *testing.T) {
	s := newKeccakStore(1024)
	for i := 0; i < 100_000; i++ {
		in := make([]byte, 8192)
		binary.PutUvarint(in, uint64(i)) // unique
		s.Store(in, crypto.Keccak256Hash(in))
	}
	if n := s.(*shardedKeccakStore).entries.Load(); n > 1024 {
		t.Fatalf("cap breached: %d entries", n)
	}
}

// TestKeccakStore_MemoryCapBounded_Concurrent proves the cap check-and-insert
// is atomic under concurrent Store calls with distinct keys. Prior to the
// fix, the cap check happened before the lock, so concurrent goroutines could
// all observe entries < cap and each insert once they acquired the lock,
// overshooting the cap by up to the number of concurrent callers. Run with
// -race to catch data races on the underlying map/entries counter too.
func TestKeccakStore_MemoryCapBounded_Concurrent(t *testing.T) {
	const cap = 1024
	const goroutines = 8
	const perGoroutine = 5_000
	s := newKeccakStore(cap)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				in := make([]byte, 8192)
				binary.PutUvarint(in, uint64(g))
				binary.PutUvarint(in[8192/2:], uint64(i)) // unique per (g, i) across all goroutines
				s.Store(in, crypto.Keccak256Hash(in))
			}
		}(g)
	}
	wg.Wait()

	if n := s.(*shardedKeccakStore).entries.Load(); n > cap {
		t.Fatalf("cap breached under concurrency: %d entries (cap %d)", n, cap)
	}
}

func TestKeccakStore_AllSizesHitEqualsMiss(t *testing.T) {
	s := newKeccakStoreForTest()
	for _, n := range []int{0, 31, 63, 64, 65, 88, 128, 349} {
		in := bytes.Repeat([]byte{byte(n)}, n)
		want := crypto.Keccak256Hash(in)
		s.Store(in, want)
		if got, ok := s.Load(in); !ok || got != want {
			t.Fatalf("size %d: ok=%v got=%x want=%x", n, ok, got, want)
		}
	}
}
