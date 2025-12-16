// Copyright 2018 The go-ethereum Authors
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
	"bytes"
	"fmt"
	"math"
	"math/big"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ethereum/evmc/bindings/go/evmc"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

type EVMC struct {
	instance *evmc.VM
	env      *EVM
	readOnly bool // TODO: The readOnly flag should not be here.
}

func createVM(config string) *evmc.VM {
	options := strings.Split(config, ",")
	path := strings.TrimSpace(options[0])

	if path == "" {
		panic("EVMC VM path not provided, set --vm.(evm|ewasm)=/path/to/vm")
	}

	vm, err := evmc.Load(path)
	if err != nil {
		panic(err.Error())
	}
	log.Info("EVMC VM loaded", "name", vm.Name(), "version", vm.Version(), "path", path)

	for _, option := range options[1:] {
		option = strings.TrimSpace(option)
		if option == "" {
			continue
		}
		if idx := strings.Index(option, "="); idx >= 0 {
			name := option[:idx]
			value := option[idx+1:]
			if err := vm.SetOption(name, value); err == nil {
				log.Info("EVMC VM option set", "name", name, "value", value)
			} else {
				log.Warn("EVMC VM option setting failed", "name", name, "error", err)
			}
		}
	}

	evm1Cap := vm.HasCapability(evmc.CapabilityEVM1)
	ewasmCap := vm.HasCapability(evmc.CapabilityEWASM)
	log.Info("EVMC VM capabilities", "evm1", evm1Cap, "ewasm", ewasmCap)

	return vm
}

func NewEVMC(options string, env *EVM) *EVMC {
	instance := createVM(options)
	ev := &EVMC{instance: instance, env: env, readOnly: false}
	runtime.SetFinalizer(ev, func(evmcInterpreter *EVMC) {
		if evmcInterpreter.instance != nil {
			evmcInterpreter.instance.Destroy()
			evmcInterpreter.instance = nil
		}
	})
	return ev
}

// Implements evmc.HostContext interface.
type HostContext struct {
	env         *EVM
	contract    *Contract
	readOnly    bool
	interrupt   *atomic.Bool
	interrupted atomic.Bool
}

// func (host *HostContext) shouldInterrupt() bool {
// 	return host.interrupt != nil && host.interrupt.Load()
// }
//
// func (host *HostContext) markInterrupted() {
// 	host.interrupted.Store(true)
// }
//
// func (host *HostContext) wasInterrupted() bool {
// 	return host.interrupted.Load()
// }

func (host *HostContext) AccountExists(addr evmc.Address) bool {
	commonAddr := common.Address(addr)
	env := host.env
	eip158 := env.ChainConfig().IsEIP158(env.Context.BlockNumber)
	if eip158 {
		if !env.StateDB.Empty(commonAddr) {
			return true
		}
	} else if env.StateDB.Exist(commonAddr) {
		return true
	}
	return false
}

func (host *HostContext) GetStorage(addr evmc.Address, key evmc.Hash) evmc.Hash {
	commonAddr := common.Address(addr)
	commonKey := common.Hash(key)
	env := host.env
	value := env.StateDB.GetState(commonAddr, commonKey)
	return evmc.Hash(value)
}

