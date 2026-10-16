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

	tests := []struct {
		name     string
		readOnly bool
		gas      uint64
		reads    common.Address
		run      func(*EVM, *Contract) error
	}{
		{
			name:     "SSTORE in a static context",
			readOnly: true,
			gas:      10_000,
			reads:    self,
			run: func(evm *EVM, c *Contract) error {
				st := newstack()
				st.push(uint256.NewInt(1)) // value
				st.push(new(uint256.Int))  // slot 0
				_, err := gasSStorePIP88(evm, c, st, NewMemory(), 0)
				return err
			},
		},
		{
			name:     "CALL with value in a static context",
			readOnly: true,
			gas:      100_000,
			reads:    target,
			run: func(evm *EVM, c *Contract) error {
				_, err := gasCallEIP7702(evm, c, callStack(1, 0), NewMemory(), 0)
				return err
			},
		},
		{
			name:  "CALL out of gas on memory expansion",
			gas:   10_000,
			reads: target,
			run: func(evm *EVM, c *Contract) error {
				_, err := gasCallEIP7702(evm, c, callStack(0, 1<<20), NewMemory(), 1<<20)
				return err
			},
		},
		{
			name:     "SELFDESTRUCT in a static context",
			readOnly: true,
			gas:      100_000,
			reads:    target,
			run: func(evm *EVM, c *Contract) error {
				st := newstack()
				st.push(new(uint256.Int).SetBytes(target.Bytes()))
				_, err := gasSelfdestructEIP3529(evm, c, st, NewMemory(), 0)
				return err
			},
		},
		{
			name:  "SELFDESTRUCT to a cold beneficiary without gas for the access",
			gas:   1_000,
			reads: target,
			run: func(evm *EVM, c *Contract) error {
				st := newstack()
				st.push(new(uint256.Int).SetBytes(target.Bytes()))
				_, err := gasSelfdestructEIP3529(evm, c, st, NewMemory(), 0)
				return err
			},
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

				err := tt.run(evm, contract)

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
				// The frame still fails before Hampi, in the opcode body's
				// write-protection check or on the gas charge, so the read only
				// changes what the witness carries.
			})
		}
	}
}
