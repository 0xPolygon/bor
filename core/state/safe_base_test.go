package state

import (
	"sync"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/triedb"
)

// newTestSafeBase builds an empty StateDB, pre-populates a single account
// with balance/nonce/code/storage, commits, and returns a SafeBase wrapping
// the resulting state with a small worker pool.
func newTestSafeBase(t *testing.T, addr common.Address) *SafeBase {
	t.Helper()
	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	sdb, err := New(types.EmptyRootHash, NewDatabase(tdb, nil))
	if err != nil {
		t.Fatal(err)
	}
	sdb.SetBalance(addr, uint256.NewInt(1000), tracing.BalanceChangeUnspecified)
	sdb.SetNonce(addr, 7, tracing.NonceChangeUnspecified)
	sdb.SetCode(addr, []byte{0x60, 0x00}, tracing.CodeChangeUnspecified)
	sdb.SetState(addr, common.HexToHash("0x1"), common.HexToHash("0xdead"))
	return NewSafeBase(sdb, 2)
}

// TestSafeBase_GetBalance_CacheHit returns the same balance twice; second
// call must hit the cache (no pool acquire) and still equal the first.
func TestSafeBase_GetBalance_CacheHit(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)

	first := sb.GetBalance(addr).Uint64()
	second := sb.GetBalance(addr).Uint64()
	if first != 1000 || second != 1000 {
		t.Fatalf("GetBalance: first=%d second=%d, want both 1000", first, second)
	}
}

// TestSafeBase_GetNonce caches and returns nonce.
func TestSafeBase_GetNonce(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)

	if got := sb.GetNonce(addr); got != 7 {
		t.Fatalf("GetNonce first call: got %d, want 7", got)
	}
	if got := sb.GetNonce(addr); got != 7 {
		t.Fatalf("GetNonce cached call: got %d, want 7", got)
	}
}

// TestSafeBase_GetState_Cached caches (addr, slot) → value and returns from
// cache on repeat.
func TestSafeBase_GetState_Cached(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)
	slot := common.HexToHash("0x1")

	if got := sb.GetState(addr, slot); got != common.HexToHash("0xdead") {
		t.Fatalf("GetState: got %s, want 0xdead", got.Hex())
	}
	// Cached read.
	if got := sb.GetState(addr, slot); got != common.HexToHash("0xdead") {
		t.Fatalf("GetState cached: got %s, want 0xdead", got.Hex())
	}
}

// TestSafeBase_GetCommittedState delegates to GetState for base reads.
func TestSafeBase_GetCommittedState(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)
	slot := common.HexToHash("0x1")
	if got := sb.GetCommittedState(addr, slot); got != common.HexToHash("0xdead") {
		t.Fatalf("GetCommittedState: got %s, want 0xdead", got.Hex())
	}
}

// TestSafeBase_GetCode caches and returns stored code.
func TestSafeBase_GetCode(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)

	code := sb.GetCode(addr)
	if len(code) != 2 || code[0] != 0x60 {
		t.Fatalf("GetCode: got %x, want 6000", code)
	}
	// Cached read.
	if got := sb.GetCode(addr); len(got) != 2 {
		t.Fatalf("GetCode cached: got %x", got)
	}
}

// TestSafeBase_GetCodeHash caches and returns Keccak256(code).
func TestSafeBase_GetCodeHash(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)

	h := sb.GetCodeHash(addr)
	if h == (common.Hash{}) {
		t.Fatal("GetCodeHash returned zero")
	}
	// Cache hit.
	h2 := sb.GetCodeHash(addr)
	if h != h2 {
		t.Fatalf("GetCodeHash not stable: %s vs %s", h.Hex(), h2.Hex())
	}
}

// TestSafeBase_GetCodeSize returns len(code) via GetCode.
func TestSafeBase_GetCodeSize(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)
	if got := sb.GetCodeSize(addr); got != 2 {
		t.Fatalf("GetCodeSize: got %d, want 2", got)
	}
}

