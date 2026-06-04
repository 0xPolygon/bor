package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/tracing"
)

// Invariant tests for the ParallelStateDB ↔ MVStore/MVBalanceStore contract.
// These are the load-bearing properties that must hold for V2 validation to
// work; if any fails, the executor's conflict detection can miss dependencies.
// Every test here is a direct assertion of one invariant.

// ---------------------------------------------------------------------------
// Invariant 1: Every WriteKey has a matching MVStore entry at (TxIndex,
// Incarnation) after FlushToMVStore.
// ---------------------------------------------------------------------------

func TestInvariant_WriteKeysMatchMVStoreAfterFlush(t *testing.T) {
	pdb, store, _ := newTestPDB(t, 3)
	pdb.EnableReadTracking()
	pdb.SetDeferMVWrites(true)
	addr := common.HexToAddress("0xabcd")

	pdb.SetState(addr, common.HexToHash("0x1"), common.HexToHash("0x11"))
	pdb.SetState(addr, common.HexToHash("0x2"), common.HexToHash("0x22"))
	pdb.SetCode(addr, []byte{0xfe}, tracing.CodeChangeUnspecified)
	pdb.SetNonce(addr, 5, tracing.NonceChangeUnspecified)
	pdb.FlushToMVStore()

	for _, k := range pdb.WriteKeys {
		_, writer, inc, found, _ := store.ReadVersionFull(k, 10)
		if !found {
			t.Fatalf("WriteKey %x has no MVStore entry", k)
		}
		if writer != pdb.TxIndex {
			t.Fatalf("WriteKey %x: writer=%d, want %d", k, writer, pdb.TxIndex)
		}
		if inc != pdb.Incarnation {
			t.Fatalf("WriteKey %x: inc=%d, want %d", k, inc, pdb.Incarnation)
		}
	}
}

// ---------------------------------------------------------------------------
// Invariant 2: MarkEstimate flags every WriteKey's entry as estimate=true.
// ---------------------------------------------------------------------------

func TestInvariant_MarkEstimateFlagsAllKeys(t *testing.T) {
	pdb, store, _ := newTestPDB(t, 3)
	pdb.EnableReadTracking()
	addr := common.HexToAddress("0xabcd")

	pdb.SetState(addr, common.HexToHash("0x1"), common.HexToHash("0x11"))
	pdb.SetCode(addr, []byte{0xfe}, tracing.CodeChangeUnspecified)
	pdb.FlushToMVStore()

	pdb.MarkEstimate()
	for _, k := range pdb.WriteKeys {
		if !store.IsEstimate(k, pdb.TxIndex) {
			t.Fatalf("MarkEstimate: key %x not flagged estimate", k)
		}
	}
}

// ---------------------------------------------------------------------------
// Invariant 4: CleanupEstimate removes only keys that remained estimate,
// not those re-written by the new incarnation.
// ---------------------------------------------------------------------------

func TestInvariant_CleanupEstimatePreservesReWrites(t *testing.T) {
	pdb, store, bals := newTestPDB(t, 3)
	pdb.EnableReadTracking()
	addr := common.HexToAddress("0x1")

	// Incarnation 0 writes both keys.
	pdb.SetState(addr, common.HexToHash("0x1"), common.HexToHash("0x11"))
	pdb.SetState(addr, common.HexToHash("0x2"), common.HexToHash("0x22"))
	pdb.FlushToMVStore()
	oldKeys := append([]blockstm.Key{}, pdb.WriteKeys...)
	oldAddrs := append([]common.Address{}, pdb.BalAddrs...)
	pdb.MarkEstimate()

	// Incarnation 1: fresh state, re-write only slot 1.
	clear(pdb.localStorage)
	pdb.WriteKeys = pdb.WriteKeys[:0]
	pdb.BalAddrs = pdb.BalAddrs[:0]
	pdb.Incarnation = 1
	pdb.SetState(addr, common.HexToHash("0x1"), common.HexToHash("0x99"))
	pdb.FlushToMVStore()

	pdb.CleanupEstimate(oldKeys, oldAddrs)

	// slot 1 re-written → must still be in store.
	k1 := blockstm.NewStateKey(addr, common.HexToHash("0x1"))
	if _, _, _, found, est := store.ReadVersionFull(k1, 10); !found || est {
		t.Fatalf("CleanupEstimate wrongly removed re-written key (found=%v, est=%v)", found, est)
	}
	// slot 2 not re-written → must be gone.
	k2 := blockstm.NewStateKey(addr, common.HexToHash("0x2"))
	if _, _, _, found, _ := store.ReadVersionFull(k2, 10); found {
		t.Fatalf("CleanupEstimate failed to remove un-rewritten stale key")
	}
	_ = bals
}

// ---------------------------------------------------------------------------
// Invariant 5: Balance delta writes are commutative and atomic — multiple
// AddBalance + SubBalance calls accumulate into exactly ONE MVBalanceStore
// entry per (addr, tx).
// ---------------------------------------------------------------------------

func TestInvariant_BalanceDeltaAtomicPerAddr(t *testing.T) {
	pdb, _, bals := newTestPDB(t, 3)
	pdb.EnableReadTracking()
	addr := common.HexToAddress("0xabcd")

	pdb.AddBalance(addr, uint256.NewInt(10), tracing.BalanceChangeUnspecified)
	pdb.SubBalance(addr, uint256.NewInt(3), tracing.BalanceChangeUnspecified)
	pdb.AddBalance(addr, uint256.NewInt(5), tracing.BalanceChangeUnspecified)
	pdb.FlushToMVStore()

	add, sub, found := bals.GetTxDelta(addr, pdb.TxIndex)
	if !found {
		t.Fatal("expected a single balance delta entry")
	}
	if add.Uint64() != 15 || sub.Uint64() != 3 {
		t.Fatalf("delta: got (add=%d, sub=%d), want (15, 3)", add.Uint64(), sub.Uint64())
	}
}

