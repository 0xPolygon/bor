package trie

/*
#cgo LDFLAGS: -L${SRCDIR}/../ffi/reth-pst-ffi/target/release -lreth_pst_ffi -ldl -lm
#include <stdlib.h>

// Forward declarations
void* pst_new();
void pst_free(void* handle);
int pst_update_leaf(void* handle, const char* key_hex, const void* value_ptr, size_t value_len);
int pst_root(void* handle, void* output);
*/
import "C"
import (
	"encoding/hex"
	"errors"
	"unsafe"
)

// RethPSTHandle wraps a handle to Reth's Parallel Sparse Trie via FFI
type RethPSTHandle struct {
	handle unsafe.Pointer
}

// NewRethPST creates a new Reth Parallel Sparse Trie instance via FFI
func NewRethPST() (*RethPSTHandle, error) {
	handle := C.pst_new()
	if handle == nil {
		return nil, errors.New("failed to create Reth PST handle")
	}
	return &RethPSTHandle{handle: handle}, nil
}

// Free releases the Reth PST handle
func (h *RethPSTHandle) Free() {
	if h.handle != nil {
		C.pst_free(h.handle)
		h.handle = nil
	}
}

// UpdateLeaf updates a leaf in the trie
// key: raw bytes (will be converted to hex nibbles)
// value: raw bytes
func (h *RethPSTHandle) UpdateLeaf(key, value []byte) error {
	if h.handle == nil {
		return errors.New("handle is nil")
	}

	// Convert key bytes to hex nibbles (matching keybytesToHex)
	nibbles := keybytesToHex(key)
	keyHex := hex.EncodeToString(nibbles)

	cKeyHex := C.CString(keyHex)
	defer C.free(unsafe.Pointer(cKeyHex))

	var valuePtr unsafe.Pointer
	if len(value) > 0 {
		valuePtr = unsafe.Pointer(&value[0])
	}

	result := C.pst_update_leaf(h.handle, cKeyHex, valuePtr, C.size_t(len(value)))
	if result != 0 {
		return errors.New("failed to update leaf in Reth PST")
	}
	return nil
}

// Root returns the root hash of the trie
func (h *RethPSTHandle) Root() ([]byte, error) {
	if h.handle == nil {
		return nil, errors.New("handle is nil")
	}

	output := make([]byte, 32)
	result := C.pst_root(h.handle, unsafe.Pointer(&output[0]))
	if result != 0 {
		return nil, errors.New("failed to get root from Reth PST")
	}
	return output, nil
}

// UpdateBatch updates multiple leaves in batch
func (h *RethPSTHandle) UpdateBatch(updates map[string][]byte) error {
	for keyStr, value := range updates {
		key := []byte(keyStr)
		if err := h.UpdateLeaf(key, value); err != nil {
			return err
		}
	}
	return nil
}
