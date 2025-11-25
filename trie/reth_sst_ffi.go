package trie

/*
#cgo LDFLAGS: -L${SRCDIR}/../ffi/reth-pst-ffi/target/release -lreth_pst_ffi -ldl -lm
#include <stdlib.h>
#include <string.h>

// Forward declarations for SparseStateTrie (SST) functions
void* sst_new();
void* sst_new_with_provider(const void* owner_ptr, void* reader);
void sst_free(void* handle);
int sst_update_account(void* handle, const void* address_ptr, const void* account_rlp_ptr, size_t account_rlp_len);
int sst_update_storage(void* handle, const void* address_ptr, const char* slot_hex, const void* value_ptr, size_t value_len);
int sst_remove_account(void* handle, const void* address_ptr);
int sst_root(void* handle, void* output);

// Callback function type for reading nodes from Bor's database
// Returns 0 on success, non-zero on error
// If node not found, sets node_data_ptr to NULL and node_data_len to 0, returns 0
// handle_ptr: Opaque pointer to identify which reader to use (for registry lookup)
typedef int (*BorNodeReaderCallback)(
    void* handle_ptr,           // Opaque handle for registry lookup
    const void* owner_ptr,      // 32-byte owner hash
    const void* path_ptr,       // path bytes
    size_t path_len,            // path length
    void** node_data_ptr,       // output: pointer to node data (caller must free with free())
    size_t* node_data_len       // output: node data length
);

// Note: borNodeReaderCallback is exported via //export directive below
// Cgo will automatically generate the declaration, so we don't declare it here
*/
import "C"
import (
	"encoding/hex"
	"errors"
	"reflect"
	"sync"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/triedb/database"
)

// RethSSTHandle wraps a handle to Reth's SparseStateTrie via FFI
type RethSSTHandle struct {
	handle unsafe.Pointer
	reader database.NodeReader // Keep reference to prevent GC
}

// Global registry to map handles to readers (thread-safe with sync.Map)
// This is needed because C callbacks can't capture Go closures
var readerRegistry sync.Map // map[unsafe.Pointer]database.NodeReader

//export borNodeReaderCallback
func borNodeReaderCallback(
	handlePtr unsafe.Pointer,
	ownerPtr unsafe.Pointer,
	pathPtr unsafe.Pointer,
	pathLen C.size_t,
	nodeDataPtr **C.void,
	nodeDataLen *C.size_t,
) C.int {
	// Look up the reader from the registry
	readerInterface, ok := readerRegistry.Load(handlePtr)
	if !ok {
		// No reader registered for this handle
		*nodeDataPtr = nil
		*nodeDataLen = 0
		return 0 // Node not found (not an error)
	}

	reader := readerInterface.(database.NodeReader)

	// Convert C types to Go types
	owner := common.BytesToHash(C.GoBytes(ownerPtr, 32))
	path := C.GoBytes(pathPtr, C.int(pathLen))

	// Bor's NodeReader.Node() requires a hash, but we only have the path
	// For now, we'll need to compute a hash or use a different approach
	// TODO: Implement proper hash lookup or extend Bor's interface to support path-only lookups

	// Try to read node - we need the hash, but don't have it
	// For now, return not found (this will need to be fixed)
	// Suppress unused variable warnings - these will be used when we implement proper node lookup
	_ = reader
	_ = owner
	_ = path
	*nodeDataPtr = nil
	*nodeDataLen = 0
	return 0
}

// NewRethSST creates a new Reth SparseStateTrie instance via FFI
func NewRethSST() (*RethSSTHandle, error) {
	handle := C.sst_new()
	if handle == nil {
		return nil, errors.New("failed to create Reth SST handle")
	}
	return &RethSSTHandle{handle: handle}, nil
}

