package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
)

// This file is the symmetric "every PDB getter is tracked" parametric
// test. The point is to encode the load-bearing invariant that any read
// path which can transitively touch MVStore MUST:
//
//   1. Record exactly one read for the queried key in StoreReads
//      (or one BalReads entry for balance reads), so validation can
//      revisit that key when an upstream tx commits / re-executes.
//   2. Surface ESTIMATE / PROVISIONAL writers as "no entry" — never
//      return their stale value to the EVM.
//   3. Use WriterIdx=-1 when falling back to base, so a writer that
//      appears later vfails this tx.
//
// The original GetCodeHash bug (untracked store.Read with no ESTIMATE
// filter) slipped past per-getter unit tests because each one verified
// the *return value* but not the side effect (read tracking). A single
// table sweeping every getter would have caught it; this test is that
// table. Adding a new PDB getter? Add a row here.

// readOpKind describes which read-set bucket a getter populates.
type readOpKind int

const (
	storeRead readOpKind = iota
	balanceRead
)

type readOp struct {
	name string
	kind readOpKind
	// key returns the MVStore key the getter is expected to track
	// (storeRead only).
	key func(addr common.Address, slot common.Hash) blockstm.Key
	// committedVal is the value to seed at txIdx=2 for the COMMITTED
	// scenario. Type must match what the getter expects to receive.
	committedVal any
	// invoke calls the getter under test, ignoring the return value
	// (we assert on read-set side effects, not return values).
	invoke func(p *ParallelStateDB, addr common.Address, slot common.Hash)
}

// readOps enumerates every PDB getter that can read MVStore /
// MVBalanceStore. New getters MUST be added here or the symmetry is
// lost. Address keys (Exist / HasSelfDestructed) read-set behaviour is
// covered separately because they can record secondary reads (createKey
// → balance fallback) that don't fit the "exactly one read" shape.
var readOps = []readOp{
	{
		name: "GetNonce",
		kind: storeRead,
		key: func(a common.Address, _ common.Hash) blockstm.Key {
			return blockstm.NewSubpathKey(a, NoncePath)
		},
		committedVal: uint64(7),
		invoke: func(p *ParallelStateDB, a common.Address, _ common.Hash) {
			_ = p.GetNonce(a)
		},
	},
	{
		name: "GetCode",
		kind: storeRead,
		key: func(a common.Address, _ common.Hash) blockstm.Key {
			return blockstm.NewSubpathKey(a, CodePath)
		},
		committedVal: []byte{0x60, 0x00, 0xfd},
		invoke: func(p *ParallelStateDB, a common.Address, _ common.Hash) {
			_ = p.GetCode(a)
		},
	},
	{
		name: "GetCodeSize",
		kind: storeRead,
		key: func(a common.Address, _ common.Hash) blockstm.Key {
			return blockstm.NewSubpathKey(a, CodePath)
		},
		committedVal: []byte{0x60, 0x00, 0xfd},
		invoke: func(p *ParallelStateDB, a common.Address, _ common.Hash) {
			_ = p.GetCodeSize(a)
		},
	},
	{
		name: "GetCodeHash",
		kind: storeRead,
		key: func(a common.Address, _ common.Hash) blockstm.Key {
			return blockstm.NewSubpathKey(a, CodePath)
		},
		committedVal: []byte{0x60, 0x00, 0xfd},
		invoke: func(p *ParallelStateDB, a common.Address, _ common.Hash) {
			_ = p.GetCodeHash(a)
		},
	},
	{
		name: "GetState",
		kind: storeRead,
		key: func(a common.Address, slot common.Hash) blockstm.Key {
			return blockstm.NewStateKey(a, slot)
		},
		committedVal: common.HexToHash("0xff"),
		invoke: func(p *ParallelStateDB, a common.Address, slot common.Hash) {
			_ = p.GetState(a, slot)
		},
	},
	{
		name: "GetCommittedState",
		kind: storeRead,
		key: func(a common.Address, slot common.Hash) blockstm.Key {
			return blockstm.NewStateKey(a, slot)
		},
		committedVal: common.HexToHash("0xff"),
		invoke: func(p *ParallelStateDB, a common.Address, slot common.Hash) {
			_ = p.GetCommittedState(a, slot)
		},
	},
	{
		name: "GetBalance",
		kind: balanceRead,
		// committedVal: balance reads use MVBalanceStore — value seeded
		// via WriteDelta in the COMMITTED case below.
		invoke: func(p *ParallelStateDB, a common.Address, _ common.Hash) {
			_ = p.GetBalance(a)
		},
	},
}

