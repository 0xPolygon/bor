package blockstm

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestKeyAccessors round-trips Key constructors through the getters and
// type-check helpers.
func TestKeyAccessors(t *testing.T) {
	addr := common.Address{0x11, 0x22, 0x33}
	slot := common.Hash{0xaa, 0xbb, 0xcc}

	// NewAddressKey: only IsAddress should be true.
	ak := NewAddressKey(addr)
	if !ak.IsAddress() || ak.IsState() || ak.IsSubpath() {
		t.Fatalf("NewAddressKey type flags: %v %v %v", ak.IsAddress(), ak.IsState(), ak.IsSubpath())
	}
	if got := ak.GetAddress(); got != addr {
		t.Fatalf("GetAddress on address key: got %x, want %x", got, addr)
	}

	// NewStateKey: only IsState should be true.
	sk := NewStateKey(addr, slot)
	if sk.IsAddress() || !sk.IsState() || sk.IsSubpath() {
		t.Fatalf("NewStateKey type flags: %v %v %v", sk.IsAddress(), sk.IsState(), sk.IsSubpath())
	}
	if got := sk.GetAddress(); got != addr {
		t.Fatalf("GetAddress on state key: got %x, want %x", got, addr)
	}
	if got := sk.GetStateKey(); got != slot {
		t.Fatalf("GetStateKey: got %x, want %x", got, slot)
	}

	// NewSubpathKey: only IsSubpath should be true.
	pk := NewSubpathKey(addr, SubpathNonce)
	if pk.IsAddress() || pk.IsState() || !pk.IsSubpath() {
		t.Fatalf("NewSubpathKey type flags: %v %v %v", pk.IsAddress(), pk.IsState(), pk.IsSubpath())
	}
	if got := pk.GetAddress(); got != addr {
		t.Fatalf("GetAddress on subpath key: got %x, want %x", got, addr)
	}
	if got := pk.GetSubpath(); got != SubpathNonce {
		t.Fatalf("GetSubpath: got %d, want %d", got, SubpathNonce)
	}
}