func (host *HostContext) SetStorage(addr evmc.Address, key evmc.Hash, value evmc.Hash) (status evmc.StorageStatus) {
	// Disallow writes when the EVM is executing in read-only/static mode (e.g. prefetch).
	if host.readOnly {
		// No-op but report a harmless status (same as "assigned") to the VM.
		return evmc.StorageAssigned
	}

	commonAddr := common.Address(addr)
	commonKey := common.Hash(key)
	commonValue := common.Hash(value)
	env := host.env

	oldValue := env.StateDB.GetState(commonAddr, commonKey)
	if oldValue == commonValue {
		return evmc.StorageAssigned
	}

	current := env.StateDB.GetState(commonAddr, commonKey)
	original := env.StateDB.GetCommittedState(commonAddr, commonKey)

	env.StateDB.SetState(commonAddr, commonKey, commonValue)

	isConstantinople := env.ChainConfig().IsConstantinople(env.Context.BlockNumber)
	if !isConstantinople {

		zero := common.Hash{}
		status = evmc.StorageModified
		if oldValue == zero {
			return evmc.StorageAdded
		} else if commonValue == zero {
			env.StateDB.AddRefund(params.SstoreRefundGas)
			return evmc.StorageDeleted
		}
		return evmc.StorageModified
	}

	if original == current {
		if original == (common.Hash{}) { // create slot (2.1.1)
			return evmc.StorageAdded
		}
		if commonValue == (common.Hash{}) { // delete slot (2.1.2b)
			env.StateDB.AddRefund(params.NetSstoreClearRefund)
			return evmc.StorageDeleted
		}
		return evmc.StorageModified
	}
	if original != (common.Hash{}) {
		if current == (common.Hash{}) { // recreate slot (2.2.1.1)
			env.StateDB.SubRefund(params.NetSstoreClearRefund)
		} else if commonValue == (common.Hash{}) { // delete slot (2.2.1.2)
			env.StateDB.AddRefund(params.NetSstoreClearRefund)
		}
	}
	if original == commonValue {
		if original == (common.Hash{}) { // reset to original inexistent slot (2.2.2.1)
			env.StateDB.AddRefund(params.NetSstoreResetClearRefund)
		} else { // reset to original existing slot (2.2.2.2)
			env.StateDB.AddRefund(params.NetSstoreResetRefund)
		}
	}
	return evmc.StorageModifiedRestored
}

func (host *HostContext) GetBalance(addr evmc.Address) evmc.Hash {
	commonAddr := common.Address(addr)
	balance := host.env.StateDB.GetBalance(commonAddr)
	if balance == nil {
		return evmc.Hash{}
	}
	return evmc.Hash(common.BigToHash(balance.ToBig()))
}

func (host *HostContext) GetCodeSize(addr evmc.Address) int {
	commonAddr := common.Address(addr)
	env := host.env
	return env.StateDB.GetCodeSize(commonAddr)
}

func (host *HostContext) GetCodeHash(addr evmc.Address) evmc.Hash {
	commonAddr := common.Address(addr)
	env := host.env
	if env.StateDB.Empty(commonAddr) {
		return evmc.Hash{}
	}
	return evmc.Hash(env.StateDB.GetCodeHash(commonAddr))
}

func (host *HostContext) GetCode(addr evmc.Address) []byte {
	commonAddr := common.Address(addr)
	env := host.env
	return env.StateDB.GetCode(commonAddr)
}

func (host *HostContext) Selfdestruct(addr evmc.Address, beneficiary evmc.Address) bool {
	commonAddr := common.Address(addr)
	commonBeneficiary := common.Address(beneficiary)
	env := host.env
	db := env.StateDB
	alreadyDestructed := db.HasSelfDestructed(commonAddr)
	if !alreadyDestructed {
		db.AddRefund(params.SelfdestructRefundGas)
	}
	balance := db.GetBalance(commonAddr)
	if balance != nil {
		db.AddBalance(commonBeneficiary, balance, tracing.BalanceIncreaseSelfdestruct)
	}
	db.SelfDestruct(commonAddr)
	return !alreadyDestructed
}

func (host *HostContext) GetTxContext() evmc.TxContext {
	env := host.env

	var gasPriceHash common.Hash
	if env.TxContext.GasPrice != nil {
		gasPriceHash = common.BigToHash(env.TxContext.GasPrice)
	}
	var chainIDHash common.Hash
	if cfg := env.ChainConfig(); cfg != nil && cfg.ChainID != nil {
		chainIDHash = common.BigToHash(cfg.ChainID)
	}
	var prevRandao common.Hash
	if env.Context.Random != nil {
		prevRandao = *env.Context.Random
	} else if env.Context.Difficulty != nil {
		prevRandao = common.BigToHash(env.Context.Difficulty)
	}
	var baseFeeHash common.Hash
	if env.Context.BaseFee != nil {
		baseFeeHash = common.BigToHash(env.Context.BaseFee)
	}
	var blobBaseFeeHash common.Hash
	if env.Context.BlobBaseFee != nil {
		blobBaseFeeHash = common.BigToHash(env.Context.BlobBaseFee)
	}

	return evmc.TxContext{
		GasPrice:    evmc.Hash(gasPriceHash),
		Origin:      evmc.Address(env.TxContext.Origin),
		Coinbase:    evmc.Address(env.Context.Coinbase),
		Number:      env.Context.BlockNumber.Int64(),
		Timestamp:   int64(env.Context.Time),
		GasLimit:    int64(env.Context.GasLimit),
		PrevRandao:  evmc.Hash(prevRandao),
		ChainID:     evmc.Hash(chainIDHash),
		BaseFee:     evmc.Hash(baseFeeHash),
		BlobBaseFee: evmc.Hash(blobBaseFeeHash),
	}
}

