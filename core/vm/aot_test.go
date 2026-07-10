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

package vm

import (
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestAOTRegistryDispatch proves the generated body is registered under the
// exact keccak hash of the bytecode fixture it was generated from, i.e. the
// AOT dispatch in Run really fires for this contract.
func TestAOTRegistryDispatch(t *testing.T) {
	raw, err := os.ReadFile("aotexp/ctfexchange.hex")
	if err != nil {
		t.Skipf("bytecode fixture missing: %v", err)
	}
	code := common.FromHex(strings.TrimSpace(string(raw)))
	hash := crypto.Keccak256Hash(code)
	if aotLookup(hash) == nil {
		t.Fatalf("no AOT body registered for fixture code hash %s", hash.Hex())
	}
	if hash != aotCodeHashCTFExchangeV2 {
		t.Fatalf("generated code hash %s != fixture hash %s", aotCodeHashCTFExchangeV2.Hex(), hash.Hex())
	}
}
