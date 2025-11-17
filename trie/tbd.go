// Package trie provides a wrapper around the triedb-go bindings
// that integrates with Bor's StateAccount type.
package trie

import (
	"errors"
	"fmt"

	"github.com/cffls/triedb-go/triedb-go"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/trie/trienode"
	"github.com/holiman/uint256"
)

// Re-export types and functions from triedb package
type (
	Database      = triedb.Database
	TransactionRO = triedb.TransactionRO
	TransactionRW = triedb.TransactionRW
	Address       = triedb.Address
	Hash          = triedb.Hash
)

// Re-export functions
var (
	Open           = triedb.Open
	CreateNew      = triedb.CreateNew
	AddressFromHex = triedb.AddressFromHex
	HashFromHex    = triedb.HashFromHex
)

// Re-export errors
var (
	ErrInvalidPath        = triedb.ErrInvalidPath
	ErrInvalidAddress     = triedb.ErrInvalidAddress
	ErrDatabaseOpenFailed = triedb.ErrDatabaseOpenFailed
	ErrTransactionFailed  = triedb.ErrTransactionFailed
	ErrNullPointer        = triedb.ErrNullPointer
	ErrUtf8Error          = triedb.ErrUtf8Error
	ErrAccountNotFound    = triedb.ErrAccountNotFound
	ErrStorageNotFound    = triedb.ErrStorageNotFound
)

// Conversion functions between triedb.Account and types.StateAccount

// ToStateAccount converts a triedb.Account to a types.StateAccount
func ToStateAccount(acc *triedb.Account) *types.StateAccount {
	if acc == nil {
		return nil
	}

	return &types.StateAccount{
		Nonce:    acc.Nonce,
		Balance:  acc.Balance,
		Root:     common.Hash(acc.StorageRoot),
		CodeHash: acc.CodeHash,
	}
}

// FromStateAccount converts a types.StateAccount to a triedb.Account
func FromStateAccount(acc *types.StateAccount) *triedb.Account {
	if acc == nil {
		return nil
	}

	return &triedb.Account{
		Nonce:       acc.Nonce,
		Balance:     acc.Balance,
		StorageRoot: triedb.Hash(acc.Root),
		CodeHash:    acc.CodeHash,
	}
}

// Extended transaction methods that work with StateAccount

// GetStateAccount retrieves a StateAccount from a read-only transaction
func GetStateAccount(tx *TransactionRO, address Address) (*types.StateAccount, error) {
	acc, err := tx.GetAccount(address)
	if err != nil {
		return nil, err
	}
	return ToStateAccount(acc), nil
}

// GetStateAccountRW retrieves a StateAccount from a read-write transaction
func GetStateAccountRW(tx *TransactionRW, address Address) (*types.StateAccount, error) {
	acc, err := tx.GetAccount(address)
	if err != nil {
		return nil, err
	}
	return ToStateAccount(acc), nil
}

// SetStateAccount sets a StateAccount in a read-write transaction
func SetStateAccount(tx *TransactionRW, address Address, account *types.StateAccount) error {
	return tx.SetAccount(address, FromStateAccount(account))
}

// TrieDB implements the Trie interface using triedb-go with overlay state
type TrieDB struct {
	db   *Database
	root common.Hash

	// Local cache of changes for reading modified data
	accounts map[Address]*types.StateAccount // nil means deleted
	storage  map[Address]map[Hash][]byte     // nil/empty means deleted
}

// NewTrieDB creates a new Trie implementation using triedb-go
func NewTrieDB(db *Database, root common.Hash) (*TrieDB, error) {
	r, err := db.StateRoot()
	if err != nil {
		return nil, err
	}
	if common.Hash(r) != root {
		return nil, fmt.Errorf("root mismatch: expected %s, got %s", root, common.Hash(r))
	}

	return &TrieDB{
		db:       db,
		root:     root,
		accounts: make(map[Address]*types.StateAccount),
		storage:  make(map[Address]map[Hash][]byte),
	}, nil
}

// GetKey returns the sha3 preimage of a hashed key
// TODO: Placeholder - not yet implemented
func (t *TrieDB) GetKey(key []byte) []byte {
	// Placeholder implementation
	return nil
}

// GetAccount retrieves an account from the trie
func (t *TrieDB) GetAccount(address common.Address) (*types.StateAccount, error) {
	addr := Address(address)

	// Check local cache first
	if acc, exists := t.accounts[addr]; exists {
		if acc == nil {
			// Account was deleted
			return nil, ErrAccountNotFound
		}
		return acc, nil
	}

	// Read from base state using a temporary transaction
	tx, err := t.db.BeginRO()
	if err != nil {
		return nil, err
	}
	defer tx.Commit()

	acc, err := tx.GetAccount(addr)
	if err != nil {
		return nil, err
	}
	return ToStateAccount(acc), nil
}

// GetStorage retrieves a storage value from the trie
func (t *TrieDB) GetStorage(addr common.Address, key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("storage key must be 32 bytes, got %d", len(key))
	}

	var slot Hash
	copy(slot[:], key)
	address := Address(addr)

	// Check local cache first
	if addrStorage, exists := t.storage[address]; exists {
		if value, exists := addrStorage[slot]; exists {
			if len(value) == 0 {
				// Storage was deleted
				return nil, nil
			}
			return value, nil
		}
	}

	// Read from base state using a temporary transaction
	tx, err := t.db.BeginRO()
	if err != nil {
		return nil, err
	}
	defer tx.Commit()

	value, err := tx.GetStorage(address, slot)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	return (*value)[:], nil
}

