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
	privateTxTTL         = 10 * time.Minute // hard TTL: remove from store after this duration regardless
	privateTxGracePeriod = 2 * time.Minute  // min age before txpool presence check applies
	sweepInterval        = 1 * time.Minute  // how often the sweep goroutine runs
)

var totalPrivateTxsMeter = metrics.NewRegisteredMeter("privatetxs/count", nil)

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
	txs map[common.Hash]time.Time // tx hash to last updated time
	mu  sync.RWMutex

	chainEventSubFn func(ch chan<- core.ChainEvent) event.Subscription
	txPoolChecker   TxPoolChecker

	// metrics
	txsAdded   atomic.Uint64
	txsPurged  atomic.Uint64 // deleted by an explicit call
	txsDeleted atomic.Uint64 // deleted because tx got included
	txsExpired atomic.Uint64 // deleted by sweep (txpool eviction or TTL)

	closeCh chan struct{}
}

func NewPrivateTxStore() *PrivateTxStore {
	store := &PrivateTxStore{
		txs:     make(map[common.Hash]time.Time),
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
	s.txsPurged.Add(1)
}

func (s *PrivateTxStore) IsTxPrivate(hash common.Hash) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if _, ok := s.txs[hash]; ok {
		return true
	}

	return false
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

// sweep periodically removes stale entries from the store. It uses two strategies:
// 1. Txpool check: if the tx is no longer in the txpool and old enough, remove it.
// 2. TTL backstop: unconditionally remove entries older than privateTxTTL.
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
func (s *PrivateTxStore) sweepOnce() {
	s.mu.Lock()
	defer s.mu.Unlock()

	expired := uint64(0)
	now := time.Now()
	for hash, addedAt := range s.txs {
		age := now.Sub(addedAt)
		if age > privateTxTTL {
			// Hard TTL: remove regardless of txpool status
			delete(s.txs, hash)
			expired++
		} else if age > privateTxGracePeriod && s.txPoolChecker != nil && !s.txPoolChecker(hash) {
			// Tx no longer in txpool and past grace period: remove
			delete(s.txs, hash)
			expired++
		}
	}
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
			totalPrivateTxsMeter.Mark(int64(storeSize))
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
