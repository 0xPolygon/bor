// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package statesender

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

// StatesenderMetaData contains all meta data concerning the Statesender contract.
var StatesenderMetaData = &bind.MetaData{
	ABI: "[{\"constant\":false,\"inputs\":[{\"name\":\"receiver\",\"type\":\"address\"},{\"name\":\"data\",\"type\":\"bytes\"}],\"name\":\"syncState\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"counter\",\"outputs\":[{\"name\":\"\",\"type\":\"uint256\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[],\"name\":\"renounceOwnership\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"owner\",\"outputs\":[{\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[],\"name\":\"isOwner\",\"outputs\":[{\"name\":\"\",\"type\":\"bool\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":true,\"inputs\":[{\"name\":\"\",\"type\":\"address\"}],\"name\":\"registrations\",\"outputs\":[{\"name\":\"\",\"type\":\"address\"}],\"payable\":false,\"stateMutability\":\"view\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"name\":\"sender\",\"type\":\"address\"},{\"name\":\"receiver\",\"type\":\"address\"}],\"name\":\"register\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"constant\":false,\"inputs\":[{\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"transferOwnership\",\"outputs\":[],\"payable\":false,\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"name\":\"user\",\"type\":\"address\"},{\"indexed\":true,\"name\":\"sender\",\"type\":\"address\"},{\"indexed\":true,\"name\":\"receiver\",\"type\":\"address\"}],\"name\":\"NewRegistration\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"name\":\"id\",\"type\":\"uint256\"},{\"indexed\":true,\"name\":\"contractAddress\",\"type\":\"address\"},{\"indexed\":false,\"name\":\"data\",\"type\":\"bytes\"}],\"name\":\"StateSynced\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"name\":\"previousOwner\",\"type\":\"address\"},{\"indexed\":true,\"name\":\"newOwner\",\"type\":\"address\"}],\"name\":\"OwnershipTransferred\",\"type\":\"event\"}]",
}

// StatesenderABI is the input ABI used to generate the binding from.
// Deprecated: Use StatesenderMetaData.ABI instead.
var StatesenderABI = StatesenderMetaData.ABI

// Statesender is an auto generated Go binding around an Ethereum contract.
type Statesender struct {
	StatesenderCaller     // Read-only binding to the contract
	StatesenderTransactor // Write-only binding to the contract
	StatesenderFilterer   // Log filterer for contract events
}

// StatesenderCaller is an auto generated read-only Go binding around an Ethereum contract.
type StatesenderCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// StatesenderTransactor is an auto generated write-only Go binding around an Ethereum contract.
type StatesenderTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// StatesenderFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type StatesenderFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// StatesenderSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type StatesenderSession struct {
	Contract     *Statesender      // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// StatesenderCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type StatesenderCallerSession struct {
	Contract *StatesenderCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts      // Call options to use throughout this session
}

// StatesenderTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type StatesenderTransactorSession struct {
	Contract     *StatesenderTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts      // Transaction auth options to use throughout this session
}

// StatesenderRaw is an auto generated low-level Go binding around an Ethereum contract.
type StatesenderRaw struct {
	Contract *Statesender // Generic contract binding to access the raw methods on
}

// StatesenderCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type StatesenderCallerRaw struct {
	Contract *StatesenderCaller // Generic read-only contract binding to access the raw methods on
}

// StatesenderTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type StatesenderTransactorRaw struct {
	Contract *StatesenderTransactor // Generic write-only contract binding to access the raw methods on
}

