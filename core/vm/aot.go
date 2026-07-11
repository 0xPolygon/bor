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
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
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

// ---- inlined stateful ops (codegen v2) -------------------------------------
//
// The helpers below replicate, bit-exactly, the interpreter loop body
// (aotStep) for the highest-frequency stateful opcodes, minus the per-op
// dispatch: no opcode fetch, no jump-table load, no stack min/max checks
// (the generated code validates stack height per basic block), and no
// indirect calls for memorySize/dynamicGas/execute. Constant gas must have
// been charged by the caller (it is folded into the generated segment gas),
// except for aotSload which charges the jump table's constant gas itself
// because SLOAD's constant/dynamic gas split is fork-dependent.
//
// Memory invariant: Memory is only ever grown via Resize with a word-aligned
// size (aotStep and the interpreter compute memorySize = toWordSize(x)*32),
// so mem.Len() is always a multiple of 32. The fast paths below additionally
// mask with &^31 so a violation of that invariant degrades to the exact slow
// path instead of undercharging.

// aotMemGas charges memory-expansion gas for an access of `length` bytes at
// offset `off` and resizes memory, exactly like aotStep does for an op whose
// dynamicGas is pureMemoryGascost. Returns the same errors aotStep would.
func aotMemGas(contract *Contract, mem *Memory, off *uint256.Int, length uint64) error {
	memSize, overflow := calcMemSize64WithUint(off, length)
	if overflow {
		return ErrGasUintOverflow
	}
	memorySize, overflow := math.SafeMul(toWordSize(memSize), 32)
	if overflow {
		return ErrGasUintOverflow
	}
	dynamicCost, err := memoryGasCost(mem, memorySize)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOutOfGas, err)
	}
	if contract.Gas < dynamicCost {
		return ErrOutOfGas
	}
	contract.Gas -= dynamicCost
	if memorySize > 0 {
		mem.Resize(memorySize)
	}
	return nil
}

// aotMload is MLOAD after constant gas: expansion gas + load. v is the
// offset operand and receives the result in place.
func aotMload(contract *Contract, mem *Memory, v *uint256.Int) error {
	if l := uint64(mem.Len()) &^ 31; l >= 32 {
		if off, overflow := v.Uint64WithOverflow(); !overflow && off <= l-32 {
			v.SetBytes(mem.GetPtr(off, 32))
			return nil
		}
	}
	if err := aotMemGas(contract, mem, v, 32); err != nil {
		return err
	}
	v.SetBytes(mem.GetPtr(v.Uint64(), 32))
	return nil
}

// aotMstore is MSTORE after constant gas: expansion gas + store.
func aotMstore(contract *Contract, mem *Memory, off, val *uint256.Int) error {
	if l := uint64(mem.Len()) &^ 31; l >= 32 {
		if o, overflow := off.Uint64WithOverflow(); !overflow && o <= l-32 {
			mem.Set32(o, val)
			return nil
		}
	}
	if err := aotMemGas(contract, mem, off, 32); err != nil {
		return err
	}
	mem.Set32(off.Uint64(), val)
	return nil
}

// aotMstore8 is MSTORE8 after constant gas: expansion gas + byte store.
func aotMstore8(contract *Contract, mem *Memory, off, val *uint256.Int) error {
	if l := uint64(mem.Len()) &^ 31; l >= 1 {
		if o, overflow := off.Uint64WithOverflow(); !overflow && o <= l-1 {
			mem.store[o] = byte(val.Uint64())
			return nil
		}
	}
	if err := aotMemGas(contract, mem, off, 1); err != nil {
		return err
	}
	mem.store[off.Uint64()] = byte(val.Uint64())
	return nil
}

// aotKeccak256 is KECCAK256 after constant gas: expansion + per-word gas,
// then the hash. offset is the top operand; size receives the result in
// place (matching opKeccak256, which pops offset and peeks size).
func aotKeccak256(evm *EVM, contract *Contract, mem *Memory, offset, size *uint256.Int) error {
	// memorySize stage (memoryKeccak256 + word-align), errors unwrapped.
	memSize, overflow := calcMemSize64(offset, size)
	if overflow {
		return ErrGasUintOverflow
	}
	memorySize, overflow := math.SafeMul(toWordSize(memSize), 32)
	if overflow {
		return ErrGasUintOverflow
	}
	// dynamicGas stage (gasKeccak256), errors wrapped like aotStep.
	gas, err := memoryGasCost(mem, memorySize)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOutOfGas, err)
	}
	wordGas, overflow := size.Uint64WithOverflow()
	if overflow {
		return fmt.Errorf("%w: %v", ErrOutOfGas, ErrGasUintOverflow)
	}
	if wordGas, overflow = math.SafeMul(toWordSize(wordGas), params.Keccak256WordGas); overflow {
		return fmt.Errorf("%w: %v", ErrOutOfGas, ErrGasUintOverflow)
	}
	if gas, overflow = math.SafeAdd(gas, wordGas); overflow {
		return fmt.Errorf("%w: %v", ErrOutOfGas, ErrGasUintOverflow)
	}
	if contract.Gas < gas {
		return ErrOutOfGas
	}
	contract.Gas -= gas
	if memorySize > 0 {
		mem.Resize(memorySize)
	}
	data := mem.GetPtr(offset.Uint64(), size.Uint64())
	evm.hasher.Reset()
	evm.hasher.Write(data)
	evm.hasher.Read(evm.hasherBuf[:])
	if evm.Config.EnablePreimageRecording {
		evm.StateDB.AddPreimage(evm.hasherBuf, data)
	}
	size.SetBytes(evm.hasherBuf[:])
	return nil
}

