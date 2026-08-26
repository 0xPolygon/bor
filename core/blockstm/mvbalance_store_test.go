package blockstm

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
)

func u(n uint64) *uint256.Int { return uint256.NewInt(n) }

func mvBalAddr(b byte) common.Address { return common.Address{b} }

// TestMVBalanceStore_WriteReadDelta covers basic accumulation: two writes
// by different txs accumulate; a reader at a later txIdx sees the sum of
// all prior deltas.
func TestMVBalanceStore_WriteReadDelta(t *testing.T) {
	s := NewMVBalanceStore()
	addr := mvBalAddr(1)

	s.WriteDelta(addr, 1, u(100), u(10))
	s.WriteDelta(addr, 2, u(50), u(0))

	add, sub := s.ReadDelta(addr, 5)
	if add.Uint64() != 150 || sub.Uint64() != 10 {
		t.Fatalf("ReadDelta at 5: got (add=%d, sub=%d), want (150, 10)", add.Uint64(), sub.Uint64())
	}

	// Reader at txIdx==writer doesn't include the writer's delta.
	add, sub = s.ReadDelta(addr, 1)
	if add.Uint64() != 0 || sub.Uint64() != 0 {
		t.Fatalf("ReadDelta at 1: got (add=%d, sub=%d), want (0, 0)", add.Uint64(), sub.Uint64())
	}
	// Reader at 2 sees only tx 1's delta.
	add, sub = s.ReadDelta(addr, 2)
	if add.Uint64() != 100 || sub.Uint64() != 10 {
		t.Fatalf("ReadDelta at 2: got (%d, %d), want (100, 10)", add.Uint64(), sub.Uint64())
	}
}

// TestMVBalanceStore_WriteDeltaAccumulatesSameTx verifies that multiple
// WriteDelta calls for the same (addr, txIdx) merge additively.
func TestMVBalanceStore_WriteDeltaAccumulatesSameTx(t *testing.T) {
	s := NewMVBalanceStore()
	addr := mvBalAddr(1)

	s.WriteDelta(addr, 2, u(10), u(0))
	s.WriteDelta(addr, 2, u(5), u(3))

	add, sub, found := s.GetTxDelta(addr, 2)
	if !found || add.Uint64() != 15 || sub.Uint64() != 3 {
		t.Fatalf("GetTxDelta: got (add=%d, sub=%d, found=%v), want (15, 3, true)", add.Uint64(), sub.Uint64(), found)
	}
}

// TestMVBalanceStore_GetTxDeltaMissing returns found=false for a missing
// (addr, txIdx).
func TestMVBalanceStore_GetTxDeltaMissing(t *testing.T) {
	s := NewMVBalanceStore()
	addr := mvBalAddr(1)
	s.WriteDelta(addr, 5, u(1), u(0))

	if _, _, found := s.GetTxDelta(addr, 2); found {
		t.Fatalf("GetTxDelta(missing): found=true, want false")
	}
}

// TestMVBalanceStore_ZeroDelta preserves the entry but zeros its amounts —
// used when MarkEstimate runs before re-execution.
func TestMVBalanceStore_ZeroDelta(t *testing.T) {
	s := NewMVBalanceStore()
	addr := mvBalAddr(1)
	s.WriteDelta(addr, 2, u(10), u(3))

	s.ZeroDelta(2, []common.Address{addr})

	add, sub, found := s.GetTxDelta(addr, 2)
	if !found || add.Uint64() != 0 || sub.Uint64() != 0 {
		t.Fatalf("after ZeroDelta: got (%d, %d, %v), want (0, 0, true)", add.Uint64(), sub.Uint64(), found)
	}
}

// TestMVBalanceStore_DeleteSingle removes just one (addr, txIdx) entry.
func TestMVBalanceStore_DeleteSingle(t *testing.T) {
	s := NewMVBalanceStore()
	addr := mvBalAddr(1)
	s.WriteDelta(addr, 1, u(1), u(0))
	s.WriteDelta(addr, 3, u(2), u(0))

	s.DeleteSingle(addr, 1)

	add, _ := s.ReadDelta(addr, 10)
	if add.Uint64() != 2 {
		t.Fatalf("ReadDelta after DeleteSingle: got %d, want 2", add.Uint64())
	}
	// Deleting missing entry is a no-op.
	s.DeleteSingle(addr, 99)
}

// TestMVBalanceStore_WriteDelta_OutOfOrderInsertion pins the slice-
// insertion `copy(entries[pos+1:], entries[pos:])` shift that surfaces
// when a smaller-indexed write lands after a larger one (forces middle
// insertion instead of append). Without the shift, the older write
// overwrites the newer one and ReadDelta returns a stale sum.
func TestMVBalanceStore_WriteDelta_OutOfOrderInsertion(t *testing.T) {
	s := NewMVBalanceStore()
	addr := mvBalAddr(1)
	// Larger txIdx first, then smaller — forces middle insertion.
	s.WriteDelta(addr, 10, u(100), nil)
	s.WriteDelta(addr, 3, u(7), nil)

	// ReadDelta(addr, 11) sums entries with TxIdx < 11.
	add, _ := s.ReadDelta(addr, 11)
	if add.Uint64() != 107 {
		t.Fatalf("ReadDelta after out-of-order insertion: got %d, want 107 (3→7 + 10→100)", add.Uint64())
	}

	// And the per-tx entries stay separately addressable.
	a3, _, found3 := s.GetTxDelta(addr, 3)
	if !found3 || a3.Uint64() != 7 {
		t.Fatalf("GetTxDelta(3): got (%d, %v), want (7, true)", a3.Uint64(), found3)
	}
	a10, _, found10 := s.GetTxDelta(addr, 10)
	if !found10 || a10.Uint64() != 100 {
		t.Fatalf("GetTxDelta(10): got (%d, %v), want (100, true)", a10.Uint64(), found10)
	}
}

// TestMVBalanceStore_HotKeysDistributeAcrossShards pins the fix for the
// shard-hash collapse: Polygon's three hottest system contracts (validator
// set, state receiver, MATIC ERC20) used to all route to shard 0 because
// `addr[0]<<8 | addr[1] % 64` reduces to `addr[1] % 64` and the leading
// 18 bytes of those addresses are all zero.
func TestMVBalanceStore_HotKeysDistributeAcrossShards(t *testing.T) {
	s := NewMVBalanceStore()
	hotKeys := []common.Address{
		common.HexToAddress("0x0000000000000000000000000000000000001000"), // Validator Set
		common.HexToAddress("0x0000000000000000000000000000000000001001"), // State Receiver
		common.HexToAddress("0x0000000000000000000000000000000000001010"), // MATIC
	}
	seen := make(map[*mvBalanceShard]struct{}, len(hotKeys))
	for _, addr := range hotKeys {
		seen[s.shard(addr)] = struct{}{}
	}
	if len(seen) < len(hotKeys) {
		t.Fatalf("Polygon hot system contracts collapsed onto %d shards (want %d distinct)", len(seen), len(hotKeys))
	}
}
