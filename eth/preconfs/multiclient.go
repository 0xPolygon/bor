package preconfs

import (
	"context"
	"fmt"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
)

// MultiClient holds multiple rpc client instances for each block producer
// to perform certain queries across all of them.
type MultiClient struct {
	urls    []string      // valid rpc urls of block producers
	clients []*rpc.Client // rpc client instances dialed to each block producer
}

func NewMultiClient(rpcUrls []string) *MultiClient {
	if len(rpcUrls) == 0 {
		return nil
	}

	var (
		urls    []string      = make([]string, 0, len(rpcUrls))
		clients []*rpc.Client = make([]*rpc.Client, 0, len(rpcUrls))
		failed  int           = 0
	)

	for _, url := range rpcUrls {
		// We use the rpc dialer for primarily 2 reasons:
		// 1. It supports automatic reconnection when connection is lost
		// 2. It allows us to do rpc queries which aren't directly available in ethclient (like txpool_contentFrom)
		client, err := rpc.Dial(url)
		if err != nil {
			failed++
			log.Warn("Failed to dial rpc endpoint for preconf multi-client, skipping", "url", url, "err", err)
			continue
		}
		urls = append(urls, url)
		clients = append(clients, client)
	}

	if failed == len(rpcUrls) {
		log.Info("Failed to dial all rpc endpoints for preconf multi-client, disabling", "count", len(rpcUrls))
		return nil
	}

	log.Info("Initialised preconf multi-client for each block producer", "count", len(urls), "failed", failed)
	return &MultiClient{
		urls:    urls,
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

	// TODOs:
	// 1. Add threshold for acceptance criteria
	// 2. Add checks to see if the block producers are in sync before checking mempool
	// 3. Add timeout for each rpc call via context

	// Check inclusion of given tx against each block producer
	count := 0
	var wg sync.WaitGroup
	for _, client := range mc.clients {
		wg.Add(1)
		go func(client *rpc.Client) {
			defer wg.Done()
			var txsInMempool MinimalTxPoolContent
			err := client.CallContext(context.Background(), &txsInMempool, "txpool_contentFrom", sender)
			if err != nil {
				log.Debug("Failed to get txpool content for sender via preconf multi-client", "sender", sender.Hex(), "err", err)
			} else {
				if isTxPresentInPending(tx, sender, txsInMempool) {
					count++
				}
			}
		}(client)
	}
	wg.Wait()

	// Currently, the acceptance criteria is that the transaction should be present in all
	// producer's pending mempool. A threshold should be introduced later if needed.
	if count == len(mc.clients) {
		return true
	}
	return false
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
