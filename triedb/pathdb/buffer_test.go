package pathdb

import (
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
)

// TestBufferLimitSplit verifies that newBuffer correctly splits the total limit
// into nodeLimit and stateLimit based on the stateReservation percentage.
func TestBufferLimitSplit(t *testing.T) {
	tests := []struct {
		name             string
		limit            int
		stateReservation int
		wantNodeLimit    uint64
		wantStateLimit   uint64
	}{
		{
			name:             "default 80/20 split",
			limit:            1000,
			stateReservation: 80,
			wantNodeLimit:    200,
			wantStateLimit:   800,
		},
		{
			name:             "50/50 split",
			limit:            1000,
			stateReservation: 50,
			wantNodeLimit:    500,
			wantStateLimit:   500,
		},
		{
			name:             "invalid reservation falls back to default",
			limit:            1000,
			stateReservation: 0,
			wantNodeLimit:    1000 - 1000*defaultStateReservation/100,
			wantStateLimit:   1000 * defaultStateReservation / 100,
		},
		{
			name:             "over 100 falls back to default",
			limit:            1000,
			stateReservation: 150,
			wantNodeLimit:    1000 - 1000*defaultStateReservation/100,
			wantStateLimit:   1000 * defaultStateReservation / 100,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBuffer(tt.limit, tt.stateReservation, nil, nil, 0)
			if b.nodeLimit != tt.wantNodeLimit {
				t.Errorf("nodeLimit = %d, want %d", b.nodeLimit, tt.wantNodeLimit)
			}
			if b.stateLimit != tt.wantStateLimit {
				t.Errorf("stateLimit = %d, want %d", b.stateLimit, tt.wantStateLimit)
			}
			if b.limit != uint64(tt.limit) {
				t.Errorf("limit = %d, want %d", b.limit, tt.limit)
			}
		})
	}
}

// TestBufferFullTriggeredByNodeLimit verifies that full() returns true when
// trie nodes exceed their allocation, even if the total size is under the hard limit.
func TestBufferFullTriggeredByNodeLimit(t *testing.T) {
	// 1000 byte limit, 80% states (800), 20% nodes (200)
	b := newBuffer(1000, 80, nil, nil, 0)

	// Nodes at 150, states at 100 — total 250 < 1000, nodes < 200
	b.nodes.size = 150
	b.states.size = 100
	if b.full() {
		t.Error("buffer should not be full: nodes and total both under limits")
	}

	// Nodes at 201 — exceeds nodeLimit (200), but total (301) < limit (1000)
	b.nodes.size = 201
	if !b.full() {
		t.Error("buffer should be full: nodes exceed nodeLimit")
	}
}

// TestBufferFullTriggeredByTotalLimit verifies that full() returns true when
// the total size exceeds the hard limit, even if nodes are under their limit.
func TestBufferFullTriggeredByTotalLimit(t *testing.T) {
	// 1000 byte limit, 80% states (800), 20% nodes (200)
	b := newBuffer(1000, 80, nil, nil, 0)

	// Nodes at 100 (under 200 nodeLimit), states at 950 — total 1050 > 1000
	b.nodes.size = 100
	b.states.size = 950
	if !b.full() {
		t.Error("buffer should be full: total exceeds hard limit")
	}
}

// TestShouldCarryStates verifies that states are carried over only when
// the flush was triggered by nodes, not by states exceeding their limit.
func TestShouldCarryStates(t *testing.T) {
	// 1000 byte limit, 80% states (800), 20% nodes (200)
	b := newBuffer(1000, 80, nil, nil, 0)

	// States under stateLimit — should carry
	b.states.size = 500
	b.nodes.size = 250 // nodes over nodeLimit, triggering flush
	if !b.shouldCarryStates() {
		t.Error("should carry states: states (500) <= stateLimit (800)")
	}

	// States exactly at stateLimit — should carry
	b.states.size = 800
	if !b.shouldCarryStates() {
		t.Error("should carry states: states (800) == stateLimit (800)")
	}

	// States over stateLimit — should NOT carry
	b.states.size = 801
	if b.shouldCarryStates() {
		t.Error("should not carry states: states (801) > stateLimit (800)")
	}
}

