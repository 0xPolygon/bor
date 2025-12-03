// EVMC: Ethereum Client-VM Connector API.
// Copyright 2018 The EVMC Authors.
// Licensed under the Apache License, Version 2.0.

package evmc

/*
#cgo CFLAGS: -I${SRCDIR}/.. -I${SRCDIR}/../../../../include -Wall -Wextra -Wno-unused-parameter

#include <evmc/evmc.h>
#include <evmc/helpers.h>

*/
import "C"

import (
	"math/big"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
)

type CallKind int

const (
	Call         CallKind = C.EVMC_CALL
	DelegateCall CallKind = C.EVMC_DELEGATECALL
	CallCode     CallKind = C.EVMC_CALLCODE
	Create       CallKind = C.EVMC_CREATE
	Create2      CallKind = C.EVMC_CREATE2
)

type StorageStatus int

const (
	StorageUnchanged     StorageStatus = C.EVMC_STORAGE_ASSIGNED
	StorageModified      StorageStatus = C.EVMC_STORAGE_MODIFIED
	StorageModifiedAgain StorageStatus = C.EVMC_STORAGE_MODIFIED_RESTORED
	StorageAdded         StorageStatus = C.EVMC_STORAGE_ADDED
	StorageDeleted       StorageStatus = C.EVMC_STORAGE_DELETED
)

func goAddress(in C.evmc_address) common.Address {
	var out common.Address
	for i := 0; i < len(out); i++ {
		out[i] = byte(in.bytes[i])
	}
	return out
}

func goHash(in C.evmc_bytes32) common.Hash {
	var out common.Hash
	for i := 0; i < len(out); i++ {
		out[i] = byte(in.bytes[i])
	}
	return out
}

func goByteSlice(data *C.uint8_t, size C.size_t) []byte {
	if size == 0 {
		return nil
	}
	return (*[1 << 30]byte)(unsafe.Pointer(data))[:size:size]
}

func evmcBytesToBigInt(in C.evmc_bytes32) *big.Int {
	return new(big.Int).SetBytes(goHash(in).Bytes())
}

// TxContext contains information about current transaction and block.
type TxContext struct {
	GasPrice    common.Hash
	Origin      common.Address
	Coinbase    common.Address
	Number      int64
	Timestamp   int64
	GasLimit    int64
	PrevRandao  common.Hash
	ChainID     common.Hash
	BaseFee     common.Hash
	BlobBaseFee common.Hash
}

type HostContext interface {
	AccountExists(addr common.Address) bool
	GetStorage(addr common.Address, key common.Hash) common.Hash
	SetStorage(addr common.Address, key common.Hash, value common.Hash) StorageStatus
	GetBalance(addr common.Address) common.Hash
	GetCodeSize(addr common.Address) int
	GetCodeHash(addr common.Address) common.Hash
	GetCode(addr common.Address) []byte
	Selfdestruct(addr common.Address, beneficiary common.Address)
	GetTxContext() TxContext
	GetBlockHash(number int64) common.Hash
	EmitLog(addr common.Address, topics []common.Hash, data []byte)
	Call(kind CallKind,
		destination common.Address, sender common.Address, value *big.Int, input []byte, gas int64, depth int,
		static bool, salt *big.Int) (output []byte, gasLeft int64, createAddr common.Address, err error)
}

//export accountExists
func accountExists(pCtx unsafe.Pointer, pAddr *C.evmc_address) C.bool {
	ctx := getHostContext(uintptr(pCtx))
	return C.bool(ctx.AccountExists(goAddress(*pAddr)))
}

//export getStorage
func getStorage(pCtx unsafe.Pointer, pAddr *C.struct_evmc_address, pKey *C.evmc_bytes32) C.evmc_bytes32 {
	ctx := getHostContext(uintptr(pCtx))
	return evmcBytes32(ctx.GetStorage(goAddress(*pAddr), goHash(*pKey)))
}

//export setStorage
func setStorage(pCtx unsafe.Pointer, pAddr *C.evmc_address, pKey *C.evmc_bytes32, pVal *C.evmc_bytes32) C.enum_evmc_storage_status {
	ctx := getHostContext(uintptr(pCtx))
	return C.enum_evmc_storage_status(ctx.SetStorage(goAddress(*pAddr), goHash(*pKey), goHash(*pVal)))
}

//export getBalance
func getBalance(pCtx unsafe.Pointer, pAddr *C.evmc_address) C.evmc_uint256be {
	ctx := getHostContext(uintptr(pCtx))
	return evmcBytes32(ctx.GetBalance(goAddress(*pAddr)))
}

