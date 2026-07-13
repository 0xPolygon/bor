package core

import (
	"os"
	"sort"
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

// opFamEnabled gates the sampled tracer entirely (BOR_OPFAM_TRACE=1). Default
// off: the sampled blocks' ~4x exec inflation contaminates every mod-64 block
// in latency aggregates, and the opcode-mix dataset it feeds is already frozen.
var opFamEnabled = os.Getenv("BOR_OPFAM_TRACE") == "1"

// OpFamEnabled reports whether the sampled opcode-family tracer is enabled
// (shared gate for the import and build sampling sites).
func OpFamEnabled() bool { return opFamEnabled }

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

type opStat struct{ n, ns int64 }

// conStat is one executing contract's attribution: total plus a per-family
// split (the compute-vs-state profile that sizes native-execution candidates).
type conStat struct {
	all  opStat
	fams [opFamCount]opStat
}

type OpFamTracer struct {
	last    time.Time
	lastFam uint8
	lastOp  byte
	fams    [opFamCount]opStat
	ops     [256]opStat

	// Per-contract attribution. The executing address only changes at call
	// boundaries, so cache the current bucket and re-resolve on change.
	contracts map[common.Address]*conStat
	curAddr   common.Address
	curCon    *conStat
	lastCon   *conStat // bucket of the contract that executed lastOp
}

func NewOpFamTracer() *OpFamTracer {
	return &OpFamTracer{contracts: make(map[common.Address]*conStat)}
}

func (t *OpFamTracer) onOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
	now := time.Now()
	if !t.last.IsZero() {
		d := now.Sub(t.last).Nanoseconds()
		t.fams[t.lastFam].ns += d
		t.ops[t.lastOp].ns += d
		if t.lastCon != nil {
			t.lastCon.all.ns += d
			t.lastCon.fams[t.lastFam].ns += d
		}
	}
	if addr := scope.Address(); addr != t.curAddr || t.curCon == nil {
		con := t.contracts[addr]
		if con == nil {
			con = &conStat{}
			t.contracts[addr] = con
		}
		t.curAddr, t.curCon = addr, con
	}
	fam := opFamTable[op]
	t.curCon.all.n++
	t.curCon.fams[fam].n++
	t.fams[fam].n++
	t.ops[op].n++
	t.lastFam = fam
	t.lastOp = op
	t.lastCon = t.curCon
	t.last = now
}

func (t *OpFamTracer) onTxStart(vmctx *tracing.VMContext, tx *types.Transaction, from common.Address) {
	// Don't attribute the inter-tx gap to the previous tx's last opcode.
	t.last = time.Time{}
}

func (t *OpFamTracer) onTxEnd(receipt *types.Receipt, err error) {
	// Close out the final opcode of the tx.
	if !t.last.IsZero() {
		d := time.Since(t.last).Nanoseconds()
		t.fams[t.lastFam].ns += d
		t.ops[t.lastOp].ns += d
		if t.lastCon != nil {
			t.lastCon.all.ns += d
			t.lastCon.fams[t.lastFam].ns += d
		}
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

// ResultOpcodes returns the topN opcodes by attributed time, with the
// remainder folded into an "other" bucket.
func (t *OpFamTracer) ResultOpcodes(topN int) map[string]OpFamStat {
	type kv struct {
		op byte
		s  opStat
	}
	var all []kv
	for op, s := range t.ops {
		if s.n > 0 {
			all = append(all, kv{byte(op), s})
		}
	}
	if len(all) == 0 {
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].s.ns > all[j].s.ns })
	out := make(map[string]OpFamStat, topN+1)
	var other opStat
	for i, e := range all {
		if i < topN {
			out[vm.OpCode(e.op).String()] = OpFamStat{N: e.s.n, Us: e.s.ns / 1e3}
		} else {
			other.n += e.s.n
			other.ns += e.s.ns
		}
	}
	if other.n > 0 {
		out["OTHER"] = OpFamStat{N: other.n, Us: other.ns / 1e3}
	}
	return out
}

// ResultContracts returns the topN executing contract addresses by attributed
// time, with the remainder folded into "other" and the distinct-address count
// under "n_contracts" (in the N field).
func (t *OpFamTracer) ResultContracts(topN int) map[string]OpFamStat {
	type kv struct {
		addr common.Address
		s    opStat
	}
	all := make([]kv, 0, len(t.contracts))
	for addr, s := range t.contracts {
		all = append(all, kv{addr, s.all})
	}
	if len(all) == 0 {
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].s.ns > all[j].s.ns })
	out := make(map[string]OpFamStat, topN+2)
	var other opStat
	for i, e := range all {
		if i < topN {
			out[e.addr.Hex()] = OpFamStat{N: e.s.n, Us: e.s.ns / 1e3}
		} else {
			other.n += e.s.n
			other.ns += e.s.ns
		}
	}
	if other.n > 0 {
		out["other"] = OpFamStat{N: other.n, Us: other.ns / 1e3}
	}
	out["n_contracts"] = OpFamStat{N: int64(len(all))}
	return out
}

// ResultContractFams returns, for the topN contracts by attributed time, the
// per-family split of that contract's opcodes — the compute-vs-state profile
// that sizes native-execution (AOT) candidates.
func (t *OpFamTracer) ResultContractFams(topN int) map[string]map[string]OpFamStat {
	type kv struct {
		addr common.Address
		s    *conStat
	}
	all := make([]kv, 0, len(t.contracts))
	for addr, s := range t.contracts {
		all = append(all, kv{addr, s})
	}
	if len(all) == 0 {
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].s.all.ns > all[j].s.all.ns })
	if len(all) > topN {
		all = all[:topN]
	}
	out := make(map[string]map[string]OpFamStat, len(all))
	for _, e := range all {
		fams := make(map[string]OpFamStat, opFamCount)
		for i, f := range e.s.fams {
			if f.n == 0 {
				continue
			}
			fams[opFamNames[i]] = OpFamStat{N: f.n, Us: f.ns / 1e3}
		}
		out[e.addr.Hex()] = fams
	}
	return out
}
