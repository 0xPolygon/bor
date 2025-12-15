// EVMC: Ethereum Client-VM Connector API.
// Copyright 2018 The EVMC Authors.
// Licensed under the Apache License, Version 2.0.

package evmc

/*
#cgo CFLAGS: -I${SRCDIR}/../../../include -Wall -Wextra
#cgo !windows LDFLAGS: -ldl

#include <evmc/bor_trace.h>
#include <evmc/evmc.h>
#include <evmc/helpers.h>
#include <evmc/loader.h>

#include <stdio.h>
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
	const evmc_address* recipient, const evmc_address* sender,
	const uint8_t* input_data, size_t input_size, const evmc_uint256be* value,
	const uint8_t* code, size_t code_size)
{

	struct evmc_message msg = {
		kind,
		flags,
		depth,
		gas,
		*recipient,
		*sender,
		input_data,
		input_size,
		*value,
		{{0}}, // create2_salt: not required for execution
		{{0}}, // code_address: not required for execution
		0,     // code
		0,     // code_size
	};

	struct evmc_host_context* context = (struct evmc_host_context*)context_index;
	struct evmc_result result = evmc_execute(vm, &evmc_go_host, context, rev, &msg, code, code_size);

	return result;
}

static inline const struct evmone_trace_result* bor_get_trace(const struct evmc_result* result)
{
	const union evmc_result_optional_storage* storage = evmc_get_const_optional_storage(result);
	if (!storage)
		return NULL;
	return (const struct evmone_trace_result*)storage->pointer;
}
*/
import "C"

import (
	"fmt"
	"log"
	"runtime/cgo"
	"unsafe"
)

// Hash represents the 32 bytes of arbitrary data (e.g. the result of Keccak256
// hash). It occasionally is used to represent 256-bit unsigned integer values
// stored in big-endian byte order.
type Hash [32]byte

// Address represents the 160-bit (20 bytes) address of an Ethereum account.
type Address [20]byte

// Static asserts.
const (
	// The size of evmc_bytes32 equals the size of Hash.
	_ = uint(len(Hash{}) - C.sizeof_evmc_bytes32)
	_ = uint(C.sizeof_evmc_bytes32 - len(Hash{}))

	// The size of evmc_address equals the size of Address.
	_ = uint(len(Address{}) - C.sizeof_evmc_address)
	_ = uint(C.sizeof_evmc_address - len(Address{}))
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

type Result struct {
	Output     []byte
	GasLeft    int64
	GasRefund  int64
	TraceSteps []TraceStep
}

// TraceStep describes a single opcode execution recorded by evmone.
type TraceStep struct {
	PC     uint32
	Opcode byte
	Gas    int64
	Depth  int32
}

func (vm *VM) Execute(ctx HostContext, rev Revision,
	kind CallKind, static bool, depth int, gas int64,
	recipient Address, sender Address, input []byte, value Hash,
	code []byte) (res Result, err error) {

	flags := C.uint32_t(0)
	if static {
		flags |= C.EVMC_STATIC
	}

	ctxHandle := addHostContext(ctx)
	defer removeHostContext(ctxHandle)
	// FIXME: Clarify passing by pointer vs passing by value.
	evmcRecipient := evmcAddress(recipient)
	evmcSender := evmcAddress(sender)
	evmcValue := evmcBytes32(value)
	result := C.execute_wrapper(vm.handle, C.uintptr_t(ctxHandle), uint32(rev),
		C.enum_evmc_call_kind(kind), flags, C.int32_t(depth), C.int64_t(gas),
		&evmcRecipient, &evmcSender, bytesPtr(input), C.size_t(len(input)), &evmcValue,
		bytesPtr(code), C.size_t(len(code)))

	res.Output = C.GoBytes(unsafe.Pointer(result.output_data), C.int(result.output_size))
	res.GasLeft = int64(result.gas_left)
	res.GasRefund = int64(result.gas_refund)

	trace := C.bor_get_trace(&result)
	if trace == nil {
		log.Printf("evmc: evmone trace payload missing (status=%d)", result.status_code)
	} else {
		count := int(trace.count)
		if count == 0 {
			log.Printf("evmc: evmone trace payload empty")
		} else {
			cSteps := (*[1 << 30]C.struct_evmone_trace_step)(unsafe.Pointer(&trace.steps[0]))[:count:count]
			res.TraceSteps = make([]TraceStep, count)
			for i := 0; i < count; i++ {
				res.TraceSteps[i] = TraceStep{
					PC:     uint32(cSteps[i].pc),
					Opcode: byte(cSteps[i].opcode),
					Gas:    int64(cSteps[i].gas),
					Depth:  int32(cSteps[i].depth),
				}
			}
		}
	}
	if result.status_code != C.EVMC_SUCCESS {
		err = Error(result.status_code)
	}

	if result.release != nil {
		C.evmc_release_result(&result)
	}

	return res, err
}

func addHostContext(ctx HostContext) uintptr {
	handle := cgo.NewHandle(ctx)
	return uintptr(handle)
}

func removeHostContext(id uintptr) {
	cgo.Handle(id).Delete()
}

func getHostContext(idx uintptr) HostContext {
	return cgo.Handle(idx).Value().(HostContext)
}

func evmcBytes32(in Hash) C.evmc_bytes32 {
	out := C.evmc_bytes32{}
	for i := 0; i < len(in); i++ {
		out.bytes[i] = C.uint8_t(in[i])
	}
	return out
}

func evmcAddress(address Address) C.evmc_address {
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