func (host *HostContext) GetBlockHash(number int64) evmc.Hash {
	env := host.env
	if number >= 0 && env.Context.GetHash != nil {
		hash := env.Context.GetHash(uint64(number))
		return evmc.Hash(hash)
	}
	return evmc.Hash{}
}

func (host *HostContext) EmitLog(addr evmc.Address, topics []evmc.Hash, data []byte) {
	commonAddr := common.Address(addr)
	env := host.env
	// Convert []evmc.Hash to []common.Hash
	commonTopics := make([]common.Hash, len(topics))
	for i, topic := range topics {
		commonTopics[i] = common.Hash(topic)
	}
	env.StateDB.AddLog(&types.Log{
		Address:     commonAddr,
		Topics:      commonTopics,
		Data:        data,
		BlockNumber: env.Context.BlockNumber.Uint64(),
	})
}

func (host *HostContext) AccessAccount(addr evmc.Address) evmc.AccessStatus {
	commonAddr := common.Address(addr)
	if host.env.StateDB.AddressInAccessList(commonAddr) {
		return evmc.WarmAccess
	}
	host.env.StateDB.AddAddressToAccessList(commonAddr)
	return evmc.ColdAccess
}

func (host *HostContext) AccessStorage(addr evmc.Address, key evmc.Hash) evmc.AccessStatus {
	commonAddr := common.Address(addr)
	commonKey := common.Hash(key)
	status := evmc.WarmAccess
	if _, slotPresent := host.env.StateDB.SlotInAccessList(commonAddr, commonKey); !slotPresent {
		host.env.StateDB.AddSlotToAccessList(commonAddr, commonKey)
		status = evmc.ColdAccess
	}
	logHostAccessStorage(host.env, commonAddr, commonKey, status)
	return status
}

func (host *HostContext) GetTransientStorage(addr evmc.Address, key evmc.Hash) evmc.Hash {
	commonAddr := common.Address(addr)
	commonKey := common.Hash(key)
	value := host.env.StateDB.GetTransientState(commonAddr, commonKey)
	return evmc.Hash(value)
}

func (host *HostContext) SetTransientStorage(addr evmc.Address, key evmc.Hash, value evmc.Hash) {
	commonAddr := common.Address(addr)
	commonKey := common.Hash(key)
	commonValue := common.Hash(value)
	host.env.StateDB.SetTransientState(commonAddr, commonKey, commonValue)
}

