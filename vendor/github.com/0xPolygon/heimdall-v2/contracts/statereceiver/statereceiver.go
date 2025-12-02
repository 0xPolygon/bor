// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package statereceiver

import (
	"errors"
	"math/big"
	"strings"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

// Reference imports to suppress errors if they are not otherwise used.
var (
	_ = errors.New
	_ = big.NewInt
	_ = strings.NewReader
	_ = ethereum.NotFound
	_ = bind.Bind
	_ = common.Big1
	_ = types.BloomLookup
	_ = event.NewSubscription
)

// StatereceiverMetaData contains all meta data concerning the Statereceiver contract.
var StatereceiverMetaData = &bind.MetaData{
	ABI: "[{\"constant\":true,\"inputs\":[],\"name\":\"SYSTEM_ADDRESS\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"lastStateId\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"syncTime\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"recordBytes\",\"type\":\"bytes\"}],\"name\":\"commitState\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"success\",\"type\":\"bool\"}],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"id\",\"type\":\"uint256\"},{\"internalType\":\"bytes\",\"name\":\"data\",\"type\":\"bytes\"}],\"name\":\"onStateReceive\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
}

// StatereceiverABI is the input ABI used to generate the binding from.
// Deprecated: Use StatereceiverMetaData.ABI instead.
var StatereceiverABI = StatereceiverMetaData.ABI

// Statereceiver is an auto generated Go binding around an Ethereum contract.
type Statereceiver struct {
	StatereceiverCaller     // Read-only binding to the contract
	StatereceiverTransactor // Write-only binding to the contract
	StatereceiverFilterer   // Log filterer for contract events
}

// StatereceiverCaller is an auto generated read-only Go binding around an Ethereum contract.
type StatereceiverCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// StatereceiverTransactor is an auto generated write-only Go binding around an Ethereum contract.
type StatereceiverTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// StatereceiverFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type StatereceiverFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// StatereceiverSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type StatereceiverSession struct {
	Contract     *Statereceiver    // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// StatereceiverCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type StatereceiverCallerSession struct {
	Contract *StatereceiverCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts        // Call options to use throughout this session
}

// StatereceiverTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type StatereceiverTransactorSession struct {
	Contract     *StatereceiverTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts        // Transaction auth options to use throughout this session
}

// StatereceiverRaw is an auto generated low-level Go binding around an Ethereum contract.
type StatereceiverRaw struct {
	Contract *Statereceiver // Generic contract binding to access the raw methods on
}

// StatereceiverCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type StatereceiverCallerRaw struct {
	Contract *StatereceiverCaller // Generic read-only contract binding to access the raw methods on
}

// StatereceiverTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type StatereceiverTransactorRaw struct {
	Contract *StatereceiverTransactor // Generic write-only contract binding to access the raw methods on
}

