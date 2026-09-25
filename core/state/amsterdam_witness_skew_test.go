package state

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/triedb"
)

// Witness content is not committed to any header field, so a producer and a
// consumer disagreeing about it is invisible to consensus: the consumer simply
// demands nodes the producer never recorded and fails the import. A destructed
// account's slot read used to walk its storage trie for the EIP-7928 access
// list, which would have pulled that proof path into the witness after
// Amsterdam only. Upstream #34776 replaced the read with in-memory access
// tracking, so the witness must be identical on both sides of the fork. This
// test pins that, so a future change cannot quietly reintroduce the read.
func TestDestructedReadWitnessIsForkIndependent(t *testing.T) {
	t.Parallel()

	var (
		contract = common.BytesToAddress([]byte("destructed-contract"))
		readSlot = common.HexToHash("0x01")
	)

	memDb := rawdb.NewMemoryDatabase()
	tdb := triedb.NewDatabase(memDb, triedb.HashDefaults)
	db := NewDatabase(tdb, nil)

	// Commit an account whose storage trie has enough slots to hold internal
	// nodes, so a read has a proof path to pull in rather than a lone leaf.
	setup, err := New(types.EmptyRootHash, db)
	if err != nil {
		t.Fatalf("setup state: %v", err)
	}
	setup.SetBalance(contract, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	for i := int64(1); i <= 32; i++ {
		setup.SetState(contract, common.BigToHash(big.NewInt(i)), common.BigToHash(big.NewInt(i*7)))
	}
	root, err := setup.Commit(0, false, false)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := tdb.Commit(root, false); err != nil {
		t.Fatalf("triedb commit: %v", err)
	}

	// collect drives one committed-state read on a destructed account under the
	// given rules and returns the witness gathered from the reader.
	collect := func(t *testing.T, amsterdam bool) *stateless.Witness {
		t.Helper()

		tr, err := newMPTTrieReader(root, tdb)
		if err != nil {
			t.Fatalf("trie reader: %v", err)
		}
		sdb, err := NewWithReader(root, db, newReader(stubCodeReader{}, tr))
		if err != nil {
			t.Fatalf("state: %v", err)
		}
		witness := &stateless.Witness{
			Headers: []*types.Header{{Number: big.NewInt(0), Root: root}},
			Codes:   make(map[string]struct{}),
			State:   make(map[string]struct{}),
		}
		sdb.SetWitness(witness)

		// Same setup on both sides, so the account-trie access it costs cancels
		// out and any difference left can only come from the storage read.
		obj := sdb.getOrNewStateObject(contract)
		sdb.stateObjectsDestruct[contract] = obj

		cfg := *params.TestChainConfig
		if amsterdam {
			cfg.ShanghaiBlock = big.NewInt(0)
			cfg.AmsterdamBlock = big.NewInt(0)
		}
		sdb.Prepare(cfg.Rules(common.Big0, false, 0), contract, common.Address{}, nil, nil, nil)

		if got := sdb.GetCommittedState(contract, readSlot); got != (common.Hash{}) {
			t.Fatalf("destructed account should read empty, got %x", got)
		}
		if err := sdb.Error(); err != nil {
			t.Fatalf("state error: %v", err)
		}
		sdb.CollectStateWitness()
		return witness
	}

	witnessPre := collect(t, false)
	witnessPost := collect(t, true)

	if len(witnessPre.State) != len(witnessPost.State) {
		t.Fatalf("witness state nodes differ across the fork: pre=%d post=%d", len(witnessPre.State), len(witnessPost.State))
	}
	for node := range witnessPost.State {
		if _, ok := witnessPre.State[node]; !ok {
			t.Fatal("post-Amsterdam witness holds a node the pre-Amsterdam witness lacks")
		}
	}
}
