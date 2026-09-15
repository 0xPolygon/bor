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

package state

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestStateObjectMissingCodeError verifies that reading the code (or code size)
// of an account whose bytecode is absent from the backing store records a typed
// *MissingCodeError carrying the account address and code hash — the signal the
// stateless self-heal path relies on (via errors.As) to fetch exactly that blob.
func TestStateObjectMissingCodeError(t *testing.T) {
	missing := common.HexToHash("0x1122334455667788990011223344556677889900112233445566778899001122")
	addr := common.HexToAddress("0x00000000000000000000000000000000deadbeef")

	assertMissing := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected a sticky error for absent code")
		}
		var mce *MissingCodeError
		if !errors.As(err, &mce) {
			t.Fatalf("expected *MissingCodeError, got %T: %v", err, err)
		}
		if mce.Hash != missing {
			t.Fatalf("wrong hash: got %x want %x", mce.Hash, missing)
		}
		if mce.Addr != addr {
			t.Fatalf("wrong addr: got %x want %x", mce.Addr, addr)
		}
	}

	// newObjWithMissingCode returns a StateDB and an account whose CodeHash points
	// at bytecode that is not present in the backing store (the stateless-node
	// condition: account present, code absent).
	newObjWithMissingCode := func(t *testing.T) (*StateDB, *stateObject) {
		t.Helper()
		db, err := New(types.EmptyRootHash, NewDatabaseForTesting())
		if err != nil {
			t.Fatalf("new statedb: %v", err)
		}
		db.CreateAccount(addr)
		obj := db.getStateObject(addr)
		if obj == nil {
			t.Fatal("state object not created")
		}
		obj.data.CodeHash = missing.Bytes()
		obj.code = nil
		return db, obj
	}

	t.Run("Code", func(t *testing.T) {
		db, obj := newObjWithMissingCode(t)
		if code := obj.Code(); len(code) != 0 {
			t.Fatalf("expected empty code for an absent blob, got %d bytes", len(code))
		}
		assertMissing(t, db.Error())
	})

	t.Run("CodeSize", func(t *testing.T) {
		db, obj := newObjWithMissingCode(t)
		if size := obj.CodeSize(); size != 0 {
			t.Fatalf("expected zero code size for an absent blob, got %d", size)
		}
		assertMissing(t, db.Error())
	})
}
