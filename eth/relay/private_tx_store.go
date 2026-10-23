package relay

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

const (
	privateTxTTL         = 30 * time.Minute // release stalled transactions for public propagation
	privateTxGracePeriod = 2 * time.Minute  // min age before txpool presence check applies
	sweepInterval        = 1 * time.Minute  // how often the sweep goroutine runs
)

var privateTxStoreSizeGauge = metrics.NewRegisteredGauge("relay/privatetx/store/size", nil)

type PrivateTxGetter interface {
	IsTxPrivate(hash common.Hash) bool
}

type PrivateTxSetter interface {
	Add(hash common.Hash)
	Purge(hash common.Hash)
}

// TxPoolChecker returns true if the given tx hash is currently in the txpool.
type TxPoolChecker func(hash common.Hash) bool

type PrivateTxStore struct {
	txs     map[common.Hash]time.Time // committed tx hash to last updated time
	pending map[common.Hash]*privateTxReservation
	mu      sync.RWMutex

	chainEventSubFn func(ch chan<- core.ChainEvent) event.Subscription
	txPoolChecker   TxPoolChecker

	// metrics
	txsAdded   atomic.Uint64
	txsPurged  atomic.Uint64 // deleted by an explicit call
	txsDeleted atomic.Uint64 // deleted because tx got included
	txsExpired atomic.Uint64 // deleted by sweep after expiry or txpool eviction

	closeCh chan struct{}
}

func NewPrivateTxStore() *PrivateTxStore {
	store := &PrivateTxStore{
		txs:     make(map[common.Hash]time.Time),
		pending: make(map[common.Hash]*privateTxReservation),
		closeCh: make(chan struct{}),
	}
	go store.report()
	go store.sweep()
	return store
}

func (s *PrivateTxStore) Add(hash common.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.txs[hash] = time.Now()
	s.txsAdded.Add(1)
}

func (s *PrivateTxStore) Purge(hash common.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.txs, hash)
	delete(s.pending, hash)
	s.txsPurged.Add(1)
}

func (s *PrivateTxStore) IsTxPrivate(hash common.Hash) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.txs[hash]; ok {
		return true
	}

	return s.pending[hash] != nil
}

func (s *PrivateTxStore) cleanupLoop() {
	for {
		if err := s.cleanup(); err != nil {
			log.Debug("Error cleaning up private tx store, restarting", "err", err)
			select {
			case <-s.closeCh:
				return
			case <-time.After(time.Second):
			}
		} else {
			break
		}
	}
}

func (s *PrivateTxStore) cleanup() error {
	if s.chainEventSubFn == nil {
		return fmt.Errorf("private tx store: chain event subscription not set")
	}

	var chainEventCh = make(chan core.ChainEvent)
	chainEventSub := s.chainEventSubFn(chainEventCh)

	for {
		select {
		case event := <-chainEventCh:
			s.mu.Lock()
			deleted := uint64(0)
			for _, tx := range event.Transactions {
				delete(s.pending, tx.Hash())
				if _, exists := s.txs[tx.Hash()]; exists {
					deleted++
					delete(s.txs, tx.Hash())
				}
			}
			s.txsDeleted.Add(deleted)
			s.mu.Unlock()
		case err := <-chainEventSub.Err():
			return err
		case <-s.closeCh:
			chainEventSub.Unsubscribe()
			return nil
		}
	}
}

func (s *PrivateTxStore) SetchainEventSubFn(fn func(ch chan<- core.ChainEvent) event.Subscription) {
	if fn != nil && s.chainEventSubFn == nil {
		s.chainEventSubFn = fn
		go s.cleanupLoop()
	}
}

func (s *PrivateTxStore) SetTxPoolChecker(checker TxPoolChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.txPoolChecker = checker
}

// Release stalled private transactions without removing them from the pool.
func (s *PrivateTxStore) sweep() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.sweepOnce()
		case <-s.closeCh:
			return
		}
	}
}

// sweepOnce performs one pass of the sweep logic. Extracted from sweep so tests
// can invoke the real eviction logic deterministically without waiting on a ticker.
// The work is performed in three phases to minimise lock contention.
func (s *PrivateTxStore) sweepOnce() {
	type entry struct {
		hash    common.Hash
		addedAt time.Time
	}

	// Snapshot under the read lock.
	s.mu.RLock()
	checker := s.txPoolChecker
	entries := make([]entry, 0, len(s.txs))
	for h, t := range s.txs {
		entries = append(entries, entry{h, t})
	}
	s.mu.RUnlock()

	// Check pool presence outside the lock. TTL expiry also releases transactions
	// that remain pending after all producer retries have failed.
	now := time.Now()
	toDelete := make([]entry, 0)
	for _, entry := range entries {
		age := now.Sub(entry.addedAt)
		if privateTxExpired(age, checker, entry.hash) {
			toDelete = append(toDelete, entry)
		}
	}
	if len(toDelete) == 0 {
		return
	}

	// Pending admissions keep ownership; successful admissions refresh addedAt.
	expired := uint64(0)
	s.mu.Lock()
	for _, entry := range toDelete {
		if s.pending[entry.hash] != nil {
			continue
		}
		if cur, ok := s.txs[entry.hash]; ok && cur.Equal(entry.addedAt) {
			delete(s.txs, entry.hash)
			expired++
		}
	}
	s.mu.Unlock()
	s.txsExpired.Add(expired)
}

func (s *PrivateTxStore) report() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.mu.RLock()
			storeSize := len(s.txs)
			s.mu.RUnlock()
			privateTxStoreSizeGauge.Update(int64(storeSize))
			log.Info("[private-tx-store] stats", "len", storeSize, "added", s.txsAdded.Load(), "purged", s.txsPurged.Load(), "deleted", s.txsDeleted.Load(), "expired", s.txsExpired.Load())
			s.txsAdded.Store(0)
			s.txsPurged.Store(0)
			s.txsDeleted.Store(0)
			s.txsExpired.Store(0)
		case <-s.closeCh:
			return
		}
	}
}

func (s *PrivateTxStore) Close() {
	close(s.closeCh)
}

func privateTxExpired(age time.Duration, checker TxPoolChecker, hash common.Hash) bool {
	return age >= privateTxTTL || (age > privateTxGracePeriod && checker != nil && !checker(hash))
}
