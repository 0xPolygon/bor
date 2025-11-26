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
int sst_reveal_account_node(void* handle, const char* path_hex, const void* node_ptr, size_t node_len);
int sst_reveal_storage_node(void* handle, const void* address_ptr, const char* path_hex, const void* node_ptr, size_t node_len);

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

*/
import "C"
import (
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"unsafe"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/triedb/database"
)

// PathBlob is an exported helper for batch reveals from tests.
type PathBlob struct {
	PathHex string
	Blob    []byte
}

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

	// For hash-based backends, the hash parameter is required to locate nodes.
	// Since the provider only has the path, pass a zero hash; pathdb readers will use (owner,path).
	var zeroHash common.Hash

	blob, err := reader.Node(owner, path, zeroHash)
	if err != nil {
		// Propagate an error code to Rust
		*nodeDataPtr = nil
		*nodeDataLen = 0
		return 1
	}
	if len(blob) == 0 {
		// Not found
		*nodeDataPtr = nil
		*nodeDataLen = 0
		return 0
	}

	// Allocate C memory and copy blob
	mem := C.CBytes(blob)
	*nodeDataPtr = (*C.void)(mem)
	*nodeDataLen = C.size_t(len(blob))
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
	// Obtain function pointer for the exported callback via reflection.
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

// RevealAccountBatchHex reveals a sequence of account nodes in root->leaf order.
func (h *RethSSTHandle) RevealAccountBatchHex(items []PathBlob) error {
	if h.handle == nil {
		return errors.New("handle is nil")
	}
	for _, it := range items {
		cPath := C.CString(it.PathHex)
		rc := C.sst_reveal_account_node(h.handle, cPath, unsafe.Pointer(&it.Blob[0]), C.size_t(len(it.Blob)))
		C.free(unsafe.Pointer(cPath))
		if rc != 0 {
			limit := 64
			if len(it.Blob) < limit {
				limit = len(it.Blob)
			}
			preview := hex.EncodeToString(it.Blob[:limit])
			fmt.Printf("[reth-sst] reveal account failed: path=%s rlp_preview=%s\n", it.PathHex, preview)
			return fmt.Errorf("sst_reveal_account_node failed for path %s", it.PathHex)
		}
	}
	return nil
}

// RevealStorageBatchHex reveals a sequence of storage nodes in root->leaf order for a given address.
func (h *RethSSTHandle) RevealStorageBatchHex(address common.Address, items []PathBlob) error {
	if h.handle == nil {
		return errors.New("handle is nil")
	}
	hashedAddr := crypto.Keccak256(address.Bytes())
	for _, it := range items {
		cPath := C.CString(it.PathHex)
		rc := C.sst_reveal_storage_node(h.handle, unsafe.Pointer(&hashedAddr[0]), cPath, unsafe.Pointer(&it.Blob[0]), C.size_t(len(it.Blob)))
		C.free(unsafe.Pointer(cPath))
		if rc != 0 {
			limit := 64
			if len(it.Blob) < limit {
				limit = len(it.Blob)
			}
			preview := hex.EncodeToString(it.Blob[:limit])
			fmt.Printf("[reth-sst] reveal storage failed: addrHash=%x path=%s rlp_preview=%s\n", hashedAddr, it.PathHex, preview)
			return fmt.Errorf("sst_reveal_storage_node failed for address %x path %s", hashedAddr, it.PathHex)
		}
	}
	return nil
}

// UpdateProviderReader swaps the underlying NodeReader used by the FFI callback.
func (h *RethSSTHandle) UpdateProviderReader(reader database.NodeReader) {
	if h == nil || h.handle == nil {
		return
	}
	readerRegistry.Store(h.handle, reader)
	h.reader = reader
}

// revealBatchAccount reveals a sequence of account nodes in root->leaf order.
func (h *RethSSTHandle) revealBatchAccount(items []struct {
	Path []byte
	Blob []byte
}) {
	for _, it := range items {
		cPath := C.CString(hex.EncodeToString(it.Path))
		C.sst_reveal_account_node(h.handle, cPath, unsafe.Pointer(&it.Blob[0]), C.size_t(len(it.Blob)))
		C.free(unsafe.Pointer(cPath))
	}
}

// revealBatchStorage reveals a sequence of storage nodes in root->leaf order for a given address.
func (h *RethSSTHandle) revealBatchStorage(hashedAddr []byte, items []struct {
	Path []byte
	Blob []byte
}) {
	for _, it := range items {
		cPath := C.CString(hex.EncodeToString(it.Path))
		C.sst_reveal_storage_node(h.handle, unsafe.Pointer(&hashedAddr[0]), cPath, unsafe.Pointer(&it.Blob[0]), C.size_t(len(it.Blob)))
		C.free(unsafe.Pointer(cPath))
	}
}

// buildAccountWitness builds root->leaf nodes for the hashed address path from the provider.
func (h *RethSSTHandle) buildAccountWitness(hashedAddr []byte) []struct {
	Path []byte
	Blob []byte
} {
	var out []struct {
		Path []byte
		Blob []byte
	}
	if h.reader == nil {
		return out
	}
	ownerZero := common.Hash{}
	nibbles := keybytesToHex(hashedAddr)
	// strip terminator
	if ln := len(nibbles); ln > 0 && nibbles[ln-1] == 16 {
		nibbles = nibbles[:ln-1]
	}
	// include root (empty path)
	for i := 0; i <= len(nibbles); i++ {
		prefix := nibbles[:i]
		blob, _ := h.reader.Node(ownerZero, prefix, common.Hash{})
		if len(blob) == 0 {
			continue
		}
		out = append(out, struct {
			Path []byte
			Blob []byte
		}{Path: append([]byte{}, prefix...), Blob: append([]byte{}, blob...)})
	}
	return out
}

