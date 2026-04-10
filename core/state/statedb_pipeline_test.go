package state

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/triedb"
)

func TestWasStorageSlotRead(t *testing.T) {
	db := NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), nil)
	sdb, _ := New(types.EmptyRootHash, db)

	addr := common.HexToAddress("0x1234")
	slot := common.HexToHash("0xabcd")

	// Slot not read yet
	if sdb.WasStorageSlotRead(addr, slot) {
		t.Error("slot should not be marked as read before any access")
	}

	// Create an account and read its storage
	sdb.CreateAccount(addr)
	sdb.SetNonce(addr, 1, 0)
	sdb.Finalise(false)

	// Read the slot
	sdb.GetState(addr, slot)

	// Now it should be marked as read
	if !sdb.WasStorageSlotRead(addr, slot) {
		t.Error("slot should be marked as read after GetState")
	}

	// A different slot should not be marked
	otherSlot := common.HexToHash("0x5678")
	if sdb.WasStorageSlotRead(addr, otherSlot) {
		t.Error("other slot should not be marked as read")
	}

	// A different address should not be marked
	otherAddr := common.HexToAddress("0x5678")
	if sdb.WasStorageSlotRead(otherAddr, slot) {
		t.Error("other address should not be marked as read")
	}
}

func TestFlatDiffOverlay_ReadThrough(t *testing.T) {
	// Create a base state with an account
	db := NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), nil)
	sdb, _ := New(types.EmptyRootHash, db)

	baseAddr := common.HexToAddress("0xbase")
	sdb.CreateAccount(baseAddr)
	sdb.SetNonce(baseAddr, 1, 0)
	sdb.SetBalance(baseAddr, uint256.NewInt(100), 0)
	root, _, _ := sdb.CommitWithUpdate(0, false, false)

	// Create a FlatDiff with a new account
	overlayAddr := common.HexToAddress("0xoverlay")
	diff := &FlatDiff{
		Accounts: map[common.Address]types.StateAccount{
			overlayAddr: {
				Nonce:    42,
				Balance:  uint256.NewInt(200),
				Root:     types.EmptyRootHash,
				CodeHash: types.EmptyCodeHash.Bytes(),
			},
		},
		Storage:          make(map[common.Address]map[common.Hash]common.Hash),
		Destructs:        make(map[common.Address]struct{}),
		Code:             make(map[common.Hash][]byte),
		ReadStorage:      make(map[common.Address][]common.Hash),
		NonExistentReads: nil,
	}

	// Create StateDB with FlatDiff overlay
	overlayDB, err := NewWithFlatBase(root, db, diff)
	if err != nil {
		t.Fatal(err)
	}

	// Should see the overlay account
	if overlayDB.GetNonce(overlayAddr) != 42 {
		t.Errorf("expected nonce 42 for overlay addr, got %d", overlayDB.GetNonce(overlayAddr))
	}

	// Should still see the base account
	if overlayDB.GetNonce(baseAddr) != 1 {
		t.Errorf("expected nonce 1 for base addr, got %d", overlayDB.GetNonce(baseAddr))
	}
}

func TestCommitSnapshot_CapturesWrites(t *testing.T) {
	db := NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), nil)
	sdb, _ := New(types.EmptyRootHash, db)

	addr := common.HexToAddress("0x1234")
	sdb.CreateAccount(addr)
	sdb.SetNonce(addr, 10, 0)
	sdb.SetBalance(addr, uint256.NewInt(500), 0)

	slot := common.HexToHash("0xaaaa")
	sdb.SetState(addr, slot, common.HexToHash("0xbbbb"))

	diff := sdb.CommitSnapshot(false)

	// Verify account is captured
	acct, ok := diff.Accounts[addr]
	if !ok {
		t.Fatal("account not captured in FlatDiff")
	}
	if acct.Nonce != 10 {
		t.Errorf("expected nonce 10, got %d", acct.Nonce)
	}

	// Verify storage is captured
	slots, ok := diff.Storage[addr]
	if !ok {
		t.Fatal("storage not captured in FlatDiff")
	}
	if slots[slot] != common.HexToHash("0xbbbb") {
		t.Errorf("expected slot value 0xbbbb, got %x", slots[slot])
	}
}

