// Copyright 2024 The go-ethereum Authors
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

package vm

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// A code-hash-keyed jumpdest cache entry can momentarily describe a shorter
// code than the Contract actually carries (an inconsistent (Code, CodeHash)
// pair from parallel EIP-7702 resolution). isCode must recompute from the
// actual code instead of indexing the cached bitvec out of range.
func TestIsCodeRecomputesShortCachedAnalysis(t *testing.T) {
	t.Parallel()

	hash := common.HexToHash("0xdeadbeef")
	cache := newMapJumpDests()
	// Poison the cache: store an analysis sized for an 8-byte code under hash.
	cache.Store(hash, codeBitmap(make([]byte, 8)))

	// The contract carries the same hash but a much longer code, with a
	// JUMPDEST at a position whose byte index is far past the cached bitvec.
	const dest = 771
	longCode := make([]byte, 800)
	longCode[dest] = byte(JUMPDEST)

	c := &Contract{jumpDests: cache, Code: longCode, CodeHash: hash}

	if !c.validJumpdest(uint256.NewInt(dest)) {
		t.Fatalf("expected %d to be a valid jumpdest after recompute", dest)
	}

	// The poisoned entry should have been replaced with one sized for the code.
	if got, _ := cache.Load(hash); len(got) < codeBitmapLen(len(longCode)) {
		t.Fatalf("cache entry not repaired: len=%d, want >= %d", len(got), codeBitmapLen(len(longCode)))
	}
}

// resolveCodeAndHash must follow an EIP-7702 delegation once and return the
// code and hash of the same resolved target, so a Contract's Code and CodeHash
// always agree.
func TestResolveCodeAndHashFollowsDelegation(t *testing.T) {
	t.Parallel()

	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())

	target := common.Address{0xaa}
	targetCode := []byte{byte(PUSH1), 0x01, byte(JUMPDEST), byte(STOP)}
	statedb.CreateAccount(target)
	statedb.SetCode(target, targetCode, tracing.CodeChangeGenesis)

	auth := common.Address{0xbb}
	statedb.CreateAccount(auth)
	statedb.SetCode(auth, types.AddressToDelegation(target), tracing.CodeChangeGenesis)

	evm := NewEVM(BlockContext{BlockNumber: big.NewInt(0)}, statedb, params.MergedTestChainConfig, Config{})

	code, hash := evm.resolveCodeAndHash(auth)
	if !bytes.Equal(code, targetCode) {
		t.Fatalf("code mismatch: got %x, want %x", code, targetCode)
	}
	if want := crypto.Keccak256Hash(targetCode); hash != want {
		t.Fatalf("hash %x does not match the resolved code (keccak %x)", hash, want)
	}
	if want := statedb.GetCodeHash(target); hash != want {
		t.Fatalf("hash %x != target code hash %x", hash, want)
	}
}

// A plain (non-delegated) account resolves to its own code and hash.
func TestResolveCodeAndHashPlainAccount(t *testing.T) {
	t.Parallel()

	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())

	addr := common.Address{0xcc}
	code := []byte{byte(PUSH1), 0x02, byte(STOP)}
	statedb.CreateAccount(addr)
	statedb.SetCode(addr, code, tracing.CodeChangeGenesis)

	evm := NewEVM(BlockContext{BlockNumber: big.NewInt(0)}, statedb, params.MergedTestChainConfig, Config{})

	gotCode, gotHash := evm.resolveCodeAndHash(addr)
	if !bytes.Equal(gotCode, code) {
		t.Fatalf("code mismatch: got %x, want %x", gotCode, code)
	}
	if want := statedb.GetCodeHash(addr); gotHash != want {
		t.Fatalf("hash %x != account code hash %x", gotHash, want)
	}
}
