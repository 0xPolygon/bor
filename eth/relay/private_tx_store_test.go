package relay

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/stretchr/testify/require"
)

// TestNewPrivateTxStore tests store initialization
func TestNewPrivateTxStore(t *testing.T) {
	t.Parallel()

	store := NewPrivateTxStore()
	if store == nil {
		t.Fatal("expected non-nil store")
	}
	defer store.Close()

	require.Empty(t, store.txs, "expected store to be empty initially")
}

// TestPrivateTxStoreOperations tests basic operations of the store
// like adding, purging and reading transactions.
func TestPrivateTxStoreOperations(t *testing.T) {
	t.Parallel()

	store := NewPrivateTxStore()
	defer store.Close()

	// Ensure store is empty initially
	require.Empty(t, store.txs, "expected store to be empty initially")

	// Add a few transactions
	hash1 := common.HexToHash("0x1")
	store.Add(hash1)
	require.Len(t, store.txs, 1, "expected store to have 1 transaction after Add")
	hash2 := common.HexToHash("0x2")
	store.Add(hash2)
	require.Len(t, store.txs, 2, "expected store to have 2 transactions after Add")
	hash3 := common.HexToHash("0x3")
	store.Add(hash3)
	require.Len(t, store.txs, 3, "expected store to have 3 transactions after Add")

	// Ensure metrics are correctly reported
	require.Equal(t, uint64(3), store.txsAdded.Load(), "expected txsAdded metric to be 3")
	require.Equal(t, uint64(0), store.txsPurged.Load(), "expected txsPurged metric to be 0")

	// Query all transactions
	require.True(t, store.IsTxPrivate(hash1), "expected hash1 to be private")
	require.True(t, store.IsTxPrivate(hash2), "expected hash2 to be private")
	require.True(t, store.IsTxPrivate(hash3), "expected hash3 to be private")
	unknownHash := common.HexToHash("0x4")
	require.False(t, store.IsTxPrivate(unknownHash), "expected unknownHash not to be private")

	// Purge
	store.Purge(hash2)
	require.Len(t, store.txs, 2, "expected store to have 2 transactions after Purge")
	require.False(t, store.IsTxPrivate(hash2), "expected hash2 not to be private after Purge")
	require.Equal(t, uint64(1), store.txsPurged.Load(), "expected txsPurged metric to be 1")

	// Purging same hash should not panic
	store.Purge(hash2)
	require.Len(t, store.txs, 2, "expected store to still have 2 transactions after purging non-existent hash")
}

// TestPrivateTxStoreConcurrentOperations tests concurrent operations on the store
func TestPrivateTxStoreConcurrentOperations(t *testing.T) {
	t.Parallel()

	store := NewPrivateTxStore()
	defer store.Close()

	var wg sync.WaitGroup
	numGoroutines := 100
	numOpsPerGoroutine := 100

	// Concurrent adds, checks, and purges
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < numOpsPerGoroutine; j++ {
				hash := common.BigToHash(big.NewInt(int64(id*numOpsPerGoroutine + j)))

				store.Add(hash)
				require.Equal(t, true, store.IsTxPrivate(hash), "expected hash to be private after Add")
				if j%2 == 0 {
					store.Purge(hash)
					require.Equal(t, false, store.IsTxPrivate(hash), "expected hash not to be private after Purge")
				}
			}
		}(i)
	}

	wg.Wait()
	// Should not panic or race

	require.Equal(t, numGoroutines*numOpsPerGoroutine/2, len(store.txs), "expected total count of private txs to match")
}

// mockChainEventGenerator simulates chain events for testing
type mockChainEventGenerator struct {
	ch  chan core.ChainEvent
	sub *mockSubscription
}

type mockSubscription struct {
	errCh chan error
}

func (s *mockSubscription) Unsubscribe() {
	close(s.errCh)
}

func (s *mockSubscription) Err() <-chan error {
	return s.errCh
}

func newMockChainEventGenerator() *mockChainEventGenerator {
	return &mockChainEventGenerator{
		ch: make(chan core.ChainEvent, 10),
		sub: &mockSubscription{
			errCh: make(chan error),
		},
	}
}

func (m *mockChainEventGenerator) subscribe(ch chan<- core.ChainEvent) event.Subscription {
	// Forward events from our internal channel to the subscriber
	go func() {
		for event := range m.ch {
			ch <- event
		}
	}()
	return m.sub
}

func (m *mockChainEventGenerator) sendEvent(event core.ChainEvent) {
	m.ch <- event
}

func (m *mockChainEventGenerator) close() {
	close(m.ch)
}