// NewRethSSTWithProvider creates a new Reth SparseStateTrie instance with a database provider
// owner: The owner hash (state root for account trie)
// reader: NodeReader to read nodes from Bor's database
func NewRethSSTWithProvider(owner common.Hash, reader database.NodeReader) (*RethSSTHandle, error) {
	// Get the function pointer address using reflection
	// We can't use C.borNodeReaderCallback directly because cgo doesn't create C types for exported functions
	// Instead, we get the function value and extract its pointer
	callbackFunc := borNodeReaderCallback
	callbackValue := reflect.ValueOf(callbackFunc)
	callbackPtr := unsafe.Pointer(callbackValue.Pointer())

	// Create handle with provider
	ownerBytes := owner.Bytes()
	handle := C.sst_new_with_provider(
		unsafe.Pointer(&ownerBytes[0]),
		callbackPtr,
	)
	if handle == nil {
		return nil, errors.New("failed to create Reth SST handle with provider")
	}

	// Register the reader in the global registry (keyed by handle pointer)
	readerRegistry.Store(handle, reader)

	return &RethSSTHandle{handle: handle, reader: reader}, nil
}

// Free releases the Reth SST handle
func (h *RethSSTHandle) Free() {
	if h.handle != nil {
		// Remove from registry
		readerRegistry.Delete(h.handle)
		C.sst_free(h.handle)
		h.handle = nil
	}
}

// UpdateAccount updates an account in the state trie
// address: Ethereum address (will be hashed)
// accountRlp: RLP-encoded StateAccount
func (h *RethSSTHandle) UpdateAccount(address common.Address, accountRlp []byte) error {
	if h.handle == nil {
		return errors.New("handle is nil")
	}

	// Hash the address (Reth uses hashed addresses)
	hashedAddr := crypto.Keccak256(address.Bytes())

	result := C.sst_update_account(
		h.handle,
		unsafe.Pointer(&hashedAddr[0]),
		unsafe.Pointer(&accountRlp[0]),
		C.size_t(len(accountRlp)),
	)
	if result != 0 {
		return errors.New("failed to update account in Reth SST")
	}
	return nil
}

// UpdateStorage updates a storage slot
// address: Ethereum address (will be hashed)
// slot: Storage slot key (will be hashed and converted to nibbles)
// value: Storage value (empty for delete)
func (h *RethSSTHandle) UpdateStorage(address common.Address, slot common.Hash, value []byte) error {
	if h.handle == nil {
		return errors.New("handle is nil")
	}

	// Hash the address
	hashedAddr := crypto.Keccak256(address.Bytes())
	// Hash the slot
	hashedSlot := crypto.Keccak256(slot.Bytes())
	// Convert to hex nibbles (matching keybytesToHex)
	nibbles := keybytesToHex(hashedSlot[:])
	slotHex := hex.EncodeToString(nibbles)

	cSlotHex := C.CString(slotHex)
	defer C.free(unsafe.Pointer(cSlotHex))

	var valuePtr unsafe.Pointer
	if len(value) > 0 {
		valuePtr = unsafe.Pointer(&value[0])
	}

	result := C.sst_update_storage(
		h.handle,
		unsafe.Pointer(&hashedAddr[0]),
		cSlotHex,
		valuePtr,
		C.size_t(len(value)),
	)
	if result != 0 {
		return errors.New("failed to update storage in Reth SST")
	}
	return nil
}

// RemoveAccount removes an account from the state trie
func (h *RethSSTHandle) RemoveAccount(address common.Address) error {
	if h.handle == nil {
		return errors.New("handle is nil")
	}

	hashedAddr := crypto.Keccak256(address.Bytes())
	result := C.sst_remove_account(h.handle, unsafe.Pointer(&hashedAddr[0]))
	if result != 0 {
		return errors.New("failed to remove account from Reth SST")
	}
	return nil
}

// Root returns the root hash of the state trie
func (h *RethSSTHandle) Root() ([]byte, error) {
	if h.handle == nil {
		return nil, errors.New("handle is nil")
	}

	output := make([]byte, 32)
	result := C.sst_root(h.handle, unsafe.Pointer(&output[0]))
	if result != 0 {
		return nil, errors.New("failed to get root from Reth SST")
	}
	return output, nil
}
