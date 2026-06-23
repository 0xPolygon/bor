package state

import (
	"fmt"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/triedb"
)

// This file pins the contract V2 settle relies on: the *Direct family of
// setters (SetNonceDirect, AddBalanceDirect, …) must produce the SAME
// state root as the journaled equivalents (SetNonce + Finalise, …) on
// the underlying StateDB.
//
// V2 SettleTo bypasses journaling for performance (no revert needed at
// settle time) and for hook-firing reasons (tracing fires per-tx via the
// EVM, not per-Direct call). If upstream go-ethereum changes journaled
// Set* to do extra side-effects that affect the trie — say, a new EIP
// adds a field — the Direct variants must mirror that or the V2 state
// root will diverge.
//
// Each subtest runs the same logical write through both paths against
// fresh StateDBs at the same root, then compares the resulting state
// roots. Mismatches mean V2 settle would produce a wrong block-final
// state root.

// freshSDB returns a fresh in-memory StateDB at the empty root.
func freshSDB(t *testing.T) *StateDB {
	t.Helper()
	memdb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memdb, triedb.HashDefaults)
	sdb, err := New(types.EmptyRootHash, NewDatabase(tdb, nil))
	if err != nil {
		t.Fatal(err)
	}
	return sdb
}

// finalize runs the post-write phase (Finalise on the journaled path,
// FinaliseFastWithPrefetch on the Direct path) and returns the state
// root. Uses IntermediateRoot rather than Commit to keep the test
// in-memory and deterministic.
func finalizeAndRoot(t *testing.T, sdb *StateDB, fast bool) common.Hash {
	t.Helper()
	if fast {
		sdb.FinaliseFastWithPrefetch(true)
	} else {
		sdb.Finalise(true)
	}
	return sdb.IntermediateRoot(true)
}

// runParity is the workhorse: applies journaledOp to one fresh SDB and
// directOp to another, runs the appropriate finalize on each, then
// asserts the resulting roots match. seedAccount preconditions both
// SDBs (e.g., create the account + give it a balance so a SubBalance
// has something to subtract from).
func runParity(
	t *testing.T,
	name string,
	seed func(*StateDB),
	journaledOp func(*StateDB),
	directOp func(*StateDB),
) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		a := freshSDB(t)
		if seed != nil {
			seed(a)
		}
		journaledOp(a)
		rootA := finalizeAndRoot(t, a, false)

		b := freshSDB(t)
		if seed != nil {
			seed(b)
		}
		directOp(b)
		rootB := finalizeAndRoot(t, b, true)

		if rootA != rootB {
			t.Fatalf("state-root drift between journaled and Direct paths: journaled=%s direct=%s", rootA.Hex(), rootB.Hex())
		}
	})
}

// TestDirectSetterParity_SetNonce: SetNonce(addr, n) must produce the
// same trie content as SetNonceDirect(addr, n).
func TestDirectSetterParity_SetNonce(t *testing.T) {
	addr := common.HexToAddress("0xa1")
	const newNonce uint64 = 9

	// Pre-condition both SDBs with a starting nonce of 3 so the change
	// is observable.
	seed := func(s *StateDB) {
		s.CreateAccount(addr)
		s.SetNonce(addr, 3, tracing.NonceChangeUnspecified)
		s.Finalise(true)
	}

	runParity(t, "SetNonce",
		seed,
		func(s *StateDB) { s.SetNonce(addr, newNonce, tracing.NonceChangeUnspecified) },
		func(s *StateDB) { s.SetNonceDirect(addr, newNonce) },
	)
}