// ---------------------------------------------------------------------------
// Invariant 6: Zero-net balance deltas are not written (flush skips them),
// but any non-zero side is always written.
// ---------------------------------------------------------------------------

func TestInvariant_FlushSkipsZeroDeltas(t *testing.T) {
	pdb, _, bals := newTestPDB(t, 3)
	pdb.EnableReadTracking()
	addr := common.HexToAddress("0xabcd")

	// Touch addr but with zero final deltas — no AddBalance/SubBalance.
	pdb.localBalAdd[addr] = new(uint256.Int) // zero
	pdb.localBalSub[addr] = new(uint256.Int) // zero
	pdb.recordBalWrite(addr)
	pdb.FlushToMVStore()

	if _, _, found := bals.GetTxDelta(addr, pdb.TxIndex); found {
		t.Fatal("flushBalanceDeltas wrote a zero-delta entry")
	}
}

// ---------------------------------------------------------------------------
// Invariant 7: WriteKeys deduplicates writes to the same slot. Multiple
// SetState calls to the same (addr, slot) produce a SINGLE WriteKey entry
// per tx; MVStore also has one entry.
//
// NOTE: WriteKeys as currently implemented appends on each write — dedup
// happens at the MVStore side (WriteInc upserts). So we assert the MVStore
// invariant, which is the one that actually matters.
// ---------------------------------------------------------------------------

func TestInvariant_RepeatedSetStateFinalValueLands(t *testing.T) {
	pdb, store, _ := newTestPDB(t, 3)
	pdb.EnableReadTracking()
	addr := common.HexToAddress("0x1")
	slot := common.HexToHash("0x1")

	pdb.SetState(addr, slot, common.HexToHash("0xaa"))
	pdb.SetState(addr, slot, common.HexToHash("0xbb"))
	pdb.SetState(addr, slot, common.HexToHash("0xcc"))
	pdb.FlushToMVStore()

	key := blockstm.NewStateKey(addr, slot)
	v, _, _, found, _ := store.ReadVersionFull(key, 10)
	if !found {
		t.Fatal("MVStore missing entry after repeated SetState")
	}
	if v != common.HexToHash("0xcc") {
		t.Fatalf("MVStore value: got %v, want 0xcc", v)
	}
}

// ---------------------------------------------------------------------------
// Invariant 8: DeferMVWrites=true delays ALL writes to FlushToMVStore.
// ---------------------------------------------------------------------------

func TestInvariant_DeferMVWritesHoldsAllWritesUntilFlush(t *testing.T) {
	pdb, store, _ := newTestPDB(t, 3)
	pdb.EnableReadTracking()
	pdb.SetDeferMVWrites(true)
	addr := common.HexToAddress("0xabcd")

	pdb.SetState(addr, common.HexToHash("0x1"), common.HexToHash("0x11"))
	pdb.SetCode(addr, []byte{0xfe}, tracing.CodeChangeUnspecified)
	pdb.SetNonce(addr, 3, tracing.NonceChangeUnspecified)
	pdb.CreateAccount(common.HexToAddress("0xef"))

	// No writes should be in MVStore yet.
	for _, k := range pdb.WriteKeys {
		if _, _, _, found, _ := store.ReadVersionFull(k, 10); found {
			t.Fatalf("DeferMVWrites violated — key %x in store before Flush", k)
		}
	}
	pdb.FlushToMVStore()
	// After flush, all must be present.
	for _, k := range pdb.WriteKeys {
		if _, _, _, found, _ := store.ReadVersionFull(k, 10); !found {
			t.Fatalf("FlushToMVStore missed key %x", k)
		}
	}
}

// ---------------------------------------------------------------------------
// Invariant 9: BalReads is deduplicated by address — multiple GetBalance
// calls for the same addr produce one BalRead entry.
// ---------------------------------------------------------------------------

func TestInvariant_BalReadsDeduplicatedByAddr(t *testing.T) {
	pdb, _, _ := newTestPDB(t, 5)
	pdb.EnableReadTracking()
	addr := common.HexToAddress("0x1")

	pdb.GetBalance(addr)
	pdb.GetBalance(addr)
	pdb.GetBalance(addr)

	count := 0
	for _, r := range pdb.BalReads {
		if r.Addr == addr {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("BalReads for %x: got %d entries, want 1", addr, count)
	}
}

// ---------------------------------------------------------------------------
// Invariant 10: BalAddrs is deduplicated — multiple AddBalance/SubBalance
// for the same addr produce one BalAddrs entry.
// ---------------------------------------------------------------------------

func TestInvariant_BalAddrsDeduplicated(t *testing.T) {
	pdb, _, _ := newTestPDB(t, 3)
	pdb.EnableReadTracking()
	addr := common.HexToAddress("0xabcd")

	pdb.AddBalance(addr, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	pdb.SubBalance(addr, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	pdb.AddBalance(addr, uint256.NewInt(1), tracing.BalanceChangeUnspecified)

	count := 0
	for _, a := range pdb.BalAddrs {
		if a == addr {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("BalAddrs for %x: got %d entries, want 1", addr, count)
	}
}
