package relay

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func TestNewService(t *testing.T) {
	t.Parallel()

	t.Run("service initializes with valid URLs", func(t *testing.T) {
		// Create mock servers
		var rpcServers []*mockRpcServer = make([]*mockRpcServer, 2)
		var urls []string = make([]string, 2)
		for i := 0; i < 2; i++ {
			rpcServers[i] = newMockRpcServer()
			urls[i] = rpcServers[i].server.URL
		}
		defer func() {
			for _, s := range rpcServers {
				s.close()
			}
		}()

		defaultConfig := DefaultServiceConfig
		service := NewService(urls, nil)
		require.NotNil(t, service, "expected non-nil service")
		require.NotNil(t, service.multiclient, "expected non-nil multiclient")
		require.NotNil(t, service.store, "expected non-nil store")
		require.NotNil(t, service.taskCh, "expected non-nil task channel")
		require.Equal(t, defaultConfig.maxQueuedTasks, cap(service.taskCh), "expected task channel capacity to match maxQueuedTasks")
		require.Equal(t, defaultConfig.maxConcurrentTasks, cap(service.semaphore), "expected semaphore capacity to match maxConcurrentTasks")

		service.close()
	})

	t.Run("service initializes with nil multiclient when no URLs", func(t *testing.T) {
		service := NewService([]string{}, nil)
		require.NotNil(t, service, "expected non-nil service")
		require.Nil(t, service.multiclient, "expected nil multiclient with empty URLs")

		service.close()
	})
}

func TestSubmitTransactionForPreconf(t *testing.T) {
	t.Parallel()

	// Create mock servers
	var rpcServers []*mockRpcServer = make([]*mockRpcServer, 2)
	var urls []string = make([]string, 2)
	for i := 0; i < 2; i++ {
		rpcServers[i] = newMockRpcServer()
		urls[i] = rpcServers[i].server.URL
	}
	defer func() {
		for _, s := range rpcServers {
			s.close()
		}
	}()

	t.Run("error when multiclient is nil", func(t *testing.T) {
		service := NewService([]string{}, nil)
		defer service.close()

		tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
		err := service.SubmitTransactionForPreconf(tx)
		require.ErrorIs(t, err, errRpcClientUnavailable, "expected errRpcClientUnavailable error on nil multiclient")
	})

	t.Run("queue valid tx for preconf", func(t *testing.T) {
		service := NewService(urls, nil)
		defer service.close()

		tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
		err := service.SubmitTransactionForPreconf(tx)
		require.NoError(t, err, "expected no error queuing task")

		// Give some time to process
		time.Sleep(100 * time.Millisecond)

		// Check task was stored
		service.storeMu.RLock()
		task, exists := service.store[tx.Hash()]
		service.storeMu.RUnlock()
		require.True(t, exists, "expected task to be stored after processing")
		require.True(t, task.preconfirmed, "expected task to be preconfirmed")
	})

	t.Run("queue overflow with burst submissions", func(t *testing.T) {
		// Update the config to a reasonable size for testing
		config := DefaultServiceConfig
		config.maxQueuedTasks = 10
		config.maxConcurrentTasks = 5

		service := NewService(urls, &config)
		defer service.close()

		// Block the semaphore so that tasks are queued entirely
		for i := 0; i < config.maxConcurrentTasks; i++ {
			service.semaphore <- struct{}{}
		}

		// Fill the queue to full capacity. We need to do config.maxQueuedTasks+1 because
		// first task will be consumed.
		for i := 0; i <= config.maxQueuedTasks; i++ {
			tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
			err := service.SubmitTransactionForPreconf(tx)
			require.NoError(t, err, "expected no error for task %d", i)
		}

		// Next submission should fail due to overflow
		tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
		err := service.SubmitTransactionForPreconf(tx)
		require.Error(t, err, "expected error when queue is full")
		require.Equal(t, errQueueOverflow, err, "expected errQueueOverflow")
	})

	t.Run("max concurrent tasks", func(t *testing.T) {
		// Update the config to a reasonable size for testing
		config := DefaultServiceConfig
		config.maxQueuedTasks = 10
		config.maxConcurrentTasks = 5

		// Update the rpc server handlers to have a delay in processing tasks
		for _, s := range rpcServers {
			s.handleSendPreconfTx = func(w http.ResponseWriter, id int, params json.RawMessage) {
				time.Sleep(time.Second)
				defaultHandleSendPreconfTx(w, id, params)
			}
		}

		service := NewService(urls, &config)
		defer service.close()

		// Start sending `maxConcurrentTasks` tasks to block the queue
		for i := 0; i <= config.maxConcurrentTasks; i++ {
			tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
			err := service.SubmitTransactionForPreconf(tx)
			require.NoError(t, err, "expected no error for task %d", i)
		}

		// While these tasks are being processed, send one more task.
		tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
		err := service.SubmitTransactionForPreconf(tx)
		require.NoError(t, err, "expected no error queuing task within capacity")

		// Check that queue size is 1 (as it should only contain the last task) after a small delay
		time.Sleep(100 * time.Millisecond)
		queueSize := len(service.taskCh)
		require.Equal(t, 1, queueSize, "expected only 1 task in queue")

		// Check again after a small delay
		time.Sleep(500 * time.Millisecond)
		queueSize = len(service.taskCh)
		require.Equal(t, 1, queueSize, "expected only 1 task in queue")

		// Check again after a small delay. By now, at least one of the tasks
		// would have been processed.
		time.Sleep(500 * time.Millisecond)
		queueSize = len(service.taskCh)
		require.Equal(t, 0, queueSize, "expected no tasks in queue")

		// Reset all rpc servers
		for _, s := range rpcServers {
			s.handleSendPreconfTx = defaultHandleSendPreconfTx
		}
	})

	t.Run("error when service is closing", func(t *testing.T) {
		service := NewService(urls, nil)

		// Close service first
		service.close()

		tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
		err := service.SubmitTransactionForPreconf(tx)
		require.Error(t, err, "expected error when service is closing")
		require.Equal(t, errRpcClientUnavailable, err, "expected errRpcClientUnavailable")
	})

	t.Run("concurrent preconf task submissions", func(t *testing.T) {
		service := NewService(urls, nil)
		defer service.close()

		var wg sync.WaitGroup
		numTasks := 2_000
		successCount := atomic.Int32{}

		var nonce atomic.Uint64

		// Launch goroutines in batches to avoid overwhelming the system
		batchSize := 100
		for batch := 0; batch < numTasks/batchSize; batch++ {
			for i := 0; i < batchSize; i++ {
				wg.Add(1)
				idx := batch*batchSize + i
				go func(taskIdx int) {
					defer wg.Done()

					tx := types.NewTransaction(nonce.Add(1), common.Address{}, nil, 0, nil, nil)
					err := service.SubmitTransactionForPreconf(tx)
					if err == nil {
						successCount.Add(1)
					}
				}(idx)
			}
		}

		wg.Wait()
		require.Equal(t, int32(numTasks), successCount.Load(), "expected all tasks to be queued without any errors")

		// Wait for all tasks to be processed
		time.Sleep(3 * time.Second)

		// Verify tasks were processed
		service.storeMu.RLock()
		storeSize := len(service.store)
		require.Equal(t, numTasks, storeSize, "expected store size to be same as number of tasks")
		for hash, task := range service.store {
			require.NoError(t, task.err, "expected no error in task %s", hash.Hex())
			require.True(t, task.preconfirmed, "expected task %s to be preconfirmed", hash.Hex())
		}
		service.storeMu.RUnlock()
	})
}