// TestDirectSetterParity_AddBalance pins the AddBalanceDirect path —
// including the EIP-161 zero-amount touch case which Direct must
// preserve to keep empty-account-deletion semantics aligned with the
// journaled path.
func TestDirectSetterParity_AddBalance(t *testing.T) {
	addr := common.HexToAddress("0xa2")

	// Non-zero add against a pre-existing account.
	seed1 := func(s *StateDB) {
		s.CreateAccount(addr)
		s.AddBalance(addr, uint256.NewInt(1000), tracing.BalanceChangeUnspecified)
		s.Finalise(true)
	}
	delta := uint256.NewInt(250)
	runParity(t, "AddBalance/NonZero",
		seed1,
		func(s *StateDB) { s.AddBalance(addr, delta, tracing.BalanceChangeUnspecified) },
		func(s *StateDB) { s.AddBalanceDirect(addr, delta) },
	)

	// Zero add on an account that pre-existed in the trie as empty.
	// The seed commits a non-empty account to the trie, then a tx
	// drains it to empty in a separate Commit. After that Commit the
	// trie still has an EMPTY account at addr (we use Finalise(false)
	// so EIP-161 doesn't delete it). At apply time, AddBalance(0)
	// on the journaled path must touch + delete the empty account;
	// AddBalanceDirect(0) must do the same. With either side missing
	// the touch, the trie diverges by exactly one account.
	seedEmptyButPresent := func(s *StateDB) {
		s.CreateAccount(addr)
		s.AddBalance(addr, uint256.NewInt(7), tracing.BalanceChangeUnspecified)
		s.Finalise(true)
		_ = s.IntermediateRoot(true)
		s.SubBalance(addr, uint256.NewInt(7), tracing.BalanceChangeUnspecified)
		s.Finalise(false) // keep the now-empty account in pending state
		_ = s.IntermediateRoot(false)
	}
	zero := uint256.NewInt(0)
	runParity(t, "AddBalance/ZeroOnEmptyExisting",
		seedEmptyButPresent,
		func(s *StateDB) { s.AddBalance(addr, zero, tracing.BalanceChangeUnspecified) },
		func(s *StateDB) { s.AddBalanceDirect(addr, zero) },
	)
}

func TestDirectSetterParity_SubBalance(t *testing.T) {
	addr := common.HexToAddress("0xa3")
	seed := func(s *StateDB) {
		s.CreateAccount(addr)
		s.AddBalance(addr, uint256.NewInt(1000), tracing.BalanceChangeUnspecified)
		s.Finalise(true)
	}
	delta := uint256.NewInt(123)
	runParity(t, "SubBalance",
		seed,
		func(s *StateDB) { s.SubBalance(addr, delta, tracing.BalanceChangeUnspecified) },
		func(s *StateDB) { s.SubBalanceDirect(addr, delta) },
	)
}

