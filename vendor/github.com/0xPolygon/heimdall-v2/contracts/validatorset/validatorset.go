// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package validatorset

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

// ValidatorsetMetaData contains all meta data concerning the Validatorset contract.
var ValidatorsetMetaData = &bind.MetaData{
	ABI: "[{\"constant\":true,\"inputs\":[{\"name\":\"span\",\"type\":\"uint256\"}],\"name\":\"getSpan\",\"outputs\":[{\"name\":\"number\",\"type\":\"uint256\"},{\"name\":\"startBlock\",\"type\":\"uint256\"},{\"name\":\"endBlock\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"name\":\"vote\",\"type\":\"bytes\"},{\"name\":\"sigs\",\"type\":\"bytes\"},{\"name\":\"txBytes\",\"type\":\"bytes\"},{\"name\":\"proof\",\"type\":\"bytes\"}],\"name\":\"commitSpan\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"currentSpanNumber\",\"outputs\":[{\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"getNextSpan\",\"outputs\":[{\"name\":\"number\",\"type\":\"uint256\"},{\"name\":\"startBlock\",\"type\":\"uint256\"},{\"name\":\"endBlock\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"getInitialValidators\",\"outputs\":[{\"name\":\"\",\"type\":\"address[]\"},{\"name\":\"\",\"type\":\"uint256[]\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"getCurrentSpan\",\"outputs\":[{\"name\":\"number\",\"type\":\"uint256\"},{\"name\":\"startBlock\",\"type\":\"uint256\"},{\"name\":\"endBlock\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"getValidators\",\"outputs\":[{\"name\":\"\",\"type\":\"address[]\"},{\"name\":\"\",\"type\":\"uint256[]\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"name\":\"vote\",\"type\":\"bytes\"},{\"name\":\"sigs\",\"type\":\"bytes\"},{\"name\":\"txBytes\",\"type\":\"bytes\"},{\"name\":\"proof\",\"type\":\"bytes\"}],\"name\":\"validateValidatorSet\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]",
}

// ValidatorsetABI is the input ABI used to generate the binding from.
// Deprecated: Use ValidatorsetMetaData.ABI instead.
var ValidatorsetABI = ValidatorsetMetaData.ABI

// Validatorset is an auto generated Go binding around an Ethereum contract.
type Validatorset struct {
	ValidatorsetCaller     // Read-only binding to the contract
	ValidatorsetTransactor // Write-only binding to the contract
	ValidatorsetFilterer   // Log filterer for contract events
}

// ValidatorsetCaller is an auto generated read-only Go binding around an Ethereum contract.
type ValidatorsetCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ValidatorsetTransactor is an auto generated write-only Go binding around an Ethereum contract.
type ValidatorsetTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ValidatorsetFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type ValidatorsetFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// ValidatorsetSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type ValidatorsetSession struct {
	Contract     *Validatorset     // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// ValidatorsetCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type ValidatorsetCallerSession struct {
	Contract *ValidatorsetCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts       // Call options to use throughout this session
}

// ValidatorsetTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type ValidatorsetTransactorSession struct {
	Contract     *ValidatorsetTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts       // Transaction auth options to use throughout this session
}

// ValidatorsetRaw is an auto generated low-level Go binding around an Ethereum contract.
type ValidatorsetRaw struct {
	Contract *Validatorset // Generic contract binding to access the raw methods on
}

// ValidatorsetCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type ValidatorsetCallerRaw struct {
	Contract *ValidatorsetCaller // Generic read-only contract binding to access the raw methods on
}

// ValidatorsetTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type ValidatorsetTransactorRaw struct {
	Contract *ValidatorsetTransactor // Generic write-only contract binding to access the raw methods on
}

