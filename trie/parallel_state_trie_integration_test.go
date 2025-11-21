package trie_test

import (
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/ethereum/go-ethereum/triedb"
	"github.com/holiman/uint256"
)

// decodeAccounts decodes RLP-encoded accounts into StateAccount structs.
func decodeAccounts(blobs [][]byte) []*types.StateAccount {
	out := make([]*types.StateAccount, len(blobs))
	for i := range blobs {
		a := new(types.StateAccount)
		if err := rlp.DecodeBytes(blobs[i], a); err != nil {
			panic(err)
		}
		out[i] = a
	}
	return out
}

// generateAccounts creates deterministic addresses and RLP-encoded accounts.
func generateAccounts(size int) (addresses [][20]byte, blobs [][]byte) {
	r := rand.New(rand.NewSource(0))
	addresses = make([][20]byte, size)
	for i := 0; i < len(addresses); i++ {
		var a [20]byte
		r.Read(a[:])
		addresses[i] = a
	}
	blobs = make([][]byte, len(addresses))
	for i := 0; i < len(blobs); i++ {
		nonce := uint64(r.Int63())
		root := types.EmptyRootHash
		code := crypto.Keccak256(nil)
		// random balance up to 32 bytes
		numBytes := uint32(r.Int31n(33)) // 0..32
		balanceBytes := make([]byte, numBytes)
		r.Read(balanceBytes)
		balance := new(uint256.Int).SetBytes(balanceBytes)
		data, _ := rlp.EncodeToBytes(&types.StateAccount{
			Nonce:    nonce,
			Balance:  balance,
			Root:     root,
			CodeHash: code,
		})
		blobs[i] = data
	}
	return
}

func TestParallelStateTrie_AccountsMatchLegacy(t *testing.T) {
	// Create a memory trie DB
	tdb := triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil)

	// Create empty tries (legacy and parallel)
	legacy, err := trie.NewStateTrie(trie.StateTrieID(types.EmptyRootHash), tdb)
	if err != nil {
		t.Fatal(err)
	}
	parallel, err := trie.NewParallelSparseTrie(trie.StateTrieID(types.EmptyRootHash), tdb)
	if err != nil {
		t.Fatal(err)
	}
	// Generate a deterministic set of accounts
	addresses, accountBlobs := generateAccounts(300)
	accounts := decodeAccounts(accountBlobs)

	// Apply writes to both tries
	for i := 0; i < len(addresses); i++ {
		if err := legacy.UpdateAccount(common.BytesToAddress(addresses[i][:]), accounts[i], 0); err != nil {
			t.Fatal(err)
		}
		if err := parallel.UpdateAccount(common.BytesToAddress(addresses[i][:]), accounts[i], 0); err != nil {
			t.Fatal(err)
		}
	}
	// Hash and commit
	start := time.Now()
	legacy.Hash()
	lRoot, _ := legacy.Commit(false)
	legacyDur := time.Since(start)

	start = time.Now()
	parallel.Hash()
	pRoot, _ := parallel.Commit(false)
	parallelDur := time.Since(start)

	t.Run("timings/accounts", func(t *testing.T) {
		t.Skipf("accounts: legacy=%v parallel=%v", legacyDur, parallelDur)
	})
	if lRoot != pRoot {
		t.Fatalf("account trie root mismatch: legacy %x vs parallel %x", lRoot, pRoot)
	}
}

func TestParallelStateTrie_StorageAndAccountsMatch(t *testing.T) {
	// Create a memory trie DB
	tdb := triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil)

	// Create empty account tries (legacy and parallel)
	legacyAcc, err := trie.NewStateTrie(trie.StateTrieID(types.EmptyRootHash), tdb)
	if err != nil {
		t.Fatal(err)
	}
	parallelAcc, err := trie.NewParallelSparseTrie(trie.StateTrieID(types.EmptyRootHash), tdb)
	if err != nil {
		t.Fatal(err)
	}
	// Random account
	var addr [20]byte
	rand.New(rand.NewSource(1)).Read(addr[:])
	addrHash := crypto.Keccak256Hash(addr[:])

	// Create empty storage tries (legacy and parallel)
	legacyStor, err := trie.NewStateTrie(trie.StorageTrieID(types.EmptyRootHash, addrHash, types.EmptyRootHash), tdb)
	if err != nil {
		t.Fatal(err)
	}
	parallelStor, err := trie.NewParallelSparseTrie(trie.StorageTrieID(types.EmptyRootHash, addrHash, types.EmptyRootHash), tdb)
	if err != nil {
		t.Fatal(err)
	}
	// Apply a few storage slots
	for i := 0; i < 200; i++ {
		var slot common.Hash
		var val common.Hash
		rand.Read(slot[:])
		rand.Read(val[:])
		// write to both storage tries
		if err := legacyStor.UpdateStorage(common.BytesToAddress(addr[:]), slot[:], common.TrimLeftZeroes(val[:])); err != nil {
			t.Fatal(err)
		}
		if err := parallelStor.UpdateStorage(common.BytesToAddress(addr[:]), slot[:], common.TrimLeftZeroes(val[:])); err != nil {
			t.Fatal(err)
		}
		// occasionally delete a slot
		if i%17 == 0 {
			if err := legacyStor.DeleteStorage(common.BytesToAddress(addr[:]), slot[:]); err != nil {
				t.Fatal(err)
			}
			if err := parallelStor.DeleteStorage(common.BytesToAddress(addr[:]), slot[:]); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Hash and commit storage
	start := time.Now()
	legacyStor.Hash()
	lStorRoot, _ := legacyStor.Commit(false)
	legacyStorDur := time.Since(start)

	start = time.Now()
	parallelStor.Hash()
	pStorRoot, _ := parallelStor.Commit(false)
	parallelStorDur := time.Since(start)

	t.Run("timings/storage", func(t *testing.T) {
		t.Skipf("storage: legacy=%v parallel=%v", legacyStorDur, parallelStorDur)
	})
	if lStorRoot != pStorRoot {
		t.Fatalf("storage trie root mismatch: legacy %x vs parallel %x", lStorRoot, pStorRoot)
	}
	// Construct account objects embedding the storage root
	accLegacy := types.NewEmptyStateAccount()
	accLegacy.Root = lStorRoot
	accParallel := types.NewEmptyStateAccount()
	accParallel.Root = pStorRoot

	// Update both account tries with the new account state
	address := common.BytesToAddress(addr[:])
	if err := legacyAcc.UpdateAccount(address, accLegacy, 0); err != nil {
		t.Fatal(err)
	}
	if err := parallelAcc.UpdateAccount(address, accParallel, 0); err != nil {
		t.Fatal(err)
	}
	// Hash and commit account tries
	start = time.Now()
	legacyAcc.Hash()
	lAccRoot, _ := legacyAcc.Commit(false)
	legacyAccDur := time.Since(start)

	start = time.Now()
	parallelAcc.Hash()
	pAccRoot, _ := parallelAcc.Commit(false)
	parallelAccDur := time.Since(start)

	t.Run("timings/accounts_with_storage", func(t *testing.T) {
		t.Skipf("accounts(with storage): legacy=%v parallel=%v", legacyAccDur, parallelAccDur)
	})
	if lAccRoot != pAccRoot {
		t.Fatalf("account trie root mismatch after storage: legacy %x vs parallel %x", lAccRoot, pAccRoot)
	}
}