// TestDirectSetterParity_SetStorageDirectWithOrigins exercises the
// origin-preserving Direct path V2 actually uses in SettleTo.
// origins seeded from the snapshot ensure the uncommittedStorage path
// matches the journaled SetState path's GetCommittedState lookup.
func TestDirectSetterParity_SetStorageDirectWithOrigins(t *testing.T) {
	addr := common.HexToAddress("0xa6")
	slot := common.HexToHash("0x02")
	prev := common.HexToHash("0xaa")
	next := common.HexToHash("0xbb")

	seed := func(s *StateDB) {
		s.CreateAccount(addr)
		s.AddBalance(addr, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
		s.SetState(addr, slot, prev)
		s.Finalise(true)
		s.IntermediateRoot(true) // promote so origin lookup stable
	}

	runParity(t, "SetStorageDirectWithOrigins",
		seed,
		func(s *StateDB) { s.SetState(addr, slot, next) },
		func(s *StateDB) {
			s.SetStorageDirectWithOrigins(addr,
				map[common.Hash]common.Hash{slot: next},
				map[common.Hash]common.Hash{slot: prev},
			)
		},
	)
}

// TestDirectSetterParity_MultipleSlots covers the bulk-storage-write path
// V2 actually exercises during settle.
func TestDirectSetterParity_MultipleSlots(t *testing.T) {
	addr := common.HexToAddress("0xa7")
	slots := map[common.Hash]common.Hash{
		common.HexToHash("0x01"): common.HexToHash("0xaa"),
		common.HexToHash("0x02"): common.HexToHash("0xbb"),
		common.HexToHash("0x03"): common.HexToHash("0xcc"),
	}
	origins := map[common.Hash]common.Hash{
		common.HexToHash("0x01"): {},
		common.HexToHash("0x02"): {},
		common.HexToHash("0x03"): {},
	}
	seed := func(s *StateDB) {
		s.CreateAccount(addr)
		s.AddBalance(addr, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
		s.Finalise(true)
	}
	runParity(t, "Storage/MultiSlot",
		seed,
		func(s *StateDB) {
			for k, v := range slots {
				s.SetState(addr, k, v)
			}
		},
		func(s *StateDB) {
			s.SetStorageDirectWithOrigins(addr, slots, origins)
		},
	)
}

// TestDirectSetterParity_CombinedTx exercises a realistic settle pattern:
// nonce + balance + storage + code, all touching the same address. The
// stress-test for "do all the Direct variants compose to the same trie
// state as the journaled equivalents".
func TestDirectSetterParity_CombinedTx(t *testing.T) {
	addr := common.HexToAddress("0xa8")
	slot := common.HexToHash("0x01")
	val := common.HexToHash("0xff")
	code := []byte{0x60, 0x00, 0x60, 0x01}
	delta := uint256.NewInt(42)

	seed := func(s *StateDB) {
		s.CreateAccount(addr)
		s.AddBalance(addr, uint256.NewInt(1000), tracing.BalanceChangeUnspecified)
		s.SetNonce(addr, 1, tracing.NonceChangeUnspecified)
		s.Finalise(true)
	}

	runParity(t, "Combined",
		seed,
		func(s *StateDB) {
			s.SetNonce(addr, 5, tracing.NonceChangeUnspecified)
			s.AddBalance(addr, delta, tracing.BalanceChangeUnspecified)
			s.SetState(addr, slot, val)
			s.SetCode(addr, code, tracing.CodeChangeUnspecified)
		},
		func(s *StateDB) {
			s.SetNonceDirect(addr, 5)
			s.AddBalanceDirect(addr, delta)
			s.SetStorageDirectWithOrigins(addr,
				map[common.Hash]common.Hash{slot: val},
				map[common.Hash]common.Hash{slot: {}},
			)
			s.SetCode(addr, code, tracing.CodeChangeUnspecified) // SetCode is shared (no Direct variant)
		},
	)
}

// TestDirectSetterParity_PanicOnNilObject is a sanity check: every
// Direct setter must short-circuit when getOrNewStateObject returns nil
// (e.g., a destructed account). If a setter forgets the nil-check, V2
// settle would crash on otherwise-valid blocks.
func TestDirectSetterParity_PanicOnNilObject(t *testing.T) {
	addr := common.HexToAddress("0xa9")

	cases := []struct {
		name string
		fn   func(*StateDB)
	}{
		{"SetNonceDirect", func(s *StateDB) { s.SetNonceDirect(addr, 1) }},
		{"AddBalanceDirect", func(s *StateDB) { s.AddBalanceDirect(addr, uint256.NewInt(1)) }},
		{"SubBalanceDirect", func(s *StateDB) { s.SubBalanceDirect(addr, uint256.NewInt(1)) }},
		{"SetStorageDirectWithOrigins", func(s *StateDB) {
			s.SetStorageDirectWithOrigins(addr,
				map[common.Hash]common.Hash{common.HexToHash("0x1"): common.HexToHash("0x2")},
				map[common.Hash]common.Hash{common.HexToHash("0x1"): {}},
			)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := freshSDB(t)
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("%s panicked on a non-existent address: %v", c.name, r)
				}
			}()
			c.fn(s)
		})
	}
	_ = fmt.Sprint(addr) // silence unused-import lint when no failures
}
