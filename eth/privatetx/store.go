package privatetx

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
)

var (
	privateTxRebroadcastTimer = 10 * time.Second
	privateTxRebroadcastAge   = 10 * time.Second
)

type PrivateTxGetter interface {
	IsTxPrivate(hash common.Hash) bool
	SubscribePrivateTxsRebroadcast(ch chan<- core.PrivateTxsEvent) event.Subscription
}

type PrivateTxSetter interface {
	Add(hash common.Hash)
	Purge(hash common.Hash)
}

type PrivateTxStore struct {
	txs map[common.Hash]time.Time // tx hash to last updated time
	mu  sync.RWMutex

	rebroadcastFeed event.Feed
	chainEventSubFn func(ch chan<- core.ChainEvent) event.Subscription

	// metrics
	txsAdded         atomic.Uint64
	txsPurged        atomic.Uint64
	txsRebroadcasted atomic.Uint64

	closeCh chan struct{}
}

func NewPrivateTxStore() *PrivateTxStore {
	store := &PrivateTxStore{
		txs: make(map[common.Hash]time.Time),
	}
	go store.rebroadcastLoop()
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

func (s *PrivateTxStore) SubscribePrivateTxsRebroadcast(ch chan<- core.PrivateTxsEvent) event.Subscription {
	return s.rebroadcastFeed.Subscribe(ch)
}

func (s *PrivateTxStore) rebroadcastLoop() {
	rebroadcastTimer := time.NewTicker(privateTxRebroadcastTimer)
	defer rebroadcastTimer.Stop()

	for {
		select {
		case <-rebroadcastTimer.C:
			var txsToRebroadcast []common.Hash
			now := time.Now()
			s.mu.Lock()
			for hash, lastUpdatedTime := range s.txs {
				if now.Sub(lastUpdatedTime) > privateTxRebroadcastAge {
					txsToRebroadcast = append(txsToRebroadcast, hash)
					s.txs[hash] = now
				}
			}
			s.txsRebroadcasted.Add(uint64(len(txsToRebroadcast)))
			s.mu.Unlock()
			s.rebroadcastFeed.Send(txsToRebroadcast)
		case <-s.closeCh:
			return
		}
	}
}

func (s *PrivateTxStore) cleanupLoop() {
	for {
		if err := s.cleanup(); err != nil {
			log.Debug("Error cleaning up private tx store, restarting", "err", err)
			time.Sleep(time.Second)
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
			for _, tx := range event.Transactions {
				delete(s.txs, tx.Hash())
			}
			s.txsPurged.Add(uint64(len(event.Transactions)))
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
	if s.chainEventSubFn != nil {
		s.chainEventSubFn = fn
		go s.cleanupLoop()
	}
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
			log.Info("[private-tx-relay] stats", "len", storeSize, "added", s.txsAdded.Load(), "purged", s.txsPurged.Load(), "rebroadcasted", s.txsRebroadcasted.Load())
			s.txsAdded.Store(0)
			s.txsPurged.Store(0)
			s.txsRebroadcasted.Store(0)
		case <-s.closeCh:
			return
		}
	}

}

func (s *PrivateTxStore) Close() {
	close(s.closeCh)
}
