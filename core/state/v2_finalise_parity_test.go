package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

// V2 settles each tx with FinaliseFast, serial execution with Finalise. The
// two must agree on the state root for a self-destructed account, which
// changes at Amsterdam (EIP-8246: a leftover balance is kept, not deleted).
func TestFinaliseFastSelfDestructParity(t *testing.T) {
	t.Parallel()

	addr := common.HexToAddress("0xaa")
	tests := []struct {
		name        string
		isAmsterdam bool
		balance     uint64
	}{
		{"pre_amsterdam/balance", false, 100},
		{"pre_amsterdam/no_balance", false, 0},
		{"amsterdam/balance", true, 100},
		{"amsterdam/no_balance", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Seed a contract with nonce, code and storage, then commit it so
			// both paths start from the same on-disk root.
			tdb := triedb.NewDatabase(rawdb.NewMemoryDatabase(), triedb.HashDefaults)
			sdb, err := New(types.EmptyRootHash, NewDatabase(tdb, nil))
			if err != nil {
				t.Fatal(err)
			}
			sdb.SetNonce(addr, 5, tracing.NonceChangeUnspecified)
			sdb.SetCode(addr, []byte{0x60, 0x00}, tracing.CodeChangeUnspecified)
			sdb.SetState(addr, common.Hash{1}, common.Hash{2})
			sdb.SetBalance(addr, uint256.NewInt(tt.balance), tracing.BalanceChangeUnspecified)
			root, err := sdb.Commit(0, true, false)
			if err != nil {
				t.Fatal(err)
			}

			open := func() *StateDB {
				db, err := New(root, NewDatabase(tdb, nil))
				if err != nil {
					t.Fatal(err)
				}
				return db
			}
			rules := params.Rules{IsAmsterdam: tt.isAmsterdam}

			serial := open()
			serial.Prepare(rules, common.Address{}, common.Address{}, nil, nil, nil)
			serial.SelfDestruct(addr)
			serial.Finalise(true)

			// V2's final StateDB is never prepared, so it only learns the
			// fork from the flag.
			fast := open()
			fast.SelfDestruct(addr)
			fast.FinaliseFast(true, tt.isAmsterdam)

			if s, f := serial.GetBalance(addr), fast.GetBalance(addr); !s.Eq(f) {
				t.Fatalf("balance mismatch: serial=%v fast=%v", s, f)
			}
			if s, f := serial.Exist(addr), fast.Exist(addr); s != f {
				t.Fatalf("existence mismatch: serial=%v fast=%v", s, f)
			}
			if s, f := serial.IntermediateRoot(true), fast.IntermediateRoot(true); s != f {
				t.Fatalf("root mismatch: serial=%x fast=%x", s, f)
			}
		})
	}
}