//export getCodeSize
func getCodeSize(pCtx unsafe.Pointer, pAddr *C.evmc_address) C.size_t {
	ctx := getHostContext(uintptr(pCtx))
	return C.size_t(ctx.GetCodeSize(goAddress(*pAddr)))
}

//export getCodeHash
func getCodeHash(pCtx unsafe.Pointer, pAddr *C.evmc_address) C.evmc_bytes32 {
	ctx := getHostContext(uintptr(pCtx))
	return evmcBytes32(ctx.GetCodeHash(goAddress(*pAddr)))
}

//export copyCode
func copyCode(pCtx unsafe.Pointer, pAddr *C.struct_evmc_address, offset C.size_t, pData *C.uint8_t, dataSize C.size_t) C.size_t {
	ctx := getHostContext(uintptr(pCtx))
	code := ctx.GetCode(goAddress(*pAddr))
	if len(code) == 0 || int(offset) >= len(code) {
		return 0
	}
	code = code[int(offset):]
	size := copy(goByteSlice(pData, dataSize), code)
	return C.size_t(size)
}

//export selfdestruct
func selfdestruct(pCtx unsafe.Pointer, pAddr *C.evmc_address, pBeneficiary *C.evmc_address) {
	ctx := getHostContext(uintptr(pCtx))
	ctx.Selfdestruct(goAddress(*pAddr), goAddress(*pBeneficiary))
}

//export getTxContext
func getTxContext(pCtx unsafe.Pointer) C.struct_evmc_tx_context {
	ctx := getHostContext(uintptr(pCtx))
	tx := ctx.GetTxContext()
	return C.struct_evmc_tx_context{
		evmcBytes32(tx.GasPrice),
		evmcAddress(tx.Origin),
		evmcAddress(tx.Coinbase),
		C.int64_t(tx.Number),
		C.int64_t(tx.Timestamp),
		C.int64_t(tx.GasLimit),
		evmcBytes32(tx.PrevRandao),
		evmcBytes32(tx.ChainID),
		evmcBytes32(tx.BaseFee),
		evmcBytes32(tx.BlobBaseFee),
		nil,
		0,
		nil,
		0,
	}
}

//export getBlockHash
func getBlockHash(pCtx unsafe.Pointer, number C.int64_t) C.evmc_bytes32 {
	ctx := getHostContext(uintptr(pCtx))
	return evmcBytes32(ctx.GetBlockHash(int64(number)))
}

//export emitLog
func emitLog(pCtx unsafe.Pointer, pAddr *C.evmc_address, pData unsafe.Pointer, dataSize C.size_t, pTopics unsafe.Pointer, topicsCount C.size_t) {
	ctx := getHostContext(uintptr(pCtx))
	topics := (*[1 << 20]C.evmc_bytes32)(pTopics)[:topicsCount:topicsCount]
	topicsGo := make([]common.Hash, topicsCount)
	for i := 0; i < len(topicsGo); i++ {
		topicsGo[i] = goHash(topics[i])
	}
	ctx.EmitLog(goAddress(*pAddr), topicsGo, C.GoBytes(pData, C.int(dataSize)))
}

//export call
func call(pCtx unsafe.Pointer, msg *C.struct_evmc_message) C.struct_evmc_result {
	ctx := getHostContext(uintptr(pCtx))

	var depth int
	if msg.depth >= 0 {
		depth = int(msg.depth) + 1
	}

	kind := CallKind(msg.kind)
	input := C.GoBytes(unsafe.Pointer(msg.input_data), C.int(msg.input_size))
	output, gasLeft, createAddr, err := ctx.Call(kind,
		goAddress(msg.recipient),
		goAddress(msg.sender),
		evmcBytesToBigInt(msg.value),
		input,
		int64(msg.gas),
		depth,
		(msg.flags&C.EVMC_STATIC) != 0,
		evmcBytesToBigInt(msg.create2_salt))

	statusCode := C.enum_evmc_status_code(0)
	if err != nil {
		statusCode = C.enum_evmc_status_code(err.(Error))
	}

	var outputPtr *C.uint8_t
	if len(output) > 0 {
		outputPtr = (*C.uint8_t)(&output[0])
	}

	result := C.evmc_make_result(statusCode, C.int64_t(gasLeft), C.int64_t(0), outputPtr, C.size_t(len(output)))
	result.create_address = evmcAddress(createAddr)
	return result
}