func TestFlatDiffOverlay_DestructedAccountReturnsNil(t *testing.T) {
	db := NewDatabaseForTesting()
	sdb, err := New(types.EmptyRootHash, db)
	require.NoError(t, err)

	addr := common.HexToAddress("0xdead01")
	sdb.CreateAccount(addr)
	sdb.SetBalance(addr, uint256.NewInt(999), 0)
	root, _, err := sdb.CommitWithUpdate(0, false, false)
	require.NoError(t, err)

	// FlatDiff marks account as destructed but does NOT add it to Accounts.
	diff := &FlatDiff{
		Accounts:  make(map[common.Address]types.StateAccount),
		Storage:   make(map[common.Address]map[common.Hash]common.Hash),
		Destructs: map[common.Address]struct{}{addr: {}},
		Code:      make(map[common.Hash][]byte),
	}

	overlayDB, err := NewWithFlatBase(root, db, diff)
	require.NoError(t, err)

	require.False(t, overlayDB.Exist(addr), "destructed account should not exist")
	require.True(t, overlayDB.GetBalance(addr).IsZero(), "destructed account balance should be zero")
}

func TestFlatDiffOverlay_DestructAndResurrect(t *testing.T) {
	db := NewDatabaseForTesting()
	sdb, err := New(types.EmptyRootHash, db)
	require.NoError(t, err)

	addr := common.HexToAddress("0xdead02")
	sdb.CreateAccount(addr)
	sdb.SetNonce(addr, 5, 0)
	root, _, err := sdb.CommitWithUpdate(0, false, false)
	require.NoError(t, err)

	// FlatDiff has addr in BOTH Destructs and Accounts (destruct + resurrect with new nonce).
	diff := &FlatDiff{
		Accounts: map[common.Address]types.StateAccount{
			addr: {
				Nonce:    10,
				Balance:  uint256.NewInt(0),
				Root:     types.EmptyRootHash,
				CodeHash: types.EmptyCodeHash.Bytes(),
			},
		},
		Storage:   make(map[common.Address]map[common.Hash]common.Hash),
		Destructs: map[common.Address]struct{}{addr: {}},
		Code:      make(map[common.Hash][]byte),
	}

	overlayDB, err := NewWithFlatBase(root, db, diff)
	require.NoError(t, err)

	// The account should be resurrected with the new nonce from FlatDiff.Accounts.
	require.Equal(t, uint64(10), overlayDB.GetNonce(addr))
}

func TestTrieOnlyReader_SkipsFlatReaders(t *testing.T) {
	db := NewDatabaseForTesting()
	sdb, err := New(types.EmptyRootHash, db)
	require.NoError(t, err)

	addr := common.HexToAddress("0xacc001")
	sdb.CreateAccount(addr)
	sdb.SetBalance(addr, uint256.NewInt(42), 0)
	root, _, err := sdb.CommitWithUpdate(0, false, false)
	require.NoError(t, err)

	// Create StateDB via NewTrieOnly — reads go through trie, not flat/snapshot.
	trieDB, err := NewTrieOnly(root, db)
	require.NoError(t, err)

	// Verify trie reader returns correct data.
	require.Equal(t, uint256.NewInt(42), trieDB.GetBalance(addr))

	// Attach a witness and modify the account via a fresh trie-only StateDB.
	// After IntermediateRoot, the witness should capture trie nodes (non-empty
	// State map). With flat readers the trie is never walked, so the witness
	// would remain empty.
	trieDB2, err := NewTrieOnly(root, db)
	require.NoError(t, err)

	witness := &stateless.Witness{
		Headers: []*types.Header{{}},
		Codes:   make(map[string]struct{}),
		State:   make(map[string]struct{}),
	}
	trieDB2.SetWitness(witness)

	// Modify the account so that IntermediateRoot walks the trie and collects
	// witness nodes from the account trie.
	trieDB2.SetBalance(addr, uint256.NewInt(99), 0)
	trieDB2.IntermediateRoot(false)

	require.NotEmpty(t, witness.State, "witness should capture trie nodes when using trie-only reader")
}

