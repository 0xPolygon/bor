package vm

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
)

// targetReadsStateDB counts state reads of one account (and one of its slots),
// the data a witness has to carry for the read to replay statelessly.
type targetReadsStateDB struct {
	StateDB
	target common.Address
	reads  int
}

func (r *targetReadsStateDB) note(addr common.Address) {
	if addr == r.target {
		r.reads++
	}
}

func (r *targetReadsStateDB) GetCode(addr common.Address) []byte {
	r.note(addr)
	return r.StateDB.GetCode(addr)
}

func (r *targetReadsStateDB) Empty(addr common.Address) bool {
	r.note(addr)
	return r.StateDB.Empty(addr)
}

func (r *targetReadsStateDB) Exist(addr common.Address) bool {
	r.note(addr)
	return r.StateDB.Exist(addr)
}

func (r *targetReadsStateDB) GetStateAndCommittedState(addr common.Address, slot common.Hash) (common.Hash, common.Hash) {
	r.note(addr)
	return r.StateDB.GetStateAndCommittedState(addr, slot)
}

// TestHampiSkippedReads pins, for every gas function that returns before a
// state read the frame no longer needs, that the read still happens before
// Hampi (so witnesses keep the data stateless nodes on earlier releases read)
// and is skipped from Hampi on. Either way the frame fails, so gas and outcome
// do not depend on the read.
func TestHampiSkippedReads(t *testing.T) {
	const forkBlock = 100
	var (
		self   = common.BytesToAddress([]byte("self"))
		target = common.BytesToAddress([]byte("target"))
	)

	// callStack lays out CALL's operands: gas, addr, value, then memory args.
	callStack := func(value uint64, argsSize uint64) *Stack {
		st := newstack()
		st.push(new(uint256.Int))         // retSize
		st.push(new(uint256.Int))         // retOffset
		st.push(uint256.NewInt(argsSize)) // argsSize
		st.push(new(uint256.Int))         // argsOffset
		st.push(uint256.NewInt(value))    // value
		st.push(new(uint256.Int).SetBytes(target.Bytes()))
		st.push(uint256.NewInt(1_000_000)) // gas
		return st
	}

	sstoreStack := func() *Stack {
		st := newstack()
		st.push(uint256.NewInt(1)) // value
		st.push(new(uint256.Int))  // slot 0
		return st
	}
	selfdestructStack := func() *Stack {
		st := newstack()
		st.push(new(uint256.Int).SetBytes(target.Bytes()))
		return st
	}

	tests := []struct {
		name       string
		readOnly   bool
		gas        uint64
		reads      common.Address
		stack      func() *Stack
		memorySize uint64
		gasFn      gasFunc
		// op is the opcode body that rejects the frame before Hampi; nil when
		// the frame fails on the dynamic gas charge instead.
		op executionFunc
	}{
		{
			name:     "SSTORE in a static context",
			readOnly: true,
			gas:      10_000,
			reads:    self,
			stack:    sstoreStack,
			gasFn:    gasSStorePIP88,
			op:       opSstore,
		},
		{
			name:     "CALL with value in a static context",
			readOnly: true,
			gas:      100_000,
			reads:    target,
			stack:    func() *Stack { return callStack(1, 0) },
			gasFn:    gasCallEIP7702,
			op:       opCall,
		},
		{
			name:       "CALL out of gas on memory expansion",
			gas:        10_000,
			reads:      target,
			stack:      func() *Stack { return callStack(0, 1<<20) },
			memorySize: 1 << 20,
			gasFn:      gasCallEIP7702,
		},
		{
			name:     "SELFDESTRUCT in a static context",
			readOnly: true,
			gas:      100_000,
			reads:    target,
			stack:    selfdestructStack,
			gasFn:    gasSelfdestructEIP3529,
			op:       opSelfdestruct,
		},
		{
			name:  "SELFDESTRUCT to a cold beneficiary without gas for the access",
			gas:   1_000,
			reads: target,
			stack: selfdestructStack,
			gasFn: gasSelfdestructEIP3529,
		},
	}

	for _, tt := range tests {
		for _, block := range []int64{forkBlock - 1, forkBlock, forkBlock + 1} {
			hampi := block >= forkBlock
			name := tt.name + "/before Hampi"
			if hampi {
				name = tt.name + "/from Hampi"
			}
			t.Run(name, func(t *testing.T) {
				inner, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
				inner.CreateAccount(self)
				inner.AddBalance(self, uint256.NewInt(1), 0)
				sdb := &targetReadsStateDB{StateDB: inner, target: tt.reads}

				evm := NewEVM(BlockContext{BlockNumber: big.NewInt(block)}, sdb, hampiTestChainConfig(big.NewInt(forkBlock)), Config{})
				evm.readOnly = tt.readOnly
				contract := NewContract(common.Address{}, self, uint256.NewInt(0), NewGasBudget(tt.gas, 0), nil)
				stack := tt.stack()
				mem := NewMemory()

				cost, err := tt.gasFn(evm, contract, stack, mem, tt.memorySize)

				if hampi {
					if sdb.reads != 0 {
						t.Fatalf("from Hampi the doomed read must be skipped, got %d reads of the target", sdb.reads)
					}
					if err == nil {
						t.Fatal("from Hampi the gas function must fail before the read")
					}
					return
				}
				if sdb.reads == 0 {
					t.Fatal("before Hampi the target must still be read, or the witness loses it")
				}
				// Before Hampi the gas function lets the frame through, so it has
				// to fail where it did before: on the dynamic gas charge or in the
				// opcode body.
				if err == nil {
					if tt.op == nil {
						if cost.ExecutionGas <= contract.Gas.ExecutionGas {
							t.Fatalf("before Hampi the frame must run out of gas, cost %d with %d left", cost.ExecutionGas, contract.Gas.ExecutionGas)
						}
						return
					}
					var pc uint64
					_, err = tt.op(&pc, evm, &ScopeContext{Memory: mem, Stack: stack, Contract: contract})
				}
				if err == nil {
					t.Fatal("before Hampi the frame must still fail")
				}
			})
		}
	}
}

