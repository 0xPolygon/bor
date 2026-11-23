package vm

import (
	"testing"

	"github.com/ethereum/go-ethereum/metrics"

	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// TestKeccakCacheMetrics drives opKeccak256 twice over the same widened
// (non-64B) input under a flag-on EVM with a wired keccak cache: the first
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

	prev := SetKeccakMeterSampling(1) // exact counts for this test
	t.Cleanup(func() { SetKeccakMeterSampling(prev) })

	meters := CacheConsumerImportSerial.keccakMeters()
	other := CacheConsumerImportParallel.keccakMeters()
	hitBefore := meters.hit.Snapshot().Count()
	missBefore := meters.miss.Snapshot().Count()
	otherBefore := other.hit.Snapshot().Count() + other.miss.Snapshot().Count()

	statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	store := newKeccakStoreForTest()
	evm := NewEVM(BlockContext{}, statedb, params.TestChainConfig, Config{
		EnablePrecompileCache: true,
		keccakCache:           store,
		ResultCacheConsumer:   CacheConsumerImportSerial,
	})

	run(evm) // miss: computes + stores
	if got := meters.miss.Snapshot().Count() - missBefore; got != 1 {
		t.Fatalf("miss delta = %d, want 1", got)
	}
	if got := meters.hit.Snapshot().Count() - hitBefore; got != 0 {
		t.Fatalf("hit delta after miss = %d, want 0", got)
	}

	run(evm) // hit: same store, same input
	if got := meters.hit.Snapshot().Count() - hitBefore; got != 1 {
		t.Fatalf("hit delta = %d, want 1", got)
	}
	if got := meters.miss.Snapshot().Count() - missBefore; got != 1 {
		t.Fatalf("miss delta after hit = %d, want 1 (unchanged)", got)
	}
	if got := other.hit.Snapshot().Count() + other.miss.Snapshot().Count() - otherBefore; got != 0 {
		t.Fatalf("another consumer's meters moved by %d, want 0", got)
	}
}

// TestEcrecoverCacheMetrics mirrors TestKeccakCacheMetrics for the
// always-on legacy ecrecover cache in runEcrecoverWithCache: a miss (compute
// + store) followed by a hit on the same input increments the ecrecover
// miss and hit meters by exactly 1 each.
func TestEcrecoverCacheMetrics(t *testing.T) {
	meters := CacheConsumerImportParallel.ecrecoverMeters()
	hitBefore := meters.hit.Snapshot().Count()
	missBefore := meters.miss.Snapshot().Count()

	cache := NewEcrecoverCache()
	evm := &EVM{}
	evm.Config.EcrecoverCache = cache
	evm.Config.ResultCacheConsumer = CacheConsumerImportParallel
	p := &stubPrecompile{gasCost: 3000}
	input := []byte{0x01, 0x02, 0x03}

	if _, _, err := evm.runPrecompile(p, ecrecoverAddr, input, 100000); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := meters.miss.Snapshot().Count() - missBefore; got != 1 {
		t.Fatalf("miss delta = %d, want 1", got)
	}
	if got := meters.hit.Snapshot().Count() - hitBefore; got != 0 {
		t.Fatalf("hit delta after miss = %d, want 0", got)
	}

	if _, _, err := evm.runPrecompile(p, ecrecoverAddr, input, 100000); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := meters.hit.Snapshot().Count() - hitBefore; got != 1 {
		t.Fatalf("hit delta = %d, want 1", got)
	}
	if got := meters.miss.Snapshot().Count() - missBefore; got != 1 {
		t.Fatalf("miss delta after hit = %d, want 1 (unchanged)", got)
	}
}

// TestKeccakMeterSampling checks that sampled marking stays close to the true
// count: 64,000 events at 1-in-64 should give 64,000 on average. The bound is
// about 5 standard deviations, so the test does not flake.
func TestKeccakMeterSampling(t *testing.T) {
	prev := SetKeccakMeterSampling(64)
	t.Cleanup(func() { SetKeccakMeterSampling(prev) })

	m := metrics.NewMeter()
	defer m.Stop()
	const events = 64_000
	for i := 0; i < events; i++ {
		markSampled(m)
	}
	got := m.Snapshot().Count()
	if got%64 != 0 {
		t.Fatalf("count %d is not a multiple of the sampling rate", got)
	}
	if got < events-10_000 || got > events+10_000 {
		t.Fatalf("sampled count %d too far from %d", got, events)
	}

	SetKeccakMeterSampling(1)
	before := m.Snapshot().Count()
	markSampled(m)
	if d := m.Snapshot().Count() - before; d != 1 {
		t.Fatalf("rate 1: delta %d, want 1", d)
	}
}