func countBalanceReadsFor(pdb *ParallelStateDB, addr common.Address) int {
	n := 0
	for _, rd := range pdb.BalReads {
		if rd.Addr == addr {
			n++
		}
	}
	return n
}

// TestPDB_AllGetters_TrackReads sweeps every read getter against three
// scenarios — committed upstream writer, ESTIMATE-flagged writer, and
// no upstream entry — and asserts read tracking matches the bug-free
// contract:
//
//	Committed → exactly 1 read with WriterIdx == upstream tx
//	ESTIMATE  → exactly 1 read with WriterIdx == -1 (falls back to base)
//	NoEntry   → exactly 1 read with WriterIdx == -1 (base)
//
// This is the test that would have caught Fix #1 (untracked
// GetCodeHash + ESTIMATE leak) on the day it was introduced.
func TestPDB_AllGetters_TrackReads(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	slot := common.HexToHash("0x42")
	const writerTx = 2
	const readerTx = 5

	for _, op := range readOps {
		t.Run(op.name+"/Committed", func(t *testing.T) {
			pdb, store, bals := newTestPDB(t, readerTx)
			pdb.EnableReadTracking()

			switch op.kind {
			case storeRead:
				key := op.key(addr, slot)
				store.WriteInc(key, writerTx, 0, op.committedVal)
				op.invoke(pdb, addr, slot)
				assertOneStoreRead(t, pdb, key, writerTx, 0)
			case balanceRead:
				bals.WriteDelta(addr, writerTx, uint256.NewInt(11), nil)
				op.invoke(pdb, addr, slot)
				if got := countBalanceReadsFor(pdb, addr); got != 1 {
					t.Fatalf("BalReads for %x: got %d, want 1", addr, got)
				}
			}
		})

		t.Run(op.name+"/Estimate", func(t *testing.T) {
			pdb, store, bals := newTestPDB(t, readerTx)
			pdb.EnableReadTracking()

			switch op.kind {
			case storeRead:
				key := op.key(addr, slot)
				store.WriteInc(key, writerTx, 0, op.committedVal)
				store.MarkEstimate(writerTx, []blockstm.Key{key})
				op.invoke(pdb, addr, slot)
				// ESTIMATE on first incarnation must fall back to base —
				// never surface the writer.
				assertOneStoreRead(t, pdb, key, -1, 0)
			case balanceRead:
				// Balance reads have no ESTIMATE concept (commutative
				// deltas), so this scenario degenerates to the
				// Committed case but with the entry zeroed via
				// ZeroDelta — still must produce exactly one BalReads
				// entry.
				bals.WriteDelta(addr, writerTx, uint256.NewInt(11), nil)
				bals.ZeroDelta(writerTx, []common.Address{addr})
				op.invoke(pdb, addr, slot)
				if got := countBalanceReadsFor(pdb, addr); got != 1 {
					t.Fatalf("BalReads for %x: got %d, want 1", addr, got)
				}
			}
		})

		t.Run(op.name+"/NoEntry", func(t *testing.T) {
			pdb, _, _ := newTestPDB(t, readerTx)
			pdb.EnableReadTracking()
			switch op.kind {
			case storeRead:
				key := op.key(addr, slot)
				op.invoke(pdb, addr, slot)
				assertOneStoreRead(t, pdb, key, -1, 0)
			case balanceRead:
				op.invoke(pdb, addr, slot)
				if got := countBalanceReadsFor(pdb, addr); got != 1 {
					t.Fatalf("BalReads for %x: got %d, want 1", addr, got)
				}
			}
		})
	}
}