func (host *HostContext) Call(kind evmc.CallKind,
	recipient evmc.Address, sender evmc.Address, value evmc.Hash, input []byte, gas int64, depth int,
	static bool, salt evmc.Hash, codeAddress evmc.Address) (output []byte, gasLeft int64, gasRefund int64,
	createAddr evmc.Address, err error) {

	// if host.shouldInterrupt() {
	// 	host.markInterrupted()
	// 	return nil, 0, 0, evmc.Address{}, evmc.Failure
	// }

	env := host.env

	gasU := uint64(gas)
	var gasLeftU uint64

	// Convert evmc.Hash to *uint256.Int for value
	valueBig := new(big.Int).SetBytes(value[:])
	amount := uint256.MustFromBig(valueBig)

	// Convert evmc.Address to common.Address
	destination := common.Address(recipient)
	callerAddr := host.contract.Address()
	originAddr := host.contract.Caller()

	var createAddrCommon common.Address

	switch kind {
	case evmc.Call:
		if static {
			output, gasLeftU, err = env.StaticCall(callerAddr, destination, input, gasU)
		} else {
			output, gasLeftU, err = env.Call(callerAddr, destination, input, gasU, amount, host.interrupt)
		}
	case evmc.DelegateCall:
		output, gasLeftU, err = env.DelegateCall(originAddr, callerAddr, destination, input, gasU, amount)
	case evmc.CallCode:
		output, gasLeftU, err = env.CallCode(callerAddr, destination, input, gasU, amount)
	case evmc.Create:
		var createOutput []byte
		createOutput, createAddrCommon, gasLeftU, err = env.Create(callerAddr, input, gasU, amount)
		isHomestead := env.ChainConfig().IsHomestead(env.Context.BlockNumber)
		if !isHomestead && err == ErrCodeStoreOutOfGas {
			err = nil
		}
		if err == ErrExecutionReverted {
			// Assign return buffer from REVERT.
			// TODO: Bad API design: return data buffer and the code is returned in the same place. In worst case
			//       the code is returned also when there is not enough funds to deploy the code.
			output = createOutput
		}
	case evmc.Create2:
		var createOutput []byte
		// Convert evmc.Hash to *uint256.Int for salt
		saltBig := new(big.Int).SetBytes(salt[:])
		saltInt := uint256.MustFromBig(saltBig)
		createOutput, createAddrCommon, gasLeftU, err = env.Create2(callerAddr, input, gasU, amount, saltInt)
		if err == ErrExecutionReverted {
			// Assign return buffer from REVERT.
			// TODO: Bad API design: return data buffer and the code is returned in the same place. In worst case
			//       the code is returned also when there is not enough funds to deploy the code.
			output = createOutput
		}
	default:
		panic(fmt.Errorf("EVMC: Unknown call kind %d", kind))
	}

	// Map errors.
	if err == ErrExecutionReverted {
		err = evmc.Revert
	} else if err != nil {
		err = evmc.Failure
	}

	gasLeft = int64(gasLeftU)
	createAddr = evmc.Address(createAddrCommon)
	gasRefund = 0 // TODO: Track gas refunds properly if needed
	return output, gasLeft, gasRefund, createAddr, err
}

func getRevision(env *EVM) evmc.Revision {
	n := env.Context.BlockNumber
	conf := env.ChainConfig()
	switch {
	case conf.IsOsaka(n):
		return evmc.Osaka
	case conf.IsPrague(n):
		return evmc.Prague
	case conf.IsCancun(n):
		return evmc.Cancun
	case conf.IsShanghai(n):
		return evmc.Shanghai
	case env.chainRules.IsMerge:
		return evmc.Paris
	case conf.IsLondon(n):
		return evmc.London
	case conf.IsBerlin(n):
		return evmc.Berlin
	case conf.IsIstanbul(n):
		return evmc.Istanbul
	case conf.IsPetersburg(n):
		return evmc.Petersburg
	case conf.IsConstantinople(n):
		return evmc.Constantinople
	case conf.IsByzantium(n):
		return evmc.Byzantium
	case conf.IsEIP158(n):
		return evmc.SpuriousDragon
	case conf.IsEIP150(n):
		return evmc.TangerineWhistle
	case conf.IsHomestead(n):
		return evmc.Homestead
	default:
		return evmc.Frontier
	}
}

