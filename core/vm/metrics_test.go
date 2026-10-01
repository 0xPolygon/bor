package vm

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// TestKeccakCacheMetrics drives opKeccak256 twice over the same widened
// (non-64B) input under a flag-on EVM with a wired KeccakStore: the first
// call is a miss (computes + stores), the second is a hit. It asserts the
// keccak hit/miss meters each increment by exactly 1. Meters are
// process-global (shared across the whole test binary), so the test
// snapshots counts before/after and asserts the delta rather than an
// absolute value.
func TestKeccakCacheMetrics(t *testing.T) {
	const n = 88 // non-64B, cacheable
	input := make([]byte, n)
	for i := range input {
		input[i] = byte(i)
	}

	run := func(evm *EVM) {
		stack := newstack()
		mem := NewMemory()
		mem.Resize(n)
		mem.Set(0, n, input)
		stack.push(uint256.NewInt(n)) // size (peeked → holds result)
		stack.push(uint256.NewInt(0)) // offset (popped)
		pc := uint64(0)
		if _, err := opKeccak256(&pc, evm, &ScopeContext{mem, stack, nil}); err != nil {
			t.Fatalf("opKeccak256: %v", err)
		}
	}

	hitBefore := keccakCacheHit.Snapshot().Count()
	missBefore := keccakCacheMiss.Snapshot().Count()

	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	store := newKeccakStore(defaultKeccakCap)
	evm := NewEVM(BlockContext{}, statedb, params.TestChainConfig, Config{
		EnablePrecompileCache: true,
		KeccakStore:           store,
	})

	run(evm) // miss: computes + stores
	if got := keccakCacheMiss.Snapshot().Count() - missBefore; got != 1 {
		t.Fatalf("miss delta = %d, want 1", got)
	}
	if got := keccakCacheHit.Snapshot().Count() - hitBefore; got != 0 {
		t.Fatalf("hit delta after miss = %d, want 0", got)
	}

	run(evm) // hit: same store, same input
	if got := keccakCacheHit.Snapshot().Count() - hitBefore; got != 1 {
		t.Fatalf("hit delta = %d, want 1", got)
	}
	if got := keccakCacheMiss.Snapshot().Count() - missBefore; got != 1 {
		t.Fatalf("miss delta after hit = %d, want 1 (unchanged)", got)
	}
}

// TestEcrecoverCacheMetrics mirrors TestKeccakCacheMetrics for the
// always-on legacy ecrecover cache in runEcrecoverWithCache: a miss (compute
// + store) followed by a hit on the same input increments the ecrecover
// miss and hit meters by exactly 1 each.
func TestEcrecoverCacheMetrics(t *testing.T) {
	hitBefore := ecrecoverCacheHit.Snapshot().Count()
	missBefore := ecrecoverCacheMiss.Snapshot().Count()

	cache := &sync.Map{}
	evm := &EVM{}
	evm.Config.EcrecoverCache = cache
	p := &stubPrecompile{gasCost: 3000}
	input := []byte{0x01, 0x02, 0x03}

	if _, _, err := evm.runPrecompile(p, ecrecoverAddr, input, 100000); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := ecrecoverCacheMiss.Snapshot().Count() - missBefore; got != 1 {
		t.Fatalf("miss delta = %d, want 1", got)
	}
	if got := ecrecoverCacheHit.Snapshot().Count() - hitBefore; got != 0 {
		t.Fatalf("hit delta after miss = %d, want 0", got)
	}

	if _, _, err := evm.runPrecompile(p, ecrecoverAddr, input, 100000); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := ecrecoverCacheHit.Snapshot().Count() - hitBefore; got != 1 {
		t.Fatalf("hit delta = %d, want 1", got)
	}
	if got := ecrecoverCacheMiss.Snapshot().Count() - missBefore; got != 1 {
		t.Fatalf("miss delta after hit = %d, want 1 (unchanged)", got)
	}
}