// assertOneStoreRead pins the "exactly one read for key, with the
// expected writer" contract. Other reads on other keys are fine —
// we only constrain the target key.
func assertOneStoreRead(t *testing.T, pdb *ParallelStateDB, key blockstm.Key, wantWriter, wantInc int) {
	t.Helper()
	var matches []StoreReadDesc
	for _, rd := range pdb.StoreReads {
		if rd.Key == key {
			matches = append(matches, rd)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("StoreReads for %x: got %d entries, want exactly 1 (all=%v)", key, len(matches), pdb.StoreReads)
	}
	got := matches[0]
	if got.WriterIdx != wantWriter || got.WriterInc != wantInc {
		t.Fatalf("StoreReads for %x: writer=(%d,%d), want (%d,%d)",
			key, got.WriterIdx, got.WriterInc, wantWriter, wantInc)
	}
}

// TestPDB_AllGetters_AtTxZero pins the boundary case where the upstream
// writer is at txIdx == 0. The `writerIdx < 0` check in readStoreWait
// (and the matching `writer >= 0` check in readForValidate) must treat
// 0 as a legitimate writer, not as "no writer". A `<` → `<=` mutant on
// either site silently drops tx-0's value into the base-read fallback.
//
// Both COMMITTED and ESTIMATE scenarios are exercised: with `<= 0` the
// COMMITTED case still returns the right value (writerIdx happens to
// flow through the early-return branch) — only the ESTIMATE case
// surfaces the bug, where the mutant leaks the stale value instead of
// falling back to base.
func TestPDB_AllGetters_AtTxZero(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	slot := common.HexToHash("0x42")

	for _, op := range readOps {
		if op.kind != storeRead {
			continue // balance reads have a different writer-id contract
		}
		t.Run(op.name+"/Committed", func(t *testing.T) {
			pdb, store, _ := newTestPDB(t, 5)
			pdb.EnableReadTracking()
			key := op.key(addr, slot)
			store.WriteInc(key, 0, 0, op.committedVal) // writer at tx 0

			op.invoke(pdb, addr, slot)

			assertOneStoreRead(t, pdb, key, 0, 0)
		})

		t.Run(op.name+"/Estimate", func(t *testing.T) {
			pdb, store, _ := newTestPDB(t, 5)
			pdb.EnableReadTracking()
			key := op.key(addr, slot)
			store.WriteInc(key, 0, 0, op.committedVal)
			store.MarkEstimate(0, []blockstm.Key{key})

			op.invoke(pdb, addr, slot)

			// ESTIMATE at writer=0 must NOT leak the stale value —
			// fall back to base, recorded as WriterIdx=-1.
			assertOneStoreRead(t, pdb, key, -1, 0)
		})
	}
}

// TestPDB_AllGetters_ValidateRoundTrip wires the symmetry property to
// Validate(): every getter should produce reads that pass validation
// when the upstream state hasn't moved, and fail when a writer changes
// underneath. This is the diff-detector for "I forgot recordStoreRead"
// regressions that don't show up in single-call assertions.
func TestPDB_AllGetters_ValidateRoundTrip(t *testing.T) {
	addr := common.HexToAddress("0xabcd")
	slot := common.HexToHash("0x42")
	const readerTx = 5

	for _, op := range readOps {
		t.Run(op.name, func(t *testing.T) {
			pdb, store, bals := newTestPDB(t, readerTx)
			pdb.EnableReadTracking()

			op.invoke(pdb, addr, slot)
			if !pdb.Validate() {
				t.Fatalf("%s: Validate must pass on a fresh read", op.name)
			}

			// Mutate upstream state so the read becomes stale.
			switch op.kind {
			case storeRead:
				store.WriteInc(op.key(addr, slot), 3, 0, op.committedVal)
			case balanceRead:
				bals.WriteDelta(addr, 3, uint256.NewInt(99), nil)
			}

			if pdb.Validate() {
				t.Fatalf("%s: Validate must fail after upstream writer commits a value", op.name)
			}
		})
	}
}
