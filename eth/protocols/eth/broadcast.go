// Copyright 2020 The go-ethereum Authors
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

package eth

import (
	"math/big"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

const (
	// maxTxPacketSize is the target size for packs of transactions or announcements.
	// A pack can get larger than this if a single transaction exceeds this size.
	// Bor: increased from the go-ethereum default of 100 KB to 1 MB.
	maxTxPacketSize = 1024 * 1024
)

// blockPropagation is a block propagation event, waiting for its turn in the
// broadcast queue.
type blockPropagation struct {
	block *types.Block
	td    *big.Int
}

type txPropagation struct {
	hashes   []common.Hash
	retained chan []common.Hash
}

func (p *Peer) queueTxPropagation(queue chan<- *txPropagation, hashes []common.Hash) []common.Hash {
	batch := &txPropagation{hashes: hashes, retained: make(chan []common.Hash, 1)}
	select {
	case queue <- batch:
	case <-p.term:
		return nil
	}
	select {
	case retained := <-batch.retained:
		p.knownTxs.Add(retained...)
		return retained
	case <-p.term:
		return nil
	}
}

func retainTxPropagation(queue []common.Hash, batch *txPropagation, limit int, failed bool) []common.Hash {
	var retained []common.Hash
	if !failed {
		retained = batch.hashes[:min(len(batch.hashes), limit-len(queue))]
		queue = append(queue, retained...)
	}
	batch.retained <- retained
	return queue
}

// broadcastBlocks is a write loop that multiplexes blocks and block announcements
// to the remote peer. The goal is to have an async writer that does not lock up
// node internals and at the same time rate limits queued data.
func (p *Peer) broadcastBlocks() {
	for {
		select {
		case prop := <-p.queuedBlocks:
			if err := p.SendNewBlock(prop.block, prop.td); err != nil {
				return
			}
			p.Log().Trace("Propagated block", "number", prop.block.Number(), "hash", prop.block.Hash(), "td", prop.td)

		case block := <-p.queuedBlockAnns:
			if err := p.SendNewBlockHashes([]common.Hash{block.Hash()}, []uint64{block.NumberU64()}); err != nil {
				return
			}
			p.Log().Trace("Announced block", "number", block.Number(), "hash", block.Hash())

		case <-p.term:
			return
		}
	}
}

// broadcastTransactions is a write loop that schedules transaction broadcasts
// to the remote peer. The goal is to have an async writer that does not lock up
// node internals and at the same time rate limits queued data.
func (p *Peer) broadcastTransactions() {
	var (
		queue  []common.Hash // Queue of hashes to broadcast as full transactions
		done   chan struct{} // Non-nil if background broadcaster is running
		failed atomic.Bool
	)
	for {
		// If there's no in-flight broadcast running, check if a new one is needed
		if done == nil && len(queue) > 0 {
			// Pile transaction until we reach our allowed network limit
			var (
				hashesCount uint64
				txs         []*types.Transaction
				size        common.StorageSize
			)
			for i := 0; i < len(queue) && size < maxTxPacketSize; i++ {
				tx := p.txpool.Get(queue[i])

				// BOR specific - DO NOT REMOVE
				// Skip PIP-15 bundled transactions
				if tx != nil && tx.GetOptions() == nil {
					txs = append(txs, tx)
					size += common.StorageSize(tx.Size())
				}

				hashesCount++
			}
			queue = queue[:copy(queue, queue[hashesCount:])]

			// If there's anything available to transfer, fire up an async writer
			if len(txs) > 0 {
				done = make(chan struct{})
				go func() {
					if err := p.SendTransactions(txs); err != nil {
						failed.Store(true)
						p.Log().Debug("Broadcast: failed to send transactions, discarding future txs", "err", err)
						return
					}
					close(done)
					p.Log().Trace("Sent transactions", "count", len(txs))
				}()
			}
		}
		// Transfer goroutine may or may not have been started, listen for events
		select {
		case batch := <-p.txBroadcast:
			queue = retainTxPropagation(queue, batch, maxQueuedTxs, failed.Load())

		case <-done:
			done = nil

		case <-p.term:
			return
		}
	}
}

// announceTransactions is a write loop that schedules transaction broadcasts
// to the remote peer. The goal is to have an async writer that does not lock up
// node internals and at the same time rate limits queued data.
func (p *Peer) announceTransactions() {
	var (
		queue  []common.Hash // Queue of hashes to announce as transaction stubs
		done   chan struct{} // Non-nil if background announcer is running
		failed atomic.Bool
	)

	queueLimit := maxQueuedTxAnns
	if p.IsTrusted() || p.IsStatic() {
		queueLimit = maxQueuedTxAnnsTrusted
	}

	for {
		// If there's no in-flight announce running, check if a new one is needed
		if done == nil && len(queue) > 0 {
			// Pile transaction hashes until we reach our allowed network limit
			var (
				count        int
				pending      []common.Hash
				pendingTypes []byte
				pendingSizes []uint32
				size         common.StorageSize
			)
			for count = 0; count < len(queue) && size < maxTxPacketSize; count++ {
				tx := p.txpool.Get(queue[count])
				meta := p.txpool.GetMetadata(queue[count])
				// BOR specific - DO NOT REMOVE
				// Skip PIP-15 bundled transactions
				if tx != nil && meta != nil && tx.GetOptions() == nil {
					pending = append(pending, queue[count])
					pendingTypes = append(pendingTypes, meta.Type)
					pendingSizes = append(pendingSizes, uint32(meta.Size))
					size += common.HashLength
				}
			}
			// Shift and trim queue
			queue = queue[:copy(queue, queue[count:])]

			// If there's anything available to transfer, fire up an async writer
			if len(pending) > 0 {
				done = make(chan struct{})
				go func() {
					if err := p.sendPooledTransactionHashes(pending, pendingTypes, pendingSizes); err != nil {
						failed.Store(true)
						p.Log().Debug("Broadcast: failed to announce transactions, discarding future txs", "err", err)
						return
					}
					close(done)
					p.Log().Trace("Sent transaction announcements", "count", len(pending))
				}()
			}
		}
		// Transfer goroutine may or may not have been started, listen for events
		select {
		case batch := <-p.txAnnounce:
			queue = retainTxPropagation(queue, batch, queueLimit, failed.Load())

		case <-done:
			done = nil

		case <-p.term:
			return
		}
	}
}
