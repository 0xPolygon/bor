package legacypool

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

// conditionalTx returns a signed tx carrying PIP-15 options with the given
// number of known storage slots (0 means one empty storage entry).
func conditionalTx(nonce uint64, slots int, key *ecdsa.PrivateKey) *types.Transaction {
	return pricedConditionalTx(nonce, slots, big.NewInt(1), key)
}

func pricedConditionalTx(nonce uint64, slots int, price *big.Int, key *ecdsa.PrivateKey) *types.Transaction {
	tx := pricedTransaction(nonce, 100000, price, key)
	storage := make(map[common.Hash]common.Hash, slots)
	for i := range slots {
		storage[common.BigToHash(big.NewInt(int64(i)))] = common.Hash{}
	}
	tx.PutOptions(&types.OptionsPIP15{KnownAccounts: types.KnownAccounts{
		common.Address{0xaa}: {Storage: storage},
	}})
	return tx
}

func setupConditionalPool(t *testing.T, enabled bool, slots uint64) (*LegacyPool, *ecdsa.PrivateKey) {
	t.Helper()
	pool, key := setupPoolWithConfig(params.TestChainConfig, func(pool *LegacyPool) {
		pool.config.ConditionalTxs = enabled
		pool.config.ConditionalSlots = slots
	})
	t.Cleanup(func() { pool.Close() })
	testAddBalance(pool, crypto.PubkeyToAddress(key.PublicKey), big.NewInt(1000000000))
	return pool, key
}

func TestConditionalTxsDisabledByDefault(t *testing.T) {
	t.Parallel()

	if DefaultConfig.ConditionalTxs {
		t.Fatal("conditional transactions must be off by default")
	}
	pool, key := setupConditionalPool(t, false, DefaultConfig.ConditionalSlots)

	rejects := conditionalRejectMeter.Snapshot().Count()
	if err := pool.addRemoteSync(conditionalTx(0, 1, key)); !errors.Is(err, txpool.ErrConditionalTxsDisabled) {
		t.Fatalf("conditional tx: have %v, want %v", err, txpool.ErrConditionalTxsDisabled)
	}
	if conditionalRejectMeter.Snapshot().Count() == rejects {
		t.Fatal("reject meter not marked")
	}
	if err := pool.addRemoteSync(transaction(0, 100000, key)); err != nil {
		t.Fatalf("plain tx rejected: %v", err)
	}
	if have := pool.all.ConditionalSlots(); have != 0 {
		t.Fatalf("conditional slots: have %d, want 0", have)
	}
}

func TestConditionalTxsBudget(t *testing.T) {
	t.Parallel()

	pool, key := setupConditionalPool(t, true, 5)

	// An empty storage entry still weighs 1, so it cannot dodge the budget.
	if err := pool.addRemoteSync(conditionalTx(0, 0, key)); err != nil {
		t.Fatalf("first conditional tx rejected: %v", err)
	}
	if err := pool.addRemoteSync(conditionalTx(1, 4, key)); err != nil {
		t.Fatalf("second conditional tx rejected: %v", err)
	}
	if have := pool.all.ConditionalSlots(); have != 5 {
		t.Fatalf("conditional slots: have %d, want 5", have)
	}
	overflows := conditionalOverflowMeter.Snapshot().Count()
	if err := pool.addRemoteSync(conditionalTx(2, 1, key)); !errors.Is(err, txpool.ErrConditionalPoolFull) {
		t.Fatalf("over budget: have %v, want %v", err, txpool.ErrConditionalPoolFull)
	}
	if conditionalOverflowMeter.Snapshot().Count() == overflows {
		t.Fatal("overflow meter not marked")
	}
	// Plain transactions do not use the budget.
	if err := pool.addRemoteSync(transaction(2, 100000, key)); err != nil {
		t.Fatalf("plain tx rejected: %v", err)
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internals mismatch: %v", err)
	}
}

func TestConditionalTxsEviction(t *testing.T) {
	t.Parallel()

	pool, key := setupConditionalPool(t, true, DefaultConfig.ConditionalSlots)
	other, _ := crypto.GenerateKey()
	testAddBalance(pool, crypto.PubkeyToAddress(other.PublicKey), big.NewInt(1000000000))

	pending := conditionalTx(0, 2, key)
	queued := conditionalTx(1, 3, key)
	plain := transaction(0, 100000, other)
	for _, tx := range []*types.Transaction{pending, plain} {
		if err := pool.addRemoteSync(tx); err != nil {
			t.Fatalf("tx %x rejected: %v", tx.Hash(), err)
		}
	}
	// Admit the next nonce without a reorg, so it is still queued.
	pool.mu.Lock()
	errs, _ := pool.addTxs([]*types.Transaction{queued}, false)
	pool.mu.Unlock()
	if errs[0] != nil {
		t.Fatalf("queued conditional tx rejected: %v", errs[0])
	}
	if pending, queued := pool.Stats(); pending != 2 || queued != 1 {
		t.Fatalf("before eviction: pending %d, queued %d, want 2 and 1", pending, queued)
	}

	pool.mu.Lock()
	// Within the lifetime nothing is dropped.
	pool.evictConditional(time.Now())
	if pool.all.Count() != 3 {
		pool.mu.Unlock()
		t.Fatalf("fresh txs evicted: %d left, want 3", pool.all.Count())
	}
	// After the lifetime both conditional txs go, pending and queued, and the
	// plain tx of the same age stays.
	evictions := conditionalEvictionMeter.Snapshot().Count()
	pool.evictConditional(time.Now().Add(pool.config.ConditionalLifetime + time.Second))
	pool.mu.Unlock()
	if have := conditionalEvictionMeter.Snapshot().Count() - evictions; have < 2 {
		t.Fatalf("eviction meter: have %d, want at least 2", have)
	}

	if pool.all.Get(pending.Hash()) != nil || pool.all.Get(queued.Hash()) != nil {
		t.Fatal("expired conditional txs still in the pool")
	}
	if pool.all.Get(plain.Hash()) == nil {
		t.Fatal("plain tx evicted")
	}
	if have := pool.all.ConditionalSlots(); have != 0 {
		t.Fatalf("conditional slots after eviction: have %d, want 0", have)
	}
	if err := validatePoolInternals(pool); err != nil {
		t.Fatalf("pool internals mismatch: %v", err)
	}
}

