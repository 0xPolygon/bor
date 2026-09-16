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
)

const (
	rebroadcastPeerGrace     = time.Minute
	maxRebroadcastPeerClaims = 4096
)

type rebroadcastState struct {
	mu     sync.Mutex
	claims map[string]*rebroadcastPeerClaim
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

	allowed := true
	for _, peer := range h.peers.all() {
		// Observe every peer so one outstanding claim cannot defer another's deadline.
		if state.blocksRebroadcast(peer, h.chain, ourTD, now) {
			allowed = false
		}
	}
	return allowed
}

func (s *rebroadcastState) blocksRebroadcast(p *ethPeer, chain *core.BlockChain, ourTD *big.Int, now time.Time) bool {
	head, td := p.Head()
	if known := rebroadcastHeadTD(chain, head); known != nil {
		td = known
	}
	state := s.claims[p.ID()]
	// Expiry and disconnects retain the claim; only verified catch-up can renew it.
	if state != nil && state.caughtUp(chain, ourTD) {
		delete(s.claims, p.ID())
		state = nil
	}
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
		s.claims[p.ID()] = state
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
