package vm

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// stubPrecompile returns a fixed output and charges gasCost.
type stubPrecompile struct {
	gasCost uint64
}

func (p *stubPrecompile) RequiredGas(input []byte) uint64  { return p.gasCost }
func (p *stubPrecompile) Run(input []byte) ([]byte, error) { return []byte{0xab}, nil }
func (p *stubPrecompile) Name() string                     { return "stub" }

// TestRunEcrecoverWithCache_NilCached covers the cache-hit/nil fast path.
// When a prior ecrecover call returned nil (invalid signature) and was
// stored as nil in the cache, a subsequent call with the same input must
// return nil without attempting a type assertion (which would panic on
// a nil interface value).
func TestRunEcrecoverWithCache_NilCached(t *testing.T) {
	cache := &sync.Map{}
	input := []byte{0x01, 0x02, 0x03}
	var key [128]byte
	copy(key[:], common.RightPadBytes(input, 128))
	cache.Store(key, nil)

	evm := &EVM{}
	evm.Config.EcrecoverCache = cache
	p := &stubPrecompile{gasCost: 3000}

	ret, remaining, err := evm.runPrecompile(p, ecrecoverAddr, input, 10000)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ret != nil {
		t.Fatalf("expected nil return (cached nil), got %x", ret)
	}
	if remaining != 10000-3000 {
		t.Fatalf("expected gas=%d, got %d", 10000-3000, remaining)
	}
}

// TestRunEcrecoverWithCache_BytesCached verifies the complementary path:
// non-nil cached bytes returned via the fast path.
func TestRunEcrecoverWithCache_BytesCached(t *testing.T) {
	cache := &sync.Map{}
	input := []byte{0x0a, 0x0b, 0x0c}
	var key [128]byte
	copy(key[:], common.RightPadBytes(input, 128))
	cache.Store(key, []byte{0xde, 0xad, 0xbe, 0xef})

	evm := &EVM{}
	evm.Config.EcrecoverCache = cache
	p := &stubPrecompile{gasCost: 3000}

	ret, remaining, err := evm.runPrecompile(p, ecrecoverAddr, input, 10000)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ret) != 4 || ret[0] != 0xde {
		t.Fatalf("expected cached bytes, got %x", ret)
	}
	if remaining != 7000 {
		t.Fatalf("expected gas=7000, got %d", remaining)
	}
}

// TestRunEcrecoverWithCache_OOG verifies OOG on cache hit.
func TestRunEcrecoverWithCache_OOG(t *testing.T) {
	cache := &sync.Map{}
	input := []byte{0x11}
	var key [128]byte
	copy(key[:], common.RightPadBytes(input, 128))
	cache.Store(key, []byte{0x42})

	evm := &EVM{}
	evm.Config.EcrecoverCache = cache
	p := &stubPrecompile{gasCost: 3000}

	_, _, err := evm.runPrecompile(p, ecrecoverAddr, input, 1000)
	if err != ErrOutOfGas {
		t.Fatalf("expected ErrOutOfGas, got %v", err)
	}
}
