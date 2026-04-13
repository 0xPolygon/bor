package relay

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	rpcTimeout             = 2 * time.Second
	privateTxRetryInterval = 2 * time.Second
	privateTxMaxRetries    = 5
)

var (
	rpcCallsSuccessMeter       = metrics.NewRegisteredMeter("preconfs/rpc/success", nil)
	rpcCallsFailureMeter       = metrics.NewRegisteredMeter("preconfs/rpc/failure", nil)
	rpcErrorInPreconfMeter     = metrics.NewRegisteredMeter("preconfs/rpcerror", nil)
	belowThresholdPreconfMeter = metrics.NewRegisteredMeter("preconfs/belowthreshold", nil)
	alreadyKnownErrMeter       = metrics.NewRegisteredMeter("relay/txalreadyknown", nil)
)

// isAlreadyKnownError checks if the error indicates the transaction is already known to the node
func isAlreadyKnownError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "already known")
}

// multiClient holds multiple rpc client instances for each block producer
// to perform certain queries across all of them and make a unified decision.
type multiClient struct {
	clients       []*rpc.Client // rpc client instances dialed to each block producer
	closed        atomic.Bool
	retryInterval time.Duration // 0 means use privateTxRetryInterval; configurable for testing
}

func newMultiClient(urls []string) *multiClient {
	if len(urls) == 0 {
		log.Warn("[tx-relay] No block producer URLs provided")
		return nil
	}

	clients := make([]*rpc.Client, 0, len(urls))
	failed := 0
	for i, url := range urls {
		// We use the rpc dialer for primarily 2 reasons:
		// 1. It supports automatic reconnection when connection is lost
		// 2. It allows us to do rpc queries which aren't directly available in ethclient (like txpool_contentFrom)
		client, err := rpc.Dial(url)
		if err != nil {
			failed++
			log.Warn("[tx-relay] Failed to dial rpc endpoint, skipping", "url", url, "index", i, "err", err)
			continue
		}

		// Test connection with a simple call
		var blockNumber string
		ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
		err = client.CallContext(ctx, &blockNumber, "eth_blockNumber")
		cancel()
		if err != nil {
			client.Close()
			failed++
			log.Warn("[tx-relay] Failed to fetch latest block number, skipping", "url", url, "index", i, "err", err)
			continue
		}

		number, err := hexutil.DecodeUint64(blockNumber)
		if err != nil {
			client.Close()
			failed++
			log.Warn("[tx-relay] Failed to decode latest block number, skipping", "url", url, "index", i, "err", err)
			continue
		}

		log.Info("[tx-relay] Dial successful", "blockNumber", number, "index", i)
		clients = append(clients, client)
	}

	if failed == len(urls) {
		log.Info("[tx-relay] Failed to dial all rpc endpoints, disabling completely", "count", len(urls))
		return nil
	}

	log.Info("[tx-relay] Initialised rpc client for each block producer", "success", len(clients), "failed", failed)
	return &multiClient{
		clients: clients,
	}
}

type SendTxForPreconfResponse struct {
	TxHash       common.Hash `json:"hash"`
	Preconfirmed bool        `json:"preconfirmed"`
}

func (mc *multiClient) submitPreconfTx(rawTx []byte) (bool, error) {
	// Submit tx to all block producers in parallel
	var (
		firstErr            error
		wg                  sync.WaitGroup
		setError            sync.Once
		preconfOfferedCount atomic.Uint64
	)
	for i, client := range mc.clients {
		wg.Add(1)
		go func(client *rpc.Client, index int) {
			defer wg.Done()

			var preconfResponse SendTxForPreconfResponse
			ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
			err := client.CallContext(ctx, &preconfResponse, "eth_sendRawTransactionForPreconf", hexutil.Encode(rawTx))
			cancel()
			if err != nil {
				rpcCallsFailureMeter.Mark(1)
				// If the tx is already known, treat it as preconfirmed for this node
				if isAlreadyKnownError(err) {
					alreadyKnownErrMeter.Mark(1)
					preconfOfferedCount.Add(1)
					return
				}
				setError.Do(func() {
					firstErr = err
				})
				return
			}
			rpcCallsSuccessMeter.Mark(1)
			if preconfResponse.Preconfirmed {
				preconfOfferedCount.Add(1)
			}
		}(client, i)
	}
	wg.Wait()

	// Note: this can be improved later to only check for current block producer instead of all
	// Only offer a preconf if the tx was accepted by all block producers
	if preconfOfferedCount.Load() == uint64(len(mc.clients)) {
		return true, nil
	}

	if firstErr != nil {
		rpcErrorInPreconfMeter.Mark(1)
	} else {
		belowThresholdPreconfMeter.Mark(1)
	}

	return false, firstErr
}