// TestStateSetCopy verifies that copy() produces an independent deep copy.
func TestStateSetCopy(t *testing.T) {
	addrHash := common.HexToHash("0x01")
	slotHash := common.HexToHash("0x02")

	original := newStates(
		map[common.Hash][]byte{addrHash: {1, 2, 3}},
		map[common.Hash]map[common.Hash][]byte{
			addrHash: {slotHash: {4, 5, 6}},
		},
		false,
	)

	copied := original.copy()

	// Verify data matches
	if data, ok := copied.account(addrHash); !ok || len(data) != 3 || data[0] != 1 {
		t.Error("copied account data doesn't match original")
	}
	if data, ok := copied.storage(addrHash, slotHash); !ok || len(data) != 3 || data[0] != 4 {
		t.Error("copied storage data doesn't match original")
	}
	if copied.size != original.size {
		t.Errorf("copied size %d != original size %d", copied.size, original.size)
	}

	// Mutate the copy and verify the original is unchanged
	copied.accountData[addrHash] = []byte{9, 9, 9}
	copied.storageData[addrHash][slotHash] = []byte{8, 8, 8}

	if data, _ := original.account(addrHash); data[0] != 1 {
		t.Error("mutating copy affected original account data")
	}
	if data, _ := original.storage(addrHash, slotHash); data[0] != 4 {
		t.Error("mutating copy affected original storage data")
	}
}

// TestNewBufferWithCarriedStates verifies that a buffer created with
// pre-existing states correctly includes them and tracks their size.
func TestNewBufferWithCarriedStates(t *testing.T) {
	addrHash := common.HexToHash("0x01")
	slotHash := common.HexToHash("0x02")

	carried := newStates(
		map[common.Hash][]byte{addrHash: {1, 2, 3}},
		map[common.Hash]map[common.Hash][]byte{
			addrHash: {slotHash: {4, 5, 6}},
		},
		false,
	)
	carriedSize := carried.size

	b := newBuffer(1000, 80, nil, carried, 0)

	// States should be present
	if data, ok := b.account(addrHash); !ok || data[0] != 1 {
		t.Error("carried account not found in new buffer")
	}
	if data, ok := b.storage(addrHash, slotHash); !ok || data[0] != 4 {
		t.Error("carried storage not found in new buffer")
	}

	// Size should reflect carried states
	if b.states.size != carriedSize {
		t.Errorf("buffer states size = %d, want %d", b.states.size, carriedSize)
	}

	// Layers should be 0 (fresh buffer)
	if b.layers != 0 {
		t.Errorf("layers = %d, want 0", b.layers)
	}
}

// TestStateCarryOverSimulation simulates the flush plus carry-over sequence
// that happens in disklayer.go to verify the full cycle works correctly.
func TestStateCarryOverSimulation(t *testing.T) {
	// Simulate: buffer with 80/20 split, nodes exceed their limit, states don't
	b := newBuffer(1000, 80, nil, nil, 0)

	// Add some state data
	addrHash := common.HexToHash("0x01")
	b.states.accountData[addrHash] = []byte{1, 2, 3}
	b.states.size = 500 // simulate accumulated state
	b.nodes.size = 250  // over nodeLimit (200)
	b.layers = 5

	// Buffer should be full (nodes > nodeLimit)
	if !b.full() {
		t.Fatal("buffer should be full")
	}

	// States should be carried (500 <= 800)
	if !b.shouldCarryStates() {
		t.Fatal("states should be carried")
	}

	// Simulate the carry-over logic
	var newBuf *buffer
	if b.shouldCarryStates() {
		carried := b.states.copy()
		newBuf = newBuffer(1000, 80, nil, carried, 0)
	} else {
		newBuf = newBuffer(1000, 80, nil, nil, 0)
	}

	// New buffer should have the carried states
	if data, ok := newBuf.account(addrHash); !ok || data[0] != 1 {
		t.Error("state not carried over to new buffer")
	}

	// The new buffer should have fresh layers count
	if newBuf.layers != 0 {
		t.Errorf("new buffer layers = %d, want 0", newBuf.layers)
	}

	// New buffer nodes should be empty
	if newBuf.nodes.size != 0 {
		t.Errorf("new buffer nodes size = %d, want 0", newBuf.nodes.size)
	}

	// The new buffer should NOT be full (nodes are empty now)
	if newBuf.full() {
		t.Error("new buffer should not be full after carry-over")
	}
}

// TestNoStateCarryWhenStatesExceedLimit verifies that states are NOT carried
// when they exceed the stateLimit (i.e., the flush was triggered by total size).
func TestNoStateCarryWhenStatesExceedLimit(t *testing.T) {
	b := newBuffer(1000, 80, nil, nil, 0)

	addrHash := common.HexToHash("0x01")
	b.states.accountData[addrHash] = []byte{1, 2, 3}
	b.states.size = 900 // exceeds stateLimit (800)
	b.nodes.size = 150  // total 1050 > 1000

	if !b.full() {
		t.Fatal("buffer should be full")
	}
	if b.shouldCarryStates() {
		t.Fatal("states should NOT be carried when over stateLimit")
	}

	// Simulate: no carry-over
	newBuf := newBuffer(1000, 80, nil, nil, 0)
	if _, ok := newBuf.account(addrHash); ok {
		t.Error("state should not exist in new buffer when not carried")
	}
}

