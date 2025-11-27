// Copyright 2019 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"bytes"
	"runtime"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"golang.org/x/sync/errgroup"
)

// statePrefetcher is a basic Prefetcher that executes transactions from a block
// on top of the parent state, aiming to prefetch potentially useful state data
// from disk. Transactions are executed in parallel to fully leverage the
// SSD's read performance.
type statePrefetcher struct {
	config *params.ChainConfig // Chain configuration options
	chain  *HeaderChain        // Canonical block chain
}

// newStatePrefetcher initialises a new statePrefetcher.
func newStatePrefetcher(config *params.ChainConfig, chain *HeaderChain) *statePrefetcher {
	return &statePrefetcher{
		config: config,
		chain:  chain,
	}
}

// Prefetch processes the state changes according to the Ethereum rules by running
// the transaction messages using the statedb, but any changes are discarded. The
// only goal is to warm the state caches.
func (p *statePrefetcher) Prefetch(block *types.Block, statedb *state.StateDB, cfg vm.Config, interrupt *atomic.Bool) {
	type prefetchResult struct {
		accounts map[common.Address]*types.StateAccount
		storage  map[common.Address]map[common.Hash]common.Hash
	}
	var (
		fails   atomic.Int64
		header  = block.Header()
		signer  = types.MakeSigner(p.config, header.Number, header.Time)
		workers errgroup.Group
		reader  = statedb.Reader()
		results = make([]prefetchResult, len(block.Transactions()))
	)
	workers.SetLimit(max(1, 4*runtime.NumCPU()/5)) // Aggressively run the prefetching

	// Iterate over and process the individual transactions
	for i, tx := range block.Transactions() {
		stateCpy := statedb.Copy() // closure
		workers.Go(func() error {
			// If block precaching was interrupted, abort
			if interrupt != nil && interrupt.Load() {
				return nil
			}
			// Preload the touched accounts and storage slots in advance
			sender, err := types.Sender(signer, tx)
			if err != nil {
				fails.Add(1)
				return nil
			}
			reader.Account(sender)

			if tx.To() != nil {
				account, _ := reader.Account(*tx.To())

				// Preload the contract code if the destination has non-empty code
				if account != nil && !bytes.Equal(account.CodeHash, types.EmptyCodeHash.Bytes()) {
					reader.Code(*tx.To(), common.BytesToHash(account.CodeHash))
				}
			}
			for _, list := range tx.AccessList() {
				reader.Account(list.Address)
				if len(list.StorageKeys) > 0 {
					for _, slot := range list.StorageKeys {
						reader.Storage(list.Address, slot)
					}
				}
			}
			// Execute the message to preload the implicit touched states
			evm := vm.NewEVM(NewEVMBlockContext(header, p.chain, nil), stateCpy, p.config, cfg)

			// Convert the transaction into an executable message and pre-cache its sender
			msg, err := TransactionToMessage(tx, signer, header.BaseFee)
			if err != nil {
				fails.Add(1)
				return nil // Also invalid block, bail out
			}
			// Disable the nonce check
			msg.SkipNonceChecks = true

			stateCpy.SetTxContext(tx.Hash(), i)

			// We attempt to apply a transaction. The goal is not to execute
			// the transaction successfully, rather to warm up touched data slots.
			if _, err := ApplyMessage(evm, msg, new(GasPool).AddGas(block.GasLimit()), interrupt); err != nil {
				fails.Add(1)
				return nil // Ugh, something went horribly wrong, bail out
			}
			acc, stor := stateCpy.PrefetchTouched()
			results[i] = prefetchResult{accounts: acc, storage: stor}
			stateCpy.IntermediateRoot(true)
			return nil
		})
	}
	workers.Wait()

	accList := make([]map[common.Address]*types.StateAccount, len(results))
	storList := make([]map[common.Address]map[common.Hash]common.Hash, len(results))
	for i, res := range results {
		accList[i] = res.accounts
		storList[i] = res.storage
	}
	accAgg, storAgg := aggregatePrefetch(accList, storList)
	seedPrefetchCaches(reader, accAgg, storAgg)

	blockPrefetchTxsValidMeter.Mark(int64(len(block.Transactions())) - fails.Load())
	blockPrefetchTxsInvalidMeter.Mark(fails.Load())
}

// aggregatePrefetch combines multiple prefetch results into a single aggregated
// set of accounts and storage slots.
func aggregatePrefetch(
	accList []map[common.Address]*types.StateAccount,
	storList []map[common.Address]map[common.Hash]common.Hash,
) (map[common.Address]*types.StateAccount, map[common.Address]map[common.Hash]common.Hash) {
	accAgg := make(map[common.Address]*types.StateAccount)
	storAgg := make(map[common.Address]map[common.Hash]common.Hash)

	for _, acc := range accList {
		for a, acct := range acc {
			if _, exists := accAgg[a]; !exists {
				accAgg[a] = acct
			}
		}
	}
	for _, stor := range storList {
		for a, bucket := range stor {
			dst := storAgg[a]
			if dst == nil {
				dst = make(map[common.Hash]common.Hash, len(bucket))
				storAgg[a] = dst
			}
			for k, v := range bucket {
				if _, exists := dst[k]; !exists {
					dst[k] = v
				}
			}
		}
	}
	return accAgg, storAgg
}

// seedPrefetchCaches warms the reader caches with aggregated accounts and storage.
func seedPrefetchCaches(
	reader state.Reader,
	accAgg map[common.Address]*types.StateAccount,
	storAgg map[common.Address]map[common.Hash]common.Hash,
) {
	if len(accAgg) == 0 && len(storAgg) == 0 {
		return
	}
	if statReader, ok := reader.(*state.ReaderWithCacheStats); ok && statReader != nil {
		statReader.SeedCaches(accAgg, storAgg)
		return
	}
	for a := range accAgg {
		reader.Account(a)
	}
	for a, bucket := range storAgg {
		for k := range bucket {
			reader.Storage(a, k)
		}
	}
}