// submitPrivateTx submits a raw private transaction to all block producers. When retry is
// true and some producers fail, a background goroutine retries those producers. The
// returned channel (non-nil only when a retry goroutine is started) is closed when the
// goroutine finishes, allowing callers to wait for completion deterministically.
func (mc *multiClient) submitPrivateTx(rawTx []byte, hash common.Hash, retry bool, txGetter TxGetter) (error, <-chan struct{}) {
	// Submit tx to all block producers in parallel (initial attempt)
	hexTx := hexutil.Encode(rawTx)

	var (
		firstErr error
		setError sync.Once
		wg       sync.WaitGroup
		mu       sync.Mutex
	)
	failedIndices := make([]int, 0)
	successfulIndices := make([]int, 0)

	for i, client := range mc.clients {
		wg.Add(1)
		go func(client *rpc.Client, index int) {
			defer wg.Done()

			var txHash common.Hash
			ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
			err := client.CallContext(ctx, &txHash, "eth_sendRawTransactionPrivate", hexTx)
			cancel()

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				rpcCallsFailureMeter.Mark(1)
				// If the tx is already known, treat it as successful submission
				if isAlreadyKnownError(err) {
					alreadyKnownErrMeter.Mark(1)
					successfulIndices = append(successfulIndices, index)
					return
				}
				setError.Do(func() {
					firstErr = err
				})
				failedIndices = append(failedIndices, index)
				log.Debug("[tx-relay] Failed to submit private tx (initial attempt)", "err", err, "producer", index, "hash", hash)
			} else {
				rpcCallsSuccessMeter.Mark(1)
				successfulIndices = append(successfulIndices, index)
			}
		}(client, i)
	}
	wg.Wait()

	// If all submissions successful, return immediately
	if len(failedIndices) == 0 {
		log.Debug("[tx-relay] Successfully submitted private tx to all producers", "hash", hash)
		return nil, nil
	}

	if retry && !mc.closed.Load() {
		// Some submissions failed, start background retry
		log.Debug("[tx-relay] Failed to submit private tx to one or more block producers, starting retry",
			"err", firstErr, "failed", len(failedIndices), "successful", len(successfulIndices), "total", len(mc.clients), "hash", hash)
		done := make(chan struct{})
		go func() {
			mc.retryPrivateTxSubmission(hexTx, hash, failedIndices, txGetter)
			close(done)
		}()
		return firstErr, done
	}

	return firstErr, nil
}

// retryPrivateTxSubmission runs in background to retry private tx submission to producers
// that failed initially. It uses local txGetter to check if tx was included in a block.
func (mc *multiClient) retryPrivateTxSubmission(hexTx string, hash common.Hash, failedIndices []int, txGetter TxGetter) {
	currentFailedIndices := failedIndices

	for retry := 0; retry < privateTxMaxRetries; retry++ {
		if mc.closed.Load() {
			return
		}

		// If no more failed producers, we're done
		if len(currentFailedIndices) == 0 {
			return
		}

		// Sleep before retry
		interval := mc.retryInterval
		if interval == 0 {
			interval = privateTxRetryInterval
		}
		time.Sleep(interval)

		log.Debug("[tx-relay] Retrying private tx submission", "producers", len(currentFailedIndices), "attempt", retry+1, "hash", hash)

		// Check if tx was already included in a block in local db. If yes, skip
		// retrying submission altogether.
		if txGetter != nil {
			found, tx, _, _, _ := txGetter(hash)
			if found && tx != nil {
				log.Debug("[tx-relay] Transaction found in local database, stopping retry", "hash", hash)
				return
			}
		}

		// Retry submission for failed producers
		var retryWg sync.WaitGroup
		var mu sync.Mutex
		newFailedIndices := make([]int, 0)

		for _, index := range currentFailedIndices {
			retryWg.Add(1)
			go func(client *rpc.Client, idx int) {
				defer retryWg.Done()

				var txHash common.Hash
				ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
				err := client.CallContext(ctx, &txHash, "eth_sendRawTransactionPrivate", hexTx)
				cancel()

				if err != nil {
					rpcCallsFailureMeter.Mark(1)
					// If the tx is already known, treat it as successful submission
					if isAlreadyKnownError(err) {
						alreadyKnownErrMeter.Mark(1)
						return
					}
					mu.Lock()
					newFailedIndices = append(newFailedIndices, idx)
					mu.Unlock()
				} else {
					rpcCallsSuccessMeter.Mark(1)
				}
			}(mc.clients[index], index)
		}
		retryWg.Wait()

		// Update failed indices for next iteration
		currentFailedIndices = newFailedIndices
	}

	if len(currentFailedIndices) > 0 {
		log.Debug("[tx-relay] Finished retry attempts with some producers still failing",
			"hash", hash, "failed", len(currentFailedIndices))
	} else {
		log.Debug("[tx-relay] All producers accepted private tx after retries", "hash", hash)
	}
}

func (mc *multiClient) checkTxStatus(hash common.Hash) (bool, error) {
	// Submit tx to all block producers in parallel
	var (
		firstErr            error
		setError            sync.Once
		wg                  sync.WaitGroup
		preconfOfferedCount atomic.Uint64
	)
	for i, client := range mc.clients {
		wg.Add(1)
		go func(client *rpc.Client, index int) {
			defer wg.Done()

			var txStatus txpool.TxStatus
			ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
			err := client.CallContext(ctx, &txStatus, "txpool_txStatus", hash)
			cancel()
			if err != nil {
				rpcCallsFailureMeter.Mark(1)
				setError.Do(func() {
					firstErr = err
				})
				return
			}
			rpcCallsSuccessMeter.Mark(1)
			if txStatus == txpool.TxStatusPending {
				preconfOfferedCount.Add(1)
			}
		}(client, i)
	}
	wg.Wait()

	// Only offer a preconf if the tx was accepted by all block producers
	if preconfOfferedCount.Load() == uint64(len(mc.clients)) {
		return true, nil
	}

	if firstErr != nil {
		rpcErrorInPreconfMeter.Mark(1)
	} else {
		belowThresholdPreconfMeter.Mark(1)
	}

	return false, firstErr
}

// Close closes all rpc client connections
func (mc *multiClient) close() {
	mc.closed.Store(true)
	for _, client := range mc.clients {
		client.Close()
	}
}
