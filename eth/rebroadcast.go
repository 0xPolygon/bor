// Copyright 2026 The go-ethereum Authors
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

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

func (h *handler) subscribeRebroadcastTransactions(ch chan<- core.StuckTxsEvent) event.Subscription {
	if pool, ok := h.txpool.(interface {
		SubscribeRebroadcastTransactionsWithAcknowledgement(chan<- core.StuckTxsEvent) event.Subscription
	}); ok {
		return pool.SubscribeRebroadcastTransactionsWithAcknowledgement(ch)
	}
	return h.txpool.SubscribeRebroadcastTransactions(ch)
}

func (h *handler) rebroadcastAcknowledgement(txs []*types.Transaction) func([]common.Hash) {
	if pool, ok := h.txpool.(interface {
		RebroadcastAcknowledgement([]*types.Transaction) func([]common.Hash)
	}); ok {
		return pool.RebroadcastAcknowledgement(txs)
	}
	return nil
}

func assignTransactionPeers(hash common.Hash, peers []*ethPeer, directSet map[*ethPeer]struct{}, txset, annos map[*ethPeer][]common.Hash) {
	for _, peer := range peers {
		if peer.KnownTransaction(hash) {
			continue
		}
		if _, direct := directSet[peer]; direct {
			txset[peer] = append(txset[peer], hash)
		} else {
			annos[peer] = append(annos[peer], hash)
		}
	}
}

func queueTransactions(peers map[*ethPeer][]common.Hash, announce bool, onBroadcast func([]common.Hash)) int {
	count := 0
	for peer, hashes := range peers {
		send := peer.QueueTransactions
		if announce {
			send = peer.QueuePooledTransactionHashes
		}
		if retained := send(hashes); len(retained) > 0 {
			count += len(retained)
			if onBroadcast != nil {
				onBroadcast(retained)
			}
		}
	}
	return count
}

func (h *handler) canRebroadcast() bool {
	if !h.synced.Load() {
		return false
	}
	head := h.chain.CurrentBlock()
	if h.snapSync.Load() {
		head = h.chain.CurrentSnapBlock()
	}
	ourTD := h.chain.GetTd(head.Hash(), head.Number.Uint64())
	return h.rebroadcastAllowed(ourTD)
}

func (h *handler) rebroadcastAllowed(ourTD *big.Int) bool {
	if !h.synced.Load() {
		return false
	}
	if ourTD == nil {
		ourTD = new(big.Int)
	}
	for _, peer := range h.peers.all() {
		head, _ := peer.Head()
		if td := rebroadcastHeadTD(h.chain, head); td != nil && td.Cmp(ourTD) > 0 {
			return false
		}
	}
	return true
}

func rebroadcastHeadTD(chain *core.BlockChain, hash common.Hash) *big.Int {
	if header := chain.GetHeaderByHash(hash); header != nil {
		return chain.GetTd(hash, header.Number.Uint64())
	}
	return nil
}
