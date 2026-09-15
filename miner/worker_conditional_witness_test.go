// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

// TestValidateConditionalOptionsIsWitnessNeutral drives the real producer call
// site, which the state-level test cannot reach.
//
// PIP-15 known-accounts validation is admission control: the conditional
// options arrive with the submission and never travel in the block, so no
// importing node re-runs the check. Its reads must leave the witness exactly as
// they found it — including when validation SUCCEEDS and the transaction goes
// on to be included. Committing them instead would make the producer's witness
// a strict superset of every importer's, which is what WIT/2's cross-peer
// page-count check punishes.
//
// Deleting the DiscardWitnessTx call leaves the scope open, and
// IntermediateRoot's fail-safe then folds the staged validation reads into the
// witness — which this test sees as a witness larger than the baseline.
func TestValidateConditionalOptionsIsWitnessNeutral(t *testing.T) {
	t.Parallel()

	var (
		addr = common.BytesToAddress([]byte("conditional-account"))
		key  = common.BytesToHash([]byte("slot"))
		val  = common.BytesToHash([]byte("value"))
		coin = common.BytesToAddress([]byte("coinbase"))
	)

	// A committed state holding the slot the condition will check.
	db := state.NewDatabaseForTesting()

	seed, err := state.New(types.EmptyRootHash, db)
	if err != nil {
		t.Fatalf("seed state: %v", err)
	}

	seed.SetBalance(addr, uint256.NewInt(1), tracing.BalanceChangeUnspecified)
	seed.SetState(addr, key, val)
	seed.SetBalance(coin, uint256.NewInt(1), tracing.BalanceChangeUnspecified)

	root, err := seed.Commit(0, false, false)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	build := func(withOptions bool) int {
		sdb, err := state.New(root, db)
		if err != nil {
			t.Fatalf("state: %v", err)
		}

		witness, err := stateless.NewWitness(&types.Header{Number: big.NewInt(1)}, nil)
		if err != nil {
			t.Fatalf("witness: %v", err)
		}

		sdb.StartPrefetcher("conditional", witness, nil)
		defer sdb.StopPrefetcher()

		env := &environment{
			state:  sdb,
			header: &types.Header{Number: big.NewInt(1), Time: 100},
		}

		tx := types.NewTx(&types.LegacyTx{Nonce: 0, Gas: 21000, Value: big.NewInt(0)})
		if withOptions {
			// A condition that PASSES: the expected value is the stored one.
			tx.PutOptions(&types.OptionsPIP15{
				KnownAccounts: types.KnownAccounts{
					addr: &types.Value{Storage: map[common.Hash]common.Hash{key: val}},
				},
			})
		}

		if err := validateConditionalOptions(env, tx); err != nil {
			t.Fatalf("validation must succeed, got %v", err)
		}

		// The block's own work, identical either way.
		sdb.SetBalance(coin, uint256.NewInt(9), tracing.BalanceChangeUnspecified)
		sdb.IntermediateRoot(true)

		return len(witness.State)
	}

	baseline := build(false)
	validated := build(true)

	if baseline == 0 {
		t.Fatal("control failed: the baseline witness is empty, so this test proves nothing")
	}

	if validated != baseline {
		t.Errorf("known-accounts validation changed the witness: %d nodes, want %d — "+
			"its reads are producer-side admission control and no importer reproduces them",
			validated, baseline)
	}
}