func TestNewTrieOnly_ReadsCorrectData(t *testing.T) {
	db := NewDatabaseForTesting()
	sdb, err := New(types.EmptyRootHash, db)
	require.NoError(t, err)

	addr1 := common.HexToAddress("0xacc101")
	addr2 := common.HexToAddress("0xacc102")
	addr3 := common.HexToAddress("0xacc103")

	sdb.CreateAccount(addr1)
	sdb.SetBalance(addr1, uint256.NewInt(100), 0)
	sdb.SetNonce(addr1, 1, 0)

	sdb.CreateAccount(addr2)
	sdb.SetBalance(addr2, uint256.NewInt(200), 0)
	sdb.SetNonce(addr2, 5, 0)
	sdb.SetCode(addr2, []byte{0x60, 0x00, 0x60, 0x00}, 0)

	sdb.CreateAccount(addr3)
	sdb.SetBalance(addr3, uint256.NewInt(300), 0)
	slot := common.HexToHash("0xaa01")
	sdb.SetState(addr3, slot, common.HexToHash("0xbb01"))

	root, _, err := sdb.CommitWithUpdate(0, false, false)
	require.NoError(t, err)

	// Create via NewTrieOnly and verify all data.
	trieDB, err := NewTrieOnly(root, db)
	require.NoError(t, err)

	require.Equal(t, uint256.NewInt(100), trieDB.GetBalance(addr1))
	require.Equal(t, uint64(1), trieDB.GetNonce(addr1))

	require.Equal(t, uint256.NewInt(200), trieDB.GetBalance(addr2))
	require.Equal(t, uint64(5), trieDB.GetNonce(addr2))
	require.Equal(t, crypto.Keccak256Hash([]byte{0x60, 0x00, 0x60, 0x00}), trieDB.GetCodeHash(addr2))
	require.Equal(t, []byte{0x60, 0x00, 0x60, 0x00}, trieDB.GetCode(addr2))

	require.Equal(t, uint256.NewInt(300), trieDB.GetBalance(addr3))
	require.Equal(t, common.HexToHash("0xbb01"), trieDB.GetState(addr3, slot))
}

func TestPropagateReadsTo_AccountsAndStorage(t *testing.T) {
	db := NewDatabaseForTesting()
	sdb, err := New(types.EmptyRootHash, db)
	require.NoError(t, err)

	addr1 := common.HexToAddress("0xaa0001")
	addr2 := common.HexToAddress("0xaa0002")
	slot1 := common.HexToHash("0xcc0001")
	slot2 := common.HexToHash("0xcc0002")

	sdb.CreateAccount(addr1)
	sdb.SetBalance(addr1, uint256.NewInt(111), 0)
	sdb.SetState(addr1, slot1, common.HexToHash("0xdd0001"))
	sdb.SetState(addr1, slot2, common.HexToHash("0xdd0002"))

	sdb.CreateAccount(addr2)
	sdb.SetBalance(addr2, uint256.NewInt(222), 0)

	root, _, err := sdb.CommitWithUpdate(0, false, false)
	require.NoError(t, err)

	// Create src and dst StateDBs at the same root.
	src, err := New(root, db)
	require.NoError(t, err)
	dst, err := New(root, db)
	require.NoError(t, err)

	// Read accounts and storage on src.
	src.GetBalance(addr1)
	src.GetBalance(addr2)
	src.GetState(addr1, slot1)
	src.GetState(addr1, slot2)

	// Propagate reads from src to dst.
	src.PropagateReadsTo(dst)

	// dst should now have the accounts and storage in its stateObjects
	// (populated by PropagateReadsTo calling GetBalance/GetState on dst).
	require.Equal(t, uint256.NewInt(111), dst.GetBalance(addr1))
	require.Equal(t, uint256.NewInt(222), dst.GetBalance(addr2))
	require.Equal(t, common.HexToHash("0xdd0001"), dst.GetState(addr1, slot1))
	require.Equal(t, common.HexToHash("0xdd0002"), dst.GetState(addr1, slot2))
}

func TestCommitSnapshot_CapturesDestructs(t *testing.T) {
	db := NewDatabaseForTesting()
	sdb, err := New(types.EmptyRootHash, db)
	require.NoError(t, err)

	addr := common.HexToAddress("0xdestruct01")
	sdb.CreateAccount(addr)
	sdb.SetBalance(addr, uint256.NewInt(500), 0)
	root, _, err := sdb.CommitWithUpdate(0, false, false)
	require.NoError(t, err)

	// Create a new StateDB at the committed root and self-destruct the account.
	sdb2, err := New(root, db)
	require.NoError(t, err)

	sdb2.SelfDestruct(addr)
	diff := sdb2.CommitSnapshot(false)

	_, destructed := diff.Destructs[addr]
	require.True(t, destructed, "self-destructed account should appear in diff.Destructs")
}

