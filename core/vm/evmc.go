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
	"math/big"
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

var (
	createMu     sync.Mutex
	evmcConfig   string // The configuration the instance was created with.
	evmcInstance *evmc.VM
)

func createVM(config string) *evmc.VM {
	createMu.Lock()
	defer createMu.Unlock()

	if evmcInstance == nil {
		options := strings.Split(config, ",")
		path := options[0]

		if path == "" {
			panic("EVMC VM path not provided, set --vm.(evm|ewasm)=/path/to/vm")
		}

		var err error
		evmcInstance, err = evmc.Load(path)
		if err != nil {
			panic(err.Error())
		}
		log.Info("EVMC VM loaded", "name", evmcInstance.Name(), "version", evmcInstance.Version(), "path", path)

		for _, option := range options[1:] {
			if idx := strings.Index(option, "="); idx >= 0 {
				name := option[:idx]
				value := option[idx+1:]
				err := evmcInstance.SetOption(name, value)
				if err == nil {
					log.Info("EVMC VM option set", "name", name, "value", value)
				} else {
					log.Warn("EVMC VM option setting failed", "name", name, "error", err)
				}
			}
		}

		evm1Cap := evmcInstance.HasCapability(evmc.CapabilityEVM1)
		log.Info("EVMC VM capabilities", "evm1", evm1Cap)

		evmcConfig = config // Remember the config.
	} else if evmcConfig != config {
		log.Error("New EVMC VM requested", "newconfig", config, "oldconfig", evmcConfig)
	}
	return evmcInstance
}

func NewEVMC(options string, env *EVM) *EVMC {
	return &EVMC{createVM(options), env, false}
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
	fmt.Println("ARE WE IN EMITLOG ?!!: ", addr, topics, data)
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
	if _, slotPresent := host.env.StateDB.SlotInAccessList(commonAddr, commonKey); slotPresent {
		return evmc.WarmAccess
	}
	host.env.StateDB.AddSlotToAccessList(commonAddr, commonKey)
	return evmc.ColdAccess
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
	if conf.IsConstantinople(n) {
		return evmc.Constantinople
	}
	if conf.IsByzantium(n) {
		return evmc.Byzantium
	}
	if conf.IsEIP158(n) {
		return evmc.SpuriousDragon
	}
	if conf.IsEIP150(n) {
		return evmc.TangerineWhistle
	}
	if conf.IsHomestead(n) {
		return evmc.Homestead
	}
	return evmc.Frontier
}

func (evm *EVMC) Run(contract *Contract, input []byte, readOnly bool, interrupt *atomic.Bool) (ret []byte, err error) {
	// if interrupt == nil {
	// 	interrupt = new(atomic.Bool)
	// }
	// if interrupt.Load() {
	// 	return nil, ErrInterrupt
	// }

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

	result, err := evm.instance.Execute(
		hostCtx,
		getRevision(evm.env),
		kind,
		evm.readOnly,
		evm.env.depth-1,
		int64(contract.Gas),
		evmc.Address(contract.Address()),
		evmc.Address(contract.Caller()),
		input,
		evmc.Hash(valueHash),
		contract.Code,
	)

	contract.Gas = uint64(result.GasLeft)

	// if interrupt.Load() || hostCtx.wasInterrupted() {
	// 	return nil, ErrInterrupt
	// }

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
