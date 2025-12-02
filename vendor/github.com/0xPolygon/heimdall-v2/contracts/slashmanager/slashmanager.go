// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package slashmanager

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

// SlashmanagerMetaData contains all meta data concerning the Slashmanager contract.
var SlashmanagerMetaData = &bind.MetaData{
	ABI: "[{\"constant\":true,\"inputs\":[],\"name\":\"proposerRate\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"value\",\"type\":\"uint256\"},{\"internalType\":\"address\",\"name\":\"token\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"destination\",\"type\":\"address\"}],\"name\":\"drainTokens\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"jailCheckpoints\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"bytes\",\"name\":\"data\",\"type\":\"bytes\"},{\"internalType\":\"bytes\",\"name\":\"sigs\",\"type\":\"bytes\"}],\"name\":\"updateSlashedAmounts\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[],\"name\":\"renounceOwnership\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"registry\",\"outputs\":[{\"internalType\":\"contractRegistry\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"owner\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"isOwner\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"slashingNonce\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"newReportRate\",\"type\":\"uint256\"}],\"name\":\"updateReportRate\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"reportRate\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"VOTE_TYPE\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"uint256\",\"name\":\"newProposerRate\",\"type\":\"uint256\"}],\"name\":\"updateProposerRate\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"string\",\"name\":\"_heimdallId\",\"type\":\"string\"}],\"name\":\"setHeimdallId\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"logger\",\"outputs\":[{\"internalType\":\"contractStakingInfo\",\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"transferOwnership\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"heimdallId\",\"outputs\":[{\"internalType\":\"bytes32\",\"name\":\"\",\"type\":\"bytes32\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"_registry\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"_logger\",\"type\":\"address\"},{\"internalType\":\"string\",\"name\":\"_heimdallId\",\"type\":\"string\"}],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"constructor\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"previousOwner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"OwnershipTransferred\",\"type\":\"event\"}]",
}

// SlashmanagerABI is the input ABI used to generate the binding from.
// Deprecated: Use SlashmanagerMetaData.ABI instead.
var SlashmanagerABI = SlashmanagerMetaData.ABI

// Slashmanager is an auto generated Go binding around an Ethereum contract.
type Slashmanager struct {
	SlashmanagerCaller     // Read-only binding to the contract
	SlashmanagerTransactor // Write-only binding to the contract
	SlashmanagerFilterer   // Log filterer for contract events
}

// SlashmanagerCaller is an auto generated read-only Go binding around an Ethereum contract.
type SlashmanagerCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// SlashmanagerTransactor is an auto generated write-only Go binding around an Ethereum contract.
type SlashmanagerTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// SlashmanagerFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type SlashmanagerFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// SlashmanagerSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type SlashmanagerSession struct {
	Contract     *Slashmanager     // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// SlashmanagerCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type SlashmanagerCallerSession struct {
	Contract *SlashmanagerCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts       // Call options to use throughout this session
}

// SlashmanagerTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type SlashmanagerTransactorSession struct {
	Contract     *SlashmanagerTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts       // Transaction auth options to use throughout this session
}

// SlashmanagerRaw is an auto generated low-level Go binding around an Ethereum contract.
type SlashmanagerRaw struct {
	Contract *Slashmanager // Generic contract binding to access the raw methods on
}

// SlashmanagerCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type SlashmanagerCallerRaw struct {
	Contract *SlashmanagerCaller // Generic read-only contract binding to access the raw methods on
}

// SlashmanagerTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type SlashmanagerTransactorRaw struct {
	Contract *SlashmanagerTransactor // Generic write-only contract binding to access the raw methods on
}

