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

// TestEcrecoverCache_NotUsedWhenPrecompilesOverridden covers invariant #6:
// an EVM whose precompile set has been overridden (mirroring the
// internal/ethapi and eth/tracers override paths, which install custom
// contracts at arbitrary addresses including 0x01) must never consult or
// populate the address-keyed ecrecover cache. If it did, a call routed to
// address 0x01 that is actually running an overridden (non-ecrecover)
// contract could be served the wrong (cached real-ecrecover) result, or
// could poison the cache with the overridden contract's output.
func TestEcrecoverCache_NotUsedWhenPrecompilesOverridden(t *testing.T) {
	cache := &sync.Map{}
	evm := &EVM{}
	evm.Config.EcrecoverCache = cache

	custom := &stubPrecompile{gasCost: 42}
	evm.SetPrecompiles(PrecompiledContracts{ecrecoverAddr: custom})

	input := make([]byte, 128)
	ret, _, err := evm.runPrecompile(custom, ecrecoverAddr, input, 100000)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ret) != 1 || ret[0] != 0xab {
		t.Fatalf("expected overridden contract's output, got %x", ret)
	}

	n := 0
	cache.Range(func(_, _ any) bool { n++; return true })
	if n != 0 {
		t.Fatalf("cache must not be populated when precompiles are overridden, got %d entries", n)
	}
}

// TestEcrecoverCache_HitNotAliased ensures a caller mutating the []byte
// returned from a cache hit cannot corrupt the cached entry (or vice versa):
// runEcrecoverWithCache stores the raw result computed by
// RunPrecompiledContract and, on a hit, returns that same stored slice by
// reference (`cached.([]byte)`) — so without defensive cloning, a caller
// mutating either the miss-path return value or a hit-path return value
// mutates the shared backing array and corrupts every future lookup.
func TestEcrecoverCache_HitNotAliased(t *testing.T) {
	cache := &sync.Map{}
	input := []byte{0x22, 0x33, 0x44}
	evm := &EVM{}
	evm.Config.EcrecoverCache = cache
	p := &stubPrecompile{gasCost: 3000}

	ret1, _, err := evm.runPrecompile(p, ecrecoverAddr, input, 100000)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ret1) != 1 || ret1[0] != 0xab {
		t.Fatalf("unexpected first result: %x", ret1)
	}

	// Mutate the slice returned to the first caller.
	ret1[0] = 0xff

	ret2, _, err := evm.runPrecompile(p, ecrecoverAddr, input, 100000)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ret2) != 1 || ret2[0] != 0xab {
		t.Fatalf("second (cached) result was corrupted by mutating the first return value, got %x", ret2)
	}
}
