package pevmbridge

import (
	"errors"
	"math/big"
	"sync"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

/*
#cgo CFLAGS: -I/Users/raneet/Desktop/pevm/crates/pevm-ffi/include
#cgo LDFLAGS: -L/Users/raneet/Desktop/pevm/target/debug -lpevm_ffi
#include "pevm_ffi.h"
#include <stdlib.h>
PevmStorageVTable pevm_make_vtable(void);
*/
import "C"

var (
	ctxMap   = make(map[uintptr]*ctxWrapper)
	ctxMapMu sync.Mutex
	ctxID    uintptr = 1
)

// Version returns the linked pevm-ffi version string.
func Version() string {
	v := C.pevm_ffi_version()
	return C.GoString(v)
}

// Healthcheck verifies the FFI bridge is callable.
func Healthcheck() error {
	var err C.PevmFfiError
	ok := C.pevm_ffi_healthcheck((*C.PevmFfiError)(unsafe.Pointer(&err)))
	if !bool(ok) {
		defer C.pevm_ffi_string_free(err.message)
		return errors.New(C.GoString(err.message))
	}
	return nil
}

// ---------- Helper conversions ----------
func u256ToC(x *big.Int) (out C.PevmU256) {
	return U256ToC(x)
}

// U256ToC converts a big.Int to C.PevmU256 (exported for use by core package)
func U256ToC(x *big.Int) (out C.PevmU256) {
	if x == nil {
		return
	}
	b := x.FillBytes(make([]byte, 32))
	for i := 0; i < 32 && i < len(b); i++ {
		out.be[i] = C.uchar(b[i])
	}
	return
}

// ---------- Storage callbacks ----------
type StorageProvider interface {
	// All methods should be side-effect-free reads for the duration of execution.
	Basic(addr [20]byte) (exists bool, balance *big.Int, nonce uint64, err error)
	CodeHash(addr [20]byte) (has bool, hash [32]byte, err error)
	CodeByHash(hash [32]byte, addressHint *[20]byte) (code []byte, err error)
	HasStorage(addr [20]byte) (has bool, err error)
	Storage(addr [20]byte, slot [32]byte) (value *big.Int, err error)
	BlockHash(number uint64) (hash [32]byte, err error)
}

// stateProvider implements StorageProvider using StateDB
// Simplified implementation: directly query StateDB without caching
type stateProvider struct {
	st *state.StateDB
}

// NewStateProvider creates a new StorageProvider from a StateDB (exported for use by core package)
func NewStateProvider(st *state.StateDB) StorageProvider {
	return &stateProvider{
		st: st,
	}
}

func (p *stateProvider) Basic(addr [20]byte) (bool, *big.Int, uint64, error) {
	a := common.BytesToAddress(addr[:])
	// Directly query StateDB - no caching complexity
	balance := p.st.GetBalance(a)
	nonce := p.st.GetNonce(a)
	code := p.st.GetCode(a)

	// If account is empty (no balance, no code, nonce 0), mark as non-existent
	if nonce == 0 && balance.Sign() == 0 && len(code) == 0 {
		return false, new(big.Int), 0, nil
	}
	return true, balance.ToBig(), nonce, nil
}

func (p *stateProvider) CodeHash(addr [20]byte) (bool, [32]byte, error) {
	a := common.BytesToAddress(addr[:])
	// Directly query StateDB
	code := p.st.GetCode(a)
	if len(code) == 0 {
		return false, [32]byte{}, nil
	}
	codeHash := crypto.Keccak256Hash(code)
	return true, codeHash, nil
}

func (p *stateProvider) CodeByHash(hash [32]byte, addressHint *[20]byte) ([]byte, error) {
	// If address hint is provided, use it directly with StateDB.GetCode
	if addressHint != nil {
		addr := common.BytesToAddress(addressHint[:])
		code := p.st.GetCode(addr)
		// Verify the code hash matches (safety check)
		if len(code) > 0 {
			codeHash := crypto.Keccak256Hash(code)
			expectedHash := common.BytesToHash(hash[:])
			if codeHash == expectedHash {
				return code, nil
			}
		}
	}
	// No address hint or hash mismatch - return nil
	// StateDB doesn't provide direct code-by-hash lookup
	return nil, nil
}

func (p *stateProvider) HasStorage(addr [20]byte) (bool, error) {
	a := common.BytesToAddress(addr[:])
	// Check if account has storage by checking storage root hash
	// An account has storage if storageRoot is not empty and not EmptyRootHash
	storageRoot := p.st.GetStorageRoot(a)
	hasStorage := storageRoot != (common.Hash{}) && storageRoot != types.EmptyRootHash
	return hasStorage, nil
}

func (p *stateProvider) Storage(addr [20]byte, slot [32]byte) (*big.Int, error) {
	a := common.BytesToAddress(addr[:])
	var s common.Hash
	copy(s[:], slot[:])
	// Directly query StateDB - GetState returns current state (including pending changes)
	value := p.st.GetState(a, s)
	// Convert common.Hash (32 bytes) to big.Int
	return new(big.Int).SetBytes(value[:]), nil
}

func (p *stateProvider) BlockHash(number uint64) ([32]byte, error) {
	// Return keccak(number) to match REVM EmptyDB default.
	var out [32]byte
	h := crypto.Keccak256Hash([]byte(new(big.Int).SetUint64(number).String()))
	copy(out[:], h[:])
	return out, nil
}

type ctxWrapper struct {
	sp StorageProvider
}

// RegisterContext registers a StorageProvider and returns a context ID (exported for use by core package)
func RegisterContext(sp StorageProvider) uintptr {
	w := &ctxWrapper{sp: sp}
	ctxMapMu.Lock()
	id := ctxID
	ctxID++
	ctxMap[id] = w
	ctxMapMu.Unlock()
	return id
}

// UnregisterContext unregisters a context ID (exported for use by core package)
func UnregisterContext(id uintptr) {
	ctxMapMu.Lock()
	delete(ctxMap, id)
	ctxMapMu.Unlock()
}

func getCtxWrapper(ctx unsafe.Pointer) (*ctxWrapper, *C.char) {
	id := uintptr(ctx)
	ctxMapMu.Lock()
	w, ok := ctxMap[id]
	ctxMapMu.Unlock()
	if !ok {
		return nil, C.CString("invalid context id")
	}
	return w, nil
}

//export go_pevm_basic
func go_pevm_basic(ctx unsafe.Pointer, addr *C.PevmAddress, exists *C.uint8_t, balance *C.PevmU256, nonce *C.uint64_t, errOut **C.char) C.int {
	w, errMsg := getCtxWrapper(ctx)
	if errMsg != nil {
		*errOut = errMsg
		return 0
	}
	prov := w.sp
	var a [20]byte
	copy(a[:], C.GoBytes(unsafe.Pointer(&addr.be[0]), 20))
	ok, bal, n, err := prov.Basic(a)
	if err != nil {
		*errOut = C.CString(err.Error())
		return 0
	}
	if ok {
		*exists = 1
		*balance = u256ToC(bal)
		*nonce = C.uint64_t(n)
	} else {
		*exists = 0
	}
	return 1
}

//export go_pevm_code_hash
func go_pevm_code_hash(ctx unsafe.Pointer, addr *C.PevmAddress, hasCode *C.uint8_t, codeHash *C.PevmB256, errOut **C.char) C.int {
	w, errMsg := getCtxWrapper(ctx)
	if errMsg != nil {
		*errOut = errMsg
		return 0
	}
	prov := w.sp
	var a [20]byte
	copy(a[:], C.GoBytes(unsafe.Pointer(&addr.be[0]), 20))
	has, h, err := prov.CodeHash(a)
	if err != nil {
		*errOut = C.CString(err.Error())
		return 0
	}
	if has {
		*hasCode = 1
		for i := 0; i < 32 && i < len(h); i++ {
			codeHash.be[i] = C.uchar(h[i])
		}
	} else {
		*hasCode = 0
	}
	return 1
}

//export go_pevm_code_by_hash
func go_pevm_code_by_hash(ctx unsafe.Pointer, hash *C.PevmB256, addressHint *C.PevmAddress, out *C.PevmBytes, errOut **C.char) C.int {
	w, errMsg := getCtxWrapper(ctx)
	if errMsg != nil {
		*errOut = errMsg
		return 0
	}
	prov := w.sp
	var h [32]byte
	copy(h[:], C.GoBytes(unsafe.Pointer(&hash.be[0]), 32))
	var addrHint *[20]byte
	if addressHint != nil {
		var a [20]byte
		copy(a[:], C.GoBytes(unsafe.Pointer(&addressHint.be[0]), 20))
		addrHint = &a
	}
	code, err := prov.CodeByHash(h, addrHint)
	if err != nil {
		*errOut = C.CString(err.Error())
		return 0
	}
	if len(code) == 0 {
		out.ptr = nil
		out.len = 0
		return 1
	}
	mem := C.CBytes(code)
	out.ptr = (*C.uchar)(mem)
	out.len = C.uintptr_t(len(code))
	return 1
}

//export go_pevm_free_bytes
func go_pevm_free_bytes(ctx unsafe.Pointer, ptr *C.uchar, len C.uintptr_t) {
	_ = ctx // unused
	_ = len // unused
	if ptr != nil {
		C.free(unsafe.Pointer(ptr))
	}
}

//export go_pevm_has_storage
func go_pevm_has_storage(ctx unsafe.Pointer, addr *C.PevmAddress, has *C.uint8_t, errOut **C.char) C.int {
	w, errMsg := getCtxWrapper(ctx)
	if errMsg != nil {
		*errOut = errMsg
		return 0
	}
	prov := w.sp
	var a [20]byte
	copy(a[:], C.GoBytes(unsafe.Pointer(&addr.be[0]), 20))
	ok, err := prov.HasStorage(a)
	if err != nil {
		*errOut = C.CString(err.Error())
		return 0
	}
	if ok {
		*has = 1
	} else {
		*has = 0
	}
	return 1
}

//export go_pevm_storage
func go_pevm_storage(ctx unsafe.Pointer, addr *C.PevmAddress, slot *C.PevmU256, value *C.PevmU256, errOut **C.char) C.int {
	w, errMsg := getCtxWrapper(ctx)
	if errMsg != nil {
		*errOut = errMsg
		return 0
	}
	prov := w.sp
	var a [20]byte
	copy(a[:], C.GoBytes(unsafe.Pointer(&addr.be[0]), 20))
	var s [32]byte
	copy(s[:], C.GoBytes(unsafe.Pointer(&slot.be[0]), 32))
	v, err := prov.Storage(a, s)
	if err != nil {
		*errOut = C.CString(err.Error())
		return 0
	}
	*value = u256ToC(v)
	return 1
}

//export go_pevm_block_hash
func go_pevm_block_hash(ctx unsafe.Pointer, number C.ulonglong, out *C.PevmB256, errOut **C.char) C.int {
	w, errMsg := getCtxWrapper(ctx)
	if errMsg != nil {
		*errOut = errMsg
		return 0
	}
	prov := w.sp
	h, err := prov.BlockHash(uint64(number))
	if err != nil {
		*errOut = C.CString(err.Error())
		return 0
	}
	for i := 0; i < 32 && i < len(h); i++ {
		out.be[i] = C.uchar(h[i])
	}
	return 1
}