// TestPrivateTxStoreCleanup tests automatic cleanup of store on
// receiving new chain events via subscription.
func TestPrivateTxStoreCleanup(t *testing.T) {
	t.Parallel()

	store := NewPrivateTxStore()
	defer store.Close()

	// Check that there's no subscription initially
	require.Nil(t, store.chainEventSubFn, "chainEventSubFn should be nil initially")

	// Explicitly start cleanup process without setting the chain event subscription function
	err := store.cleanup()
	require.Error(t, err, "expected error when doing cleanup without chain event subscription function")

	// Create mock chain event generator
	mockGen := newMockChainEventGenerator()
	defer mockGen.close()

	// Set the chain event subscription function to start cleanup routine
	// in background.
	store.SetchainEventSubFn(mockGen.subscribe)

	// Create some mock transactions
	tx1 := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
	tx2 := types.NewTransaction(2, common.Address{}, nil, 0, nil, nil)
	tx3 := types.NewTransaction(3, common.Address{}, nil, 0, nil, nil)
	store.Add(tx1.Hash())
	store.Add(tx2.Hash())
	store.Add(tx3.Hash())

	require.Equal(t, true, store.IsTxPrivate(tx1.Hash()), "expected tx1 to be in store")
	require.Equal(t, true, store.IsTxPrivate(tx2.Hash()), "expected tx2 to be in store")
	require.Equal(t, true, store.IsTxPrivate(tx3.Hash()), "expected tx3 to be in store")

	// Create a chain event including some transactions
	tx4 := types.NewTransaction(4, common.Address{}, nil, 0, nil, nil)
	mockGen.sendEvent(core.ChainEvent{
		Transactions: types.Transactions{tx2, tx4},
	})

	// Give the cleanup goroutine time to process
	time.Sleep(100 * time.Millisecond)

	// Confirm that tx2 is removed from the store
	require.Equal(t, false, store.IsTxPrivate(tx2.Hash()), "expected tx2 to be removed from store")

	// Confirm that tx1 and tx3 are still present
	require.Equal(t, true, store.IsTxPrivate(tx1.Hash()), "expected tx1 to still be in store")
	require.Equal(t, true, store.IsTxPrivate(tx3.Hash()), "expected tx3 to still be in store")

	// Ensure metrics are correctly reported
	require.Equal(t, uint64(1), store.txsDeleted.Load(), "expected txsDeleted metric to be 1")
}

func TestPrivateTxStoreSweepPoolRetention(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		age         time.Duration
		checker     bool
		inPool      bool
		wantPrivate bool
	}{
		{"fresh absent", time.Minute, true, false, true},
		{"absent past grace", 3 * time.Minute, true, false, false},
		{"absent past former TTL", 11 * time.Minute, true, false, false},
		{"present past former TTL", 11 * time.Minute, true, true, true},
		{"present for a day", 24 * time.Hour, true, true, true},
		{"unknown past grace", 3 * time.Minute, false, false, true},
		{"unknown past former TTL", 11 * time.Minute, false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewPrivateTxStore()
			defer store.Close()
			hash := common.HexToHash("0x1")
			store.Add(hash)
			store.mu.Lock()
			store.txs[hash] = time.Now().Add(-test.age)
			store.mu.Unlock()
			if test.checker {
				store.SetTxPoolChecker(func(common.Hash) bool { return test.inPool })
			}
			store.sweepOnce()
			require.Equal(t, test.wantPrivate, store.IsTxPrivate(hash))
			if test.wantPrivate {
				require.Zero(t, store.txsExpired.Load())
			} else {
				require.Equal(t, uint64(1), store.txsExpired.Load())
			}
		})
	}
}

func TestPrivateTxStoreSweepCheckerAvailability(t *testing.T) {
	t.Parallel()
	store := NewPrivateTxStore()
	defer store.Close()
	hash := common.HexToHash("0x1")
	store.Add(hash)
	store.mu.Lock()
	store.txs[hash] = time.Now().Add(-time.Hour)
	store.mu.Unlock()

	store.sweepOnce()
	require.True(t, store.IsTxPrivate(hash))
	store.SetTxPoolChecker(func(common.Hash) bool { return true })
	store.sweepOnce()
	require.True(t, store.IsTxPrivate(hash))
	store.SetTxPoolChecker(nil)
	store.sweepOnce()
	require.True(t, store.IsTxPrivate(hash))
	store.SetTxPoolChecker(func(common.Hash) bool { return false })
	store.sweepOnce()
	require.False(t, store.IsTxPrivate(hash))
	require.Equal(t, uint64(1), store.txsExpired.Load())
}

func TestPrivateTxStoreSweepPreservesRefresh(t *testing.T) {
	t.Parallel()
	for name, purge := range map[string]bool{"refresh": false, "purge and re-add": true} {
		t.Run(name, func(t *testing.T) {
			store := NewPrivateTxStore()
			defer store.Close()
			hash := common.HexToHash("0x1")
			store.Add(hash)
			store.mu.Lock()
			store.txs[hash] = time.Now().Add(-3 * time.Minute)
			store.mu.Unlock()

			checked, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(release)
			store.SetTxPoolChecker(func(common.Hash) bool {
				close(checked)
				<-release
				return false
			})
			go func() {
				store.sweepOnce()
				close(done)
			}()
			select {
			case <-checked:
			case <-time.After(time.Second):
				t.Fatal("sweep did not check the pool")
			}
			if purge {
				store.Purge(hash)
			}
			store.Add(hash)
			release <- struct{}{}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("sweep did not finish")
			}
			require.True(t, store.IsTxPrivate(hash))
			require.Zero(t, store.txsExpired.Load())
		})
	}
}

func TestPrivateTxStoreSweepConcurrentChecker(t *testing.T) {
	t.Parallel()
	store := NewPrivateTxStore()
	defer store.Close()
	hash := common.HexToHash("0x1")
	store.Add(hash)
	store.mu.Lock()
	store.txs[hash] = time.Now().Add(-3 * time.Minute)
	store.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 1000 {
			store.SetTxPoolChecker(func(common.Hash) bool { return true })
			store.SetTxPoolChecker(nil)
		}
	}()
	for range 1000 {
		store.sweepOnce()
	}
	wg.Wait()
	require.True(t, store.IsTxPrivate(hash))
}

// TestPrivateTxStoreSetTxPoolChecker tests the SetTxPoolChecker method.
func TestPrivateTxStoreSetTxPoolChecker(t *testing.T) {
	t.Parallel()

	store := NewPrivateTxStore()
	defer store.Close()

	require.Nil(t, store.txPoolChecker, "txPoolChecker should be nil initially")

	checker := func(hash common.Hash) bool { return true }
	store.SetTxPoolChecker(checker)
	require.NotNil(t, store.txPoolChecker, "txPoolChecker should be set after calling SetTxPoolChecker")
}
