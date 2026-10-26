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
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestPrecompileForkMapsNoSilentRemoval walks the fork precompile maps in
// the priority order of activePrecompiledContracts and fails if a newer map
// drops an address of the map before it, unless the removal is listed. A new
// fork map copied from the wrong parent would otherwise silently remove
// precompiles (as the Madhugiri map did with P256VERIFY on Amoy).
func TestPrecompileForkMapsNoSilentRemoval(t *testing.T) {
	type fork struct {
		name string
		set  PrecompiledContracts
	}
	// Oldest first; Verkle is excluded (never active on bor).
	forks := []fork{
		{"Homestead", PrecompiledContractsHomestead},
		{"Byzantium", PrecompiledContractsByzantium},
		{"Istanbul", PrecompiledContractsIstanbul},
		{"Berlin", PrecompiledContractsBerlin},
		{"Cancun", PrecompiledContractsCancun},
		{"Prague", PrecompiledContractsPrague},
		{"Osaka", PrecompiledContractsOsaka},
		{"Madhugiri", PrecompiledContractsMadhugiri},
		{"MadhugiriPro", PrecompiledContractsMadhugiriPro},
		{"Lisovo", PrecompiledContractsLisovo},
		{"LisovoPro", PrecompiledContractsLisovoPro},
		{"Chicago", PrecompiledContractsChicago},
	}
	// Intended removals, keyed by the newer fork.
	allowed := map[string][]common.Address{
		"Madhugiri": {common.BytesToAddress([]byte{0x01, 0x00})}, // restored by MadhugiriPro
		"LisovoPro": {common.BytesToAddress([]byte{0x0a})},       // KZG point evaluation removed
	}
	for i := 1; i < len(forks); i++ {
		older, newer := forks[i-1], forks[i]
		ok := make(map[common.Address]bool)
		for _, a := range allowed[newer.name] {
			ok[a] = true
		}
		for addr := range older.set {
			if _, present := newer.set[addr]; !present && !ok[addr] {
				t.Errorf("%s drops %v (%s) from %s", newer.name, addr, older.set[addr].Name(), older.name)
			}
		}
	}
}
