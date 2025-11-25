// This test requires sst_new_with_provider which is not available in the current compiled library
// To enable this test:
// 1. Update Rust toolchain: rustup update (need 1.82+)
// 2. Rebuild FFI: cargo build --release --manifest-path=./ffi/reth-pst-ffi/Cargo.toml
// 3. Uncomment NewRethSSTWithProvider in reth_sst_ffi.go
// 4. Run: go test -tags="reth_ffi reth_sst_provider"

package trie_test

import (
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// TestRethSST_Correctness verifies that Reth's SparseStateTrie produces
// the same root hash as the legacy MPT for account and storage operations
func TestRethSST_Correctness(t *testing.T) {
	const numAccounts = 100
	const numStorageSlots = 50
	const numUpdates = 20
	const numDeletes = 10

	tdb := triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil)
	r := rand.New(rand.NewSource(42))

	// Generate test addresses
	addresses := make([]common.Address, numAccounts)
	for i := 0; i < numAccounts; i++ {
		var addr [20]byte
		r.Read(addr[:])
		addresses[i] = common.BytesToAddress(addr[:])
	}

	// Generate test storage slots
	slots := make([]common.Hash, numStorageSlots)
	for i := 0; i < numStorageSlots; i++ {
		var slot [32]byte
		r.Read(slot[:])
		slots[i] = common.BytesToHash(slot[:])
	}

	// Create legacy MPT
	legacyTrie, err := trie.NewStateTrie(trie.StateTrieID(types.EmptyRootHash), tdb)
	if err != nil {
		t.Fatal(err)
	}

	// Create Reth SST with database provider
	// Get a NodeReader from the database
	reader, err := tdb.NodeReader(types.EmptyRootHash)
	if err != nil {
		t.Fatal(err)
	}
	rethSST, err := trie.NewRethSSTWithProvider(types.EmptyRootHash, reader)
	if err != nil {
		t.Fatal(err)
	}
	defer rethSST.Free()

	// Phase 1: Initial account updates
	t.Log("Phase 1: Initial account updates")
	for i := 0; i < numAccounts; i++ {
		addr := addresses[i]
		nonce := uint64(r.Int63())
		balance := uint256.NewInt(uint64(r.Int63()))
		acc := &types.StateAccount{
			Nonce:    nonce,
			Balance:  balance,
			Root:     types.EmptyRootHash,
			CodeHash: crypto.Keccak256(nil),
		}

		// Update legacy
		if err := legacyTrie.UpdateAccount(addr, acc, 0); err != nil {
			t.Fatal(err)
		}

		// Update Reth SST
		encAcc, err := rlp.EncodeToBytes(acc)
		if err != nil {
			t.Fatal(err)
		}
		if err := rethSST.UpdateAccount(addr, encAcc); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 2: Storage updates for some accounts
	t.Log("Phase 2: Storage updates")
	for i := 0; i < numAccounts/2; i++ {
		addr := addresses[i]
		addrHash := crypto.Keccak256Hash(addr.Bytes())

		// Create separate storage tries for legacy (Bor uses separate storage tries)
		legacyStorageTrie, err := trie.NewStateTrie(trie.StorageTrieID(types.EmptyRootHash, addrHash, types.EmptyRootHash), tdb)
		if err != nil {
			t.Fatal(err)
		}

		// Update some storage slots
		for j := 0; j < numStorageSlots/2; j++ {
			slot := slots[j]
			value := uint256.NewInt(uint64(r.Int63()))
			valueBytes := value.Bytes32()

			// Update legacy storage trie
			if err := legacyStorageTrie.UpdateStorage(addr, slot.Bytes(), valueBytes[:]); err != nil {
				t.Fatal(err)
			}

			// Update Reth SST storage (handles storage root automatically)
			if err := rethSST.UpdateStorage(addr, slot, valueBytes[:]); err != nil {
				t.Fatal(err)
			}
		}

		// Commit legacy storage trie to get storage root
		legacyStorageTrie.Hash()
		storageRoot, _ := legacyStorageTrie.Commit(false)

		// Get existing account or create new one
		acc, err := legacyTrie.GetAccount(addr)
		if err != nil {
			t.Fatal(err)
		}
		if acc == nil {
			acc = types.NewEmptyStateAccount()
		}

		// Update account with storage root in legacy trie
		acc.Root = storageRoot
		if err := legacyTrie.UpdateAccount(addr, acc, 0); err != nil {
			t.Fatal(err)
		}

		// Update account in Reth SST (storage root already updated by UpdateStorage)
		encAcc, err := rlp.EncodeToBytes(acc)
		if err != nil {
			t.Fatal(err)
		}
		if err := rethSST.UpdateAccount(addr, encAcc); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 3: Additional account updates
	t.Log("Phase 3: Additional account updates")
	for i := 0; i < numUpdates; i++ {
		idx := i % numAccounts
		addr := addresses[idx]
		nonce := uint64(r.Int63())
		balance := uint256.NewInt(uint64(r.Int63()))
		acc := &types.StateAccount{
			Nonce:    nonce,
			Balance:  balance,
			Root:     types.EmptyRootHash,
			CodeHash: crypto.Keccak256(nil),
		}

		if err := legacyTrie.UpdateAccount(addr, acc, 0); err != nil {
			t.Fatal(err)
		}

		encAcc, err := rlp.EncodeToBytes(acc)
		if err != nil {
			t.Fatal(err)
		}
		if err := rethSST.UpdateAccount(addr, encAcc); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 4: Storage updates (modify existing)
	t.Log("Phase 4: Storage updates (modify existing)")
	for i := 0; i < numAccounts/4; i++ {
		addr := addresses[i]
		addrHash := crypto.Keccak256Hash(addr.Bytes())

		// Get existing account to retrieve current storage root
		acc, err := legacyTrie.GetAccount(addr)
		if err != nil {
			t.Fatal(err)
		}
		if acc == nil {
			// Account doesn't exist, create empty one
			acc = types.NewEmptyStateAccount()
		}

		// Open existing storage trie
		legacyStorageTrie, err := trie.NewStateTrie(trie.StorageTrieID(types.EmptyRootHash, addrHash, acc.Root), tdb)
		if err != nil {
			t.Fatal(err)
		}

		// Update existing storage slots
		for j := 0; j < numStorageSlots/4; j++ {
			slot := slots[j]
			value := uint256.NewInt(uint64(r.Int63()))
			valueBytes := value.Bytes32()

			if err := legacyStorageTrie.UpdateStorage(addr, slot.Bytes(), valueBytes[:]); err != nil {
				t.Fatal(err)
			}
			if err := rethSST.UpdateStorage(addr, slot, valueBytes[:]); err != nil {
				t.Fatal(err)
			}
		}

		// Commit storage trie and update account
		legacyStorageTrie.Hash()
		storageRoot, _ := legacyStorageTrie.Commit(false)
		acc.Root = storageRoot
		if err := legacyTrie.UpdateAccount(addr, acc, 0); err != nil {
			t.Fatal(err)
		}

		encAcc, err := rlp.EncodeToBytes(acc)
		if err != nil {
			t.Fatal(err)
		}
		if err := rethSST.UpdateAccount(addr, encAcc); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 5: Storage deletes
	t.Log("Phase 5: Storage deletes")
	for i := 0; i < numAccounts/4; i++ {
		addr := addresses[i]
		addrHash := crypto.Keccak256Hash(addr.Bytes())

		// Get existing account to retrieve current storage root
		acc, err := legacyTrie.GetAccount(addr)
		if err != nil {
			t.Fatal(err)
		}
		if acc == nil {
			// Account doesn't exist, create empty one
			acc = types.NewEmptyStateAccount()
		}

		// Open existing storage trie
		legacyStorageTrie, err := trie.NewStateTrie(trie.StorageTrieID(types.EmptyRootHash, addrHash, acc.Root), tdb)
		if err != nil {
			t.Fatal(err)
		}

		// Delete some storage slots
		for j := 0; j < numStorageSlots/8; j++ {
			slot := slots[j]

			if err := legacyStorageTrie.DeleteStorage(addr, slot.Bytes()); err != nil {
				t.Fatal(err)
			}
			if err := rethSST.UpdateStorage(addr, slot, []byte{}); err != nil {
				t.Fatal(err)
			}
		}

		// Commit storage trie and update account
		legacyStorageTrie.Hash()
		storageRoot, _ := legacyStorageTrie.Commit(false)
		acc.Root = storageRoot
		if err := legacyTrie.UpdateAccount(addr, acc, 0); err != nil {
			t.Fatal(err)
		}

		encAcc, err := rlp.EncodeToBytes(acc)
		if err != nil {
			t.Fatal(err)
		}
		if err := rethSST.UpdateAccount(addr, encAcc); err != nil {
			t.Fatal(err)
		}
	}

	// Phase 6: Account deletes
	t.Log("Phase 6: Account deletes")
	for i := 0; i < numDeletes; i++ {
		idx := i % numAccounts
		addr := addresses[idx]

		if err := legacyTrie.DeleteAccount(addr); err != nil {
			t.Fatal(err)
		}
		if err := rethSST.RemoveAccount(addr); err != nil {
			t.Fatal(err)
		}
	}

	// Commit legacy trie
	legacyHash := legacyTrie.Hash()
	_, _ = legacyTrie.Commit(false)

	// Get Reth SST root
	rethRoot, err := rethSST.Root()
	if err != nil {
		t.Fatal(err)
	}

	// Compare roots
	rethHash := common.BytesToHash(rethRoot)
	if legacyHash != rethHash {
		t.Errorf("Root mismatch!\nLegacy: %x\nReth:   %x", legacyHash, rethHash)
	} else {
		t.Logf("Roots match: %x", legacyHash)
	}
}
