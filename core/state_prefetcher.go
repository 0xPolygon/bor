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
func (p *statePrefetcher) Prefetch(block *types.Block, statedb *state.StateDB, cfg vm.Config, interrupt *atomic.Bool) (map[common.Address]*types.StateAccount, map[common.Address]map[common.Hash]common.Hash) {
	var (
		fails   atomic.Int64
		header  = block.Header()
		signer  = types.MakeSigner(p.config, header.Number, header.Time)
		workers errgroup.Group
		reader  = statedb.Reader()
	)
	workers.SetLimit(max(1, 4*runtime.NumCPU()/5)) // Aggressively run the prefetching

	type workerRes struct {
		accs map[common.Address]*types.StateAccount
		stor map[common.Address]map[common.Hash]common.Hash
	}
	results := make(chan workerRes, len(block.Transactions()))

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
			// Pre-load trie nodes for the intermediate root.
			//
			// This operation incurs significant memory allocations due to
			// trie hashing and node decoding. TODO(rjl493456442): investigate
			// ways to mitigate this overhead.
			stateCpy.IntermediateRoot(true)

			// Aggregate warmed accounts and storage from this tx copy and send to collator
			acc2, stor2 := stateCpy.WarmSnapshot()
			results <- workerRes{accs: acc2, stor: stor2}
			return nil
		})
	}
	workers.Wait()
	close(results)

	accounts := make(map[common.Address]*types.StateAccount)
	storage := make(map[common.Address]map[common.Hash]common.Hash)
	for res := range results {
		for addr, acct := range res.accs {
			if _, ok := accounts[addr]; !ok {
				accounts[addr] = acct
			}
		}
		for addr, slots := range res.stor {
			dst := storage[addr]
			if dst == nil {
				dst = make(map[common.Hash]common.Hash, len(slots))
				storage[addr] = dst
			}
			for k, v := range slots {
				if _, ok := dst[k]; !ok {
					dst[k] = v
				}
			}
		}
	}

	blockPrefetchTxsValidMeter.Mark(int64(len(block.Transactions())) - fails.Load())
	blockPrefetchTxsInvalidMeter.Mark(fails.Load())
	return accounts, storage
}