func (evm *EVMC) Run(contract *Contract, input []byte, readOnly bool, interrupt *atomic.Bool) (ret []byte, err error) {
	// if interrupt == nil {
	// 	interrupt = new(atomic.Bool)
	// }
	// if interrupt.Load() {
	// 	return nil, ErrInterrupt
	// }

	baseDepth := evm.env.depth
	evm.env.depth++
	defer func() { evm.env.depth-- }()

	// Don't bother with the execution if there's no code.
	if len(contract.Code) == 0 {
		return nil, nil
	}

	kind := evmc.Call
	if evm.env.StateDB.GetCodeSize(contract.Address()) == 0 {
		// Guess if this is a CREATE.
		kind = evmc.Create
	}

	// Make sure the readOnly is only set if we aren't in readOnly yet.
	// This makes also sure that the readOnly flag isn't removed for child calls.
	if readOnly && !evm.readOnly {
		evm.readOnly = true
		defer func() { evm.readOnly = false }()
	}

	hostCtx := &HostContext{
		env:      evm.env,
		contract: contract,
		readOnly: evm.readOnly,
		// interrupt: interrupt,
	}

	var valueHash common.Hash
	if contract.value != nil {
		valueHash = common.BigToHash(contract.value.ToBig())
	}

	initialGas := contract.Gas

	traceEnabled := evmcTraceFlag.Load()
	revision := getRevision(evm.env)
	if traceEnabled {
		if err := evm.instance.SetOption("bor.trace_steps", "on"); err != nil {
			log.Warn("Failed to enable evmone step tracing", "err", err)
			traceEnabled = false
		} else {
			defer func() {
				if err := evm.instance.SetOption("bor.trace_steps", "off"); err != nil {
					log.Warn("Failed to disable evmone step tracing", "err", err)
				}
			}()
			log.Info("EVMC revision context", "addr", contract.Address(), "rev", revisionName(revision), "block", evm.env.Context.BlockNumber, "chainRules", chainRulesSummary(evm.env.chainRules))
		}
	}
	result, err := evm.instance.Execute(
		hostCtx,
		revision,
		kind,
		evm.readOnly,
		baseDepth,
		int64(contract.Gas),
		evmc.Address(contract.Address()),
		evmc.Address(contract.Caller()),
		input,
		evmc.Hash(valueHash),
		contract.Code,
	)

	evmcGasLeft := uint64(0)
	if result.GasLeft > 0 {
		evmcGasLeft = uint64(result.GasLeft)
	}
	contract.Gas = evmcGasLeft

	if len(result.TraceSteps) > 0 {
		frameDepth := int32(baseDepth + 1)
		for i := range result.TraceSteps {
			result.TraceSteps[i].Depth = frameDepth
		}
	}

	// if interrupt.Load() || hostCtx.wasInterrupted() {
	// 	return nil, ErrInterrupt
	// }

	if traceEnabled {
		evm.traceEVMC(contract, input, initialGas, evmcGasLeft, err, result.TraceSteps)
	}

	if err == evmc.Revert {
		err = ErrExecutionReverted
	} else if evmcError, ok := err.(evmc.Error); ok && evmcError.IsInternalError() {
		panic(fmt.Sprintf("EVMC VM internal error: %s", evmcError.Error()))
	}

	return result.Output, err
}

func (evm *EVMC) CanRun(code []byte) bool {
	cap := evmc.CapabilityEVM1
	wasmPreamble := []byte("\x00asm")
	if bytes.HasPrefix(code, wasmPreamble) {
		cap = evmc.CapabilityEWASM
	}
	// FIXME: Optimize. Access capabilities once.
	return evm.instance.HasCapability(cap)
}

func (evm *EVMC) traceEVMC(contract *Contract, input []byte, initialGas uint64, evmcGasLeft uint64, evmcErr error, evmoneSteps []evmc.TraceStep) {
	if !evmcTraceFlag.Load() {
		return
	}

	txHash, txIndex := extractTxInfo(evm.env.StateDB)
	if !shouldTraceTx(txHash, txIndex) {
		return
	}

	steps, goGasLeft, goErr := evm.runGoInterpreterTrace(contract, input, initialGas)
	if goErr != nil {
		log.Debug("EVMC shadow trace failed", "addr", contract.Address(), "err", goErr)
		return
	}

	evmcUsed := initialGas - evmcGasLeft
	goUsed := initialGas - goGasLeft
	if goUsed == evmcUsed && evmcErr == nil {
		return
	}

	delta := int64(goUsed) - int64(evmcUsed)

	fields := []interface{}{
		"txHash", txHash,
		"txIndex", txIndex,
		"addr", contract.Address(),
		"evmcRevision", revisionName(getRevision(evm.env)),
		"chainRules", chainRulesSummary(evm.env.chainRules),
		"evmoneGas", evmcUsed,
		"borGas", goUsed,
		"delta", delta,
		"evmcErr", errToString(evmcErr),
		"borErr", errToString(goErr),
	}

	if len(evmoneSteps) == 0 {
		fields = append(fields, "reason", "evmone trace missing")
		log.Warn("EVMC gas mismatch", fields...)
		return
	}

	goIdx, evmoneIdx := findTraceMismatch(steps, evmoneSteps)
	if goIdx < 0 && evmoneIdx < 0 {
		fields = append(fields, "reason", "no opcode mismatch")
		log.Warn("EVMC gas mismatch", fields...)
		return
	}

	var (
		goStep     *evmcTraceStep
		evmoneStep *evmc.TraceStep
	)

	if goIdx >= 0 && goIdx < len(steps) {
		goStep = &steps[goIdx]
	}
	if evmoneIdx >= 0 && evmoneIdx < len(evmoneSteps) {
		evmoneStep = &evmoneSteps[evmoneIdx]
	}

	if goStep != nil {
		fields = append(fields,
			"borPC", goStep.pc,
			"borOp", OpCode(goStep.op),
			"borGas", int64(goStep.gas),
			"borDepth", goStep.depth,
			"borFault", goStep.fault,
		)
	}
	if evmoneStep != nil {
		fields = append(fields,
			"evmonePC", evmoneStep.PC,
			"evmoneOp", OpCode(evmoneStep.Opcode),
			"evmoneGas", evmoneStep.Gas,
			"evmoneDepth", evmoneStep.Depth,
		)
	}

	if ctx := summarizeGoTraceContext(steps, goIdx, 4); ctx != "" {
		fields = append(fields, "borTrace", ctx)
	}
	if ctx := summarizeEvmoneTraceContext(evmoneSteps, evmoneIdx, 4, evmcGasLeft); ctx != "" {
		fields = append(fields, "evmoneTrace", ctx)
	}

	log.Warn("EVMC gas mismatch", fields...)
}

