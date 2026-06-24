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
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm/nativectf"
	"github.com/ethereum/go-ethereum/metrics"
)

// nativeCTFFastPathCounter counts how often the native getCollectionId fast-path
// substituted for interpreter execution (active mode). Operators can use this to
// confirm the fast-path is firing and gauge its hit rate.
var nativeCTFFastPathCounter = metrics.NewRegisteredCounter("vm/nativectf/fastpath", nil)

// tryNativeCTF runs the native getCollectionId fast-path iff every precondition
// holds. It returns ok=false (and the caller MUST fall through to the stock
// interpreter) unless the flag is Active, no tracer is attached, the calldata is
// a well-formed getCollectionId call to a recognized CTF codehash, and gas
// suffices for the exact charge the interpreter would impose.
//
// The native path is result-and-gas-exact and performs no state mutation, so a
// successful substitution is byte-identical to interpreter execution and changes
// no chain result.
func (evm *EVM) tryNativeCTF(addr common.Address, input []byte, gas uint64) (ret []byte, leftover uint64, ok bool) {
	if evm.Config.NativeCTF != NativeCTFActive {
		return nil, gas, false
	}
	if evm.Config.Tracer != nil { // preserve exact traces
		return nil, gas, false
	}
	if len(input) != 4+32*3 {
		return nil, gas, false
	}
	if ([4]byte{input[0], input[1], input[2], input[3]}) != nativectf.GetCollectionIdSelector {
		return nil, gas, false
	}
	if !nativectf.IsCanonicalCTF(evm.resolveCodeHash(addr)) {
		return nil, gas, false
	}
	var parent, conditionId [32]byte
	copy(parent[:], input[4:36])
	copy(conditionId[:], input[36:68])
	indexSet := new(big.Int).SetBytes(input[68:100])
	if parent != ([32]byte{}) {
		return nil, gas, false // parent != 0 handled in a later task
	}
	result, iters, pf, b254 := nativectf.GetCollectionId(parent, conditionId, indexSet)
	needed := nativectf.ExternalCallGas(iters, pf, b254)
	if gas < needed {
		return nil, gas, false // interpreter would OOG identically
	}
	nativeCTFFastPathCounter.Inc(1)
	return result[:], gas - needed, true
}
