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
	"github.com/ethereum/go-ethereum/rpc"
)

const (
	rpcTimeout = time.Second
	maxRetry   = 5
	threshold  = int(1) // reduce later based on how testing goes
)

// MultiClient holds multiple rpc client instances for each block producer
// to perform certain queries across all of them.
type MultiClient struct {
	clients []*rpc.Client // rpc client instances dialed to each block producer
}

func NewMultiClient(urls []string) *MultiClient {
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
			log.Warn("Failed to dial rpc endpoint for preconf multi-client, skipping", "url", url, "err", err)
			continue
		}
		clients = append(clients, client)
	}

	if failed == len(urls) {
		log.Info("Failed to dial all rpc endpoints for preconf multi-client, disabling", "count", len(urls))
		return nil
	}

	log.Info("Initialised preconf multi-client for each block producer", "count", len(clients), "failed", failed)
	return &MultiClient{
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

// ValidateTxInclusionInMempool checks if the given transaction is included in the
// pending mempool of all block producers or not. Return true only if the transaction
// is included in the pending pool of all block producers.
func (mc *MultiClient) ValidateTxInclusionInMempool(tx *types.Transaction, sender common.Address) bool {
	if len(mc.clients) == 0 {
		return false
	}

	// TODO: check if bp's are in sync or not before validating

	// Check inclusion of given tx against each block producer
	validationCount := 0
	var wg sync.WaitGroup
	for i, client := range mc.clients {
		wg.Add(1)
		go func(client *rpc.Client, index int) {
			defer wg.Done()

			tries := 0
			for {
				if tries >= maxRetry {
					break
				}
				var txsInMempool MinimalTxPoolContent
				ctx, cancel := context.WithTimeout(context.Background(), rpcTimeout)
				err := client.CallContext(ctx, &txsInMempool, "txpool_contentFrom", sender)
				cancel()
				if err != nil {
					tries++
					log.Info("Failed to get txpool content for sender via preconf multi-client, retrying after 1s", "sender", sender.Hex(), "producer", index, "try", tries, "err", err)
					continue
				}
				isTxPresent := isTxPresentInPending(tx, sender, txsInMempool)
				if !isTxPresent {
					tries++
					log.Info("Transaction missing in pending pool, retrying after 1s", "sender", sender.Hex(), "producer", index, "try", tries)
					time.Sleep(time.Second)
					continue
				}
				validationCount++
				break
			}
		}(client, i)
	}
	wg.Wait()

	if validationCount/len(mc.clients) < threshold {
		log.Info("Transaction not present in enough block producers", "sender", sender.Hex(), "validations", validationCount, "total", len(mc.clients), "threshold", threshold)
		return false
	}

	log.Info("Tx present in enough block producers", "sender", sender.Hash(), "hash", tx.Hash())
	return true
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
func (mc *MultiClient) Close() {
	for _, client := range mc.clients {
		client.Close()
	}
}