func (evm *EVMC) runGoInterpreterTrace(contract *Contract, input []byte, initialGas uint64) ([]evmcTraceStep, uint64, error) {
	snapshot := evm.env.StateDB.Snapshot()
	defer evm.env.StateDB.RevertToSnapshot(snapshot)

	contractCopy := cloneContractForTrace(contract, initialGas)
	tracer := &evmcTraceLogger{}
	hooks := tracing.Hooks{OnOpcode: tracer.onOpcode}
	prevTracer := evm.env.Config.Tracer
	evm.env.Config.Tracer = &hooks
	adjustDepth := false
	if evm.env.depth > 0 {
		evm.env.depth--
		adjustDepth = true
	}
	if adjustDepth {
		defer func() {
			evm.env.depth++
		}()
	}
	_, err := evm.env.interpreter.Run(contractCopy, append([]byte(nil), input...), evm.readOnly, nil)
	evm.env.Config.Tracer = prevTracer

	return tracer.steps, contractCopy.Gas, err
}

type txHashProvider interface {
	TxHash() common.Hash
}

type txIndexProvider interface {
	TxIndex() int
}

func extractTxInfo(db StateDB) (common.Hash, int) {
	var hash common.Hash
	index := -1

	if provider, ok := db.(txHashProvider); ok {
		hash = provider.TxHash()
	} else if inner := db.Inner(); inner != nil {
		if provider, ok := interface{}(inner).(txHashProvider); ok {
			hash = provider.TxHash()
		}
	}

	if provider, ok := db.(txIndexProvider); ok {
		index = provider.TxIndex()
	} else if inner := db.Inner(); inner != nil {
		if provider, ok := interface{}(inner).(txIndexProvider); ok {
			index = provider.TxIndex()
		}
	}

	return hash, index
}

func shouldTraceTx(hash common.Hash, index int) bool {
	traceTargetMu.RLock()
	defer traceTargetMu.RUnlock()
	if !traceTargetSet {
		return true
	}
	if traceTargetHash != (common.Hash{}) && hash == traceTargetHash {
		return true
	}
	if traceTargetIndex >= 0 && index == traceTargetIndex {
		return true
	}
	return false
}

func setEVMCMismatchTraceTarget(hash common.Hash, index int) {
	traceTargetMu.Lock()
	defer traceTargetMu.Unlock()
	traceTargetHash = hash
	traceTargetIndex = index
	traceTargetSet = true
}

func clearEVMCMismatchTraceTarget() {
	traceTargetMu.Lock()
	defer traceTargetMu.Unlock()
	traceTargetHash = common.Hash{}
	traceTargetIndex = -1
	traceTargetSet = false
}

