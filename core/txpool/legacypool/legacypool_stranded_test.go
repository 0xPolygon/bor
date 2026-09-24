package legacypool

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// Fee values: an account signs its head transactions with a 40 gwei fee cap
// while the base fee is ~15 gwei, then the base fee returns to ~248 gwei.
var (
	strandedLowBaseFee  = big.NewInt(15 * params.GWei)
	strandedHighBaseFee = big.NewInt(248 * params.GWei)
	strandedHeadFeeCap  = big.NewInt(40 * params.GWei)
	strandedTailFeeCap  = big.NewInt(400 * params.GWei)
	strandedTip         = big.NewInt(30 * params.GWei)
)

const (
	strandedTestLifetime = 400 * time.Millisecond
	strandedTestInterval = 25 * time.Millisecond
	strandedHeadCount    = 3
	strandedChainLength  = 200
)

// setupStrandedPool creates an EIP-1559 pool with a short lifetime and a fast
// eviction ticker, restoring the package level ticker on cleanup.
func setupStrandedPool(t *testing.T) *LegacyPool {
	t.Helper()
	return setupStrandedPoolWithInterval(t, strandedTestInterval)
}

// setupManualStrandedPool creates the same pool with the eviction ticker
// effectively disabled, for tests that drive evictStranded with explicit times.
func setupManualStrandedPool(t *testing.T) *LegacyPool {
	t.Helper()
	return setupStrandedPoolWithInterval(t, time.Hour)
}

func setupStrandedPoolWithInterval(t *testing.T, interval time.Duration) *LegacyPool {
	t.Helper()

	oldInterval := evictionInterval
	evictionInterval = interval
	t.Cleanup(func() { evictionInterval = oldInterval })

	pool, _ := setupPoolWithConfig(eip1559Config, func(pool *LegacyPool) {
		pool.config.Lifetime = strandedTestLifetime
	})
	t.Cleanup(func() { pool.Close() })

	// Enforce the mainnet minimum tip that producers apply in Pending.
	pool.gasTip.Store(uint256.NewInt(params.BorDefaultTxPoolPriceLimit))
	setStrandedBaseFee(pool, strandedLowBaseFee)
	return pool
}

func setStrandedBaseFee(pool *LegacyPool, baseFee *big.Int) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.currentHead.Store(&types.Header{Number: big.NewInt(1), Difficulty: common.Big0, GasLimit: 10_000_000, BaseFee: baseFee})
}

func fundedKey(t *testing.T, pool *LegacyPool) (*ecdsa.PrivateKey, common.Address) {
	t.Helper()
	key, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(key.PublicKey)
	testAddBalance(pool, addr, new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(params.Ether)))
	return key, addr
}

// addStrandedChain adds strandedChainLength gapless transactions from key: the
// first strandedHeadCount carry a fee cap that the high base fee cannot pay.
func addStrandedChain(t *testing.T, pool *LegacyPool, key *ecdsa.PrivateKey) []*types.Transaction {
	t.Helper()
	txs := make([]*types.Transaction, 0, strandedChainLength)
	for nonce := uint64(0); nonce < strandedChainLength; nonce++ {
		feeCap := strandedTailFeeCap
		if nonce < strandedHeadCount {
			feeCap = strandedHeadFeeCap
		}
		txs = append(txs, dynamicFeeTx(nonce, 100_000, feeCap, strandedTip, key))
	}
	for _, err := range pool.addRemotesSync(txs) {
		if err != nil {
			t.Fatalf("failed to add stranded chain: %v", err)
		}
	}
	return txs
}

func addHealthyChain(t *testing.T, pool *LegacyPool, key *ecdsa.PrivateKey, count uint64) {
	t.Helper()
	addHealthyTail(t, pool, key, 0, count)
}

func accountCounts(pool *LegacyPool, addr common.Address) (pending, queued int) {
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if list := pool.pending[addr]; list != nil {
		pending = list.Len()
	}
	if list, ok := pool.queue.get(addr); ok {
		queued = list.Len()
	}
	return pending, queued
}

// waitForCounts polls until the account reaches the wanted pending/queued sizes
// or the deadline passes, returning the last observed counts.
func waitForCounts(pool *LegacyPool, addr common.Address, wantPending, wantQueued int, deadline time.Duration) (int, int) {
	var pending, queued int
	for end := time.Now().Add(deadline); time.Now().Before(end); time.Sleep(strandedTestInterval) {
		pending, queued = accountCounts(pool, addr)
		if pending == wantPending && queued == wantQueued {
			break
		}
	}
	return pending, queued
}

