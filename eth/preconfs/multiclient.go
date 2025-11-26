package preconfs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	rpcTimeout      = time.Second
	waitBeforeRetry = time.Second
	maxRetry        = 5
	threshold       = int(1) // reduce later based on how testing goes
)

var (
	errNoClients = fmt.Errorf("no rpc clients available")
	errRpcFailed = fmt.Errorf("transaction inclusion ratio below threshold: unable to query block producers")
	errMissingTx = fmt.Errorf("transaction inclusion ratio below threshold: transaction missing in mempool")
)

var (
	rpcCallsSuccessMeter    = metrics.NewRegisteredMeter("preconfs/rpc/success", nil)
	rpcCallsFailureMeter    = metrics.NewRegisteredMeter("preconfs/rpc/failure", nil)
	missingTransactionMeter = metrics.NewRegisteredMeter("preconfs/missingtx", nil)
	belowThresholdMeter     = metrics.NewRegisteredMeter("preconfs/belowthreshold", nil)
)

// multiClient holds multiple rpc client instances for each block producer
// to perform certain queries across all of them and make a unified decision.
type multiClient struct {
	clients []*rpc.Client // rpc client instances dialed to each block producer
}

func newMultiClient(urls []string) *multiClient {
	if len(urls) == 0 {
		return nil
	}

	var (
		clients []*rpc.Client = make([]*rpc.Client, 0, len(urls))
		failed  int           = 0
	)

	for _, url := range urls {
		// We use the rpc dialer for primarily 2 reasons:
		// 1. It supports automatic reconnection when connection is lost
		// 2. It allows us to do rpc queries which aren't directly available in ethclient (like txpool_contentFrom)
		client, err := rpc.Dial(url)
		if err != nil {
			failed++
			log.Warn("[preconfs] Failed to dial rpc endpoint for multi-client, skipping", "url", url, "err", err)
			continue
		}
		clients = append(clients, client)
	}

	if failed == len(urls) {
		log.Info("[preconfs] Failed to dial all rpc endpoints for multi-client, disabling completely", "count", len(urls))
		return nil
	}

	log.Info("[preconfs] Initialised multi-client for each block producer", "count", len(clients), "failed", failed)
	return &multiClient{
		clients: clients,
	}
}

type MinimalTxPoolContent struct {
	Pending map[string]MinimalTransaction `json:"pending"`
}

type MinimalTransaction struct {
	From  common.Address `json:"from"`
	Hash  common.Hash    `json:"hash"`
	Nonce hexutil.Uint64 `json:"nonce"`
}

// checkTxInclusionInMempool checks if the given transaction is included in the
// pending mempool of all block producers or not. Returns true only if the
// transaction is included in more than `threshold`% of block producers.
func (mc *multiClient) checkTxInclusionInMempool(tx *types.Transaction, sender common.Address) (bool, error) {
	if len(mc.clients) == 0 {
		return false, errNoClients
	}

	// Track relevant metrics
	var (
		d              []time.Duration = make([]time.Duration, len(mc.clients))
		t              []int           = make([]int, len(mc.clients))
		rpcErrCount                    = 0
		txMissingCount                 = 0
	)

	// TODO: check if bp's are in sync or not before validating

	// Check inclusion of given tx against each block producer
	validationCount := 0
	var wg sync.WaitGroup
	for i, client := range mc.clients {
		wg.Add(1)
		go func(client *rpc.Client, index int) {
			defer wg.Done()
			start := time.Now()
			tries := 0
			defer func() {
				d[index] = time.Since(start)
			}()
			defer func() {
				t[index] = tries
			}()
			var isTxPresent bool
			var err error
			for {
				if tries >= maxRetry {
					if !isTxPresent {
						txMissingCount++
					}
					if err != nil {
						rpcErrCount++
					}
					break
				}
				var txsInMempool MinimalTxPoolContent
				ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
				err = client.CallContext(ctx, &txsInMempool, "txpool_contentFrom", sender)
				cancel()
				if err != nil {
					tries++
					rpcCallsFailureMeter.Mark(1)
					log.Info("[preconfs] Failed to get txpool content for sender via multi-client, retrying after 1s", "sender", sender.Hex(), "producer", index, "tries", tries, "err", err)
					continue
				}
				rpcCallsSuccessMeter.Mark(1)
				isTxPresent = isTxPresentInPending(tx, sender, txsInMempool)
				if !isTxPresent {
					tries++
					missingTransactionMeter.Mark(1)
					log.Info("[preconfs] Transaction missing in pending pool, retrying after 1s", "sender", sender.Hex(), "producer", index, "tries", tries)
					time.Sleep(waitBeforeRetry)
					continue
				}
				validationCount++
				break
			}
		}(client, i)
	}
	wg.Wait()

	log.Info("[preconfs] done with validation, stats", "duration", d, "retries", t)

	if validationCount/len(mc.clients) < threshold {
		belowThresholdMeter.Mark(1)
		log.Info("[preconfs] Transaction not present in enough block producers", "sender", sender.Hex(), "validations", validationCount, "total", len(mc.clients), "threshold", threshold)
		err := errMissingTx
		if rpcErrCount > txMissingCount {
			err = errRpcFailed
		}
		return false, err
	}

	log.Info("[preconfs] Tx present in enough block producers", "sender", sender.Hash(), "hash", tx.Hash())
	return true, nil
}

func isTxPresentInPending(tx *types.Transaction, sender common.Address, txsInMempool MinimalTxPoolContent) bool {
	if txsInMempool.Pending == nil {
		return false
	}
	nonce := fmt.Sprintf("%d", tx.Nonce())
	if pendingTx, ok := txsInMempool.Pending[nonce]; ok {
		// Compare tx hash, sender and nonce
		if pendingTx.Hash.Cmp(tx.Hash()) != 0 {
			return false
		}
		if pendingTx.From.Cmp(sender) != 0 {
			return false
		}
		if pendingTx.Nonce != hexutil.Uint64(tx.Nonce()) {
			return false
		}
		return true
	}
	return false
}

// Close closes all rpc client connections
func (mc *multiClient) Close() {
	for _, client := range mc.clients {
		client.Close()
	}
}