// buildStorageWitness builds root->leaf nodes for the storage path under the hashed address and hashed slot.
func (h *RethSSTHandle) buildStorageWitness(hashedAddr, hashedSlot []byte, storageRoot common.Hash) []struct {
	Path []byte
	Blob []byte
} {
	var out []struct {
		Path []byte
		Blob []byte
	}
	if h.reader == nil {
		return out
	}
	owner := common.BytesToHash(hashedAddr)
	// root node
	want := storageRoot
	if want == (common.Hash{}) {
		want = types.EmptyRootHash
	}
	rootBlob, _ := h.reader.Node(owner, nil, want)
	if len(rootBlob) == 0 && want == types.EmptyRootHash {
		rootBlob = []byte{0x80}
	}
	if len(rootBlob) > 0 {
		out = append(out, struct {
			Path []byte
			Blob []byte
		}{Path: nil, Blob: append([]byte{}, rootBlob...)})
	}
	// slot path
	nibbles := keybytesToHex(hashedSlot)
	if ln := len(nibbles); ln > 0 && nibbles[ln-1] == 16 {
		nibbles = nibbles[:ln-1]
	}
	for i := 1; i <= len(nibbles); i++ { // start from first nibble; root already handled
		prefix := nibbles[:i]
		blob, _ := h.reader.Node(owner, prefix, common.Hash{})
		if len(blob) == 0 {
			continue
		}
		out = append(out, struct {
			Path []byte
			Blob []byte
		}{Path: append([]byte{}, prefix...), Blob: append([]byte{}, blob...)})
	}
	return out
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

	// Build and reveal account witness before update
	accItems := h.buildAccountWitness(hashedAddr)
	h.revealBatchAccount(accItems)

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

	// Reveal account path and storage path before update
	accItems := h.buildAccountWitness(hashedAddr)
	h.revealBatchAccount(accItems)
	// Try to derive current storage root by reading the account leaf if available
	var storageRoot common.Hash
	{
		ownerZero := common.Hash{}
		accPath := keybytesToHex(hashedAddr)
		if ln := len(accPath); ln > 0 && accPath[ln-1] == 16 {
			accPath = accPath[:ln-1]
		}
		leafBlob, _ := h.reader.Node(ownerZero, accPath, common.Hash{})
		// If unavailable, assume empty storage root; PST will handle
		if len(leafBlob) == 32 {
			// defensive; real decoding omitted; rely on empty root fallback
		}
	}
	storItems := h.buildStorageWitness(hashedAddr, hashedSlot[:], storageRoot)
	h.revealBatchStorage(hashedAddr, storItems)

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

// RevealStorageRoot reveals the storage trie root node for an address using the provided root hash.
// This mirrors Reth's reveal-before-mutate flow for storage tries.
func (h *RethSSTHandle) RevealStorageRoot(address common.Address, storageRoot common.Hash) error {
	if h.handle == nil {
		return errors.New("handle is nil")
	}
	if h.reader == nil {
		return errors.New("no reader registered")
	}
	// Owner is hashed address; path is empty for root.
	hashedAddr := crypto.Keccak256(address.Bytes())
	want := storageRoot
	if want == (common.Hash{}) {
		want = types.EmptyRootHash
	}
	blob, err := h.reader.Node(common.BytesToHash(hashedAddr), nil, want)
	if err != nil {
		return err
	}
	if len(blob) == 0 {
		// If the expected root is the empty trie root, synthesize the empty node (RLP 0x80).
		if want == types.EmptyRootHash {
			cPath := C.CString("")
			defer C.free(unsafe.Pointer(cPath))
			emptyNode := []byte{0x80}
			rc := C.sst_reveal_storage_node(h.handle, unsafe.Pointer(&hashedAddr[0]), cPath, unsafe.Pointer(&emptyNode[0]), C.size_t(len(emptyNode)))
			if rc != 0 {
				return fmt.Errorf("root reveal failed for empty root, addrHash=%x", hashedAddr)
			}
		}
		return nil
	}
	// Empty path hex string
	cPath := C.CString("")
	defer C.free(unsafe.Pointer(cPath))
	// Diagnostics
	got := crypto.Keccak256Hash(blob)
	preview := blob
	if len(preview) > 64 {
		preview = preview[:64]
	}
	fmt.Printf("[reth-sst][root] reveal path=\"\" addrHash=%x keccak=%x expected=%x rlp_len=%d preview=%s\n",
		hashedAddr, got, want, len(blob), hex.EncodeToString(preview))
	rc := C.sst_reveal_storage_node(h.handle, unsafe.Pointer(&hashedAddr[0]), cPath, unsafe.Pointer(&blob[0]), C.size_t(len(blob)))
	if rc != 0 {
		return fmt.Errorf("root reveal failed rc=%d addrHash=%x", int(rc), hashedAddr)
	}
	// Optional: verify hash matches expected
	if got != want {
		return fmt.Errorf("root reveal hash mismatch: got %x want %x", got, want)
	}
	return nil
}