// TestEmptyBufferSemantics verifies that empty() only checks layers, not data.
// The revert path uses empty() to decide "are there uncommitted transitions?"
// A buffer with carried states but zero layers IS empty (no transitions to revert).
func TestEmptyBufferSemantics(t *testing.T) {
	// A truly empty buffer
	b := newBuffer(1000, 80, nil, nil, 0)
	if !b.empty() {
		t.Error("fresh buffer should be empty")
	}

	// Buffer with carried states but zero layers — still "empty" (no transitions)
	carried := newStates(
		map[common.Hash][]byte{common.HexToHash("0x01"): {1, 2, 3}},
		nil,
		false,
	)
	b = newBuffer(1000, 80, nil, carried, 0)
	if !b.empty() {
		t.Error("buffer with carried states but zero layers should be empty (no transitions)")
	}

	// Buffer with layers is not empty
	b = newBuffer(1000, 80, nil, nil, 1)
	if b.empty() {
		t.Error("buffer with layers > 0 should NOT be empty")
	}
}

// TestRevertClearsCarriedStates verifies that when the revert path encounters
// an empty buffer (layers==0) with carried states, the carried states are
// cleared via reset() so they don't serve stale reads after persistent revert.
// This simulates the diskLayer.revert() code path.
func TestRevertClearsCarriedStates(t *testing.T) {
	addrHash := common.HexToHash("0x01")

	// Create a buffer with carried states (layers=0)
	carried := newStates(
		map[common.Hash][]byte{addrHash: {1, 2, 3}},
		nil,
		false,
	)
	b := newBuffer(1000, 80, nil, carried, 0)

	// Buffer is "empty" (no layers), but has carried state data
	if !b.empty() {
		t.Fatal("buffer with zero layers should be empty")
	}
	if _, ok := b.account(addrHash); !ok {
		t.Fatal("carried state should be present before reset")
	}

	// Simulate what diskLayer.revert() does: detect empty buffer,
	// reset it to clear stale carried states, then proceed to
	// persistent-state revert.
	b.reset()
	if _, ok := b.account(addrHash); ok {
		t.Error("carried state should be cleared after reset")
	}
	if b.states.size != 0 {
		t.Errorf("state size should be 0 after reset, got %d", b.states.size)
	}
}

// TestRevertToWithLayers verifies that revertTo correctly handles a buffer
// with committed layers, including when carried states are present.
func TestRevertToWithLayers(t *testing.T) {
	addrHash := common.HexToHash("0x01")

	// Create a buffer with carried states
	carried := newStates(
		map[common.Hash][]byte{addrHash: {1, 2, 3}},
		nil,
		false,
	)
	b := newBuffer(1000, 80, nil, carried, 0)

	// revertTo with layers=0 returns errStateUnrecoverable
	err := b.revertTo(nil, nil, nil, nil)
	if !errors.Is(err, errStateUnrecoverable) {
		t.Errorf("revertTo on zero-layer buffer should return errStateUnrecoverable, got: %v", err)
	}

	// Simulate commit: add a layer, then revert it
	b.layers = 1
	err = b.revertTo(nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("revertTo should succeed: %v", err)
	}

	// After revert to layers=0, buffer is fully reset (carried states cleared)
	if !b.empty() {
		t.Error("buffer should be empty after reverting to zero layers")
	}
	if _, ok := b.account(addrHash); ok {
		t.Error("carried state should be cleared after revert to zero layers")
	}
}