// TestPrefetchRoot_FlatDiffAccountUsesCommittedRoot verifies that accounts
// loaded from FlatDiff get their prefetchRoot set to the committed parent's
// storage root, not the FlatDiff's storage root. This is critical for
// pipelined SRC: the prefetcher's NodeReader is opened at the committed
// parent root (grandparent), so it can only resolve trie nodes for that
// state's storage root. Using FlatDiff's root (block N's post-state) would
// cause "Unexpected trie node" hash mismatches.
func TestPrefetchRoot_FlatDiffAccountUsesCommittedRoot(t *testing.T) {
	db := NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), nil)

	// --- Set up a committed state with a contract that has storage ---
	sdb, err := New(types.EmptyRootHash, db)
	require.NoError(t, err)

	addr := common.HexToAddress("0xcontract")
	sdb.CreateAccount(addr)
	sdb.SetNonce(addr, 1, 0)
	sdb.SetState(addr, common.HexToHash("0x01"), common.HexToHash("0xaa"))
	sdb.Finalise(false)

	committedRoot, _, err := sdb.CommitWithUpdate(0, false, false)
	require.NoError(t, err)

	// Read back the committed account to get its storage root.
	committedSDB, err := New(committedRoot, db)
	require.NoError(t, err)
	committedObj := committedSDB.getStateObject(addr)
	require.NotNil(t, committedObj)
	committedStorageRoot := committedObj.data.Root
	require.NotEqual(t, types.EmptyRootHash, committedStorageRoot, "committed account should have non-empty storage root")

	// --- Simulate block N: modify the contract's storage and extract FlatDiff ---
	sdb2, err := New(committedRoot, db)
	require.NoError(t, err)
	sdb2.SetState(addr, common.HexToHash("0x02"), common.HexToHash("0xbb")) // new slot
	sdb2.Finalise(false)
	diff := sdb2.CommitSnapshot(false)

	// The FlatDiff account has block N's storage root (different from committed).
	flatDiffAcct, ok := diff.Accounts[addr]
	require.True(t, ok, "contract should be in FlatDiff")
	flatDiffStorageRoot := flatDiffAcct.Root
	// The FlatDiff root is the account's root BEFORE IntermediateRoot (i.e.,
	// CommitSnapshot doesn't hash — it captures the current data.Root). So it
	// equals the committed root here. But the key point is that getPrefetchRoot
	// returns the committed root regardless.

	// --- Create a pipelined StateDB with FlatDiff overlay ---
	overlayDB, err := NewWithFlatBase(committedRoot, db, diff)
	require.NoError(t, err)

	// Load the account from FlatDiff
	obj := overlayDB.getStateObject(addr)
	require.NotNil(t, obj)

	// Verify origin/data roots come from FlatDiff
	require.Equal(t, flatDiffStorageRoot, obj.data.Root, "data.Root should be from FlatDiff")

	// Verify prefetchRoot was set to the committed storage root
	require.Equal(t, committedStorageRoot, obj.prefetchRoot, "prefetchRoot should be the committed parent's storage root")

	// Verify getPrefetchRoot returns the committed root (not data.Root)
	require.Equal(t, committedStorageRoot, obj.getPrefetchRoot(), "getPrefetchRoot should return the committed storage root")
}

// TestPrefetchRoot_NormalAccountFallsBackToDataRoot verifies that accounts
// loaded from the committed state (not FlatDiff) have prefetchRoot=zero,
// and getPrefetchRoot falls back to data.Root.
func TestPrefetchRoot_NormalAccountFallsBackToDataRoot(t *testing.T) {
	db := NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), nil)

	sdb, err := New(types.EmptyRootHash, db)
	require.NoError(t, err)

	addr := common.HexToAddress("0xnormal")
	sdb.CreateAccount(addr)
	sdb.SetNonce(addr, 1, 0)
	sdb.SetState(addr, common.HexToHash("0x01"), common.HexToHash("0xaa"))
	sdb.Finalise(false)

	root, _, err := sdb.CommitWithUpdate(0, false, false)
	require.NoError(t, err)

	// Load the account normally (no FlatDiff)
	sdb2, err := New(root, db)
	require.NoError(t, err)

	obj := sdb2.getStateObject(addr)
	require.NotNil(t, obj)

	// prefetchRoot should be zero (not set for non-FlatDiff accounts)
	require.Equal(t, common.Hash{}, obj.prefetchRoot, "prefetchRoot should be zero for non-FlatDiff accounts")

	// getPrefetchRoot should fall back to data.Root
	require.Equal(t, obj.data.Root, obj.getPrefetchRoot(), "getPrefetchRoot should fall back to data.Root")
}