// TestStrandedPendingChainEvicted reproduces the mainnet incident: a gapless
// pending chain whose head can no longer pay the base fee must be evicted once
// it has stayed stranded for longer than the pool lifetime, while healthy
// accounts are left untouched.
func TestStrandedPendingChainEvicted(t *testing.T) {
	pool := setupStrandedPool(t)

	botKey, bot := fundedKey(t, pool)
	honestKey, honest := fundedKey(t, pool)

	addStrandedChain(t, pool, botKey)
	addHealthyChain(t, pool, honestKey, 5)

	if pending, _ := accountCounts(pool, bot); pending != strandedChainLength {
		t.Fatalf("stranded account pending = %d, want %d", pending, strandedChainLength)
	}

	// Base fee recovers above the head's fee cap: the head can never be mined.
	evictedBefore := strandedEvictionMeter.Snapshot().Count()
	setStrandedBaseFee(pool, strandedHighBaseFee)

	if pending, queued := waitForCounts(pool, bot, 0, 0, 4*strandedTestLifetime); pending != 0 || queued != 0 {
		t.Fatalf("stranded account not evicted after lifetime: pending %d, queued %d", pending, queued)
	}
	if got := strandedEvictionMeter.Snapshot().Count() - evictedBefore; got != strandedChainLength {
		t.Fatalf("stranded eviction meter = %d, want %d", got, strandedChainLength)
	}
	if pending, _ := accountCounts(pool, honest); pending != 5 {
		t.Fatalf("healthy account pending = %d, want 5", pending)
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
}

// TestStrandedPendingNotEvictedBeforeLifetime checks that a chain is kept while
// it has been stranded for less than the lifetime, so a short base fee move
// never drops transactions. It drives evictStranded with explicit times so the
// result doesn't depend on ticker scheduling.
func TestStrandedPendingNotEvictedBeforeLifetime(t *testing.T) {
	pool := setupManualStrandedPool(t)

	botKey, bot := fundedKey(t, pool)
	addStrandedChain(t, pool, botKey)

	evictAt := strandedEvictor(pool)
	t0 := time.Now()

	// Stranded for half a lifetime, then payable again, then stranded again:
	// the clock restarts, so the chain is kept until a full lifetime passes.
	setStrandedBaseFee(pool, strandedHighBaseFee)
	evictAt(t0)
	evictAt(t0.Add(strandedTestLifetime / 2))
	setStrandedBaseFee(pool, strandedLowBaseFee)
	evictAt(t0.Add(strandedTestLifetime/2 + time.Millisecond))
	setStrandedBaseFee(pool, strandedHighBaseFee)
	restrand := t0.Add(strandedTestLifetime/2 + 2*time.Millisecond)
	evictAt(restrand)
	evictAt(restrand.Add(strandedTestLifetime))

	if pending, _ := accountCounts(pool, bot); pending != strandedChainLength {
		t.Fatalf("chain evicted although never stranded for more than a lifetime: pending %d, want %d", pending, strandedChainLength)
	}
	// Positive control: one more sample past the lifetime evicts it.
	evictAt(restrand.Add(strandedTestLifetime + time.Millisecond))
	if pending, queued := accountCounts(pool, bot); pending != 0 || queued != 0 {
		t.Fatalf("chain not evicted past the lifetime: pending %d, queued %d", pending, queued)
	}
}

// TestStrandedHeadReplacedNotEvicted checks that replacing the underpriced head
// with a payable one (the normal user fix) clears the stranded state.
func TestStrandedHeadReplacedNotEvicted(t *testing.T) {
	pool := setupStrandedPool(t)

	botKey, bot := fundedKey(t, pool)
	addStrandedChain(t, pool, botKey)
	setStrandedBaseFee(pool, strandedHighBaseFee)
	time.Sleep(strandedTestLifetime / 2)

	bumpedTip := big.NewInt(40 * params.GWei)
	for nonce := uint64(0); nonce < strandedHeadCount; nonce++ {
		if err := pool.addRemoteSync(dynamicFeeTx(nonce, 100_000, strandedTailFeeCap, bumpedTip, botKey)); err != nil {
			t.Fatalf("failed to replace head %d: %v", nonce, err)
		}
	}
	time.Sleep(2 * strandedTestLifetime)

	if pending, _ := accountCounts(pool, bot); pending != strandedChainLength {
		t.Fatalf("chain evicted after its head was repriced: pending %d, want %d", pending, strandedChainLength)
	}
}

// TestStrandedEvictedTxNotReaccepted checks that peers cannot push an evicted
// underpriced head straight back into the pool, while a repriced head and the
// original one (once the base fee allows it again) are still accepted.
func TestStrandedEvictedTxNotReaccepted(t *testing.T) {
	pool := setupStrandedPool(t)

	botKey, bot := fundedKey(t, pool)
	txs := addStrandedChain(t, pool, botKey)
	setStrandedBaseFee(pool, strandedHighBaseFee)

	if pending, queued := waitForCounts(pool, bot, 0, 0, 4*strandedTestLifetime); pending != 0 || queued != 0 {
		t.Fatalf("stranded account not evicted after lifetime: pending %d, queued %d", pending, queued)
	}

	// Re-announced evicted head: rejected as underpriced so the fetcher also
	// stops requesting it.
	rejectedBefore := strandedRejectMeter.Snapshot().Count()
	if err := pool.addRemoteSync(txs[0]); !errors.Is(err, txpool.ErrUnderpriced) {
		t.Fatalf("re-adding evicted head: err = %v, want %v", err, txpool.ErrUnderpriced)
	}
	if got := strandedRejectMeter.Snapshot().Count() - rejectedBefore; got != 1 {
		t.Fatalf("stranded reject meter = %d, want 1", got)
	}
	// The same slot re-signed with a slightly higher, still unminable fee
	// cap is refused too.
	resigned := dynamicFeeTx(0, 100_000, new(big.Int).Add(strandedHeadFeeCap, common.Big1), strandedTip, botKey)
	if err := pool.addRemoteSync(resigned); !errors.Is(err, txpool.ErrUnderpriced) {
		t.Fatalf("re-signed evicted head: err = %v, want %v", err, txpool.ErrUnderpriced)
	}
	// A payable tail transaction may come back, but only as a gapped (queued)
	// transaction bounded by the queue limits and lifetime.
	if err := pool.addRemoteSync(txs[strandedHeadCount]); err != nil {
		t.Fatalf("re-adding payable tail tx: %v", err)
	}
	if pending, queued := accountCounts(pool, bot); pending != 0 || queued != 1 {
		t.Fatalf("tail tx placement: pending %d queued %d, want 0/1", pending, queued)
	}
	// The sender fixes the head with a payable fee: accepted as usual.
	if err := pool.addRemoteSync(dynamicFeeTx(0, 100_000, strandedTailFeeCap, strandedTip, botKey)); err != nil {
		t.Fatalf("adding repriced head: %v", err)
	}
	// Once the base fee drops below the original fee cap, the original head
	// is payable again and must be accepted.
	setStrandedBaseFee(pool, strandedLowBaseFee)
	if err := pool.addRemoteSync(txs[1]); err != nil {
		t.Fatalf("re-adding evicted tx after base fee drop: %v", err)
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internal state corrupted: %v", err)
	}
}

// TestStrandedLowEffectiveTipEvicted covers a head whose fee cap covers the
// base fee but leaves an effective tip below the pool minimum: producers skip
// it in the same way, so the chain is stranded as well.
func TestStrandedLowEffectiveTipEvicted(t *testing.T) {
	pool := setupStrandedPool(t)

	key, addr := fundedKey(t, pool)
	// Next-block base fee + 10 gwei leaves a 10 gwei effective tip, below 25 gwei.
	setStrandedBaseFee(pool, strandedHighBaseFee)
	nextBaseFee := pool.strandedBaseFee()
	setStrandedBaseFee(pool, strandedLowBaseFee)
	headFeeCap := new(big.Int).Add(nextBaseFee, big.NewInt(10*params.GWei))
	if err := pool.addRemoteSync(dynamicFeeTx(0, 100_000, headFeeCap, strandedTip, key)); err != nil {
		t.Fatalf("failed to add head: %v", err)
	}
	addHealthyTail(t, pool, key, 1, 20)
	setStrandedBaseFee(pool, strandedHighBaseFee)

	if pending, queued := waitForCounts(pool, addr, 0, 0, 4*strandedTestLifetime); pending != 0 || queued != 0 {
		t.Fatalf("low effective tip chain not evicted: pending %d, queued %d", pending, queued)
	}
}

func addHealthyTail(t *testing.T, pool *LegacyPool, key *ecdsa.PrivateKey, from, to uint64) {
	t.Helper()
	for nonce := from; nonce < to; nonce++ {
		if err := pool.addRemoteSync(dynamicFeeTx(nonce, 100_000, strandedTailFeeCap, strandedTip, key)); err != nil {
			t.Fatalf("failed to add tail tx %d: %v", nonce, err)
		}
	}
}

func strandedEvictor(pool *LegacyPool) func(time.Time) {
	return func(now time.Time) {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		pool.evictStranded(now)
	}
}

// TestStrandedClockRestartsAfterAccountLeaves checks that an account whose
// pending txs left the pool (mined or dropped) starts a fresh stranded clock
// when a new unminable head arrives, instead of inheriting the old one.
func TestStrandedClockRestartsAfterAccountLeaves(t *testing.T) {
	pool := setupManualStrandedPool(t)
	evictAt := strandedEvictor(pool)

	key, addr := fundedKey(t, pool)
	head := dynamicFeeTx(0, 100_000, strandedHeadFeeCap, strandedTip, key)
	if err := pool.addRemoteSync(head); err != nil {
		t.Fatalf("failed to add head: %v", err)
	}
	setStrandedBaseFee(pool, strandedHighBaseFee)
	t0 := time.Now()
	evictAt(t0)

	// The account leaves the pending set before the lifetime passes.
	pool.mu.Lock()
	pool.removeTx(head.Hash(), true, true)
	pool.mu.Unlock()
	evictAt(t0.Add(time.Millisecond))

	// A new unminable head arrives later: the old clock must not apply.
	if err := pool.addRemoteSync(dynamicFeeTx(0, 100_000, strandedHeadFeeCap, big.NewInt(35*params.GWei), key)); err != nil {
		t.Fatalf("failed to add new head: %v", err)
	}
	restart := t0.Add(strandedTestLifetime)
	evictAt(restart)
	evictAt(restart.Add(time.Millisecond))
	if pending, _ := accountCounts(pool, addr); pending != 1 {
		t.Fatalf("new head evicted with the old stranded clock: pending %d, want 1", pending)
	}
	evictAt(restart.Add(strandedTestLifetime + time.Millisecond))
	if pending, _ := accountCounts(pool, addr); pending != 0 {
		t.Fatalf("new head not evicted after its own lifetime: pending %d, want 0", pending)
	}
}

// TestStrandedRejectionExpires checks that the evicted-tx memory only refuses
// re-admission for one lifetime.
func TestStrandedRejectionExpires(t *testing.T) {
	pool := setupManualStrandedPool(t)
	evictAt := strandedEvictor(pool)

	key, addr := fundedKey(t, pool)
	head := dynamicFeeTx(0, 100_000, strandedHeadFeeCap, strandedTip, key)
	if err := pool.addRemoteSync(head); err != nil {
		t.Fatalf("failed to add head: %v", err)
	}
	setStrandedBaseFee(pool, strandedHighBaseFee)
	t0 := time.Now().Add(-3 * strandedTestLifetime)
	evictAt(t0)
	evictAt(t0.Add(strandedTestLifetime + time.Millisecond))
	if pending, _ := accountCounts(pool, addr); pending != 0 {
		t.Fatalf("head not evicted: pending %d", pending)
	}
	// Evicted more than a lifetime ago: the memory has expired.
	if err := pool.addRemoteSync(head); err != nil {
		t.Fatalf("re-adding head after the memory expired: %v", err)
	}
	if pending, _ := accountCounts(pool, addr); pending != 1 {
		t.Fatalf("head not re-admitted: pending %d, want 1", pending)
	}
}

// TestStrandedNoBaseFee checks that nothing is tracked or evicted while the
// head carries no base fee (pre-London), and that tracking restarts from zero.
func TestStrandedNoBaseFee(t *testing.T) {
	pool := setupManualStrandedPool(t)
	evictAt := strandedEvictor(pool)

	key, addr := fundedKey(t, pool)
	head := dynamicFeeTx(0, 100_000, strandedHeadFeeCap, strandedTip, key)
	if err := pool.addRemoteSync(head); err != nil {
		t.Fatalf("failed to add head: %v", err)
	}
	setStrandedBaseFee(pool, strandedHighBaseFee)
	t0 := time.Now()
	evictAt(t0)

	setStrandedBaseFee(pool, nil)
	evictAt(t0.Add(strandedTestLifetime + time.Millisecond))
	if pending, _ := accountCounts(pool, addr); pending != 1 {
		t.Fatalf("evicted without a base fee: pending %d, want 1", pending)
	}
	// Base fee is back: the earlier sample must not count towards the lifetime.
	setStrandedBaseFee(pool, strandedHighBaseFee)
	evictAt(t0.Add(strandedTestLifetime + 2*time.Millisecond))
	if pending, _ := accountCounts(pool, addr); pending != 1 {
		t.Fatalf("stale stranded clock survived a base fee gap: pending %d, want 1", pending)
	}
}

// TestStrandedDropBookkeeping checks the pool side state that dropping a
// stranded account must release: rebroadcast tracking, priced heap stale
// accounting and the pending gauge.
func TestStrandedDropBookkeeping(t *testing.T) {
	pool := setupManualStrandedPool(t)
	evictAt := strandedEvictor(pool)

	botKey, bot := fundedKey(t, pool)
	honestKey, _ := fundedKey(t, pool)
	txs := addStrandedChain(t, pool, botKey)
	addHealthyChain(t, pool, honestKey, 5)

	pool.mu.Lock()
	pool.lastRebroadcast[txs[strandedHeadCount].Hash()] = time.Now()
	pool.mu.Unlock()
	gaugeBefore := pendingGauge.Snapshot().Value()

	setStrandedBaseFee(pool, strandedHighBaseFee)
	t0 := time.Now()
	evictAt(t0)
	evictAt(t0.Add(strandedTestLifetime + time.Millisecond))
	if pending, queued := accountCounts(pool, bot); pending != 0 || queued != 0 {
		t.Fatalf("stranded account not evicted: pending %d, queued %d", pending, queued)
	}

	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if _, ok := pool.lastRebroadcast[txs[strandedHeadCount].Hash()]; ok {
		t.Fatal("rebroadcast tracking kept for an evicted transaction")
	}
	// Priced heap entries minus stale entries must equal the live transactions.
	heaped := len(pool.priced.urgent.list) + len(pool.priced.floating.list)
	if live := heaped - int(pool.priced.stales.Load()); live != pool.all.Count() {
		t.Fatalf("priced heap live entries = %d, want %d", live, pool.all.Count())
	}
	if got := gaugeBefore - pendingGauge.Snapshot().Value(); got != strandedChainLength {
		t.Fatalf("pending gauge dropped by %d, want %d", got, strandedChainLength)
	}
}

func TestUnminable(t *testing.T) {
	key, _ := crypto.GenerateKey()
	baseFee := big.NewInt(100 * params.GWei)
	tests := []struct {
		name   string
		feeCap int64
		tip    int64
		minTip uint64
		want   bool
	}{
		{"fee cap below base fee", 99, 30, 25, true},
		{"fee cap equals base fee, no min tip", 100, 30, 0, false},
		{"effective tip below min tip", 110, 30, 25, true},
		{"effective tip at min tip", 125, 30, 25, false},
		{"tip cap limits effective tip", 200, 20, 25, true},
	}
	for _, tt := range tests {
		tx := dynamicFeeTx(0, 21_000, big.NewInt(tt.feeCap*params.GWei), big.NewInt(tt.tip*params.GWei), key)
		if got := unminable(tx, baseFee, uint256.NewInt(tt.minTip*params.GWei)); got != tt.want {
			t.Errorf("%s: unminable = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestStrandedEvictedMemorySizedToPool checks that the evicted-tx memory can
// hold as many entries as the pool has slots, so a broad eviction does not push
// entries out before their lifetime ends.
func TestStrandedEvictedMemorySizedToPool(t *testing.T) {
	config := testTxPoolConfig
	config.GlobalSlots, config.GlobalQueue = 96, 32
	state := newStrandedState(config)

	for i := range 200 {
		state.evicted.Add(strandedKey{nonce: uint64(i)}, time.Now())
	}
	if got := state.evicted.Len(); got != 128 {
		t.Fatalf("evicted memory holds %d entries, want GlobalSlots+GlobalQueue = 128", got)
	}
}