// NewSlashmanager creates a new instance of Slashmanager, bound to a specific deployed contract.
func NewSlashmanager(address common.Address, backend bind.ContractBackend) (*Slashmanager, error) {
	contract, err := bindSlashmanager(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &Slashmanager{SlashmanagerCaller: SlashmanagerCaller{contract: contract}, SlashmanagerTransactor: SlashmanagerTransactor{contract: contract}, SlashmanagerFilterer: SlashmanagerFilterer{contract: contract}}, nil
}

// NewSlashmanagerCaller creates a new read-only instance of Slashmanager, bound to a specific deployed contract.
func NewSlashmanagerCaller(address common.Address, caller bind.ContractCaller) (*SlashmanagerCaller, error) {
	contract, err := bindSlashmanager(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &SlashmanagerCaller{contract: contract}, nil
}

// NewSlashmanagerTransactor creates a new write-only instance of Slashmanager, bound to a specific deployed contract.
func NewSlashmanagerTransactor(address common.Address, transactor bind.ContractTransactor) (*SlashmanagerTransactor, error) {
	contract, err := bindSlashmanager(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &SlashmanagerTransactor{contract: contract}, nil
}

// NewSlashmanagerFilterer creates a new log filterer instance of Slashmanager, bound to a specific deployed contract.
func NewSlashmanagerFilterer(address common.Address, filterer bind.ContractFilterer) (*SlashmanagerFilterer, error) {
	contract, err := bindSlashmanager(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &SlashmanagerFilterer{contract: contract}, nil
}

// bindSlashmanager binds a generic wrapper to an already deployed contract.
func bindSlashmanager(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := abi.JSON(strings.NewReader(SlashmanagerABI))
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Slashmanager *SlashmanagerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Slashmanager.Contract.SlashmanagerCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Slashmanager *SlashmanagerRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Slashmanager.Contract.SlashmanagerTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Slashmanager *SlashmanagerRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Slashmanager.Contract.SlashmanagerTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Slashmanager *SlashmanagerCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Slashmanager.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Slashmanager *SlashmanagerTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Slashmanager.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Slashmanager *SlashmanagerTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Slashmanager.Contract.contract.Transact(opts, method, params...)
}

// VOTETYPE is a free data retrieval call binding the contract method 0xd5b844eb.
//
// Solidity: function VOTE_TYPE() view returns(uint8)
func (_Slashmanager *SlashmanagerCaller) VOTETYPE(opts *bind.CallOpts) (uint8, error) {
	var out []interface{}
	err := _Slashmanager.contract.Call(opts, &out, "VOTE_TYPE")
	if err != nil {
		return *new(uint8), err
	}

	out0 := *abi.ConvertType(out[0], new(uint8)).(*uint8)

	return out0, err
}

// VOTETYPE is a free data retrieval call binding the contract method 0xd5b844eb.
//
// Solidity: function VOTE_TYPE() view returns(uint8)
func (_Slashmanager *SlashmanagerSession) VOTETYPE() (uint8, error) {
	return _Slashmanager.Contract.VOTETYPE(&_Slashmanager.CallOpts)
}

// VOTETYPE is a free data retrieval call binding the contract method 0xd5b844eb.
//
// Solidity: function VOTE_TYPE() view returns(uint8)
func (_Slashmanager *SlashmanagerCallerSession) VOTETYPE() (uint8, error) {
	return _Slashmanager.Contract.VOTETYPE(&_Slashmanager.CallOpts)
}

// HeimdallId is a free data retrieval call binding the contract method 0xfbc3dd36.
//
// Solidity: function heimdallId() view returns(bytes32)
func (_Slashmanager *SlashmanagerCaller) HeimdallId(opts *bind.CallOpts) ([32]byte, error) {
	var out []interface{}
	err := _Slashmanager.contract.Call(opts, &out, "heimdallId")
	if err != nil {
		return *new([32]byte), err
	}

	out0 := *abi.ConvertType(out[0], new([32]byte)).(*[32]byte)

	return out0, err
}

// HeimdallId is a free data retrieval call binding the contract method 0xfbc3dd36.
//
// Solidity: function heimdallId() view returns(bytes32)
func (_Slashmanager *SlashmanagerSession) HeimdallId() ([32]byte, error) {
	return _Slashmanager.Contract.HeimdallId(&_Slashmanager.CallOpts)
}

// HeimdallId is a free data retrieval call binding the contract method 0xfbc3dd36.
//
// Solidity: function heimdallId() view returns(bytes32)
func (_Slashmanager *SlashmanagerCallerSession) HeimdallId() ([32]byte, error) {
	return _Slashmanager.Contract.HeimdallId(&_Slashmanager.CallOpts)
}

// IsOwner is a free data retrieval call binding the contract method 0x8f32d59b.
//
// Solidity: function isOwner() view returns(bool)
func (_Slashmanager *SlashmanagerCaller) IsOwner(opts *bind.CallOpts) (bool, error) {
	var out []interface{}
	err := _Slashmanager.contract.Call(opts, &out, "isOwner")
	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err
}

// IsOwner is a free data retrieval call binding the contract method 0x8f32d59b.
//
// Solidity: function isOwner() view returns(bool)
func (_Slashmanager *SlashmanagerSession) IsOwner() (bool, error) {
	return _Slashmanager.Contract.IsOwner(&_Slashmanager.CallOpts)
}

// IsOwner is a free data retrieval call binding the contract method 0x8f32d59b.
//
// Solidity: function isOwner() view returns(bool)
func (_Slashmanager *SlashmanagerCallerSession) IsOwner() (bool, error) {
	return _Slashmanager.Contract.IsOwner(&_Slashmanager.CallOpts)
}

// JailCheckpoints is a free data retrieval call binding the contract method 0x556b2ce9.
//
// Solidity: function jailCheckpoints() view returns(uint256)
func (_Slashmanager *SlashmanagerCaller) JailCheckpoints(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Slashmanager.contract.Call(opts, &out, "jailCheckpoints")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// JailCheckpoints is a free data retrieval call binding the contract method 0x556b2ce9.
//
// Solidity: function jailCheckpoints() view returns(uint256)
func (_Slashmanager *SlashmanagerSession) JailCheckpoints() (*big.Int, error) {
	return _Slashmanager.Contract.JailCheckpoints(&_Slashmanager.CallOpts)
}

// JailCheckpoints is a free data retrieval call binding the contract method 0x556b2ce9.
//
// Solidity: function jailCheckpoints() view returns(uint256)
func (_Slashmanager *SlashmanagerCallerSession) JailCheckpoints() (*big.Int, error) {
	return _Slashmanager.Contract.JailCheckpoints(&_Slashmanager.CallOpts)
}

// Logger is a free data retrieval call binding the contract method 0xf24ccbfe.
//
// Solidity: function logger() view returns(address)
func (_Slashmanager *SlashmanagerCaller) Logger(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Slashmanager.contract.Call(opts, &out, "logger")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// Logger is a free data retrieval call binding the contract method 0xf24ccbfe.
//
// Solidity: function logger() view returns(address)
func (_Slashmanager *SlashmanagerSession) Logger() (common.Address, error) {
	return _Slashmanager.Contract.Logger(&_Slashmanager.CallOpts)
}

// Logger is a free data retrieval call binding the contract method 0xf24ccbfe.
//
// Solidity: function logger() view returns(address)
func (_Slashmanager *SlashmanagerCallerSession) Logger() (common.Address, error) {
	return _Slashmanager.Contract.Logger(&_Slashmanager.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Slashmanager *SlashmanagerCaller) Owner(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Slashmanager.contract.Call(opts, &out, "owner")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Slashmanager *SlashmanagerSession) Owner() (common.Address, error) {
	return _Slashmanager.Contract.Owner(&_Slashmanager.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Slashmanager *SlashmanagerCallerSession) Owner() (common.Address, error) {
	return _Slashmanager.Contract.Owner(&_Slashmanager.CallOpts)
}

// ProposerRate is a free data retrieval call binding the contract method 0x3199e305.
//
// Solidity: function proposerRate() view returns(uint256)
func (_Slashmanager *SlashmanagerCaller) ProposerRate(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Slashmanager.contract.Call(opts, &out, "proposerRate")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// ProposerRate is a free data retrieval call binding the contract method 0x3199e305.
//
// Solidity: function proposerRate() view returns(uint256)
func (_Slashmanager *SlashmanagerSession) ProposerRate() (*big.Int, error) {
	return _Slashmanager.Contract.ProposerRate(&_Slashmanager.CallOpts)
}

// ProposerRate is a free data retrieval call binding the contract method 0x3199e305.
//
// Solidity: function proposerRate() view returns(uint256)
func (_Slashmanager *SlashmanagerCallerSession) ProposerRate() (*big.Int, error) {
	return _Slashmanager.Contract.ProposerRate(&_Slashmanager.CallOpts)
}

// Registry is a free data retrieval call binding the contract method 0x7b103999.
//
// Solidity: function registry() view returns(address)
func (_Slashmanager *SlashmanagerCaller) Registry(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Slashmanager.contract.Call(opts, &out, "registry")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// Registry is a free data retrieval call binding the contract method 0x7b103999.
//
// Solidity: function registry() view returns(address)
func (_Slashmanager *SlashmanagerSession) Registry() (common.Address, error) {
	return _Slashmanager.Contract.Registry(&_Slashmanager.CallOpts)
}

// Registry is a free data retrieval call binding the contract method 0x7b103999.
//
// Solidity: function registry() view returns(address)
func (_Slashmanager *SlashmanagerCallerSession) Registry() (common.Address, error) {
	return _Slashmanager.Contract.Registry(&_Slashmanager.CallOpts)
}

// ReportRate is a free data retrieval call binding the contract method 0xc25593be.
//
// Solidity: function reportRate() view returns(uint256)
func (_Slashmanager *SlashmanagerCaller) ReportRate(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Slashmanager.contract.Call(opts, &out, "reportRate")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// ReportRate is a free data retrieval call binding the contract method 0xc25593be.
//
// Solidity: function reportRate() view returns(uint256)
func (_Slashmanager *SlashmanagerSession) ReportRate() (*big.Int, error) {
	return _Slashmanager.Contract.ReportRate(&_Slashmanager.CallOpts)
}

// ReportRate is a free data retrieval call binding the contract method 0xc25593be.
//
// Solidity: function reportRate() view returns(uint256)
func (_Slashmanager *SlashmanagerCallerSession) ReportRate() (*big.Int, error) {
	return _Slashmanager.Contract.ReportRate(&_Slashmanager.CallOpts)
}

// SlashingNonce is a free data retrieval call binding the contract method 0xa2d32176.
//
// Solidity: function slashingNonce() view returns(uint256)
func (_Slashmanager *SlashmanagerCaller) SlashingNonce(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Slashmanager.contract.Call(opts, &out, "slashingNonce")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// SlashingNonce is a free data retrieval call binding the contract method 0xa2d32176.
//
// Solidity: function slashingNonce() view returns(uint256)
func (_Slashmanager *SlashmanagerSession) SlashingNonce() (*big.Int, error) {
	return _Slashmanager.Contract.SlashingNonce(&_Slashmanager.CallOpts)
}

// SlashingNonce is a free data retrieval call binding the contract method 0xa2d32176.
//
// Solidity: function slashingNonce() view returns(uint256)
func (_Slashmanager *SlashmanagerCallerSession) SlashingNonce() (*big.Int, error) {
	return _Slashmanager.Contract.SlashingNonce(&_Slashmanager.CallOpts)
}

// DrainTokens is a paid mutator transaction binding the contract method 0x43f7505a.
//
// Solidity: function drainTokens(uint256 value, address token, address destination) returns()
func (_Slashmanager *SlashmanagerTransactor) DrainTokens(opts *bind.TransactOpts, value *big.Int, token common.Address, destination common.Address) (*types.Transaction, error) {
	return _Slashmanager.contract.Transact(opts, "drainTokens", value, token, destination)
}

// DrainTokens is a paid mutator transaction binding the contract method 0x43f7505a.
//
// Solidity: function drainTokens(uint256 value, address token, address destination) returns()
func (_Slashmanager *SlashmanagerSession) DrainTokens(value *big.Int, token common.Address, destination common.Address) (*types.Transaction, error) {
	return _Slashmanager.Contract.DrainTokens(&_Slashmanager.TransactOpts, value, token, destination)
}

// DrainTokens is a paid mutator transaction binding the contract method 0x43f7505a.
//
// Solidity: function drainTokens(uint256 value, address token, address destination) returns()
func (_Slashmanager *SlashmanagerTransactorSession) DrainTokens(value *big.Int, token common.Address, destination common.Address) (*types.Transaction, error) {
	return _Slashmanager.Contract.DrainTokens(&_Slashmanager.TransactOpts, value, token, destination)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Slashmanager *SlashmanagerTransactor) RenounceOwnership(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Slashmanager.contract.Transact(opts, "renounceOwnership")
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Slashmanager *SlashmanagerSession) RenounceOwnership() (*types.Transaction, error) {
	return _Slashmanager.Contract.RenounceOwnership(&_Slashmanager.TransactOpts)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Slashmanager *SlashmanagerTransactorSession) RenounceOwnership() (*types.Transaction, error) {
	return _Slashmanager.Contract.RenounceOwnership(&_Slashmanager.TransactOpts)
}

// SetHeimdallId is a paid mutator transaction binding the contract method 0xea0688b3.
//
// Solidity: function setHeimdallId(string _heimdallId) returns()
func (_Slashmanager *SlashmanagerTransactor) SetHeimdallId(opts *bind.TransactOpts, _heimdallId string) (*types.Transaction, error) {
	return _Slashmanager.contract.Transact(opts, "setHeimdallId", _heimdallId)
}

// SetHeimdallId is a paid mutator transaction binding the contract method 0xea0688b3.
//
// Solidity: function setHeimdallId(string _heimdallId) returns()
func (_Slashmanager *SlashmanagerSession) SetHeimdallId(_heimdallId string) (*types.Transaction, error) {
	return _Slashmanager.Contract.SetHeimdallId(&_Slashmanager.TransactOpts, _heimdallId)
}

// SetHeimdallId is a paid mutator transaction binding the contract method 0xea0688b3.
//
// Solidity: function setHeimdallId(string _heimdallId) returns()
func (_Slashmanager *SlashmanagerTransactorSession) SetHeimdallId(_heimdallId string) (*types.Transaction, error) {
	return _Slashmanager.Contract.SetHeimdallId(&_Slashmanager.TransactOpts, _heimdallId)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Slashmanager *SlashmanagerTransactor) TransferOwnership(opts *bind.TransactOpts, newOwner common.Address) (*types.Transaction, error) {
	return _Slashmanager.contract.Transact(opts, "transferOwnership", newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Slashmanager *SlashmanagerSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _Slashmanager.Contract.TransferOwnership(&_Slashmanager.TransactOpts, newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Slashmanager *SlashmanagerTransactorSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _Slashmanager.Contract.TransferOwnership(&_Slashmanager.TransactOpts, newOwner)
}

// UpdateProposerRate is a paid mutator transaction binding the contract method 0xe5bbd4a7.
//
// Solidity: function updateProposerRate(uint256 newProposerRate) returns()
func (_Slashmanager *SlashmanagerTransactor) UpdateProposerRate(opts *bind.TransactOpts, newProposerRate *big.Int) (*types.Transaction, error) {
	return _Slashmanager.contract.Transact(opts, "updateProposerRate", newProposerRate)
}

// UpdateProposerRate is a paid mutator transaction binding the contract method 0xe5bbd4a7.
//
// Solidity: function updateProposerRate(uint256 newProposerRate) returns()
func (_Slashmanager *SlashmanagerSession) UpdateProposerRate(newProposerRate *big.Int) (*types.Transaction, error) {
	return _Slashmanager.Contract.UpdateProposerRate(&_Slashmanager.TransactOpts, newProposerRate)
}

// UpdateProposerRate is a paid mutator transaction binding the contract method 0xe5bbd4a7.
//
// Solidity: function updateProposerRate(uint256 newProposerRate) returns()
func (_Slashmanager *SlashmanagerTransactorSession) UpdateProposerRate(newProposerRate *big.Int) (*types.Transaction, error) {
	return _Slashmanager.Contract.UpdateProposerRate(&_Slashmanager.TransactOpts, newProposerRate)
}

// UpdateReportRate is a paid mutator transaction binding the contract method 0xb7a071e9.
//
// Solidity: function updateReportRate(uint256 newReportRate) returns()
func (_Slashmanager *SlashmanagerTransactor) UpdateReportRate(opts *bind.TransactOpts, newReportRate *big.Int) (*types.Transaction, error) {
	return _Slashmanager.contract.Transact(opts, "updateReportRate", newReportRate)
}

// UpdateReportRate is a paid mutator transaction binding the contract method 0xb7a071e9.
//
// Solidity: function updateReportRate(uint256 newReportRate) returns()
func (_Slashmanager *SlashmanagerSession) UpdateReportRate(newReportRate *big.Int) (*types.Transaction, error) {
	return _Slashmanager.Contract.UpdateReportRate(&_Slashmanager.TransactOpts, newReportRate)
}

// UpdateReportRate is a paid mutator transaction binding the contract method 0xb7a071e9.
//
// Solidity: function updateReportRate(uint256 newReportRate) returns()
func (_Slashmanager *SlashmanagerTransactorSession) UpdateReportRate(newReportRate *big.Int) (*types.Transaction, error) {
	return _Slashmanager.Contract.UpdateReportRate(&_Slashmanager.TransactOpts, newReportRate)
}

// UpdateSlashedAmounts is a paid mutator transaction binding the contract method 0x67a4f7c6.
//
// Solidity: function updateSlashedAmounts(bytes data, bytes sigs) returns()
func (_Slashmanager *SlashmanagerTransactor) UpdateSlashedAmounts(opts *bind.TransactOpts, data []byte, sigs []byte) (*types.Transaction, error) {
	return _Slashmanager.contract.Transact(opts, "updateSlashedAmounts", data, sigs)
}

// UpdateSlashedAmounts is a paid mutator transaction binding the contract method 0x67a4f7c6.
//
// Solidity: function updateSlashedAmounts(bytes data, bytes sigs) returns()
func (_Slashmanager *SlashmanagerSession) UpdateSlashedAmounts(data []byte, sigs []byte) (*types.Transaction, error) {
	return _Slashmanager.Contract.UpdateSlashedAmounts(&_Slashmanager.TransactOpts, data, sigs)
}

// UpdateSlashedAmounts is a paid mutator transaction binding the contract method 0x67a4f7c6.
//
// Solidity: function updateSlashedAmounts(bytes data, bytes sigs) returns()
func (_Slashmanager *SlashmanagerTransactorSession) UpdateSlashedAmounts(data []byte, sigs []byte) (*types.Transaction, error) {
	return _Slashmanager.Contract.UpdateSlashedAmounts(&_Slashmanager.TransactOpts, data, sigs)
}

// SlashmanagerOwnershipTransferredIterator is returned from FilterOwnershipTransferred and is used to iterate over the raw logs and unpacked data for OwnershipTransferred events raised by the Slashmanager contract.
type SlashmanagerOwnershipTransferredIterator struct {
	Event *SlashmanagerOwnershipTransferred // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *SlashmanagerOwnershipTransferredIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(SlashmanagerOwnershipTransferred)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(SlashmanagerOwnershipTransferred)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *SlashmanagerOwnershipTransferredIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *SlashmanagerOwnershipTransferredIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// SlashmanagerOwnershipTransferred represents a OwnershipTransferred event raised by the Slashmanager contract.
type SlashmanagerOwnershipTransferred struct {
	PreviousOwner common.Address
	NewOwner      common.Address
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterOwnershipTransferred is a free log retrieval operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_Slashmanager *SlashmanagerFilterer) FilterOwnershipTransferred(opts *bind.FilterOpts, previousOwner []common.Address, newOwner []common.Address) (*SlashmanagerOwnershipTransferredIterator, error) {
	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _Slashmanager.contract.FilterLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return &SlashmanagerOwnershipTransferredIterator{contract: _Slashmanager.contract, event: "OwnershipTransferred", logs: logs, sub: sub}, nil
}

// WatchOwnershipTransferred is a free log subscription operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_Slashmanager *SlashmanagerFilterer) WatchOwnershipTransferred(opts *bind.WatchOpts, sink chan<- *SlashmanagerOwnershipTransferred, previousOwner []common.Address, newOwner []common.Address) (event.Subscription, error) {
	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _Slashmanager.contract.WatchLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(SlashmanagerOwnershipTransferred)
				if err := _Slashmanager.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseOwnershipTransferred is a log parse operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_Slashmanager *SlashmanagerFilterer) ParseOwnershipTransferred(log types.Log) (*SlashmanagerOwnershipTransferred, error) {
	event := new(SlashmanagerOwnershipTransferred)
	if err := _Slashmanager.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
