// EVMC: Ethereum Client-VM Connector API.
// Copyright 2018 The EVMC Authors.
// Licensed under the Apache License, Version 2.0.

package evmc

/*
#cgo CFLAGS: -I${SRCDIR}/../../../include -Wall -Wextra
#cgo !windows LDFLAGS: -ldl

#include <evmc/evmc.h>
#include <evmc/helpers.h>
#include <evmc/loader.h>

#include <stdlib.h>
#include <string.h>

static inline enum evmc_set_option_result set_option(struct evmc_vm* vm, char* name, char* value)
{
	enum evmc_set_option_result ret = evmc_set_option(vm, name, value);
	free(name);
	free(value);
	return ret;
}

extern const struct evmc_host_interface evmc_go_host;

static struct evmc_result execute_wrapper(struct evmc_vm* vm,
	uintptr_t context_index, enum evmc_revision rev,
	enum evmc_call_kind kind, uint32_t flags, int32_t depth, int64_t gas,
	const evmc_address* destination, const evmc_address* sender,
	const uint8_t* input_data, size_t input_size, const evmc_uint256be* value,
	const uint8_t* code, size_t code_size, const evmc_bytes32* create2_salt)
{
	struct evmc_message msg = {
		kind,
		flags,
		depth,
		gas,
		*destination,
		*sender,
		input_data,
		input_size,
		*value,
		*create2_salt,
		{{0}}, // code_address: not required for execution
		0,     // code
		0,     // code_size
	};

	struct evmc_host_context* context = (struct evmc_host_context*)context_index;
	return evmc_execute(vm, &evmc_go_host, context, rev, &msg, code, code_size);
}
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
)

// Static asserts.
const (
	// The size of evmc_bytes32 equals the size of Hash.
	_ = uint(common.HashLength - C.sizeof_evmc_bytes32)
	_ = uint(C.sizeof_evmc_bytes32 - common.HashLength)

	// The size of evmc_address equals the size of Address.
	_ = uint(common.AddressLength - C.sizeof_evmc_address)
	_ = uint(C.sizeof_evmc_address - common.AddressLength)
)

type Error int32

func (err Error) IsInternalError() bool {
	return err < 0
}

func (err Error) Error() string {
	return C.GoString(C.evmc_status_code_to_string(C.enum_evmc_status_code(err)))
}

const (
	Failure = Error(C.EVMC_FAILURE)
	Revert  = Error(C.EVMC_REVERT)
)

type Revision int32

const (
	Frontier             Revision = C.EVMC_FRONTIER
	Homestead            Revision = C.EVMC_HOMESTEAD
	TangerineWhistle     Revision = C.EVMC_TANGERINE_WHISTLE
	SpuriousDragon       Revision = C.EVMC_SPURIOUS_DRAGON
	Byzantium            Revision = C.EVMC_BYZANTIUM
	Constantinople       Revision = C.EVMC_CONSTANTINOPLE
	Petersburg           Revision = C.EVMC_PETERSBURG
	Istanbul             Revision = C.EVMC_ISTANBUL
	Berlin               Revision = C.EVMC_BERLIN
	London               Revision = C.EVMC_LONDON
	Paris                Revision = C.EVMC_PARIS
	Shanghai             Revision = C.EVMC_SHANGHAI
	Cancun               Revision = C.EVMC_CANCUN
	Prague               Revision = C.EVMC_PRAGUE
	Osaka                Revision = C.EVMC_OSAKA
	MaxRevision          Revision = C.EVMC_MAX_REVISION
	LatestStableRevision Revision = C.EVMC_LATEST_STABLE_REVISION
)

type VM struct {
	handle *C.struct_evmc_vm
}

// Instance aliases VM to keep go-ethereum code working.
type Instance = VM

func Load(filename string) (vm *VM, err error) {
	cfilename := C.CString(filename)
	loaderErr := C.enum_evmc_loader_error_code(C.EVMC_LOADER_UNSPECIFIED_ERROR)
	handle := C.evmc_load_and_create(cfilename, &loaderErr)
	C.free(unsafe.Pointer(cfilename))

	if loaderErr == C.EVMC_LOADER_SUCCESS {
		vm = &VM{handle}
	} else {
		errMsg := C.evmc_last_error_msg()
		if errMsg != nil {
			err = fmt.Errorf("EVMC loading error: %s", C.GoString(errMsg))
		} else {
			err = fmt.Errorf("EVMC loading error %d", int(loaderErr))
		}
	}

	return vm, err
}

func LoadAndConfigure(config string) (vm *VM, err error) {
	cconfig := C.CString(config)
	loaderErr := C.enum_evmc_loader_error_code(C.EVMC_LOADER_UNSPECIFIED_ERROR)
	handle := C.evmc_load_and_configure(cconfig, &loaderErr)
	C.free(unsafe.Pointer(cconfig))

	if loaderErr == C.EVMC_LOADER_SUCCESS {
		vm = &VM{handle}
	} else {
		errMsg := C.evmc_last_error_msg()
		if errMsg != nil {
			err = fmt.Errorf("EVMC loading error: %s", C.GoString(errMsg))
		} else {
			err = fmt.Errorf("EVMC loading error %d", int(loaderErr))
		}
	}

	return vm, err
}

func (vm *VM) Destroy() {
	C.evmc_destroy(vm.handle)
}

func (vm *VM) Name() string {
	// TODO: consider using C.evmc_vm_name(vm.handle)
	return C.GoString(vm.handle.name)
}

func (vm *VM) Version() string {
	// TODO: consider using C.evmc_vm_version(vm.handle)
	return C.GoString(vm.handle.version)
}

type Capability uint32

const (
	CapabilityEVM1  Capability = C.EVMC_CAPABILITY_EVM1
	CapabilityEWASM Capability = C.EVMC_CAPABILITY_EWASM
)

func (vm *VM) HasCapability(capability Capability) bool {
	return bool(C.evmc_vm_has_capability(vm.handle, uint32(capability)))
}

func (vm *VM) SetOption(name string, value string) (err error) {

	r := C.set_option(vm.handle, C.CString(name), C.CString(value))
	switch r {
	case C.EVMC_SET_OPTION_INVALID_NAME:
		err = fmt.Errorf("evmc: option '%s' not accepted", name)
	case C.EVMC_SET_OPTION_INVALID_VALUE:
		err = fmt.Errorf("evmc: option '%s' has invalid value", name)
	case C.EVMC_SET_OPTION_SUCCESS:
	}
	return err
}

func (vm *VM) Execute(ctx HostContext, rev Revision,
	kind CallKind, static bool, depth int, gas int64,
	destination common.Address, sender common.Address, input []byte, value common.Hash,
	code []byte, create2Salt common.Hash) (output []byte, gasLeft int64, err error) {

	flags := C.uint32_t(0)
	if static {
		flags |= C.EVMC_STATIC
	}

	ctxId := addHostContext(ctx)
	// FIXME: Clarify passing by pointer vs passing by value.
	evmcDestination := evmcAddress(destination)
	evmcSender := evmcAddress(sender)
	evmcValue := evmcBytes32(value)
	evmcCreate2Salt := evmcBytes32(create2Salt)
	result := C.execute_wrapper(vm.handle, C.uintptr_t(ctxId), uint32(rev),
		C.enum_evmc_call_kind(kind), flags, C.int32_t(depth), C.int64_t(gas),
		&evmcDestination, &evmcSender, bytesPtr(input), C.size_t(len(input)), &evmcValue,
		bytesPtr(code), C.size_t(len(code)), &evmcCreate2Salt)
	removeHostContext(ctxId)

	output = C.GoBytes(unsafe.Pointer(result.output_data), C.int(result.output_size))
	gasLeft = int64(result.gas_left)
	if result.status_code != C.EVMC_SUCCESS {
		err = Error(result.status_code)
	}

	if result.release != nil {
		C.evmc_release_result(&result)
	}

	return output, gasLeft, err
}

var (
	hostContextCounter uintptr
	hostContextMap     = map[uintptr]HostContext{}
	hostContextMapMu   sync.Mutex
)

func addHostContext(ctx HostContext) uintptr {
	hostContextMapMu.Lock()
	id := hostContextCounter
	hostContextCounter++
	hostContextMap[id] = ctx
	hostContextMapMu.Unlock()
	return id
}

func removeHostContext(id uintptr) {
	hostContextMapMu.Lock()
	delete(hostContextMap, id)
	hostContextMapMu.Unlock()
}

func getHostContext(idx uintptr) HostContext {
	hostContextMapMu.Lock()
	ctx := hostContextMap[idx]
	hostContextMapMu.Unlock()
	return ctx
}

func evmcBytes32(in common.Hash) C.evmc_bytes32 {
	out := C.evmc_bytes32{}
	for i := 0; i < len(in); i++ {
		out.bytes[i] = C.uint8_t(in[i])
	}
	return out
}

func evmcAddress(address common.Address) C.evmc_address {
	r := C.evmc_address{}
	for i := 0; i < len(address); i++ {
		r.bytes[i] = C.uint8_t(address[i])
	}
	return r
}

func bytesPtr(bytes []byte) *C.uint8_t {
	if len(bytes) == 0 {
		return nil
	}
	return (*C.uint8_t)(unsafe.Pointer(&bytes[0]))
}
