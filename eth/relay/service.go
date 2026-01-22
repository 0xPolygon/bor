package relay

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

var (
	errRpcClientUnavailable      = fmt.Errorf("rpc client unavailable to submit transactions")
	errQueueOverflow             = fmt.Errorf("relay task queue overflow")
	errPreconfTaskNotFound       = errors.New("unable to find preconf task associated with transaction hash")
	errPreconfValidationFailed   = errors.New("failed to validate transaction inclusion status for issuing preconf")
	errPrivateTxSubmissionFailed = errors.New("private tx submission failed partially, background retry scheduled")
)

const (
	expiryTickerInterval = time.Minute
	expiryInterval       = 10 * time.Minute
	maxQueuedTasks       = 40_000
	maxConcurrentTasks   = 1024
)

// TxTask represents a transaction submission task
type TxTask struct {
	rawtx      []byte
	hash       common.Hash
	insertedAt time.Time

	preconfirmed bool // whether block producer preconfirmed the tx or not
	err          error
}

type Service struct {
	multiclient *multiClient
	store       map[common.Hash]TxTask
	storeMu     sync.RWMutex
	taskCh      chan TxTask // channel to queue new tasks
	semaphore   chan struct{}
	closeCh     chan struct{} // to limit concurrent tasks
}

func NewService(urls []string) *Service {
	s := &Service{
		multiclient: newMultiClient(urls),
		store:       make(map[common.Hash]TxTask),
		taskCh:      make(chan TxTask, maxQueuedTasks),
		semaphore:   make(chan struct{}, maxConcurrentTasks),
		closeCh:     make(chan struct{}),
	}
	go s.processPreconfTasks()
	go s.cleanup()
	return s
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

	// Queue for processing (non-blocking until queue is full)
	select {
	case s.taskCh <- TxTask{rawtx: rawTx, hash: tx.Hash()}:
		return nil
	case <-s.closeCh:
		log.Debug("[tx-relay] Dropping task, service closing", "hash", tx.Hash())
		return errRpcClientUnavailable
	default:
		log.Debug("[tx-relay] Task queue full, dropping transaction", "hash", tx.Hash())
		return errQueueOverflow
	}
}

func (s *Service) SubmitPrivateTx(tx *types.Transaction) error {
	if s.multiclient == nil {
		log.Warn("[tx-relay] No rpc client available to submit transactions")
		return errRpcClientUnavailable
	}

	rawTx, err := tx.MarshalBinary()
	if err != nil {
		log.Warn("[tx-relay] Failed to marshal transaction", "hash", tx.Hash(), "err", err)
		return err
	}

	err = s.multiclient.submitPrivateTx(rawTx, tx.Hash())
	if err != nil {
		log.Warn("[tx-relay] Error submitting private tx to atleast one block producer", "hash", tx.Hash(), "err", err)
		return errPrivateTxSubmissionFailed
	}

	return nil
}

func (s *Service) processPreconfTask(task TxTask) {
	res, err := s.multiclient.submitPreconfTx(task.rawtx)
	task.preconfirmed = res
	task.err = err
	task.insertedAt = time.Now()

	s.storeMu.Lock()
	s.store[task.hash] = task
	s.storeMu.Unlock()
}

func (s *Service) CheckTxPreconfStatus(hash common.Hash) (bool, error) {
	s.storeMu.RLock()
	task, exists := s.store[hash]
	s.storeMu.RUnlock()

	// Note: If this method is exposed for all transactions (not just preconf ones), check
	// the status of a tx on the fly.
	if !exists {
		return false, errPreconfTaskNotFound
	}
	if task.preconfirmed {
		return true, nil
	}

	// Re-check the tx status from block producers if the tx was not preconfirmed earlier
	res, err := s.multiclient.checkTxStatus(hash)
	task.preconfirmed = res
	task.err = err
	s.storeMu.Lock()
	s.store[hash] = task
	s.storeMu.Unlock()

	if err != nil {
		log.Debug("[tx-relay] Unable to validate tx status for preconf", "err", err)
	}

	return task.preconfirmed, errPreconfValidationFailed
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
	ticker := time.NewTicker(expiryTickerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			count := 0
			s.storeMu.Lock()
			now := time.Now()
			for hash, task := range s.store {
				if now.Sub(task.insertedAt) > expiryInterval {
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