// TestAmsterdamSkipsCallVariantReads pins the Amsterdam side of
// skipsDoomedReads with Hampi unscheduled: Amsterdam's CALLCODE, DELEGATECALL
// and STATICCALL share the EIP-7702 helper, so an intrinsic-cost failure reads
// the target before Amsterdam and must not read it from Amsterdam on, or the
// target would leak into the block access list.
func TestAmsterdamSkipsCallVariantReads(t *testing.T) {
	const forkBlock = 100
	var (
		self   = common.BytesToAddress([]byte("self"))
		target = common.BytesToAddress([]byte("target"))
	)
	cfg := hampiTestChainConfig(nil)
	cfg.AmsterdamBlock = big.NewInt(forkBlock)

	// DELEGATECALL's operands: gas, addr, then memory args.
	stack := func() *Stack {
		st := newstack()
		st.push(new(uint256.Int))        // retSize
		st.push(new(uint256.Int))        // retOffset
		st.push(uint256.NewInt(1 << 20)) // argsSize
		st.push(new(uint256.Int))        // argsOffset
		st.push(new(uint256.Int).SetBytes(target.Bytes()))
		st.push(uint256.NewInt(1_000_000)) // gas
		return st
	}

	for _, block := range []int64{forkBlock - 1, forkBlock, forkBlock + 1} {
		amsterdam := block >= forkBlock
		gasFn := gasDelegateCallEIP7702
		if amsterdam {
			gasFn = gasDelegateCall8038
		}
		t.Run(big.NewInt(block).String(), func(t *testing.T) {
			inner, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
			inner.CreateAccount(self)
			sdb := &targetReadsStateDB{StateDB: inner, target: target}

			evm := NewEVM(BlockContext{BlockNumber: big.NewInt(block)}, sdb, cfg, Config{})
			if evm.chainRules.IsHampi || evm.chainRules.IsAmsterdam != amsterdam {
				t.Fatalf("unexpected rules: hampi=%v amsterdam=%v", evm.chainRules.IsHampi, evm.chainRules.IsAmsterdam)
			}
			contract := NewContract(common.Address{}, self, uint256.NewInt(0), NewGasBudget(10_000, 0), nil)

			if _, err := gasFn(evm, contract, stack(), NewMemory(), 1<<20); err == nil {
				t.Fatal("the intrinsic memory cost must fail the call")
			}
			if amsterdam && sdb.reads != 0 {
				t.Fatalf("from Amsterdam the target must not be read, got %d reads", sdb.reads)
			}
			if !amsterdam && sdb.reads == 0 {
				t.Fatal("before Amsterdam the target must still be read, or the witness loses it")
			}
		})
	}
}
