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

package core

import (
	"fmt"
	"os"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
)

const opcodeLogEnvVar = "PEVM_OPCODE_LOGS"

// opcodeGasTracer emits per-opcode gas logs when enabled via PEVM_OPCODE_LOGS.
type opcodeGasTracer struct {
	label        string
	mu           sync.Mutex
	currentTxIdx int
	currentTx    common.Hash
}

func newOpcodeGasTracer(label string) *tracing.Hooks {
	if os.Getenv(opcodeLogEnvVar) != "1" {
		return nil
	}
	tracer := &opcodeGasTracer{label: label}
	return &tracing.Hooks{
		OnTxStart: tracer.onTxStart,
		OnOpcode:  tracer.onOpcode,
	}
}

func (t *opcodeGasTracer) onTxStart(_ *tracing.VMContext, tx *types.Transaction, _ common.Address) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.currentTxIdx++
	t.currentTx = tx.Hash()
	fmt.Printf("[%s-OP] tx=%d hash=%s START\n", t.label, t.currentTxIdx-1, tx.Hash())
}

func (t *opcodeGasTracer) onOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, _ []byte, depth int, _ error) {
	t.mu.Lock()
	txIdx := t.currentTxIdx - 1
	txHash := t.currentTx
	t.mu.Unlock()

	fmt.Printf(
		"[%s-OP] tx=%d hash=%s depth=%d pc=0x%x opcode=%s contract=%s gas_before=%d cost=%d\n",
		t.label,
		txIdx,
		txHash,
		depth,
		pc,
		vm.OpCode(op).String(),
		scope.Address(),
		gas,
		cost,
	)
}