func findTraceMismatch(goSteps []evmcTraceStep, evmoneSteps []evmc.TraceStep) (int, int) {
	limit := len(goSteps)
	if len(evmoneSteps) < limit {
		limit = len(evmoneSteps)
	}
	for i := 0; i < limit; i++ {
		goGas := int64(goSteps[i].gas)
		evmoneGas := evmoneSteps[i].Gas
		if goGas != evmoneGas || goSteps[i].op != evmoneSteps[i].Opcode {
			return i, i
		}
	}
	if len(goSteps) > len(evmoneSteps) {
		return limit, -1
	}
	if len(evmoneSteps) > len(goSteps) {
		return -1, limit
	}
	return -1, -1
}

func summarizeGoTraceContext(steps []evmcTraceStep, idx int, history int) string {
	if len(steps) == 0 {
		return ""
	}
	if idx < 0 || idx >= len(steps) {
		idx = len(steps) - 1
	}
	start := idx - history
	if start < 0 {
		start = 0
	}
	var b strings.Builder
	for i := start; i <= idx; i++ {
		if b.Len() > 0 {
			b.WriteString(" | ")
		}
		step := steps[i]
		fmt.Fprintf(&b, "#%d pc=%d op=%s gas=%d cost=%d depth=%d", i, step.pc, OpCode(step.op), step.gas, step.cost, step.depth)
		if step.fault != "" {
			fmt.Fprintf(&b, " fault=%s", step.fault)
		}
	}
	return b.String()
}

func summarizeEvmoneTraceContext(steps []evmc.TraceStep, idx int, history int, finalGas uint64) string {
	if len(steps) == 0 {
		return ""
	}
	if idx < 0 || idx >= len(steps) {
		idx = len(steps) - 1
	}
	start := idx - history
	if start < 0 {
		start = 0
	}
	var b strings.Builder
	var finalGasAfter int64
	if finalGas > math.MaxInt64 {
		finalGasAfter = math.MaxInt64
	} else {
		finalGasAfter = int64(finalGas)
	}
	for i := start; i <= idx && i < len(steps); i++ {
		if b.Len() > 0 {
			b.WriteString(" | ")
		}
		step := steps[i]
		nextGas := finalGasAfter
		if i+1 < len(steps) {
			nextGas = steps[i+1].Gas
		}
		cost := step.Gas - nextGas
		fmt.Fprintf(&b, "#%d pc=%d op=%s gas=%d cost=%d depth=%d gasAfter=%d",
			i, step.PC, OpCode(step.Opcode), step.Gas, cost, step.Depth, nextGas)
	}
	return b.String()
}

func cloneContractForTrace(original *Contract, gas uint64) *Contract {
	var valueCopy *uint256.Int
	if original.value != nil {
		valueCopy = new(uint256.Int)
		valueCopy.Set(original.value)
	}

	clone := NewContract(original.caller, original.address, valueCopy, gas, original.jumpdests)
	if len(original.Code) > 0 {
		clone.Code = append([]byte(nil), original.Code...)
	}
	clone.CodeHash = original.CodeHash
	if len(original.Input) > 0 {
		clone.Input = append([]byte(nil), original.Input...)
	}
	clone.IsDeployment = original.IsDeployment
	clone.IsSystemCall = original.IsSystemCall
	return clone
}

type evmcTraceStep struct {
	pc       uint64
	op       byte
	gas      uint64
	cost     uint64
	gasAfter uint64
	depth    int
	fault    string
}

type evmcTraceLogger struct {
	steps []evmcTraceStep
}

func (l *evmcTraceLogger) onOpcode(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, fault error) {
	gasAfter := uint64(0)
	if gas > cost {
		gasAfter = gas - cost
	}
	step := evmcTraceStep{pc: pc, op: op, gas: gas, cost: cost, gasAfter: gasAfter, depth: depth}
	if fault != nil {
		step.fault = fault.Error()
	}
	l.steps = append(l.steps, step)
}