// NewStatesender creates a new instance of Statesender, bound to a specific deployed contract.
func NewStatesender(address common.Address, backend bind.ContractBackend) (*Statesender, error) {
	contract, err := bindStatesender(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &Statesender{StatesenderCaller: StatesenderCaller{contract: contract}, StatesenderTransactor: StatesenderTransactor{contract: contract}, StatesenderFilterer: StatesenderFilterer{contract: contract}}, nil
}

// NewStatesenderCaller creates a new read-only instance of Statesender, bound to a specific deployed contract.
func NewStatesenderCaller(address common.Address, caller bind.ContractCaller) (*StatesenderCaller, error) {
	contract, err := bindStatesender(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &StatesenderCaller{contract: contract}, nil
}

// NewStatesenderTransactor creates a new write-only instance of Statesender, bound to a specific deployed contract.
func NewStatesenderTransactor(address common.Address, transactor bind.ContractTransactor) (*StatesenderTransactor, error) {
	contract, err := bindStatesender(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &StatesenderTransactor{contract: contract}, nil
}

// NewStatesenderFilterer creates a new log filterer instance of Statesender, bound to a specific deployed contract.
func NewStatesenderFilterer(address common.Address, filterer bind.ContractFilterer) (*StatesenderFilterer, error) {
	contract, err := bindStatesender(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &StatesenderFilterer{contract: contract}, nil
}

// bindStatesender binds a generic wrapper to an already deployed contract.
func bindStatesender(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := abi.JSON(strings.NewReader(StatesenderABI))
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Statesender *StatesenderRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Statesender.Contract.StatesenderCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Statesender *StatesenderRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Statesender.Contract.StatesenderTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Statesender *StatesenderRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Statesender.Contract.StatesenderTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_Statesender *StatesenderCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _Statesender.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_Statesender *StatesenderTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Statesender.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_Statesender *StatesenderTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _Statesender.Contract.contract.Transact(opts, method, params...)
}

// Counter is a free data retrieval call binding the contract method 0x61bc221a.
//
// Solidity: function counter() view returns(uint256)
func (_Statesender *StatesenderCaller) Counter(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _Statesender.contract.Call(opts, &out, "counter")
	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err
}

// Counter is a free data retrieval call binding the contract method 0x61bc221a.
//
// Solidity: function counter() view returns(uint256)
func (_Statesender *StatesenderSession) Counter() (*big.Int, error) {
	return _Statesender.Contract.Counter(&_Statesender.CallOpts)
}

// Counter is a free data retrieval call binding the contract method 0x61bc221a.
//
// Solidity: function counter() view returns(uint256)
func (_Statesender *StatesenderCallerSession) Counter() (*big.Int, error) {
	return _Statesender.Contract.Counter(&_Statesender.CallOpts)
}

// IsOwner is a free data retrieval call binding the contract method 0x8f32d59b.
//
// Solidity: function isOwner() view returns(bool)
func (_Statesender *StatesenderCaller) IsOwner(opts *bind.CallOpts) (bool, error) {
	var out []interface{}
	err := _Statesender.contract.Call(opts, &out, "isOwner")
	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err
}

// IsOwner is a free data retrieval call binding the contract method 0x8f32d59b.
//
// Solidity: function isOwner() view returns(bool)
func (_Statesender *StatesenderSession) IsOwner() (bool, error) {
	return _Statesender.Contract.IsOwner(&_Statesender.CallOpts)
}

// IsOwner is a free data retrieval call binding the contract method 0x8f32d59b.
//
// Solidity: function isOwner() view returns(bool)
func (_Statesender *StatesenderCallerSession) IsOwner() (bool, error) {
	return _Statesender.Contract.IsOwner(&_Statesender.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Statesender *StatesenderCaller) Owner(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _Statesender.contract.Call(opts, &out, "owner")
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Statesender *StatesenderSession) Owner() (common.Address, error) {
	return _Statesender.Contract.Owner(&_Statesender.CallOpts)
}

// Owner is a free data retrieval call binding the contract method 0x8da5cb5b.
//
// Solidity: function owner() view returns(address)
func (_Statesender *StatesenderCallerSession) Owner() (common.Address, error) {
	return _Statesender.Contract.Owner(&_Statesender.CallOpts)
}

// Registrations is a free data retrieval call binding the contract method 0x942e6bcf.
//
// Solidity: function registrations(address ) view returns(address)
func (_Statesender *StatesenderCaller) Registrations(opts *bind.CallOpts, arg0 common.Address) (common.Address, error) {
	var out []interface{}
	err := _Statesender.contract.Call(opts, &out, "registrations", arg0)
	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err
}

// Registrations is a free data retrieval call binding the contract method 0x942e6bcf.
//
// Solidity: function registrations(address ) view returns(address)
func (_Statesender *StatesenderSession) Registrations(arg0 common.Address) (common.Address, error) {
	return _Statesender.Contract.Registrations(&_Statesender.CallOpts, arg0)
}

// Registrations is a free data retrieval call binding the contract method 0x942e6bcf.
//
// Solidity: function registrations(address ) view returns(address)
func (_Statesender *StatesenderCallerSession) Registrations(arg0 common.Address) (common.Address, error) {
	return _Statesender.Contract.Registrations(&_Statesender.CallOpts, arg0)
}

// Register is a paid mutator transaction binding the contract method 0xaa677354.
//
// Solidity: function register(address sender, address receiver) returns()
func (_Statesender *StatesenderTransactor) Register(opts *bind.TransactOpts, sender common.Address, receiver common.Address) (*types.Transaction, error) {
	return _Statesender.contract.Transact(opts, "register", sender, receiver)
}

// Register is a paid mutator transaction binding the contract method 0xaa677354.
//
// Solidity: function register(address sender, address receiver) returns()
func (_Statesender *StatesenderSession) Register(sender common.Address, receiver common.Address) (*types.Transaction, error) {
	return _Statesender.Contract.Register(&_Statesender.TransactOpts, sender, receiver)
}

// Register is a paid mutator transaction binding the contract method 0xaa677354.
//
// Solidity: function register(address sender, address receiver) returns()
func (_Statesender *StatesenderTransactorSession) Register(sender common.Address, receiver common.Address) (*types.Transaction, error) {
	return _Statesender.Contract.Register(&_Statesender.TransactOpts, sender, receiver)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Statesender *StatesenderTransactor) RenounceOwnership(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _Statesender.contract.Transact(opts, "renounceOwnership")
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Statesender *StatesenderSession) RenounceOwnership() (*types.Transaction, error) {
	return _Statesender.Contract.RenounceOwnership(&_Statesender.TransactOpts)
}

// RenounceOwnership is a paid mutator transaction binding the contract method 0x715018a6.
//
// Solidity: function renounceOwnership() returns()
func (_Statesender *StatesenderTransactorSession) RenounceOwnership() (*types.Transaction, error) {
	return _Statesender.Contract.RenounceOwnership(&_Statesender.TransactOpts)
}

// SyncState is a paid mutator transaction binding the contract method 0x16f19831.
//
// Solidity: function syncState(address receiver, bytes data) returns()
func (_Statesender *StatesenderTransactor) SyncState(opts *bind.TransactOpts, receiver common.Address, data []byte) (*types.Transaction, error) {
	return _Statesender.contract.Transact(opts, "syncState", receiver, data)
}

// SyncState is a paid mutator transaction binding the contract method 0x16f19831.
//
// Solidity: function syncState(address receiver, bytes data) returns()
func (_Statesender *StatesenderSession) SyncState(receiver common.Address, data []byte) (*types.Transaction, error) {
	return _Statesender.Contract.SyncState(&_Statesender.TransactOpts, receiver, data)
}

// SyncState is a paid mutator transaction binding the contract method 0x16f19831.
//
// Solidity: function syncState(address receiver, bytes data) returns()
func (_Statesender *StatesenderTransactorSession) SyncState(receiver common.Address, data []byte) (*types.Transaction, error) {
	return _Statesender.Contract.SyncState(&_Statesender.TransactOpts, receiver, data)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Statesender *StatesenderTransactor) TransferOwnership(opts *bind.TransactOpts, newOwner common.Address) (*types.Transaction, error) {
	return _Statesender.contract.Transact(opts, "transferOwnership", newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Statesender *StatesenderSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _Statesender.Contract.TransferOwnership(&_Statesender.TransactOpts, newOwner)
}

// TransferOwnership is a paid mutator transaction binding the contract method 0xf2fde38b.
//
// Solidity: function transferOwnership(address newOwner) returns()
func (_Statesender *StatesenderTransactorSession) TransferOwnership(newOwner common.Address) (*types.Transaction, error) {
	return _Statesender.Contract.TransferOwnership(&_Statesender.TransactOpts, newOwner)
}

// StatesenderNewRegistrationIterator is returned from FilterNewRegistration and is used to iterate over the raw logs and unpacked data for NewRegistration events raised by the Statesender contract.
type StatesenderNewRegistrationIterator struct {
	Event *StatesenderNewRegistration // Event containing the contract specifics and raw log

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
func (it *StatesenderNewRegistrationIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(StatesenderNewRegistration)
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
		it.Event = new(StatesenderNewRegistration)
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
func (it *StatesenderNewRegistrationIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *StatesenderNewRegistrationIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// StatesenderNewRegistration represents a NewRegistration event raised by the Statesender contract.
type StatesenderNewRegistration struct {
	User     common.Address
	Sender   common.Address
	Receiver common.Address
	Raw      types.Log // Blockchain specific contextual infos
}

// FilterNewRegistration is a free log retrieval operation binding the contract event 0x3f4512aacd7a664fdb321a48e8340120d63253a91c6367a143abd19ecf68aedd.
//
// Solidity: event NewRegistration(address indexed user, address indexed sender, address indexed receiver)
func (_Statesender *StatesenderFilterer) FilterNewRegistration(opts *bind.FilterOpts, user []common.Address, sender []common.Address, receiver []common.Address) (*StatesenderNewRegistrationIterator, error) {
	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var senderRule []interface{}
	for _, senderItem := range sender {
		senderRule = append(senderRule, senderItem)
	}
	var receiverRule []interface{}
	for _, receiverItem := range receiver {
		receiverRule = append(receiverRule, receiverItem)
	}

	logs, sub, err := _Statesender.contract.FilterLogs(opts, "NewRegistration", userRule, senderRule, receiverRule)
	if err != nil {
		return nil, err
	}
	return &StatesenderNewRegistrationIterator{contract: _Statesender.contract, event: "NewRegistration", logs: logs, sub: sub}, nil
}

// WatchNewRegistration is a free log subscription operation binding the contract event 0x3f4512aacd7a664fdb321a48e8340120d63253a91c6367a143abd19ecf68aedd.
//
// Solidity: event NewRegistration(address indexed user, address indexed sender, address indexed receiver)
func (_Statesender *StatesenderFilterer) WatchNewRegistration(opts *bind.WatchOpts, sink chan<- *StatesenderNewRegistration, user []common.Address, sender []common.Address, receiver []common.Address) (event.Subscription, error) {
	var userRule []interface{}
	for _, userItem := range user {
		userRule = append(userRule, userItem)
	}
	var senderRule []interface{}
	for _, senderItem := range sender {
		senderRule = append(senderRule, senderItem)
	}
	var receiverRule []interface{}
	for _, receiverItem := range receiver {
		receiverRule = append(receiverRule, receiverItem)
	}

	logs, sub, err := _Statesender.contract.WatchLogs(opts, "NewRegistration", userRule, senderRule, receiverRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(StatesenderNewRegistration)
				if err := _Statesender.contract.UnpackLog(event, "NewRegistration", log); err != nil {
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

// ParseNewRegistration is a log parse operation binding the contract event 0x3f4512aacd7a664fdb321a48e8340120d63253a91c6367a143abd19ecf68aedd.
//
// Solidity: event NewRegistration(address indexed user, address indexed sender, address indexed receiver)
func (_Statesender *StatesenderFilterer) ParseNewRegistration(log types.Log) (*StatesenderNewRegistration, error) {
	event := new(StatesenderNewRegistration)
	if err := _Statesender.contract.UnpackLog(event, "NewRegistration", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// StatesenderOwnershipTransferredIterator is returned from FilterOwnershipTransferred and is used to iterate over the raw logs and unpacked data for OwnershipTransferred events raised by the Statesender contract.
type StatesenderOwnershipTransferredIterator struct {
	Event *StatesenderOwnershipTransferred // Event containing the contract specifics and raw log

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
func (it *StatesenderOwnershipTransferredIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(StatesenderOwnershipTransferred)
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
		it.Event = new(StatesenderOwnershipTransferred)
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
func (it *StatesenderOwnershipTransferredIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *StatesenderOwnershipTransferredIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// StatesenderOwnershipTransferred represents a OwnershipTransferred event raised by the Statesender contract.
type StatesenderOwnershipTransferred struct {
	PreviousOwner common.Address
	NewOwner      common.Address
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterOwnershipTransferred is a free log retrieval operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_Statesender *StatesenderFilterer) FilterOwnershipTransferred(opts *bind.FilterOpts, previousOwner []common.Address, newOwner []common.Address) (*StatesenderOwnershipTransferredIterator, error) {
	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _Statesender.contract.FilterLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return &StatesenderOwnershipTransferredIterator{contract: _Statesender.contract, event: "OwnershipTransferred", logs: logs, sub: sub}, nil
}

// WatchOwnershipTransferred is a free log subscription operation binding the contract event 0x8be0079c531659141344cd1fd0a4f28419497f9722a3daafe3b4186f6b6457e0.
//
// Solidity: event OwnershipTransferred(address indexed previousOwner, address indexed newOwner)
func (_Statesender *StatesenderFilterer) WatchOwnershipTransferred(opts *bind.WatchOpts, sink chan<- *StatesenderOwnershipTransferred, previousOwner []common.Address, newOwner []common.Address) (event.Subscription, error) {
	var previousOwnerRule []interface{}
	for _, previousOwnerItem := range previousOwner {
		previousOwnerRule = append(previousOwnerRule, previousOwnerItem)
	}
	var newOwnerRule []interface{}
	for _, newOwnerItem := range newOwner {
		newOwnerRule = append(newOwnerRule, newOwnerItem)
	}

	logs, sub, err := _Statesender.contract.WatchLogs(opts, "OwnershipTransferred", previousOwnerRule, newOwnerRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(StatesenderOwnershipTransferred)
				if err := _Statesender.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
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
func (_Statesender *StatesenderFilterer) ParseOwnershipTransferred(log types.Log) (*StatesenderOwnershipTransferred, error) {
	event := new(StatesenderOwnershipTransferred)
	if err := _Statesender.contract.UnpackLog(event, "OwnershipTransferred", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// StatesenderStateSyncedIterator is returned from FilterStateSynced and is used to iterate over the raw logs and unpacked data for StateSynced events raised by the Statesender contract.
type StatesenderStateSyncedIterator struct {
	Event *StatesenderStateSynced // Event containing the contract specifics and raw log

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
func (it *StatesenderStateSyncedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(StatesenderStateSynced)
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
		it.Event = new(StatesenderStateSynced)
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
func (it *StatesenderStateSyncedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *StatesenderStateSyncedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// StatesenderStateSynced represents a StateSynced event raised by the Statesender contract.
type StatesenderStateSynced struct {
	Id              *big.Int
	ContractAddress common.Address
	Data            []byte
	Raw             types.Log // Blockchain specific contextual infos
}

// FilterStateSynced is a free log retrieval operation binding the contract event 0x103fed9db65eac19c4d870f49ab7520fe03b99f1838e5996caf47e9e43308392.
//
// Solidity: event StateSynced(uint256 indexed id, address indexed contractAddress, bytes data)
func (_Statesender *StatesenderFilterer) FilterStateSynced(opts *bind.FilterOpts, id []*big.Int, contractAddress []common.Address) (*StatesenderStateSyncedIterator, error) {
	var idRule []interface{}
	for _, idItem := range id {
		idRule = append(idRule, idItem)
	}
	var contractAddressRule []interface{}
	for _, contractAddressItem := range contractAddress {
		contractAddressRule = append(contractAddressRule, contractAddressItem)
	}

	logs, sub, err := _Statesender.contract.FilterLogs(opts, "StateSynced", idRule, contractAddressRule)
	if err != nil {
		return nil, err
	}
	return &StatesenderStateSyncedIterator{contract: _Statesender.contract, event: "StateSynced", logs: logs, sub: sub}, nil
}

// WatchStateSynced is a free log subscription operation binding the contract event 0x103fed9db65eac19c4d870f49ab7520fe03b99f1838e5996caf47e9e43308392.
//
// Solidity: event StateSynced(uint256 indexed id, address indexed contractAddress, bytes data)
func (_Statesender *StatesenderFilterer) WatchStateSynced(opts *bind.WatchOpts, sink chan<- *StatesenderStateSynced, id []*big.Int, contractAddress []common.Address) (event.Subscription, error) {
	var idRule []interface{}
	for _, idItem := range id {
		idRule = append(idRule, idItem)
	}
	var contractAddressRule []interface{}
	for _, contractAddressItem := range contractAddress {
		contractAddressRule = append(contractAddressRule, contractAddressItem)
	}

	logs, sub, err := _Statesender.contract.WatchLogs(opts, "StateSynced", idRule, contractAddressRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(StatesenderStateSynced)
				if err := _Statesender.contract.UnpackLog(event, "StateSynced", log); err != nil {
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

// ParseStateSynced is a log parse operation binding the contract event 0x103fed9db65eac19c4d870f49ab7520fe03b99f1838e5996caf47e9e43308392.
//
// Solidity: event StateSynced(uint256 indexed id, address indexed contractAddress, bytes data)
func (_Statesender *StatesenderFilterer) ParseStateSynced(log types.Log) (*StatesenderStateSynced, error) {
	event := new(StatesenderStateSynced)
	if err := _Statesender.contract.UnpackLog(event, "StateSynced", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
