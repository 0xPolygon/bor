package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
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
