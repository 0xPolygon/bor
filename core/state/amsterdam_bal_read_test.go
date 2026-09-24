package state

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

// countingReader counts storage reads reaching the underlying reader.
type countingReader struct {
	Reader
	storageReads int
}

func (r *countingReader) Storage(addr common.Address, slot common.Hash) (common.Hash, error) {
	r.storageReads++
	return r.Reader.Storage(addr, slot)
}

// A destructed account's slot read in GetCommittedState must not reach the
// reader at any height. Upstream #34776 records the access for the block-level
// access list in memory instead of reading it, so the storage trie is never
// walked for it and the witness never gains the account's storage proof path.
// A reader access here would put nodes into the witness that a producer on an
// older version never recorded, and fail the import on the consuming side.
func TestDestructedSlotReadSkipsReader(t *testing.T) {
	t.Parallel()

	addr := common.HexToAddress("0xdead")
	slot := common.HexToHash("0x01")

	newDestructedState := func(t *testing.T) (*StateDB, *countingReader) {
		t.Helper()

		db := NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), nil)
		inner, err := db.Reader(types.EmptyRootHash)
		if err != nil {
			t.Fatalf("reader: %v", err)
		}
		counting := &countingReader{Reader: inner}

		statedb, err := NewWithReader(types.EmptyRootHash, db, counting)
		if err != nil {
			t.Fatalf("statedb: %v", err)
		}

		// Put the account in the destructed set so GetCommittedState takes the
		// branch under test.
		obj := statedb.getOrNewStateObject(addr)
		statedb.stateObjectsDestruct[addr] = obj
		counting.storageReads = 0

		return statedb, counting
	}

	// Cover both sides of a real Amsterdam activation boundary, since the
	// access list the read once served exists only after it.
	const forkBlock = 100

	cfg := *params.TestChainConfig
	cfg.AmsterdamBlock = big.NewInt(forkBlock)

	for _, tc := range []struct {
		name      string
		number    int64
		amsterdam bool
	}{
		{name: "N-1", number: forkBlock - 1, amsterdam: false},
		{name: "N", number: forkBlock, amsterdam: true},
		{name: "N+1", number: forkBlock + 1, amsterdam: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			statedb, counting := newDestructedState(t)
			rules := cfg.Rules(big.NewInt(tc.number), false, 0)
			if rules.IsAmsterdam != tc.amsterdam {
				t.Fatalf("IsAmsterdam at block %d = %v, want %v", tc.number, rules.IsAmsterdam, tc.amsterdam)
			}
			statedb.Prepare(rules, addr, common.Address{}, nil, nil, nil)

			if got := statedb.GetCommittedState(addr, slot); got != (common.Hash{}) {
				t.Errorf("expected empty slot, got %x", got)
			}
			if counting.storageReads != 0 {
				t.Errorf("reader accesses = %d, want 0", counting.storageReads)
			}
		})
	}
}
