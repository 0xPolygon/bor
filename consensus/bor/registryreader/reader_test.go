package registryreader

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
)

// mockReader is a controllable Reader for exercising BuildSnapshot and the
// Snapshot accessors without an EVM-backed registry.
type mockReader struct {
	has         bool
	root        common.Hash
	whitelist   []common.Address
	totalGas    uint64
	clients     map[common.Address]ClientLookup
	rootErr     error
	wlErr       error
	totalErr    error
	clientErr   error
	clientCalls int
}

func (m *mockReader) HasReservedRegistry() bool { return m.has }

func (m *mockReader) IsReservedAddress(_ *state.StateDB, _ uint64, _ common.Hash, _ common.Address) (bool, error) {
	return false, nil
}

func (m *mockReader) ReservedClientForAddress(_ *state.StateDB, _ uint64, _ common.Hash, a common.Address) (ClientLookup, error) {
	m.clientCalls++
	if m.clientErr != nil {
		return ClientLookup{}, m.clientErr
	}
	return m.clients[a], nil
}

func (m *mockReader) Root(_ *state.StateDB, _ uint64, _ common.Hash) (common.Hash, error) {
	return m.root, m.rootErr
}

func (m *mockReader) WhitelistedAddresses(_ *state.StateDB, _ uint64, _ common.Hash) ([]common.Address, error) {
	return m.whitelist, m.wlErr
}

func (m *mockReader) TotalReservedGas(_ *state.StateDB, _ uint64, _ common.Hash) (uint64, error) {
	return m.totalGas, m.totalErr
}

func addr(b byte) common.Address { return common.Address{19: b} }

func TestBuildSnapshot(t *testing.T) {
	errBoom := errors.New("boom")
	a1, a2 := addr(1), addr(2)
	base := func() *mockReader {
		return &mockReader{
			has:       true,
			root:      common.HexToHash("0xabc"),
			whitelist: []common.Address{a1, a2},
			totalGas:  60_000_000,
			clients: map[common.Address]ClientLookup{
				a1: {ClientID: big.NewInt(1), GasQuota: 30_000_000, Active: true},
				a2: {ClientID: big.NewInt(2), GasQuota: 30_000_000, Active: true},
			},
		}
	}

	t.Run("nil reader yields nil snapshot", func(t *testing.T) {
		snap, err := BuildSnapshot(nil, nil, 1, common.Hash{})
		if err != nil || snap != nil {
			t.Fatalf("snap=%v err=%v, want nil,nil", snap, err)
		}
	})

	t.Run("registry not configured yields nil snapshot", func(t *testing.T) {
		snap, err := BuildSnapshot(&mockReader{has: false}, nil, 1, common.Hash{})
		if err != nil || snap != nil {
			t.Fatalf("snap=%v err=%v, want nil,nil", snap, err)
		}
	})

	t.Run("happy path populates root, capacity and clients", func(t *testing.T) {
		r := base()
		snap, err := BuildSnapshot(r, nil, 7, common.Hash{})
		if err != nil {
			t.Fatal(err)
		}
		if snap.Root() != r.root {
			t.Errorf("root=%s, want %s", snap.Root(), r.root)
		}
		if snap.Capacity() != 60_000_000 {
			t.Errorf("capacity=%d, want 60000000", snap.Capacity())
		}
		if r.clientCalls != len(r.whitelist) {
			t.Errorf("client lookups=%d, want %d", r.clientCalls, len(r.whitelist))
		}
		if !snap.IsReserved(a1, 7) || !snap.IsReserved(a2, 7) {
			t.Error("both whitelisted addresses should be reserved")
		}
	})

	for _, tc := range []struct {
		name  string
		mutta func(*mockReader)
	}{
		{"root error", func(m *mockReader) { m.rootErr = errBoom }},
		{"whitelist error", func(m *mockReader) { m.wlErr = errBoom }},
		{"total gas error", func(m *mockReader) { m.totalErr = errBoom }},
		{"per-client error", func(m *mockReader) { m.clientErr = errBoom }},
	} {
		t.Run(tc.name+" propagates", func(t *testing.T) {
			r := base()
			tc.mutta(r)
			snap, err := BuildSnapshot(r, nil, 7, common.Hash{})
			if !errors.Is(err, errBoom) {
				t.Fatalf("err=%v, want boom", err)
			}
			if snap != nil {
				t.Errorf("snap=%v, want nil on error", snap)
			}
		})
	}
}

func TestSnapshotIsReserved(t *testing.T) {
	a := addr(1)
	snap := NewSnapshot(common.HexToHash("0x1"), 30_000_000, map[common.Address]ClientLookup{
		a:       {ClientID: big.NewInt(1), GasQuota: 30_000_000, Active: true, EffectiveFrom: 100},
		addr(2): {ClientID: big.NewInt(2), GasQuota: 30_000_000, Active: false, EffectiveFrom: 0},
	})

	tests := []struct {
		name    string
		account common.Address
		number  uint64
		want    bool
	}{
		{"before effectiveFrom", a, 99, false},
		{"exactly at effectiveFrom", a, 100, true},
		{"after effectiveFrom", a, 101, true},
		{"inactive client", addr(2), 1, false},
		{"unknown account", addr(9), 100, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := snap.IsReserved(tt.account, tt.number); got != tt.want {
				t.Errorf("IsReserved(%s, %d)=%v, want %v", tt.account, tt.number, got, tt.want)
			}
		})
	}
}

func TestSnapshotNilSafe(t *testing.T) {
	var snap *Snapshot
	if snap.IsReserved(addr(1), 1) {
		t.Error("nil snapshot must not classify anything as reserved")
	}
	if snap.FeeMode(addr(1)) != 0 {
		t.Error("nil snapshot FeeMode must be 0")
	}
	if snap.Capacity() != 0 {
		t.Error("nil snapshot Capacity must be 0")
	}
	if snap.Root() != (common.Hash{}) {
		t.Error("nil snapshot Root must be zero hash")
	}
}

func TestSnapshotFeeModeAndCapacity(t *testing.T) {
	a := addr(1)
	snap := NewSnapshot(common.HexToHash("0x2"), 12_345, map[common.Address]ClientLookup{
		a: {ClientID: big.NewInt(1), GasQuota: 12_345, Active: true, FeeMode: 1},
	})
	if snap.FeeMode(a) != 1 {
		t.Errorf("FeeMode=%d, want 1", snap.FeeMode(a))
	}
	if snap.FeeMode(addr(9)) != 0 {
		t.Errorf("FeeMode(unknown)=%d, want 0", snap.FeeMode(addr(9)))
	}
	if snap.Capacity() != 12_345 {
		t.Errorf("Capacity=%d, want 12345", snap.Capacity())
	}
}
