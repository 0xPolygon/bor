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

// SubmitTransaction attempts to queue a transaction submission task for preconf / private tx
// and returns true if the task is successfully queued. It fails if either the rpc clients
// are unavailable or the task queue is full.
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

func (s *Service) processPreconfTask(task TxTask) {
	res, err := s.multiclient.submitPreconfTx(task.rawtx)
	if err != nil {
		log.Warn("[tx-relay] failed to submit preconf tx", "err", err)
	}
	task.preconfirmed = res
	task.err = err
	task.insertedAt = time.Now()
	// Note: We can purge the raw tx here to save memory. Keeping it
	// incase we have some changes in the retry logic.

	s.storeMu.Lock()
	s.store[task.hash] = task
	s.storeMu.Unlock()
}

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
			// Create a new task if there wasn't one earlier
			if !exists {
				task = TxTask{hash: hash, insertedAt: time.Now()}
			}
			task.preconfirmed = true
			task.err = nil
			s.storeMu.Lock()
			s.store[hash] = task
			s.storeMu.Unlock()
			log.Debug("[tx-relay] Transaction found in local database", "hash", hash)
			return true, nil
		}
	}

	// If tx not found locally, query block producers for status
	res, err := s.multiclient.checkTxStatus(hash)
	if !res && err == nil {
		err = errPreconfValidationFailed
	}
	// Create a new task if there wasn't one earlier
	if !exists {
		task = TxTask{hash: hash, insertedAt: time.Now()}
	}
	task.preconfirmed = res
	task.err = err
	s.storeMu.Lock()
	s.store[hash] = task
	s.storeMu.Unlock()

	if err != nil {
		log.Info("[tx-relay] Unable to validate tx status for preconf", "err", err)
	}

	return task.preconfirmed, err
}

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
