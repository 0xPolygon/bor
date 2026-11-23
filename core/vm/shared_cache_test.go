package vm

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func newKeccakStoreForTest() *keccakResultCache {
	return newKeccakResultCache(defaultKeccakMaxEntries, defaultKeccakMaxBytes, CacheScopeImport)
}

func TestSharedResultCaches_ApplyTo(t *testing.T) {
	// Extended off: legacy caches wired, no widened store.
	base := NewSharedResultCaches(false, CacheScopeImport)
	var cfg Config
	base.ApplyTo(&cfg, CacheConsumerImportSerial)
	if cfg.Keccak256Cache == nil || cfg.EcrecoverCache == nil || cfg.SharedJumpDestCache == nil {
		t.Fatal("legacy caches must always be wired by ApplyTo")
	}
	if cfg.keccakCache != nil {
		t.Fatal("widened store must be nil when extended is off")
	}
	if cfg.EnablePrecompileCache {
		t.Fatal("EnablePrecompileCache must be false when extended is off")
	}
	if cfg.ResultCacheConsumer != CacheConsumerImportSerial {
		t.Fatalf("consumer = %v, want %v", cfg.ResultCacheConsumer, CacheConsumerImportSerial)
	}
	// Extended on: widened store present too.
	ext := NewSharedResultCaches(true, CacheScopeBuild)
	var cfg2 Config
	ext.ApplyTo(&cfg2, CacheConsumerBuildSeal)
	if cfg2.keccakCache == nil {
		t.Fatal("widened store must be wired when extended is on")
	}
	if !cfg2.EnablePrecompileCache {
		t.Fatal("EnablePrecompileCache must be true when extended is on")
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
	// A different-length input that shares a prefix must not read the short entry.
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
	s := newKeccakResultCache(1024, defaultKeccakMaxBytes, CacheScopeImport)
	for i := 0; i < 100_000; i++ {
		in := make([]byte, 64)
		binary.PutUvarint(in, uint64(i)) // unique
		s.Store(in, crypto.Keccak256Hash(in))
	}
	if n := s.entries.Load(); n > 1024 {
		t.Fatalf("entry cap breached: %d entries", n)
	}
	if !s.full.Load() {
		t.Fatal("store must be marked full once the entry cap is reached")
	}
}

// TestKeccakStore_ByteBudgetBounded checks that retained preimage bytes stay
// within the byte budget even when the entry cap is far away, and that a
// large input which does not fit does not block smaller ones that still do.
func TestKeccakStore_ByteBudgetBounded(t *testing.T) {
	const budget = 1 << 20 // 1 MiB
	s := newKeccakResultCache(1<<30, budget, CacheScopeImport)
	for i := 0; i < 1000; i++ {
		in := make([]byte, 8192)
		binary.PutUvarint(in, uint64(i)) // unique
		s.Store(in, crypto.Keccak256Hash(in))
	}
	if b := s.bytes.Load(); b > budget {
		t.Fatalf("byte budget breached: %d bytes (budget %d)", b, budget)
	}
	if n := s.entries.Load(); n != budget/8192 {
		t.Fatalf("entries = %d, want %d (budget filled exactly by 8 KiB inputs)", n, budget/8192)
	}

	// Leave room for one small input but not for a large one.
	s2 := newKeccakResultCache(1<<30, 8192+100, CacheScopeImport)
	big1, big2 := make([]byte, 8192), make([]byte, 8192)
	big2[0] = 1
	s2.Store(big1, crypto.Keccak256Hash(big1))
	s2.Store(big2, crypto.Keccak256Hash(big2)) // does not fit
	if _, ok := s2.Load(big2); ok {
		t.Fatal("input over the remaining byte budget must not be stored")
	}
	if s2.full.Load() {
		t.Fatal("store must stay open while smaller inputs still fit")
	}
	small := []byte{1, 2, 3}
	s2.Store(small, crypto.Keccak256Hash(small))
	if got, ok := s2.Load(small); !ok || got != crypto.Keccak256Hash(small) {
		t.Fatal("small input that fits the remaining budget must be stored")
	}
}

// TestKeccakStore_DuplicateStoreCountsOnce checks that storing the same input
// twice counts its entry and bytes once.
func TestKeccakStore_DuplicateStoreCountsOnce(t *testing.T) {
	s := newKeccakStoreForTest()
	in := bytes.Repeat([]byte{7}, 100)
	s.Store(in, crypto.Keccak256Hash(in))
	s.Store(in, crypto.Keccak256Hash(in))
	if n, b := s.entries.Load(), s.bytes.Load(); n != 1 || b != 100 {
		t.Fatalf("entries=%d bytes=%d, want 1 and 100", n, b)
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
	s := newKeccakResultCache(cap, defaultKeccakMaxBytes, CacheScopeImport)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				in := make([]byte, 64)
				binary.PutUvarint(in, uint64(g))
				binary.PutUvarint(in[32:], uint64(i)) // unique per (g, i) across all goroutines
				s.Store(in, crypto.Keccak256Hash(in))
			}
		}(g)
	}
	wg.Wait()

	if n := s.entries.Load(); n > cap {
		t.Fatalf("cap breached under concurrency: %d entries (cap %d)", n, cap)
	}

	// Same check for the byte budget.
	const budget = 64 * 1024
	sb := newKeccakResultCache(1<<30, budget, CacheScopeImport)
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				in := make([]byte, 1000)
				binary.PutUvarint(in, uint64(g))
				binary.PutUvarint(in[500:], uint64(i))
				sb.Store(in, crypto.Keccak256Hash(in))
			}
		}(g)
	}
	wg.Wait()
	if b := sb.bytes.Load(); b > budget {
		t.Fatalf("byte budget breached under concurrency: %d bytes (budget %d)", b, budget)
	}
}

