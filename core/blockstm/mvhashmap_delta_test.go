package blockstm

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// TestFlushMVWriteSetWritesBalance verifies that FlushMVWriteSet writes
// balance keys normally (via Write/FlagDone), not skipping them. After the
// V1 delta-balance revert (commit e27474841), balance reads go through the
// standard MVRead path and balance writes must be in the MVHashMap.
func TestFlushMVWriteSetWritesBalance(t *testing.T) {
	mv := MakeMVHashMap()
	addr := common.HexToAddress("0x2222")
	balKey := NewSubpathKey(addr, SubpathBalance)
	nonceKey := NewSubpathKey(addr, 2)
	stateKey := NewStateKey(addr, common.HexToHash("0xAA"))

	writes := []WriteDescriptor{
		{Path: balKey, V: Version{0, 0}, Val: "balance_data"},
		{Path: nonceKey, V: Version{0, 0}, Val: "nonce_data"},
		{Path: stateKey, V: Version{0, 0}, Val: "state_data"},
	}

	mv.FlushMVWriteSet(writes)

	// All three keys should be written via FlushMVWriteSet.
	for _, tc := range []struct {
		name string
		key  Key
	}{
		{"balance", balKey},
		{"nonce", nonceKey},
		{"state", stateKey},
	} {
		readRes := mv.Read(tc.key, 1)
		if readRes.Status() != MVReadResultDone {
			t.Errorf("%s key should be in MVHashMap, got status %d", tc.name, readRes.Status())
		}
	}
}

// TestMVBalanceStoreDoubleWriteDelta verifies that MVBalanceStore.WriteDelta
// accumulates (not overwrites) for the same txIdx. This is why
// FlushToMVStore must only be called once per execution — a second call
// doubles the balance deltas. (V2 infrastructure — MVBalanceStore is
// independent of the legacy MVHashMap delta path.)
func TestMVBalanceStoreDoubleWriteDelta(t *testing.T) {
	bals := NewMVBalanceStore()
	addr := common.HexToAddress("0x1234")

	one := new(uint256.Int).SetUint64(1)
	bals.WriteDelta(addr, 0, one, nil)

	add, sub, found := bals.GetTxDelta(addr, 0)
	if !found {
		t.Fatal("expected to find delta")
	}
	if add.Uint64() != 1 {
		t.Errorf("add after first write: got %d, want 1", add.Uint64())
	}
	if sub.Uint64() != 0 {
		t.Errorf("sub after first write: got %d, want 0", sub.Uint64())
	}

	// Second WriteDelta with same txIdx ACCUMULATES
	bals.WriteDelta(addr, 0, one, nil)
	add2, _, _ := bals.GetTxDelta(addr, 0)
	if add2.Uint64() != 2 {
		t.Errorf("add after double write: got %d, want 2 (accumulated)", add2.Uint64())
	}
}
