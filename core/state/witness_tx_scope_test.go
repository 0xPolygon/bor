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

package state

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// witnessScopeState returns a committed state with a live witness and trie
// prefetcher, the configuration a block producer runs in.
func witnessScopeState(t *testing.T) (*StateDB, []common.Address, map[common.Address]common.Hash, []common.Hash) {
	t.Helper()

	state, addrs, roots, keys := benchState(4, 4)

	witness, err := stateless.NewWitness(&types.Header{Number: big.NewInt(1)}, nil)
	if err != nil {
		t.Fatalf("witness: %v", err)
	}
	state.StartPrefetcher("witness-scope-test", witness, nil)
	t.Cleanup(state.StopPrefetcher)

	return state, addrs, roots, keys
}

// TestWitnessTxScopeIsWitnessGated pins that the scope is inert without a
// witness, so every non-producing path keeps today's behaviour exactly.
func TestWitnessTxScopeIsWitnessGated(t *testing.T) {
	state, _, _, _ := benchState(2, 2)
	state.StartPrefetcher("no-witness", nil, nil)
	defer state.StopPrefetcher()

	state.BeginWitnessTx()

	if state.witnessTx != nil {
		t.Fatal("BeginWitnessTx opened a scope on a state with no witness")
	}

	// Commit and Discard must both stay safe with no scope open.
	state.CommitWitnessTx()
	state.DiscardWitnessTx()
}

// TestCommitWitnessTxAppliesDeferredReads checks that a committed transaction's
// held-back bookkeeping is applied rather than dropped.
func TestCommitWitnessTxAppliesDeferredReads(t *testing.T) {
	state, addrs, roots, keys := witnessScopeState(t)

	ghost := common.BytesToAddress([]byte("does-not-exist"))

	state.BeginWitnessTx()
	state.recordWitnessAccountRead(addrs[0])
	state.recordWitnessSlotRead(crypto.Keccak256Hash(addrs[0][:]), roots[addrs[0]], addrs[0], keys[0])
	state.recordNonExistentRead(ghost)

	if got := len(state.witnessTx.accounts); got != 1 {
		t.Fatalf("buffered accounts = %d, want 1", got)
	}
	if got := len(state.witnessTx.slots); got != 1 {
		t.Fatalf("buffered slots = %d, want 1", got)
	}
	if _, ok := state.nonExistentReads[ghost]; ok {
		t.Fatal("non-existent read reached the StateDB before the transaction committed")
	}

	state.CommitWitnessTx()

	if state.witnessTx != nil {
		t.Fatal("CommitWitnessTx left the scope open")
	}
	if _, ok := state.nonExistentReads[ghost]; !ok {
		t.Fatal("CommitWitnessTx dropped the non-existent read")
	}
}

// TestDiscardWitnessTxEvictsReadCaches is the property the whole scope exists
// for: a dropped transaction must leave no cached read behind, because a later
// included read hitting that cache would skip the prefetch call that puts the
// trie path into the witness -- producing a witness that is too SMALL.
func TestDiscardWitnessTxEvictsReadCaches(t *testing.T) {
	state, addrs, roots, keys := witnessScopeState(t)

	addr, key := addrs[0], keys[0]

	state.BeginWitnessTx()

	// Load the object and its slot the way a transaction's reads would.
	state.GetBalance(addr)
	state.GetState(addr, key)
	state.recordWitnessAccountRead(addr)
	state.recordWitnessSlotRead(crypto.Keccak256Hash(addr[:]), roots[addr], addr, key)

	if state.stateObjects[addr] == nil {
		t.Fatal("precondition: account was not loaded")
	}

	state.DiscardWitnessTx()

	if state.witnessTx != nil {
		t.Fatal("DiscardWitnessTx left the scope open")
	}
	if obj := state.stateObjects[addr]; obj != nil {
		if _, ok := obj.originStorage[key]; ok {
			t.Error("DiscardWitnessTx left the dropped transaction's slot in originStorage")
		}
	}
}

// TestDiscardWitnessTxKeepsDirtyObjects guards the other side: an account an
// INCLUDED transaction already mutated must survive the discard, or the block
// loses state it legitimately needs.
func TestDiscardWitnessTxKeepsDirtyObjects(t *testing.T) {
	state, addrs, _, _ := witnessScopeState(t)

	addr := addrs[0]

	// An earlier, INCLUDED transaction mutates the account. Finalise is what
	// makes it dirty for this purpose -- s.mutations fills there, and the miner
	// calls it once per applied transaction. An account touched only by a
	// still-in-flight transaction has no mutations entry, which is exactly why
	// the discard may evict it.
	state.SetBalance(addr, uint256.NewInt(99), tracing.BalanceChangeUnspecified)
	state.Finalise(true)

	state.BeginWitnessTx()
	state.recordWitnessAccountRead(addr)
	state.DiscardWitnessTx()

	if state.stateObjects[addr] == nil {
		t.Fatal("DiscardWitnessTx evicted an account an included transaction had mutated")
	}
	if got := state.GetBalance(addr); got.Uint64() != 99 {
		t.Fatalf("balance = %d, want 99", got.Uint64())
	}
}

// TestIntermediateRootClosesOpenScope covers the fail-safe: a caller bug that
// leaves a scope open at root computation must commit the staged work, not
// drop it. Too much witness is merely large; too little fails stateless
// execution outright.
func TestIntermediateRootClosesOpenScope(t *testing.T) {
	state, addrs, _, _ := witnessScopeState(t)

	ghost := common.BytesToAddress([]byte("does-not-exist"))

	state.BeginWitnessTx()
	state.recordWitnessAccountRead(addrs[0])
	state.recordNonExistentRead(ghost)

	state.IntermediateRoot(true)

	if state.witnessTx != nil {
		t.Fatal("IntermediateRoot left the witness scope open")
	}
	if _, ok := state.nonExistentReads[ghost]; !ok {
		t.Fatal("IntermediateRoot dropped the staged non-existent read")
	}
}