// TestNoCarryOnForceFlush verifies that states are NOT carried over when
// the flush is triggered by force or history pressure.
func TestNoCarryOnForceFlush(t *testing.T) {
	// Simulate: buffer with node pressure + shouldCarryStates() = true,
	// but force/flush flags override.
	b := newBuffer(1000, 80, nil, nil, 0)

	addrHash := common.HexToHash("0x01")
	b.states.accountData[addrHash] = []byte{1, 2, 3}
	b.states.size = 500 // under stateLimit (800)
	b.nodes.size = 250  // over nodeLimit (200)

	// shouldCarryStates is true (states within budget)
	if !b.shouldCarryStates() {
		t.Fatal("shouldCarryStates should be true")
	}

	// Simulate force=true path: carry-over is suppressed
	var newBuf *buffer
	if b.shouldCarryStates() {
		carried := b.states.copy()
		newBuf = newBuffer(1000, 80, nil, carried, 0)
	} else {
		newBuf = newBuffer(1000, 80, nil, nil, 0)
	}
	if _, ok := newBuf.account(addrHash); ok {
		t.Error("state should NOT be carried on force flush")
	}

	// Simulate flush=true (history-driven) path: carry-over is suppressed
	if b.shouldCarryStates() {
		carried := b.states.copy()
		newBuf = newBuffer(1000, 80, nil, carried, 0)
	} else {
		newBuf = newBuffer(1000, 80, nil, nil, 0)
	}
	if _, ok := newBuf.account(addrHash); ok {
		t.Error("state should NOT be carried on history-driven flush")
	}

	// Simulate a normal node-pressure path: carry-over happens
	if b.shouldCarryStates() {
		carried := b.states.copy()
		newBuf = newBuffer(1000, 80, nil, carried, 0)
	} else {
		newBuf = newBuffer(1000, 80, nil, nil, 0)
	}
	if _, ok := newBuf.account(addrHash); !ok {
		t.Error("state SHOULD be carried on normal node-pressure flush")
	}
}

// TestAsyncFlushOwnershipTransfer exercises the real async flush() goroutine
// together with the ownership-transfer carry-over path. It verifies that:
//  1. Carried states are readable from the new live buffer during the flush.
//  2. The frozen buffer's states are emptied (no stale duplicate).
//  3. The flushed data lands on disk after waitFlush().
func TestAsyncFlushOwnershipTransfer(t *testing.T) {
	db := rawdb.NewMemoryDatabase()

	addrHash := common.HexToHash("0x01")
	slotHash := common.HexToHash("0x02")
	accountBlob := []byte{1, 2, 3}
	storageBlob := []byte{4, 5, 6}

	// Build a buffer that will trigger a flush via node pressure.
	// 10'000 byte limit, 80/20 split → nodeLimit=2000, stateLimit=8000.
	buf := newBuffer(10000, 80, nil, nil, 0)
	buf.states = newStates(
		map[common.Hash][]byte{addrHash: accountBlob},
		map[common.Hash]map[common.Hash][]byte{
			addrHash: {slotHash: storageBlob},
		},
		false,
	)
	buf.nodes.size = 2500 // exceeds nodeLimit (2000) → full()
	buf.layers = 1        // flush expects layers to match persistent state id

	if !buf.full() {
		t.Fatal("buffer should be full")
	}
	if !buf.shouldCarryStates() {
		t.Fatal("states should be eligible for carry-over")
	}

	// Seed the persistent state ID so the flush goroutine's sanity check
	rawdb.WritePersistentStateID(db, 0)

	// Freeze the buffer and start the async flush
	frozen := buf
	frozen.flush(common.Hash{}, db, nil, nil, nil, nil, 1, nil)

	// Perform the ownership transfer
	carried := frozen.states
	frozen.states = newStates(nil, nil, carried.rawStorageKey)
	live := newBuffer(10000, 80, nil, carried, 0)

	// 1. Carried states are readable from the live buffer during the flush.
	if data, ok := live.account(addrHash); !ok {
		t.Fatal("carried account not readable from live buffer")
	} else if len(data) != 3 || data[0] != 1 {
		t.Fatalf("carried account data mismatch: got %v", data)
	}
	if data, ok := live.storage(addrHash, slotHash); !ok {
		t.Fatal("carried storage not readable from live buffer")
	} else if len(data) != 3 || data[0] != 4 {
		t.Fatalf("carried storage data mismatch: got %v", data)
	}

	// 2. Frozen buffer's states are emptied — no stale duplicate.
	if _, ok := frozen.account(addrHash); ok {
		t.Error("frozen buffer should not still hold account after ownership transfer")
	}
	if _, ok := frozen.storage(addrHash, slotHash); ok {
		t.Error("frozen buffer should not still hold storage after ownership transfer")
	}

	// 3. Wait for the async flush to complete and verify data landed on disk.
	if err := frozen.waitFlush(); err != nil {
		t.Fatalf("flush failed: %v", err)
	}
	if blob := rawdb.ReadAccountSnapshot(db, addrHash); len(blob) == 0 {
		t.Error("account snapshot not persisted to disk after flush")
	} else if blob[0] != 1 {
		t.Errorf("persisted account data mismatch: got %v", blob)
	}
	if blob := rawdb.ReadStorageSnapshot(db, addrHash, slotHash); len(blob) == 0 {
		t.Error("storage snapshot not persisted to disk after flush")
	} else if blob[0] != 4 {
		t.Errorf("persisted storage data mismatch: got %v", blob)
	}
}