// aotMloadC is aotMload for a generation-time-constant offset (off+32 known
// not to wrap). v is the result slot.
func aotMloadC(contract *Contract, mem *Memory, off uint64, v *uint256.Int) error {
	if off+32 <= uint64(mem.Len())&^31 {
		v.SetBytes(mem.GetPtr(off, 32))
		return nil
	}
	memorySize, overflow := math.SafeMul(toWordSize(off+32), 32)
	if overflow {
		return ErrGasUintOverflow
	}
	dynamicCost, err := memoryGasCost(mem, memorySize)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOutOfGas, err)
	}
	if contract.Gas < dynamicCost {
		return ErrOutOfGas
	}
	contract.Gas -= dynamicCost
	mem.Resize(memorySize)
	v.SetBytes(mem.GetPtr(off, 32))
	return nil
}

// aotMstoreC is aotMstore for a generation-time-constant offset.
func aotMstoreC(contract *Contract, mem *Memory, off uint64, val *uint256.Int) error {
	if off+32 <= uint64(mem.Len())&^31 {
		mem.Set32(off, val)
		return nil
	}
	memorySize, overflow := math.SafeMul(toWordSize(off+32), 32)
	if overflow {
		return ErrGasUintOverflow
	}
	dynamicCost, err := memoryGasCost(mem, memorySize)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOutOfGas, err)
	}
	if contract.Gas < dynamicCost {
		return ErrOutOfGas
	}
	contract.Gas -= dynamicCost
	mem.Resize(memorySize)
	mem.Set32(off, val)
	return nil
}

// aotKeccak256C is aotKeccak256 for generation-time-constant offset and size
// (off+size known not to wrap; wordGas precomputed as toWordSize(size)*6).
// r is the result slot.
func aotKeccak256C(evm *EVM, contract *Contract, mem *Memory, off, size, wordGas uint64, r *uint256.Int) error {
	if size > 0 {
		if off+size <= uint64(mem.Len())&^31 {
			// Memory already covers the range: dynamic gas is word gas only.
			if contract.Gas < wordGas {
				return ErrOutOfGas
			}
			contract.Gas -= wordGas
		} else {
			memorySize, overflow := math.SafeMul(toWordSize(off+size), 32)
			if overflow {
				return ErrGasUintOverflow
			}
			gas, err := memoryGasCost(mem, memorySize)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrOutOfGas, err)
			}
			if gas, overflow = math.SafeAdd(gas, wordGas); overflow {
				return fmt.Errorf("%w: %v", ErrOutOfGas, ErrGasUintOverflow)
			}
			if contract.Gas < gas {
				return ErrOutOfGas
			}
			contract.Gas -= gas
			mem.Resize(memorySize)
		}
	}
	// size == 0: memorySize and word gas are both zero (calcMemSize64
	// short-circuits on zero length), so nothing is charged.
	data := mem.GetPtr(off, size)
	evm.hasher.Reset()
	evm.hasher.Write(data)
	evm.hasher.Read(evm.hasherBuf[:])
	if evm.Config.EnablePreimageRecording {
		evm.StateDB.AddPreimage(evm.hasherBuf, data)
	}
	r.SetBytes(evm.hasherBuf[:])
	return nil
}

// aotSload is a full SLOAD through the live jump table's gas rule (constant
// and dynamic gas are fork-dependent: EIP-2929 vs PIP-88 vs pre-Berlin).
// The caller must have synced stack.top: the dynamicGas func peeks the slot.
func aotSload(evm *EVM, contract *Contract, scope *ScopeContext, jt *JumpTable) error {
	operation := jt[SLOAD]
	if contract.Gas < operation.constantGas {
		return ErrOutOfGas
	}
	contract.Gas -= operation.constantGas
	if operation.dynamicGas != nil {
		dynamicCost, err := operation.dynamicGas(evm, contract, scope.Stack, scope.Memory, 0)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrOutOfGas, err)
		}
		if contract.Gas < dynamicCost {
			return ErrOutOfGas
		}
		contract.Gas -= dynamicCost
	}
	loc := scope.Stack.peek()
	hash := common.Hash(loc.Bytes32())
	val := evm.StateDB.GetState(contract.Address(), hash)
	loc.SetBytes(val.Bytes())
	return nil
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