func errToString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func revisionName(rev evmc.Revision) string {
	switch rev {
	case evmc.Frontier:
		return "Frontier"
	case evmc.Homestead:
		return "Homestead"
	case evmc.TangerineWhistle:
		return "TangerineWhistle"
	case evmc.SpuriousDragon:
		return "SpuriousDragon"
	case evmc.Byzantium:
		return "Byzantium"
	case evmc.Constantinople:
		return "Constantinople"
	case evmc.Petersburg:
		return "Petersburg"
	case evmc.Istanbul:
		return "Istanbul"
	case evmc.Berlin:
		return "Berlin"
	case evmc.London:
		return "London"
	case evmc.Paris:
		return "Paris"
	case evmc.Shanghai:
		return "Shanghai"
	case evmc.Cancun:
		return "Cancun"
	case evmc.Prague:
		return "Prague"
	case evmc.Osaka:
		return "Osaka"
	default:
		return fmt.Sprintf("rev(%d)", int32(rev))
	}
}

func chainRulesSummary(r params.Rules) string {
	return fmt.Sprintf("Homestead=%t EIP150=%t EIP158=%t Byzantium=%t Constantinople=%t Petersburg=%t Istanbul=%t Berlin=%t London=%t Merge=%t Shanghai=%t Cancun=%t Prague=%t Osaka=%t",
		r.IsHomestead, r.IsEIP150, r.IsEIP158, r.IsByzantium, r.IsConstantinople, r.IsPetersburg, r.IsIstanbul, r.IsBerlin, r.IsLondon, r.IsMerge, r.IsShanghai, r.IsCancun, r.IsPrague, r.IsOsaka)
}

func traceLoggingContext(env *EVM) (common.Hash, int, bool) {
	if env == nil {
		return common.Hash{}, -1, false
	}
	if !evmcTraceFlag.Load() {
		return common.Hash{}, -1, false
	}
	txHash, txIndex := extractTxInfo(env.StateDB)
	if !shouldTraceTx(txHash, txIndex) {
		return common.Hash{}, -1, false
	}
	return txHash, txIndex, true
}

func logBorGasDecision(env *EVM, op OpCode, addr common.Address, slot *common.Hash, warm bool, cost uint64) {
	txHash, txIndex, ok := traceLoggingContext(env)
	if !ok {
		return
	}
	fields := []interface{}{
		"txHash", txHash,
		"txIndex", txIndex,
		"addr", addr,
		"op", op,
		"warm", warm,
		"cost", cost,
		"depth", env.depth,
		"borRevision", revisionName(getRevision(env)),
		"chainRules", chainRulesSummary(env.chainRules),
	}
	if slot != nil {
		fields = append(fields, "slot", *slot)
	}
	log.Debug("EVMC bor gas cost decision", fields...)
}

func logHostAccessStorage(env *EVM, addr common.Address, slot common.Hash, status evmc.AccessStatus) {
	txHash, txIndex, ok := traceLoggingContext(env)
	if !ok {
		return
	}
	fields := []interface{}{
		"txHash", txHash,
		"txIndex", txIndex,
		"addr", addr,
		"slot", slot,
		"status", accessStatusName(status),
		"warm", status == evmc.WarmAccess,
		"depth", env.depth,
		"borRevision", revisionName(getRevision(env)),
		"chainRules", chainRulesSummary(env.chainRules),
	}
	log.Debug("EVMC host access storage", fields...)
}

var (
	evmcTraceFlag atomic.Bool

	traceTargetMu    sync.RWMutex
	traceTargetSet   bool
	traceTargetHash  common.Hash
	traceTargetIndex int = -1
)

// EnableEVMCMismatchTrace enables EVMC shadow tracing for the next block replay.

// SetEVMCMismatchTraceTarget restricts tracing to a specific transaction.
func SetEVMCMismatchTraceTarget(hash common.Hash, index int) {
	setEVMCMismatchTraceTarget(hash, index)
}

// ClearEVMCMismatchTraceTarget removes any transaction-specific trace filter.
func ClearEVMCMismatchTraceTarget() {
	clearEVMCMismatchTraceTarget()
}

func EnableEVMCMismatchTrace() func() {
	evmcTraceFlag.Store(true)
	return func() {
		evmcTraceFlag.Store(false)
		clearEVMCMismatchTraceTarget()
	}
}

func accessStatusName(status evmc.AccessStatus) string {
	switch status {
	case evmc.WarmAccess:
		return "warm"
	case evmc.ColdAccess:
		return "cold"
	default:
		return fmt.Sprintf("status(%d)", int(status))
	}
}