// NewStatereceiver creates a new instance of Statereceiver, bound to a specific deployed contract.
func NewStatereceiver(address common.Address, backend bind.ContractBackend) (*Statereceiver, error) {
	contract, err := bindStatereceiver(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &Statereceiver{StatereceiverCaller: StatereceiverCaller{contract: contract}, StatereceiverTransactor: StatereceiverTransactor{contract: contract}, StatereceiverFilterer: StatereceiverFilterer{contract: contract}}, nil
}

// NewStatereceiverCaller creates a new read-only instance of Statereceiver, bound to a specific deployed contract.
func NewStatereceiverCaller(address common.Address, caller bind.ContractCaller) (*StatereceiverCaller, error) {
	contract, err := bindStatereceiver(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &StatereceiverCaller{contract: contract}, nil
}

// NewStatereceiverTransactor creates a new write-only instance of Statereceiver, bound to a specific deployed contract.
func NewStatereceiverTransactor(address common.Address, transactor bind.ContractTransactor) (*StatereceiverTransactor, error) {
	contract, err := bindStatereceiver(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &StatereceiverTransactor{contract: contract}, nil
}

// NewStatereceiverFilterer creates a new log filterer instance of Statereceiver, bound to a specific deployed contract.
func NewStatereceiverFilterer(address common.Address, filterer bind.ContractFilterer) (*StatereceiverFilterer, error) {
	contract, err := bindStatereceiver(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &StatereceiverFilterer{contract: contract}, nil
}

// bindStatereceiver binds a generic wrapper to an already deployed contract.
func bindStatereceiver(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := abi.JSON(strings.NewReader(StatereceiverABI))
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Statereceiver *StatereceiverRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Statereceiver.Contract.StatereceiverCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Statereceiver *StatereceiverRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Statereceiver.Contract.StatereceiverTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Statereceiver *StatereceiverRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Statereceiver.Contract.StatereceiverTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Statereceiver *StatereceiverCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Statereceiver.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Statereceiver *StatereceiverTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Statereceiver.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Statereceiver *StatereceiverTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Statereceiver.Contract.contract.Transact(opts, method, params...)
}

// SYSTEMADDRESS is a free data retrieval call binding the contract method 0x3434735f.
//
// Solidity: function SYSTEM_ADDRESS() view returns(address)
func (_Statereceiver *StatereceiverCaller) SYSTEMADDRESS(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Statereceiver.contract.Call(opts, &out, "SYSTEM_ADDRESS")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// SYSTEMADDRESS is a free data retrieval call binding the contract method 0x3434735f.
//
// Solidity: function SYSTEM_ADDRESS() view returns(address)
func (_Statereceiver *StatereceiverSession) SYSTEMADDRESS() (common.Address, error) {
	return _Statereceiver.Contract.SYSTEMADDRESS(&_Statereceiver.CallOpts)
}

// SYSTEMADDRESS is a free data retrieval call binding the contract method 0x3434735f.
//
// Solidity: function SYSTEM_ADDRESS() view returns(address)
func (_Statereceiver *StatereceiverCallerSession) SYSTEMADDRESS() (common.Address, error) {
	return _Statereceiver.Contract.SYSTEMADDRESS(&_Statereceiver.CallOpts)
}

// LastStateId is a free data retrieval call binding the contract method 0x5407ca67.
//
// Solidity: function lastStateId() view returns(uint256)
func (_Statereceiver *StatereceiverCaller) LastStateId(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Statereceiver.contract.Call(opts, &out, "lastStateId")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// LastStateId is a free data retrieval call binding the contract method 0x5407ca67.
//
// Solidity: function lastStateId() view returns(uint256)
func (_Statereceiver *StatereceiverSession) LastStateId() (*big.Int, error) {
	return _Statereceiver.Contract.LastStateId(&_Statereceiver.CallOpts)
}

// LastStateId is a free data retrieval call binding the contract method 0x5407ca67.
//
// Solidity: function lastStateId() view returns(uint256)
func (_Statereceiver *StatereceiverCallerSession) LastStateId() (*big.Int, error) {
	return _Statereceiver.Contract.LastStateId(&_Statereceiver.CallOpts)
}

// CommitState is a paid mutator transaction binding the contract method 0x19494a17.
//
// Solidity: function commitState(uint256 syncTime, bytes recordBytes) returns(bool success)
func (_Statereceiver *StatereceiverTransactor) CommitState(opts *bind.TransactOpts, syncTime *big.Int, recordBytes []byte) (*types.Transaction, error) {
	return _Statereceiver.contract.Transact(opts, "commitState", syncTime, recordBytes)
}

// CommitState is a paid mutator transaction binding the contract method 0x19494a17.
//
// Solidity: function commitState(uint256 syncTime, bytes recordBytes) returns(bool success)
func (_Statereceiver *StatereceiverSession) CommitState(syncTime *big.Int, recordBytes []byte) (*types.Transaction, error) {
	return _Statereceiver.Contract.CommitState(&_Statereceiver.TransactOpts, syncTime, recordBytes)
}

// CommitState is a paid mutator transaction binding the contract method 0x19494a17.
//
// Solidity: function commitState(uint256 syncTime, bytes recordBytes) returns(bool success)
func (_Statereceiver *StatereceiverTransactorSession) CommitState(syncTime *big.Int, recordBytes []byte) (*types.Transaction, error) {
	return _Statereceiver.Contract.CommitState(&_Statereceiver.TransactOpts, syncTime, recordBytes)
}

// OnStateReceive is a paid mutator transaction binding the contract method 0x26c53bea.
//
// Solidity: function onStateReceive(uint256 id, bytes data) returns()
func (_Statereceiver *StatereceiverTransactor) OnStateReceive(opts *bind.TransactOpts, id *big.Int, data []byte) (*types.Transaction, error) {
	return _Statereceiver.contract.Transact(opts, "onStateReceive", id, data)
}

// OnStateReceive is a paid mutator transaction binding the contract method 0x26c53bea.
//
// Solidity: function onStateReceive(uint256 id, bytes data) returns()
func (_Statereceiver *StatereceiverSession) OnStateReceive(id *big.Int, data []byte) (*types.Transaction, error) {
	return _Statereceiver.Contract.OnStateReceive(&_Statereceiver.TransactOpts, id, data)
}

// OnStateReceive is a paid mutator transaction binding the contract method 0x26c53bea.
//
// Solidity: function onStateReceive(uint256 id, bytes data) returns()
func (_Statereceiver *StatereceiverTransactorSession) OnStateReceive(id *big.Int, data []byte) (*types.Transaction, error) {
	return _Statereceiver.Contract.OnStateReceive(&_Statereceiver.TransactOpts, id, data)
}
