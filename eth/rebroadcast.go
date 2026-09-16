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
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
)

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
		if send(hashes) {
			count += len(hashes)
			if onBroadcast != nil {
				onBroadcast(hashes)
			}
		}
	}
	return count
}

const (
	rebroadcastPeerGrace      = time.Minute
	maxRebroadcastPeerClaims  = 4096
	maxRebroadcastClaimChecks = 64
)

type rebroadcastState struct {
	mu          sync.Mutex
	claims      map[string]*rebroadcastPeerClaim
	claimOrder  []string
	claimFree   []int
	claimCursor int
}

type rebroadcastPeerClaim struct {
	head  common.Hash
	td    *big.Int
	until time.Time
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
	return h.rebroadcastAllowed(ourTD, time.Now())
}

func (h *handler) rebroadcastAllowed(ourTD *big.Int, now time.Time) bool {
	if !h.synced.Load() {
		return false
	}
	if ourTD == nil {
		ourTD = new(big.Int)
	}
	state := &h.rebroadcast
	state.mu.Lock()
	defer state.mu.Unlock()
	state.clearVerifiedClaims(h.chain, ourTD)

	allowed := true
	for _, peer := range h.peers.all() {
		// Observe every peer so one outstanding claim cannot defer another's deadline.
		if state.blocksRebroadcast(peer, h.chain, ourTD, now) {
			allowed = false
		}
	}
	return allowed
}

func (s *rebroadcastState) clearVerifiedClaims(chain *core.BlockChain, ourTD *big.Int) {
	if len(s.claims) == 0 {
		s.claimOrder = nil
		s.claimFree = nil
		s.claimCursor = 0
		return
	}
	checks := min(maxRebroadcastClaimChecks, len(s.claimOrder))
	for range checks {
		if s.claimCursor == len(s.claimOrder) {
			s.claimCursor = 0
		}
		index := s.claimCursor
		id := s.claimOrder[index]
		s.claimCursor++
		claim := s.claims[id]
		if claim == nil {
			continue
		}
		if claim.caughtUp(chain, ourTD) {
			delete(s.claims, id)
			s.claimOrder[index] = ""
			s.claimFree = append(s.claimFree, index)
		}
	}
}

func (s *rebroadcastState) addClaim(id string, claim *rebroadcastPeerClaim) {
	s.claims[id] = claim
	if len(s.claimFree) == 0 {
		s.claimOrder = append(s.claimOrder, id)
		return
	}
	last := len(s.claimFree) - 1
	index := s.claimFree[last]
	s.claimFree = s.claimFree[:last]
	s.claimOrder[index] = id
}

func (s *rebroadcastState) blocksRebroadcast(p *ethPeer, chain *core.BlockChain, ourTD *big.Int, now time.Time) bool {
	head, td := p.Head()
	if known := rebroadcastHeadTD(chain, head); known != nil {
		return known.Cmp(ourTD) > 0
	}
	state := s.claims[p.ID()]
	if td.Cmp(ourTD) <= 0 {
		return false
	}
	if state == nil {
		// Do not evict unresolved claims: reconnecting after eviction would reset the bound.
		if len(s.claims) >= maxRebroadcastPeerClaims {
			return false
		}
		if s.claims == nil {
			s.claims = make(map[string]*rebroadcastPeerClaim)
		}
		state = &rebroadcastPeerClaim{head: head, td: new(big.Int).Set(td), until: now.Add(rebroadcastPeerGrace)}
		s.addClaim(p.ID(), state)
	}
	return now.Before(state.until)
}

func (c *rebroadcastPeerClaim) caughtUp(chain *core.BlockChain, ourTD *big.Int) bool {
	if ourTD.Cmp(c.td) < 0 {
		return false
	}
	known := rebroadcastHeadTD(chain, c.head)
	return known != nil && known.Cmp(c.td) == 0
}

func rebroadcastHeadTD(chain *core.BlockChain, hash common.Hash) *big.Int {
	if header := chain.GetHeaderByHash(hash); header != nil {
		return chain.GetTd(hash, header.Number.Uint64())
	}
	return nil
}