// TestPrefetchRoot_NewAccountInFlatDiff verifies that an account created in
// block N (exists in FlatDiff but not in committed state) gets prefetchRoot=zero
// since there's nothing to prefetch at the committed parent root.
func TestPrefetchRoot_NewAccountInFlatDiff(t *testing.T) {
	db := NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), nil)

	// Commit an empty state
	sdb, err := New(types.EmptyRootHash, db)
	require.NoError(t, err)
	committedRoot, _, err := sdb.CommitWithUpdate(0, false, false)
	require.NoError(t, err)

	// FlatDiff with a new account that doesn't exist in committed state
	newAddr := common.HexToAddress("0xnew")
	diff := &FlatDiff{
		Accounts: map[common.Address]types.StateAccount{
			newAddr: {
				Nonce:    1,
				Balance:  uint256.NewInt(100),
				Root:     crypto.Keccak256Hash([]byte("fake-storage-root")), // non-empty root
				CodeHash: types.EmptyCodeHash.Bytes(),
			},
		},
		Storage:          make(map[common.Address]map[common.Hash]common.Hash),
		Destructs:        make(map[common.Address]struct{}),
		Code:             make(map[common.Hash][]byte),
		ReadStorage:      make(map[common.Address][]common.Hash),
		NonExistentReads: nil,
	}

	overlayDB, err := NewWithFlatBase(committedRoot, db, diff)
	require.NoError(t, err)

	obj := overlayDB.getStateObject(newAddr)
	require.NotNil(t, obj)

	// Account is new (not in committed state), so prefetchRoot should be zero
	require.Equal(t, common.Hash{}, obj.prefetchRoot, "prefetchRoot should be zero for new accounts not in committed state")

	// getPrefetchRoot falls back to data.Root
	require.Equal(t, obj.data.Root, obj.getPrefetchRoot(), "getPrefetchRoot should fall back to data.Root for new accounts")
}

// TestPrefetchRoot_DeepCopyPreserves verifies that stateObject.deepCopy
// preserves the prefetchRoot field, which is important for StateDB.Copy()
// used by the block-level prefetcher.
func TestPrefetchRoot_DeepCopyPreserves(t *testing.T) {
	db := NewDatabase(triedb.NewDatabase(rawdb.NewMemoryDatabase(), nil), nil)

	sdb, err := New(types.EmptyRootHash, db)
	require.NoError(t, err)

	addr := common.HexToAddress("0xcopy")
	sdb.CreateAccount(addr)
	sdb.SetNonce(addr, 1, 0)
	sdb.SetState(addr, common.HexToHash("0x01"), common.HexToHash("0xaa"))
	sdb.Finalise(false)

	committedRoot, _, err := sdb.CommitWithUpdate(0, false, false)
	require.NoError(t, err)

	// Simulate a FlatDiff account with a different storage root
	sdb2, err := New(committedRoot, db)
	require.NoError(t, err)
	sdb2.SetState(addr, common.HexToHash("0x02"), common.HexToHash("0xbb"))
	sdb2.Finalise(false)
	diff := sdb2.CommitSnapshot(false)

	// Create overlay StateDB and load account
	overlayDB, err := NewWithFlatBase(committedRoot, db, diff)
	require.NoError(t, err)
	obj := overlayDB.getStateObject(addr)
	require.NotNil(t, obj)
	require.NotEqual(t, common.Hash{}, obj.prefetchRoot)

	// Copy the StateDB and verify prefetchRoot is preserved
	copiedDB := overlayDB.Copy()
	copiedObj := copiedDB.getStateObject(addr)
	require.NotNil(t, copiedObj)
	require.Equal(t, obj.prefetchRoot, copiedObj.prefetchRoot, "deepCopy should preserve prefetchRoot")
	require.Equal(t, obj.getPrefetchRoot(), copiedObj.getPrefetchRoot(), "getPrefetchRoot should match after deepCopy")
}