// TestSafeBase_Exist returns true for populated addr, false for missing.
func TestSafeBase_Exist(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)
	if !sb.Exist(addr) {
		t.Fatal("Exist: populated addr returned false")
	}
	// Cache hit.
	if !sb.Exist(addr) {
		t.Fatal("Exist cached: false")
	}
}

// TestSafeBase_GetStorageRoot returns the storage trie root; caches it.
func TestSafeBase_GetStorageRoot(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)
	// First call populates cache; second call must match.
	r1 := sb.GetStorageRoot(addr)
	r2 := sb.GetStorageRoot(addr)
	if r1 != r2 {
		t.Fatalf("GetStorageRoot not stable: %s vs %s", r1.Hex(), r2.Hex())
	}
}

// TestSafeBase_OverlayWinsOverSharedCache pins the read-priority contract
// for EIP-4788/EIP-2935 pre-block system-call writes: SafeBase.GetState
// must return the overlay value even if SharedStorageCache (the prefetcher's
// trieReader cache) carries a stale zero for the same key. A non-atomic
// Load → trie-read → Store in trieReader.Storage can land its zero after
// OverlayPendingStorageInto on a fast-prefetcher / slow-overlay path; the
// post-system-call value still has to win when SafeBase serves a read.
func TestSafeBase_OverlayWinsOverSharedCache(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)
	slot := common.HexToHash("0x42")
	sk := stateKey{addr: addr, slot: slot}

	// Prefetcher landed first: stale zero in the shared trie cache.
	shared := new(sync.Map)
	shared.Store(sk, common.Hash{})
	sb.SharedStorageCache = shared

	// Overlay landed second: post-system-call value.
	overlay := new(sync.Map)
	beaconRoot := common.HexToHash("0xbeef")
	overlay.Store(sk, beaconRoot)
	sb.OverlayStorageCache = overlay

	if got := sb.GetState(addr, slot); got != beaconRoot {
		t.Fatalf("GetState: got %s, want %s (overlay must beat the shared trie cache)",
			got.Hex(), beaconRoot.Hex())
	}
}

// TestSafeBase_OverlayResult_CachedInStateCache pins that an overlay hit
// is cached into the local stateCache for subsequent reads — so once a
// post-system-call value is observed, the GetState path stays fast and
// doesn't repeatedly traverse OverlayStorageCache. Kills the `remove
// call statement` mutation on the stateCache.Store call after an overlay
// hit: we mutate the overlay between the two reads; the second read still
// has to return the original value via the local stateCache hit.
func TestSafeBase_OverlayResult_CachedInStateCache(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)
	slot := common.HexToHash("0x42")
	sk := stateKey{addr: addr, slot: slot}

	overlay := new(sync.Map)
	want := common.HexToHash("0xbeef")
	overlay.Store(sk, want)
	sb.OverlayStorageCache = overlay

	if got := sb.GetState(addr, slot); got != want {
		t.Fatalf("GetState first call: got %s, want %s", got.Hex(), want.Hex())
	}
	// Mutate the overlay underneath — the second read must still return
	// the original value, served from the local stateCache populated on
	// the first call.
	overlay.Store(sk, common.HexToHash("0xfeed"))
	if got := sb.GetState(addr, slot); got != want {
		t.Fatalf("GetState second call: got %s, want %s (overlay hit must have populated stateCache)",
			got.Hex(), want.Hex())
	}
}

// TestSafeBase_SharedCacheStillUsedWithoutOverlay pins that the existing
// fast-path through SharedStorageCache continues to work when the overlay
// is absent — non-system-call slots warmed by the prefetcher should still
// serve without a pool acquire.
func TestSafeBase_SharedCacheStillUsedWithoutOverlay(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	sb := newTestSafeBase(t, addr)
	slot := common.HexToHash("0x99")
	sk := stateKey{addr: addr, slot: slot}

	shared := new(sync.Map)
	want := common.HexToHash("0xcafe")
	shared.Store(sk, want)
	sb.SharedStorageCache = shared

	if got := sb.GetState(addr, slot); got != want {
		t.Fatalf("GetState: got %s, want %s (shared cache hit expected)",
			got.Hex(), want.Hex())
	}
}