// UpdateAccount updates an account in the trie
func (t *TrieDB) UpdateAccount(address common.Address, account *types.StateAccount, codeLen int) error {
	addr := Address(address)

	// Update local cache
	t.accounts[addr] = account

	return nil
}

// UpdateStorage updates a storage value in the trie
func (t *TrieDB) UpdateStorage(addr common.Address, key, value []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("storage key must be 32 bytes, got %d", len(key))
	}

	var slot Hash
	copy(slot[:], key)
	address := Address(addr)

	// Initialize storage map for this address if needed
	if t.storage[address] == nil {
		t.storage[address] = make(map[Hash][]byte)
	}

	if len(value) == 0 {
		// Delete storage
		t.storage[address][slot] = nil
		return nil
	}

	// Validate value
	if len(value) > 32 {
		return fmt.Errorf("storage value must be at most 32 bytes, got %d", len(value))
	}

	// Store in cache (keep original value for reads)
	valueCopy := make([]byte, len(value))
	copy(valueCopy, value)
	t.storage[address][slot] = valueCopy

	return nil
}

// DeleteAccount deletes an account from the trie
func (t *TrieDB) DeleteAccount(address common.Address) error {
	addr := Address(address)

	// Mark as deleted in cache
	t.accounts[addr] = nil

	return nil
}

// DeleteStorage deletes a storage value from the trie
func (t *TrieDB) DeleteStorage(addr common.Address, key []byte) error {
	if len(key) != 32 {
		return fmt.Errorf("storage key must be 32 bytes, got %d", len(key))
	}

	var slot Hash
	copy(slot[:], key)
	address := Address(addr)

	// Initialize storage map for this address if needed
	if t.storage[address] == nil {
		t.storage[address] = make(map[Hash][]byte)
	}

	// Mark as deleted in cache
	t.storage[address][slot] = nil

	return nil
}

// UpdateContractCode stores contract code
// Note: This is a placeholder implementation as triedb-go may handle code differently
func (t *TrieDB) UpdateContractCode(address common.Address, codeHash common.Hash, code []byte) error {
	// TODO: Implement proper code storage mechanism
	// For now, this is a placeholder that doesn't actually store the code
	// The code hash should already be part of the account state
	return nil
}

// buildOverlay creates an overlay state from the cached changes
func (t *TrieDB) buildOverlay() (*triedb.OverlayState, error) {
	overlay, err := triedb.NewOverlayState()
	if err != nil {
		return nil, err
	}

	// Insert all account changes
	for addr, acc := range t.accounts {
		if err := overlay.InsertAccount(addr, FromStateAccount(acc)); err != nil {
			overlay.Close()
			return nil, err
		}
	}

	// Insert all storage changes
	for addr, slots := range t.storage {
		for slot, value := range slots {
			if len(value) == 0 {
				// Deletion
				if err := overlay.InsertStorage(addr, slot, nil); err != nil {
					overlay.Close()
					return nil, err
				}
			} else {
				// Update - convert value to uint256.Int
				var valBytes [32]byte
				if len(value) > 32 {
					overlay.Close()
					return nil, fmt.Errorf("storage value too large: %d bytes", len(value))
				}
				copy(valBytes[32-len(value):], value)
				valInt := new(uint256.Int)
				valInt.SetBytes(valBytes[:])

				if err := overlay.InsertStorage(addr, slot, valInt); err != nil {
					overlay.Close()
					return nil, err
				}
			}
		}
	}

	return overlay, nil
}

// Hash returns the root hash of the trie
func (t *TrieDB) Hash() common.Hash {
	h, _ := t.Commit(false)
	return h
}

// Commit computes the new state root by applying the overlay changes
// Note: This does NOT persist changes to the database, it only computes the new root
func (t *TrieDB) Commit(collectLeaf bool) (common.Hash, *trienode.NodeSet) {
	overlay, err := t.buildOverlay()
	if err != nil {
		return t.root, nil
	}
	defer overlay.Close()

	tx, err := t.db.BeginRO()
	if err != nil {
		return t.root, nil
	}
	defer tx.Commit()

	root, err := tx.ComputeRootWithOverlay(overlay)
	if err != nil {
		return t.root, nil
	}

	return common.Hash(root), nil
}

// Witness returns the set of accessed trie nodes
// TODO: Placeholder - not yet implemented
func (t *TrieDB) Witness() map[string]struct{} {
	// Placeholder implementation
	return nil
}

// NodeIterator returns an iterator for trie nodes
// TODO: Placeholder - not yet implemented
func (t *TrieDB) NodeIterator(startKey []byte) (NodeIterator, error) {
	// Placeholder implementation
	return nil, errors.New("NodeIterator not yet implemented")
}

// Prove generates a Merkle proof for a key
// TODO: Placeholder - not yet implemented
func (t *TrieDB) Prove(key []byte, proofDb ethdb.KeyValueWriter) error {
	// Placeholder implementation
	return errors.New("Prove not yet implemented")
}

// IsVerkle returns false as this is not a Verkle trie
func (t *TrieDB) IsVerkle() bool {
	return false
}