// NewValidatorset creates a new instance of Validatorset, bound to a specific deployed contract.
func NewValidatorset(address common.Address, backend bind.ContractBackend) (*Validatorset, error) {
	contract, err := bindValidatorset(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &Validatorset{ValidatorsetCaller: ValidatorsetCaller{contract: contract}, ValidatorsetTransactor: ValidatorsetTransactor{contract: contract}, ValidatorsetFilterer: ValidatorsetFilterer{contract: contract}}, nil
}

// NewValidatorsetCaller creates a new read-only instance of Validatorset, bound to a specific deployed contract.
func NewValidatorsetCaller(address common.Address, caller bind.ContractCaller) (*ValidatorsetCaller, error) {
	contract, err := bindValidatorset(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &ValidatorsetCaller{contract: contract}, nil
}

// NewValidatorsetTransactor creates a new write-only instance of Validatorset, bound to a specific deployed contract.
func NewValidatorsetTransactor(address common.Address, transactor bind.ContractTransactor) (*ValidatorsetTransactor, error) {
	contract, err := bindValidatorset(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &ValidatorsetTransactor{contract: contract}, nil
}

// NewValidatorsetFilterer creates a new log filterer instance of Validatorset, bound to a specific deployed contract.
func NewValidatorsetFilterer(address common.Address, filterer bind.ContractFilterer) (*ValidatorsetFilterer, error) {
	contract, err := bindValidatorset(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &ValidatorsetFilterer{contract: contract}, nil
}

// bindValidatorset binds a generic wrapper to an already deployed contract.
func bindValidatorset(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := abi.JSON(strings.NewReader(ValidatorsetABI))
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Validatorset *ValidatorsetRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Validatorset.Contract.ValidatorsetCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Validatorset *ValidatorsetRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Validatorset.Contract.ValidatorsetTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Validatorset *ValidatorsetRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Validatorset.Contract.ValidatorsetTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Validatorset *ValidatorsetCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Validatorset.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Validatorset *ValidatorsetTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Validatorset.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Validatorset *ValidatorsetTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Validatorset.Contract.contract.Transact(opts, method, params...)
}

// CurrentSpanNumber is a free data retrieval call binding the contract method 0x4dbc959f.
//
// Solidity: function currentSpanNumber() view returns(uint256)
func (_Validatorset *ValidatorsetCaller) CurrentSpanNumber(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Validatorset.contract.Call(opts, &out, "currentSpanNumber")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// CurrentSpanNumber is a free data retrieval call binding the contract method 0x4dbc959f.
//
// Solidity: function currentSpanNumber() view returns(uint256)
func (_Validatorset *ValidatorsetSession) CurrentSpanNumber() (*big.Int, error) {
	return _Validatorset.Contract.CurrentSpanNumber(&_Validatorset.CallOpts)
}

// CurrentSpanNumber is a free data retrieval call binding the contract method 0x4dbc959f.
//
// Solidity: function currentSpanNumber() view returns(uint256)
func (_Validatorset *ValidatorsetCallerSession) CurrentSpanNumber() (*big.Int, error) {
	return _Validatorset.Contract.CurrentSpanNumber(&_Validatorset.CallOpts)
}

// GetCurrentSpan is a free data retrieval call binding the contract method 0xaf26aa96.
//
// Solidity: function getCurrentSpan() view returns(uint256 number, uint256 startBlock, uint256 endBlock)
func (_Validatorset *ValidatorsetCaller) GetCurrentSpan(opts *bind.CallOpts) (struct {
	Number     *big.Int
	StartBlock *big.Int
	EndBlock   *big.Int
}, error,
) {
	var out []interface{}
	err := _Validatorset.contract.Call(opts, &out, "getCurrentSpan")

	outstruct := new(struct {
		Number     *big.Int
		StartBlock *big.Int
		EndBlock   *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Number = *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)
	outstruct.StartBlock = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)
	outstruct.EndBlock = *abi.ConvertType(out[2], new(*big.Int)).(**big.Int)

	return *outstruct, err
}

// GetCurrentSpan is a free data retrieval call binding the contract method 0xaf26aa96.
//
// Solidity: function getCurrentSpan() view returns(uint256 number, uint256 startBlock, uint256 endBlock)
func (_Validatorset *ValidatorsetSession) GetCurrentSpan() (struct {
	Number     *big.Int
	StartBlock *big.Int
	EndBlock   *big.Int
}, error,
) {
	return _Validatorset.Contract.GetCurrentSpan(&_Validatorset.CallOpts)
}

// GetCurrentSpan is a free data retrieval call binding the contract method 0xaf26aa96.
//
// Solidity: function getCurrentSpan() view returns(uint256 number, uint256 startBlock, uint256 endBlock)
func (_Validatorset *ValidatorsetCallerSession) GetCurrentSpan() (struct {
	Number     *big.Int
	StartBlock *big.Int
	EndBlock   *big.Int
}, error,
) {
	return _Validatorset.Contract.GetCurrentSpan(&_Validatorset.CallOpts)
}

// GetInitialValidators is a free data retrieval call binding the contract method 0x65b3a1e2.
//
// Solidity: function getInitialValidators() view returns(address[], uint256[])
func (_Validatorset *ValidatorsetCaller) GetInitialValidators(opts *bind.CallOpts) ([]common.Address, []*big.Int, error) {
	var out []interface{}
	err := _Validatorset.contract.Call(opts, &out, "getInitialValidators")
	if err != nil {
		return *new([]common.Address), *new([]*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new([]common.Address)).(*[]common.Address)
	out1 := *abi.ConvertType(out[1], new([]*big.Int)).(*[]*big.Int)

	return out0, out1, err
}

// GetInitialValidators is a free data retrieval call binding the contract method 0x65b3a1e2.
//
// Solidity: function getInitialValidators() view returns(address[], uint256[])
func (_Validatorset *ValidatorsetSession) GetInitialValidators() ([]common.Address, []*big.Int, error) {
	return _Validatorset.Contract.GetInitialValidators(&_Validatorset.CallOpts)
}

// GetInitialValidators is a free data retrieval call binding the contract method 0x65b3a1e2.
//
// Solidity: function getInitialValidators() view returns(address[], uint256[])
func (_Validatorset *ValidatorsetCallerSession) GetInitialValidators() ([]common.Address, []*big.Int, error) {
	return _Validatorset.Contract.GetInitialValidators(&_Validatorset.CallOpts)
}

// GetNextSpan is a free data retrieval call binding the contract method 0x60c8614d.
//
// Solidity: function getNextSpan() view returns(uint256 number, uint256 startBlock, uint256 endBlock)
func (_Validatorset *ValidatorsetCaller) GetNextSpan(opts *bind.CallOpts) (struct {
	Number     *big.Int
	StartBlock *big.Int
	EndBlock   *big.Int
}, error,
) {
	var out []interface{}
	err := _Validatorset.contract.Call(opts, &out, "getNextSpan")

	outstruct := new(struct {
		Number     *big.Int
		StartBlock *big.Int
		EndBlock   *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Number = *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)
	outstruct.StartBlock = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)
	outstruct.EndBlock = *abi.ConvertType(out[2], new(*big.Int)).(**big.Int)

	return *outstruct, err
}

// GetNextSpan is a free data retrieval call binding the contract method 0x60c8614d.
//
// Solidity: function getNextSpan() view returns(uint256 number, uint256 startBlock, uint256 endBlock)
func (_Validatorset *ValidatorsetSession) GetNextSpan() (struct {
	Number     *big.Int
	StartBlock *big.Int
	EndBlock   *big.Int
}, error,
) {
	return _Validatorset.Contract.GetNextSpan(&_Validatorset.CallOpts)
}

// GetNextSpan is a free data retrieval call binding the contract method 0x60c8614d.
//
// Solidity: function getNextSpan() view returns(uint256 number, uint256 startBlock, uint256 endBlock)
func (_Validatorset *ValidatorsetCallerSession) GetNextSpan() (struct {
	Number     *big.Int
	StartBlock *big.Int
	EndBlock   *big.Int
}, error,
) {
	return _Validatorset.Contract.GetNextSpan(&_Validatorset.CallOpts)
}

// GetSpan is a free data retrieval call binding the contract method 0x047a6c5b.
//
// Solidity: function getSpan(uint256 span) view returns(uint256 number, uint256 startBlock, uint256 endBlock)
func (_Validatorset *ValidatorsetCaller) GetSpan(opts *bind.CallOpts, span *big.Int) (struct {
	Number     *big.Int
	StartBlock *big.Int
	EndBlock   *big.Int
}, error,
) {
	var out []interface{}
	err := _Validatorset.contract.Call(opts, &out, "getSpan", span)

	outstruct := new(struct {
		Number     *big.Int
		StartBlock *big.Int
		EndBlock   *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.Number = *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)
	outstruct.StartBlock = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)
	outstruct.EndBlock = *abi.ConvertType(out[2], new(*big.Int)).(**big.Int)

	return *outstruct, err
}

// GetSpan is a free data retrieval call binding the contract method 0x047a6c5b.
//
// Solidity: function getSpan(uint256 span) view returns(uint256 number, uint256 startBlock, uint256 endBlock)
func (_Validatorset *ValidatorsetSession) GetSpan(span *big.Int) (struct {
	Number     *big.Int
	StartBlock *big.Int
	EndBlock   *big.Int
}, error,
) {
	return _Validatorset.Contract.GetSpan(&_Validatorset.CallOpts, span)
}

// GetSpan is a free data retrieval call binding the contract method 0x047a6c5b.
//
// Solidity: function getSpan(uint256 span) view returns(uint256 number, uint256 startBlock, uint256 endBlock)
func (_Validatorset *ValidatorsetCallerSession) GetSpan(span *big.Int) (struct {
	Number     *big.Int
	StartBlock *big.Int
	EndBlock   *big.Int
}, error,
) {
	return _Validatorset.Contract.GetSpan(&_Validatorset.CallOpts, span)
}

// GetValidators is a free data retrieval call binding the contract method 0xb7ab4db5.
//
// Solidity: function getValidators() view returns(address[], uint256[])
func (_Validatorset *ValidatorsetCaller) GetValidators(opts *bind.CallOpts) ([]common.Address, []*big.Int, error) {
	var out []interface{}
	err := _Validatorset.contract.Call(opts, &out, "getValidators")
	if err != nil {
		return *new([]common.Address), *new([]*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new([]common.Address)).(*[]common.Address)
	out1 := *abi.ConvertType(out[1], new([]*big.Int)).(*[]*big.Int)

	return out0, out1, err
}

// GetValidators is a free data retrieval call binding the contract method 0xb7ab4db5.
//
// Solidity: function getValidators() view returns(address[], uint256[])
func (_Validatorset *ValidatorsetSession) GetValidators() ([]common.Address, []*big.Int, error) {
	return _Validatorset.Contract.GetValidators(&_Validatorset.CallOpts)
}

// GetValidators is a free data retrieval call binding the contract method 0xb7ab4db5.
//
// Solidity: function getValidators() view returns(address[], uint256[])
func (_Validatorset *ValidatorsetCallerSession) GetValidators() ([]common.Address, []*big.Int, error) {
	return _Validatorset.Contract.GetValidators(&_Validatorset.CallOpts)
}

// CommitSpan is a paid mutator transaction binding the contract method 0x1fa60ced.
//
// Solidity: function commitSpan(bytes vote, bytes sigs, bytes txBytes, bytes proof) returns()
func (_Validatorset *ValidatorsetTransactor) CommitSpan(opts *bind.TransactOpts, vote []byte, sigs []byte, txBytes []byte, proof []byte) (*types.Transaction, error) {
	return _Validatorset.contract.Transact(opts, "commitSpan", vote, sigs, txBytes, proof)
}

// CommitSpan is a paid mutator transaction binding the contract method 0x1fa60ced.
//
// Solidity: function commitSpan(bytes vote, bytes sigs, bytes txBytes, bytes proof) returns()
func (_Validatorset *ValidatorsetSession) CommitSpan(vote []byte, sigs []byte, txBytes []byte, proof []byte) (*types.Transaction, error) {
	return _Validatorset.Contract.CommitSpan(&_Validatorset.TransactOpts, vote, sigs, txBytes, proof)
}

// CommitSpan is a paid mutator transaction binding the contract method 0x1fa60ced.
//
// Solidity: function commitSpan(bytes vote, bytes sigs, bytes txBytes, bytes proof) returns()
func (_Validatorset *ValidatorsetTransactorSession) CommitSpan(vote []byte, sigs []byte, txBytes []byte, proof []byte) (*types.Transaction, error) {
	return _Validatorset.Contract.CommitSpan(&_Validatorset.TransactOpts, vote, sigs, txBytes, proof)
}

// ValidateValidatorSet is a paid mutator transaction binding the contract method 0xd0504f89.
//
// Solidity: function validateValidatorSet(bytes vote, bytes sigs, bytes txBytes, bytes proof) returns()
func (_Validatorset *ValidatorsetTransactor) ValidateValidatorSet(opts *bind.TransactOpts, vote []byte, sigs []byte, txBytes []byte, proof []byte) (*types.Transaction, error) {
	return _Validatorset.contract.Transact(opts, "validateValidatorSet", vote, sigs, txBytes, proof)
}

// ValidateValidatorSet is a paid mutator transaction binding the contract method 0xd0504f89.
//
// Solidity: function validateValidatorSet(bytes vote, bytes sigs, bytes txBytes, bytes proof) returns()
func (_Validatorset *ValidatorsetSession) ValidateValidatorSet(vote []byte, sigs []byte, txBytes []byte, proof []byte) (*types.Transaction, error) {
	return _Validatorset.Contract.ValidateValidatorSet(&_Validatorset.TransactOpts, vote, sigs, txBytes, proof)
}

// ValidateValidatorSet is a paid mutator transaction binding the contract method 0xd0504f89.
//
// Solidity: function validateValidatorSet(bytes vote, bytes sigs, bytes txBytes, bytes proof) returns()
func (_Validatorset *ValidatorsetTransactorSession) ValidateValidatorSet(vote []byte, sigs []byte, txBytes []byte, proof []byte) (*types.Transaction, error) {
	return _Validatorset.Contract.ValidateValidatorSet(&_Validatorset.TransactOpts, vote, sigs, txBytes, proof)
}
