package preconfs

import (
	"errors"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

const (
	expiryTickerInterval = time.Minute
	expiryInterval       = 10 * time.Minute
	maxQueuedTasks       = 10_000
	maxConcurrentTasks   = 1024
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
}

func NewPreconfService(urls []string) *Service {
	s := &Service{
		mc:        newMultiClient(urls),
		store:     make(map[common.Hash]Task, 1024),
		taskCh:    make(chan Task, maxQueuedTasks),
		close:     make(chan struct{}),
		semaphore: make(chan struct{}, maxConcurrentTasks),
	}
	go s.processTasks()
	go s.cleanup()
	return s
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
	res, err := s.mc.checkTxInclusionInMempool(task.tx, task.sender)
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
	s.storeMu.RLock()
	defer s.storeMu.RUnlock()

	if task, ok := s.store[hash]; ok {
		if task.result && task.err == nil {
			return true, nil
		}
		if !task.result || task.err != nil {
			log.Info("[preconfs] re-processing preconf task", "hash", hash, "err", task.err)
			s.storeMu.RUnlock()
			s.processTask(task, true)
			s.storeMu.RLock()
			if task, ok = s.store[hash]; ok {
				if task.result {
					return true, nil
				}
				return task.result, errors.New("failed to validate transaction inclusion status for issuing preconf")
			} else {
				return false, errors.New("preconf result pruned")
			}
		}

		return task.result, task.err
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

func (s *Service) Close() {
	close(s.close)
	s.mc.Close()
}
