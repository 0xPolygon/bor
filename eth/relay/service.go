package relay

import (
	"errors"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

var (
	errRpcClientUnavailable      = errors.New("rpc client unavailable to submit transactions")
	errQueueOverflow             = errors.New("relay task queue overflow")
	errPreconfValidationFailed   = errors.New("failed to validate transaction inclusion status for issuing preconf")
	errPrivateTxSubmissionFailed = errors.New("private tx submission failed partially, background retry scheduled")
)

// TxGetter defines a function that retrieves a transaction by its hash from local database.
// Returns: found (bool), transaction, blockHash, blockNumber, txIndex
type TxGetter func(hash common.Hash) (bool, *types.Transaction, common.Hash, uint64, uint64)

type ServiceConfig struct {
	expiryTickerInterval time.Duration
	expiryInterval       time.Duration
	maxQueuedTasks       int
	maxConcurrentTasks   int
}

var DefaultServiceConfig = ServiceConfig{
	expiryTickerInterval: time.Minute,
	expiryInterval:       10 * time.Minute,
	maxQueuedTasks:       40_000,
	maxConcurrentTasks:   1024,
}

// TxTask represents a transaction submission task
type TxTask struct {
	rawtx      []byte
	hash       common.Hash
	insertedAt time.Time

	preconfirmed bool // whether block producer preconfirmed the tx or not
	err          error
}

type Service struct {
	config      *ServiceConfig
	multiclient *multiClient
	store       map[common.Hash]TxTask
	storeMu     sync.RWMutex
	taskCh      chan TxTask // channel to queue new tasks
	semaphore   chan struct{}
	closeCh     chan struct{} // to limit concurrent tasks

	txGetter TxGetter // function to get transaction from local database
}

func NewService(urls []string, config *ServiceConfig) *Service {
	if config == nil {
		defaultConfig := DefaultServiceConfig
		config = &defaultConfig
	}
	s := &Service{
		config:      config,
		multiclient: newMultiClient(urls),
		store:       make(map[common.Hash]TxTask),
		taskCh:      make(chan TxTask, config.maxQueuedTasks),
		semaphore:   make(chan struct{}, config.maxConcurrentTasks),
		closeCh:     make(chan struct{}),
	}
	go s.processPreconfTasks()
	go s.cleanup()
	return s
}

// SetTxGetter sets the transaction getter function for querying local database
func (s *Service) SetTxGetter(getter TxGetter) {
	s.txGetter = getter
}

// SubmitTransactionForPreconf attempts to queue a transaction submission task for preconf
// and returns true if the task is successfully queued. It fails if either the rpc clients
// are unavailable or if the task queue is full.
func (s *Service) SubmitTransactionForPreconf(tx *types.Transaction) error {
	if s.multiclient == nil {
		log.Warn("[tx-relay] No rpc client available to submit transactions")
		return errRpcClientUnavailable
	}

	rawTx, err := tx.MarshalBinary()
	if err != nil {
		log.Warn("[tx-relay] Failed to marshal transaction", "hash", tx.Hash(), "err", err)
		return err
	}

	// First check if service is closed/closing
	select {
	case <-s.closeCh:
		log.Info("[tx-relay] Dropping task, service closing", "hash", tx.Hash())
		return errRpcClientUnavailable
	default:
	}

	// Queue for processing (non-blocking until queue is full)
	select {
	case s.taskCh <- TxTask{rawtx: rawTx, hash: tx.Hash()}:
		return nil
	default:
		log.Info("[tx-relay] Task queue full, dropping transaction", "hash", tx.Hash())
		return errQueueOverflow
	}
}

// processPreconfTasks continuously picks new tasks from the queue and
// processes them. It rate limits the number of parallel tasks.
func (s *Service) processPreconfTasks() {
	for {
		select {
		case task := <-s.taskCh:
			// Acquire semaphore to limit concurrent submissions
			s.semaphore <- struct{}{}
			go func(task TxTask) {
				defer func() { <-s.semaphore }()
				s.processPreconfTask(task)
			}(task)
		case <-s.closeCh:
			return
		}
	}
}

// processPreconfTask submits the preconf transaction from the task to the block
// producers via multiclient and updates the status in cache.
func (s *Service) processPreconfTask(task TxTask) {
	res, err := s.multiclient.submitPreconfTx(task.rawtx)
	// It's possible that the calls succeeded but preconf was not offered in which
	// case err would be nil. Update with a generic error as preconf wasn't offered.
	if !res && err == nil {
		err = errPreconfValidationFailed
	}
	if err != nil {
		log.Warn("[tx-relay] failed to submit preconf tx", "err", err)
	}
	task.preconfirmed = res
	task.err = err
	// Note: We can purge the raw tx here to save memory. Keeping it
	// incase we have some changes in the retry logic.

	s.updateTaskInCache(task)
}

// updateTaskInCache safely updates or inserts a task in cache by acting as a
// common gateway. A race condition can happen when the process task function
// and check preconf status function try to update the same task concurrently.
// It also ensures that a preconf status once marked is never reverted and
// latest error is preserved. Returns the latest preconf status and error.
func (s *Service) updateTaskInCache(newTask TxTask) (bool, error) {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()

	existingTask, exists := s.store[newTask.hash]
	if !exists {
		// Task doesn't exist, create it and update the cache
		newTask.insertedAt = time.Now()
		s.store[newTask.hash] = newTask
		return newTask.preconfirmed, newTask.err
	}

	// If a task already exists and is preconfirmed, skip doing any updates. It
	// is possible that first write tries to set preconfirmation status but second
	// write contains an error thus making status false. We don't want to revert
	// the status in that case.
	if existingTask.preconfirmed {
		return existingTask.preconfirmed, existingTask.err
	}

	existingTask.preconfirmed = newTask.preconfirmed
	existingTask.err = newTask.err
	s.store[newTask.hash] = existingTask
	return existingTask.preconfirmed, existingTask.err
}

// CheckTxPreconfStatus checks whether a given transaction hash has been preconfirmed
// or not. It checks things in following order:
// - Checks the availability of preconf status of the task in cache
// - Checks locally if the transaction is already included in a block
// - Queries all block producers via multiclient to get the preconf status
func (s *Service) CheckTxPreconfStatus(hash common.Hash) (bool, error) {
	s.storeMu.RLock()
	task, exists := s.store[hash]
	s.storeMu.RUnlock()

	// If task exists in cache and is already preconfirmed, return immediately
	if exists && task.preconfirmed {
		return true, nil
	}

	// If task is not in cache or not preconfirmed, check locally if the tx
	// was included in a block or not.
	if s.txGetter != nil {
		found, tx, _, _, _ := s.txGetter(hash)
		if found && tx != nil {
			s.updateTaskInCache(TxTask{hash: hash, preconfirmed: true, err: nil})
			log.Debug("[tx-relay] Transaction found in local database", "hash", hash)
			return true, nil
		}
	}

	if s.multiclient == nil {
		return false, errRpcClientUnavailable
	}

	// If tx not found locally, query block producers for status
	res, err := s.multiclient.checkTxStatus(hash)
	// It's possible that the calls succeeded but preconf was not offered in which
	// case err would be nil. Update with a generic error as preconf wasn't offered.
	if !res && err == nil {
		err = errPreconfValidationFailed
	}

	// Update the task in cache and return the latest status
	res, err = s.updateTaskInCache(TxTask{hash: hash, preconfirmed: res, err: err})
	if err != nil {
		log.Info("[tx-relay] Unable to validate tx status for preconf", "err", task.err)
	}
	return res, err
}

// SubmitPrivateTx attempts to submit a private transaction to all block producers
func (s *Service) SubmitPrivateTx(tx *types.Transaction, retry bool) error {
	if s.multiclient == nil {
		log.Warn("[tx-relay] No rpc client available to submit transactions")
		return errRpcClientUnavailable
	}

	rawTx, err := tx.MarshalBinary()
	if err != nil {
		log.Warn("[tx-relay] Failed to marshal transaction", "hash", tx.Hash(), "err", err)
		return err
	}

	err = s.multiclient.submitPrivateTx(rawTx, tx.Hash(), retry, s.txGetter)
	if err != nil {
		log.Warn("[tx-relay] Error submitting private tx to atleast one block producer", "hash", tx.Hash(), "err", err)
		return errPrivateTxSubmissionFailed
	}

	return nil
}

// cleanup is a periodic routine to delete old preconf results
func (s *Service) cleanup() {
	ticker := time.NewTicker(s.config.expiryTickerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			count := 0
			s.storeMu.Lock()
			now := time.Now()
			for hash, task := range s.store {
				if now.Sub(task.insertedAt) > s.config.expiryInterval {
					delete(s.store, hash)
					count++
				}
			}
			s.storeMu.Unlock()
			if count > 0 {
				log.Info("[tx-relay] Purged expired tasks", "count", count)
			}
		case <-s.closeCh:
			return
		}
	}
}

func (s *Service) close() {
	close(s.closeCh)
	if s.multiclient != nil {
		s.multiclient.close()
	}
}
