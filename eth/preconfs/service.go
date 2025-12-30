package preconfs

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
)

const (
	expiryTickerInterval = time.Minute
	expiryInterval       = 10 * time.Minute
	maxQueuedTasks       = 10_000
	maxConcurrentTasks   = 1024
)

var (
	taskProcessingTimer = metrics.NewRegisteredTimer("preconfs/processing", nil)
	taskStatusTimer     = metrics.NewRegisteredTimer("preconfs/status", nil)
	taskReadTimer       = metrics.NewRegisteredTimer("preconfs/dbread", nil)

	uniquePreconfsTaskMeter = metrics.NewRegisteredMeter("preconfs/tasks", nil)
	validPreconfsMeter      = metrics.NewRegisteredMeter("preconfs/valid", nil)
	invalidPreconfsMeter    = metrics.NewRegisteredMeter("preconfs/invalid", nil)
)

type Task struct {
	tx         *types.Transaction
	sender     common.Address
	result     bool
	insertedAt time.Time
	err        error
}

type Service struct {
	mc        *multiClient         // rpc client to interact with block producers
	store     map[common.Hash]Task // cache for preconf results
	storeMu   sync.RWMutex
	taskCh    chan Task // channel to queue new tasks
	close     chan struct{}
	semaphore chan struct{} // to limit concurrent tasks

	waitForCanonicalTxGetter chan struct{}
	canonicalTxGetter        func(common.Hash) (bool, *types.Transaction, common.Hash, uint64, uint64)

	// metric collection
	totalProcessed atomic.Uint64
	totalValid     atomic.Uint64
	totalInvalid   atomic.Uint64
}

func NewPreconfService(urls []string) *Service {
	s := &Service{
		mc:                       newMultiClient(urls),
		store:                    make(map[common.Hash]Task, 1024),
		taskCh:                   make(chan Task, maxQueuedTasks),
		close:                    make(chan struct{}),
		semaphore:                make(chan struct{}, maxConcurrentTasks),
		waitForCanonicalTxGetter: make(chan struct{}),
	}
	go func() {
		<-s.waitForCanonicalTxGetter
		go s.processTasks()
		go s.cleanup()
		go s.report()
	}()
	return s
}

// SetCanonicalTxGetter sets the function to get canonical transaction by hash which is needed
// for processing preconf tasks. It can only be called once.
func (s *Service) SetCanonicalTxGetter(f func(common.Hash) (bool, *types.Transaction, common.Hash, uint64, uint64)) {
	s.waitForCanonicalTxGetter <- struct{}{}
	s.canonicalTxGetter = f
}

// QueuePreconfTask creates and adds a new preconf task to the processing queue. Returns
// immediately unless the buffered task queue is full.
func (s *Service) QueuePreconfTask(tx *types.Transaction, sender common.Address) {
	if s.mc == nil {
		return
	}
	select {
	case s.taskCh <- Task{tx: tx, sender: sender}:
	case <-s.close:
		log.Debug("Dropping preconf task")
	}
}

// processTasks is a background routine which sends queued preconf tasks
// for processing. It limits the number of concurrent tasks using a semaphore.
func (s *Service) processTasks() {
	for {
		select {
		case task := <-s.taskCh:
			s.semaphore <- struct{}{}
			go func(task Task) {
				defer func() { <-s.semaphore }()
				uniquePreconfsTaskMeter.Mark(1)
				s.processTask(task, false)
			}(task)
		case <-s.close:
			return
		}
	}
}

// processTask processes a single preconf task and populates the result. It uses
// multi-client to check for presence of transaction.
func (s *Service) processTask(task Task, reprocess bool) {
	// Ensure the tx is not already included in canonical chain
	start := time.Now()
	found, _, _, _, _ := s.canonicalTxGetter(task.tx.Hash())
	taskReadTimer.UpdateSince(start)
	if found {
		task.result = true
		if !reprocess {
			task.insertedAt = time.Now()
		}
		s.storeMu.Lock()
		s.store[task.tx.Hash()] = task
		s.storeMu.Unlock()
		return
	}

	// Tx still not included, check against block producers mempool
	start = time.Now()
	res, err := s.mc.checkTxInclusionInMempool(task.tx, task.sender)
	taskProcessingTimer.UpdateSince(start)
	s.totalProcessed.Add(1)
	if res {
		validPreconfsMeter.Mark(1)
		s.totalValid.Add(1)
	} else {
		invalidPreconfsMeter.Mark(1)
		s.totalInvalid.Add(1)
	}
	task.result = res
	if !reprocess {
		task.insertedAt = time.Now()
	}
	task.err = err
	s.storeMu.Lock()
	s.store[task.tx.Hash()] = task
	s.storeMu.Unlock()
}

// CheckTxPreconfStatus checks the preconfirmation status of a transaction by given hash
// against the cache.
func (s *Service) CheckTxPreconfStatus(hash common.Hash) (bool, error) {
	start := time.Now()
	defer func() {
		taskStatusTimer.UpdateSince(start)
	}()

	s.storeMu.RLock()
	defer s.storeMu.RUnlock()

	if task, ok := s.store[hash]; ok {
		if task.result {
			return true, nil
		} else {
			log.Info("[preconfs] re-processing preconf task", "hash", hash, "err", task.err)
			s.storeMu.RUnlock()
			s.processTask(task, true)
			s.storeMu.RLock()
			if task, ok = s.store[hash]; ok {
				if task.result {
					return true, nil
				}
				return false, errors.New("failed to validate transaction inclusion status for issuing preconf")
			} else {
				// result pruned
				return false, errors.New("unable to find preconf task associated with transaction hash")
			}
		}
	}

	// either such task for given transaction hash doesn't exist, or task is not processed
	// yet, or is deleted.
	return false, errors.New("unable to find preconf task associated with transaction hash")
}

func (s *Service) DeleteTaskEntry(hash common.Hash) {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	delete(s.store, hash)
}

// cleanup is a periodic routine to delete old preconf results
func (s *Service) cleanup() {
	ticker := time.NewTicker(expiryTickerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.storeMu.Lock()
			now := time.Now()
			for hash, task := range s.store {
				if now.Sub(task.insertedAt) > expiryInterval {
					delete(s.store, hash)
				}
			}
			s.storeMu.Unlock()
		case <-s.close:
			return
		}
	}
}

// report logs periodic stats about the preconf service
func (s *Service) report() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.storeMu.RLock()
			storeSize := len(s.store)
			s.storeMu.RUnlock()
			log.Info("[preconfs] stats", "cache", storeSize, "queued", len(s.taskCh), "processed", s.totalProcessed.Load(), "valid", s.totalValid.Load(), "invalid", s.totalInvalid.Load())
			s.totalProcessed.Store(0)
			s.totalValid.Store(0)
			s.totalInvalid.Store(0)
		case <-s.close:
			return
		}
	}
}

func (s *Service) Close() {
	close(s.close)
	s.mc.Close()
}