// FuzzKeccakWidened exercises the widened-cache miss→store→hit cycle against a
// direct keccak, for arbitrary input lengths. It also pins the cacheability
// predicate: only cacheable-length inputs go through the store.
func FuzzKeccakWidened(f *testing.F) {
	for _, n := range []int{0, 1, 31, 63, 64, 65, 88, 128, 349, 8192, 8193} {
		f.Add(bytes.Repeat([]byte{0x5A}, n))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		want := crypto.Keccak256Hash(data)
		// Simulate opKeccak256's widened miss→store→hit for cacheable sizes.
		// A fresh store per execution mirrors a single opcode's cycle and keeps
		// each input independent of the per-block budgets, which a shared
		// store would exhaust across fuzz executions.
		if cacheableKeccakLen(len(data)) {
			store := newKeccakStoreForTest()
			if _, ok := store.Load(data); !ok {
				store.Store(data, want)
			}
			if got, _ := store.Load(data); got != want {
				t.Fatalf("widened cache mismatch len=%d", len(data))
			}
		}
	})
}

// TestCacheableKeccakLen pins the eligibility boundaries of the widened keccak
// cache. Both bounds are load-bearing: n > 0 excludes the trivial empty hash
// (and must not become n >= 0), and n <= 8192 caps retained memory against
// adversarial inputs (and must not become n < 8192). A drift in either boundary
// silently changes what the cache admits.
func TestCacheableKeccakLen(t *testing.T) {
	cases := []struct {
		n    int
		want bool
	}{
		{0, false},       // excluded: trivial to hash; pins the n > 0 lower bound
		{1, true},        // smallest eligible input
		{64, true},       // 64B is length-eligible (served by the legacy fast path)
		{88, true},       // typical variable-length input
		{8192, true},     // upper bound is inclusive; pins the n <= 8192 boundary
		{8193, false},    // one past the cap
		{1 << 20, false}, // far past the cap
	}
	for _, c := range cases {
		if got := cacheableKeccakLen(c.n); got != c.want {
			t.Errorf("cacheableKeccakLen(%d) = %v, want %v", c.n, got, c.want)
		}
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

// TestKeccakStore_ConcurrentDuplicatesExact stores the same inputs from many
// goroutines while others read them. Each input must be counted once, so the
// entry and byte counters end exactly at the unique totals.
func TestKeccakStore_ConcurrentDuplicatesExact(t *testing.T) {
	const (
		goroutines = 8
		unique     = 2000
	)
	inputs := make([][]byte, unique)
	var wantBytes int64
	for i := range inputs {
		in := make([]byte, 40+i%300)
		binary.PutUvarint(in, uint64(i))
		inputs[i] = in
		wantBytes += int64(len(in))
	}
	s := newKeccakStoreForTest()

	var wg sync.WaitGroup
	wg.Add(2 * goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for _, in := range inputs {
				s.Store(in, crypto.Keccak256Hash(in))
			}
		}()
		go func() {
			defer wg.Done()
			for _, in := range inputs {
				if h, ok := s.Load(in); ok && h != crypto.Keccak256Hash(in) {
					t.Errorf("wrong hash for input of %d bytes", len(in))
					return
				}
			}
		}()
	}
	wg.Wait()

	if n := s.entries.Load(); n != unique {
		t.Fatalf("entries = %d, want %d", n, unique)
	}
	if b := s.bytes.Load(); b != wantBytes {
		t.Fatalf("bytes = %d, want %d", b, wantBytes)
	}
	for _, in := range inputs {
		if h, ok := s.Load(in); !ok || h != crypto.Keccak256Hash(in) {
			t.Fatalf("input of %d bytes: ok=%v", len(in), ok)
		}
	}
}
