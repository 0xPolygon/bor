package core

import (
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
)

// Opcode-family timing tracer (lab instrumentation). Attach its Hooks() to
// vm.Config.Tracer for a SAMPLED block: each OnOpcode call attributes the
// wall time since the previous OnOpcode to the previous opcode's family, so
// per-family sums include interpreter dispatch overhead (proportionally) and
// a CALL to a precompile absorbs the precompile's runtime. Tracing inflates
// the sampled block's wall time (hook + HookedState overhead); consumers must
// exclude sampled blocks from latency aggregates.
//
// NOT thread-safe: one tracer per serially-executed block.

// opFamSampleEvery selects which import blocks get the opcode-family tracer
// (block number modulo). Sampled blocks pay hook + HookedState overhead.
const opFamSampleEvery = 64

// OpFamStat is one family's opcode count and attributed wall time.
type OpFamStat struct {
	N  int64 `json:"n"`
	Us int64 `json:"us"`
}

// opFamilies indexes OpFamTracer buckets; keep opFamNames in sync.
const (
	opFamSload = iota
	opFamSstore
	opFamStateEnv // BALANCE, EXTCODE*, SELFBALANCE, BLOCKHASH, TLOAD/TSTORE
	opFamKeccak
	opFamCall // CALL/CALLCODE/DELEGATECALL/STATICCALL (incl. precompile bodies)
	opFamCreate
	opFamLog
	opFamMemory // MLOAD/MSTORE/MSTORE8/MCOPY and *COPY opcodes
	opFamCompute
	opFamControl // JUMP*, PC, GAS, STOP/RETURN/REVERT/SELFDESTRUCT
	opFamContext // ADDRESS..CALLDATASIZE, block/env introspection
	opFamCount
)

var opFamNames = [opFamCount]string{
	"sload", "sstore", "state_env", "keccak", "call", "create",
	"log", "memory", "compute", "control", "context",
}

// opFamTable maps opcode byte → family. Built once at init.
var opFamTable [256]uint8

func init() {
	for i := range opFamTable {
		opFamTable[i] = opFamCompute
	}
	set := func(fam uint8, ops ...vm.OpCode) {
		for _, op := range ops {
			opFamTable[byte(op)] = fam
		}
	}
	set(opFamSload, vm.SLOAD)
	set(opFamSstore, vm.SSTORE)
	set(opFamStateEnv, vm.BALANCE, vm.SELFBALANCE, vm.EXTCODESIZE, vm.EXTCODECOPY,
		vm.EXTCODEHASH, vm.BLOCKHASH, vm.TLOAD, vm.TSTORE)
	set(opFamKeccak, vm.KECCAK256)
	set(opFamCall, vm.CALL, vm.CALLCODE, vm.DELEGATECALL, vm.STATICCALL)
	set(opFamCreate, vm.CREATE, vm.CREATE2)
	set(opFamLog, vm.LOG0, vm.LOG1, vm.LOG2, vm.LOG3, vm.LOG4)
	set(opFamMemory, vm.MLOAD, vm.MSTORE, vm.MSTORE8, vm.MCOPY, vm.MSIZE,
		vm.CALLDATACOPY, vm.CODECOPY, vm.RETURNDATACOPY)
	set(opFamControl, vm.JUMP, vm.JUMPI, vm.JUMPDEST, vm.PC, vm.GAS,
		vm.STOP, vm.RETURN, vm.REVERT, vm.SELFDESTRUCT, vm.INVALID)
	set(opFamContext, vm.ADDRESS, vm.ORIGIN, vm.CALLER, vm.CALLVALUE,
		vm.CALLDATALOAD, vm.CALLDATASIZE, vm.CODESIZE, vm.GASPRICE,
		vm.RETURNDATASIZE, vm.COINBASE, vm.TIMESTAMP, vm.NUMBER,
		vm.PREVRANDAO, vm.GASLIMIT, vm.CHAINID, vm.BASEFEE,
		vm.BLOBHASH, vm.BLOBBASEFEE)
}

type OpFamTracer struct {
	last    time.Time
	lastFam uint8
	fams    [opFamCount]struct{ n, ns int64 }
}

func NewOpFamTracer() *OpFamTracer { return &OpFamTracer{} }

func (t *OpFamTracer) onOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	now := time.Now()
	if !t.last.IsZero() {
		t.fams[t.lastFam].ns += now.Sub(t.last).Nanoseconds()
	}
	fam := opFamTable[op]
	t.fams[fam].n++
	t.lastFam = fam
	t.last = now
}

func (t *OpFamTracer) onTxStart(vmctx *tracing.VMContext, tx *types.Transaction, from common.Address) {
	// Don't attribute the inter-tx gap to the previous tx's last opcode.
	t.last = time.Time{}
}

func (t *OpFamTracer) onTxEnd(receipt *types.Receipt, err error) {
	// Close out the final opcode of the tx.
	if !t.last.IsZero() {
		t.fams[t.lastFam].ns += time.Since(t.last).Nanoseconds()
		t.last = time.Time{}
	}
}

// Hooks returns the tracing hooks for attachment to vm.Config.Tracer.
func (t *OpFamTracer) Hooks() *tracing.Hooks {
	return &tracing.Hooks{
		OnOpcode:  t.onOpcode,
		OnTxStart: t.onTxStart,
		OnTxEnd:   t.onTxEnd,
	}
}

// Result returns the per-family stats, dropping empty families.
func (t *OpFamTracer) Result() map[string]OpFamStat {
	out := make(map[string]OpFamStat, opFamCount)
	for i, f := range t.fams {
		if f.n == 0 {
			continue
		}
		out[opFamNames[i]] = OpFamStat{N: f.n, Us: f.ns / 1e3}
	}
	return out
}
