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
	"fmt"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
)

// aotFunc is the signature of a transpiled contract body. It executes the
// whole frame and returns like the interpreter loop: (result, errStopToken)
// on STOP/RETURN, (result, ErrExecutionReverted) on REVERT, or a hard error.
type aotFunc func(evm *EVM, contract *Contract, scope *ScopeContext, jt *JumpTable, interrupt *atomic.Bool) ([]byte, error)

// aotRegistry maps contract code hashes to transpiled bodies. Populated by
// init() functions in generated files; read-only afterwards.
var aotRegistry = make(map[common.Hash]aotFunc)

// aotRegister links a transpiled contract body to its code hash. Must only
// be called from init().
func aotRegister(codeHash common.Hash, fn aotFunc) {
	aotRegistry[codeHash] = fn
}

// aotLookup returns the transpiled body for the given code hash, if any.
func aotLookup(codeHash common.Hash) aotFunc {
	if len(aotRegistry) == 0 {
		return nil
	}
	return aotRegistry[codeHash]
}

// aotStep executes exactly one opcode at *pc through the jump table,
// replicating the interpreter loop body (stack validation, constant and
// dynamic gas, memory expansion, execute). Generated code calls this for
// stateful and dynamic-gas opcodes so their semantics and gas accounting
// remain bit-identical with the interpreter. The caller must have charged
// all preceding straight-line constant gas exactly, so contract.Gas is
// identical to what the interpreter would observe here.
func aotStep(evm *EVM, contract *Contract, scope *ScopeContext, jt *JumpTable, pc *uint64) ([]byte, error) {
	op := contract.GetOp(*pc)
	operation := jt[op]
	// Validate stack
	if sLen := scope.Stack.len(); sLen < operation.minStack {
		return nil, &ErrStackUnderflow{stackLen: sLen, required: operation.minStack}
	} else if sLen > operation.maxStack {
		return nil, &ErrStackOverflow{stackLen: sLen, limit: operation.maxStack}
	}
	if contract.Gas < operation.constantGas {
		return nil, ErrOutOfGas
	}
	contract.Gas -= operation.constantGas

	var memorySize uint64
	if operation.dynamicGas != nil {
		if operation.memorySize != nil {
			memSize, overflow := operation.memorySize(scope.Stack)
			if overflow {
				return nil, ErrGasUintOverflow
			}
			if memorySize, overflow = math.SafeMul(toWordSize(memSize), 32); overflow {
				return nil, ErrGasUintOverflow
			}
		}
		dynamicCost, err := operation.dynamicGas(evm, contract, scope.Stack, scope.Memory, memorySize)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrOutOfGas, err)
		}
		if contract.Gas < dynamicCost {
			return nil, ErrOutOfGas
		}
		contract.Gas -= dynamicCost
		if memorySize > 0 {
			scope.Memory.Resize(memorySize)
		}
	}
	return operation.execute(pc, evm, scope)
}