func TestServiceSubmitPrivateTx(t *testing.T) {
	t.Parallel()

	// Create mock servers
	var rpcServers []*mockRpcServer = make([]*mockRpcServer, 2)
	var urls []string = make([]string, 2)
	for i := 0; i < 2; i++ {
		rpcServers[i] = newMockRpcServer()
		urls[i] = rpcServers[i].server.URL
	}
	defer func() {
		for _, s := range rpcServers {
			s.close()
		}
	}()

	t.Run("error when multiclient is nil", func(t *testing.T) {
		service := NewService([]string{}, nil)
		defer service.close()

		tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
		err := service.SubmitPrivateTx(tx, false)
		require.ErrorIs(t, err, errRpcClientUnavailable, "expected errRpcClientUnavailable error on nil multiclient")
	})

	t.Run("submit valid private tx", func(t *testing.T) {
		service := NewService(urls, nil)
		defer service.close()

		tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
		err := service.SubmitPrivateTx(tx, false)
		require.NoError(t, err, "expected no error submitting private tx")
	})

	t.Run("error when submission fails", func(t *testing.T) {
		// Mock server to fail private tx submissions
		rpcServers[0].handleSendPrivateTx = func(w http.ResponseWriter, id int, params json.RawMessage) {
			defaultSendError(w, id, -32601, "internal server error")
		}

		service := NewService(urls, nil)
		defer service.close()

		tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
		err := service.SubmitPrivateTx(tx, false)
		require.Equal(t, errPrivateTxSubmissionFailed, err, "expected errPrivateTxSubmissionFailed")

		// Reset handler
		rpcServers[0].handleSendPrivateTx = defaultHandleSendPrivateTx
	})

	t.Run("concurrent private tx submissions", func(t *testing.T) {
		service := NewService(urls, nil)
		defer service.close()

		var wg sync.WaitGroup
		numTxs := 50
		successCount := atomic.Int32{}

		for i := 0; i < numTxs; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				tx := types.NewTransaction(uint64(idx), common.Address{}, nil, 0, nil, nil)
				err := service.SubmitPrivateTx(tx, false)
				if err == nil {
					successCount.Add(1)
				}
			}(i)
		}

		wg.Wait()
		require.Equal(t, int32(numTxs), successCount.Load(), "expected all private txs to be submitted successfully")
	})
}