func TestConditionalTxsSanitize(t *testing.T) {
	t.Parallel()

	conf := (&Config{ConditionalLifetime: -1, ConditionalSlots: 0}).sanitize()
	if conf.ConditionalLifetime != DefaultConfig.ConditionalLifetime {
		t.Errorf("lifetime: have %v, want %v", conf.ConditionalLifetime, DefaultConfig.ConditionalLifetime)
	}
	if conf.ConditionalSlots != DefaultConfig.ConditionalSlots {
		t.Errorf("slots: have %d, want %d", conf.ConditionalSlots, DefaultConfig.ConditionalSlots)
	}
	// The smallest valid values are kept.
	conf = (&Config{ConditionalLifetime: 1, ConditionalSlots: 1}).sanitize()
	if conf.ConditionalLifetime != 1 || conf.ConditionalSlots != 1 {
		t.Errorf("smallest valid values changed: lifetime %v, slots %d", conf.ConditionalLifetime, conf.ConditionalSlots)
	}
}

func TestConditionalTxsNonceGap(t *testing.T) {
	t.Parallel()

	pool, key := setupConditionalPool(t, true, DefaultConfig.ConditionalSlots)

	// A gapped conditional tx would sit in the queue, never mined.
	gapped := conditionalGappedMeter.Snapshot().Count()
	if err := pool.addRemoteSync(conditionalTx(1, 1, key)); !errors.Is(err, txpool.ErrConditionalNonceGap) {
		t.Fatalf("gapped tx: have %v, want %v", err, txpool.ErrConditionalNonceGap)
	}
	if conditionalGappedMeter.Snapshot().Count() == gapped {
		t.Fatal("gapped meter not marked")
	}
	if err := pool.addRemoteSync(conditionalTx(0, 1, key)); err != nil {
		t.Fatalf("executable tx rejected: %v", err)
	}
	if err := pool.addRemoteSync(conditionalTx(1, 1, key)); err != nil {
		t.Fatalf("next nonce rejected: %v", err)
	}
}

func TestConditionalTxsAccountLimit(t *testing.T) {
	t.Parallel()

	pool, key := setupConditionalPool(t, true, DefaultConfig.ConditionalSlots)
	other, _ := crypto.GenerateKey()
	testAddBalance(pool, crypto.PubkeyToAddress(other.PublicKey), big.NewInt(1000000000))

	if err := pool.addRemoteSync(conditionalTx(0, 999, key)); err != nil {
		t.Fatalf("first tx rejected: %v", err)
	}
	if err := pool.addRemoteSync(conditionalTx(1, 1, key)); err != nil {
		t.Fatalf("tx up to the account share rejected: %v", err)
	}
	if err := pool.addRemoteSync(conditionalTx(2, 1, key)); !errors.Is(err, txpool.ErrConditionalAccountLimit) {
		t.Fatalf("over the account share: have %v, want %v", err, txpool.ErrConditionalAccountLimit)
	}
	// Other senders keep their own share.
	if err := pool.addRemoteSync(conditionalTx(0, 999, other)); err != nil {
		t.Fatalf("other sender rejected: %v", err)
	}
}

func TestConditionalTxsReplacementAtFullBudget(t *testing.T) {
	t.Parallel()

	pool, key := setupConditionalPool(t, true, 3)

	if err := pool.addRemoteSync(conditionalTx(0, 3, key)); err != nil {
		t.Fatalf("tx rejected: %v", err)
	}
	// A fee bump of the same weight frees the old tx's weight first.
	bump := pricedConditionalTx(0, 3, big.NewInt(2), key)
	if err := pool.addRemoteSync(bump); err != nil {
		t.Fatalf("fee bump at full budget rejected: %v", err)
	}
	if have := pool.all.ConditionalSlots(); have != 3 {
		t.Fatalf("conditional slots: have %d, want 3", have)
	}
	if pool.all.Get(bump.Hash()) == nil {
		t.Fatal("fee bump not in the pool")
	}
}

// A pending conditional tx whose options no longer hold is dropped on reset.
func TestConditionalTxsFilteredOnReset(t *testing.T) {
	t.Parallel()

	pool, key := setupConditionalPool(t, true, DefaultConfig.ConditionalSlots)

	tx := conditionalTx(0, 1, key)
	tx.GetOptions().BlockNumberMax = big.NewInt(0)
	if err := pool.addRemoteSync(tx); err != nil {
		t.Fatalf("tx rejected: %v", err)
	}
	// Move the head past the tx's block range, then reset.
	head := types.CopyHeader(pool.currentHead.Load())
	head.Number = big.NewInt(1)
	pool.currentHead.Store(head)
	pool.mu.Lock()
	pool.demoteUnexecutables()
	pool.mu.Unlock()

	if pool.all.Get(tx.Hash()) != nil {
		t.Fatal("conditional tx with an expired block range still pooled")
	}
	if have := pool.all.ConditionalSlots(); have != 0 {
		t.Fatalf("conditional slots: have %d, want 0", have)
	}
}