func TestCheckTxPreconfStatus(t *testing.T) {
	t.Parallel()

	// Create mock servers
	var rpcServers []*mockRpcServer = make([]*mockRpcServer, 2)
	var urls []string = make([]string, 2)
	for i := 0; i < 2; i++ {
		rpcServers[i] = newMockRpcServer()
		urls[i] = rpcServers[i].server.URL
	}
	defer func() {
		for _, s := range rpcServers {
			s.close()
		}
	}()

	t.Run("error when task not found", func(t *testing.T) {
		service := NewService(urls, nil)
		defer service.close()

		unknownHash := common.HexToHash("0x1")
		preconfirmed, err := service.CheckTxPreconfStatus(unknownHash)
		require.Equal(t, errPreconfTaskNotFound, err, "expected errPreconfTaskNotFound")
		require.False(t, preconfirmed, "expected preconfirmed to be false")
	})

	t.Run("returns true when task already preconfirmed", func(t *testing.T) {
		service := NewService(urls, nil)
		defer service.close()

		// Submit and wait for processing
		tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
		err := service.SubmitTransactionForPreconf(tx)
		require.NoError(t, err)
		time.Sleep(100 * time.Millisecond)

		// Check preconfirmation status
		preconfirmed, err := service.CheckTxPreconfStatus(tx.Hash())
		require.NoError(t, err, "expected no error when checking preconf status")
		require.True(t, preconfirmed, "expected preconfirmation to be true")
	})

	t.Run("re-checks status when not preconfirmed initially", func(t *testing.T) {
		// Mock servers to reject preconf initially
		rpcServers[0].handleSendPreconfTx = handleSendPreconfTxWithRejection

		service := NewService(urls, nil)
		defer service.close()

		// Submit and wait for processing
		tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
		err := service.SubmitTransactionForPreconf(tx)
		require.NoError(t, err)
		time.Sleep(200 * time.Millisecond)

		// Ensure that the preconfirmation task is stored as not preconfirmed
		service.storeMu.RLock()
		task, exists := service.store[tx.Hash()]
		service.storeMu.RUnlock()
		require.True(t, exists, "expected task to be stored")
		require.False(t, task.preconfirmed, "expected task to be not preconfirmed")

		// Mock servers to return unknown tx status on initial status
		for i := range rpcServers {
			rpcServers[i].handleTxStatus = makeTxStatusHandler(map[common.Hash]txpool.TxStatus{})
		}

		// Check status - should re-check via checkTxStatus
		preconfirmed, err := service.CheckTxPreconfStatus(tx.Hash())
		require.Equal(t, errPreconfValidationFailed, err, "expected errPreconfValidationFailed")
		require.False(t, preconfirmed, "expected preconfirmed to be false after re-check")

		// Now update the mock servers to return pending status
		for i := range rpcServers {
			rpcServers[i].handleTxStatus = makeTxStatusHandler(map[common.Hash]txpool.TxStatus{
				tx.Hash(): txpool.TxStatusPending,
			})
		}

		// Check status - should again re-check via checkTxStatus
		preconfirmed, err = service.CheckTxPreconfStatus(tx.Hash())
		require.NoError(t, err, "expected no error on re-check with pending status")
		require.True(t, preconfirmed, "expected preconfirmed to be true after re-check")

		// Reset handlers
		for i := range rpcServers {
			rpcServers[i].handleTxStatus = defaultHandleTxStatus
			rpcServers[i].handleSendPreconfTx = defaultHandleSendPreconfTx
		}
	})
}

func TestTaskCleanup(t *testing.T) {
	t.Parallel()

	// Create mock servers
	var rpcServers []*mockRpcServer = make([]*mockRpcServer, 2)
	var urls []string = make([]string, 2)
	for i := 0; i < 2; i++ {
		rpcServers[i] = newMockRpcServer()
		urls[i] = rpcServers[i].server.URL
	}
	defer func() {
		for _, s := range rpcServers {
			s.close()
		}
	}()

	// Use a short expiry interval for testing
	config := DefaultServiceConfig
	config.expiryTickerInterval = 200 * time.Millisecond
	config.expiryInterval = time.Second

	service := NewService(urls, &config)
	defer service.close()

	tx := types.NewTransaction(1, common.Address{}, nil, 0, nil, nil)
	err := service.SubmitTransactionForPreconf(tx)
	require.NoError(t, err, "expected no error queuing task")

	// Give some time to process
	time.Sleep(100 * time.Millisecond)

	// Check task was stored
	service.storeMu.RLock()
	_, exists := service.store[tx.Hash()]
	service.storeMu.RUnlock()
	require.True(t, exists, "expected task to be stored after processing")

	// Wait for longer than expiry interval to allow cleanup to run
	time.Sleep(time.Second + 200*time.Millisecond)

	// Check task was deleted
	service.storeMu.RLock()
	_, exists = service.store[tx.Hash()]
	service.storeMu.RUnlock()
	require.False(t, exists, "expected task to be deleted after expiry interval")
}
