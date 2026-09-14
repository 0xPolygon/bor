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

const rebroadcastPeerGrace = time.Minute

type rebroadcastPeerState struct {
	mu    sync.Mutex
	head  common.Hash
	td    *big.Int
	until time.Time
}

func (h *handler) canRebroadcast() bool {
	_, ourTD := h.chainSync.modeAndLocalHead()
	return h.rebroadcastAllowed(ourTD, time.Now())
}

func (h *handler) rebroadcastAllowed(ourTD *big.Int, now time.Time) bool {
	if !h.synced.Load() {
		return false
	}
	if ourTD == nil {
		ourTD = new(big.Int)
	}
	allowed := true
	for _, peer := range h.peers.all() {
		// Observe every peer so one outstanding claim cannot defer another's deadline.
		if peer.blocksRebroadcast(h.chain, ourTD, now) {
			allowed = false
		}
	}
	return allowed
}

func (p *ethPeer) blocksRebroadcast(chain *core.BlockChain, ourTD *big.Int, now time.Time) bool {
	state := &p.rebroadcast
	state.mu.Lock()
	defer state.mu.Unlock()

	head, td := p.Head()
	if known := rebroadcastHeadTD(chain, head); known != nil {
		td = known
	}
	// Only catching up to the previously claimed, locally verified TD renews
	// the window. New announcements, retries, and unrelated blocks cannot renew it.
	if state.td != nil && ourTD.Cmp(state.td) >= 0 {
		if known := rebroadcastHeadTD(chain, state.head); known != nil && known.Cmp(state.td) == 0 {
			state.td = nil
		}
	}
	if td.Cmp(ourTD) <= 0 {
		return false
	}
	if state.td == nil {
		state.head, state.td = head, new(big.Int).Set(td)
		state.until = now.Add(rebroadcastPeerGrace)
	}
	return now.Before(state.until)
}

func rebroadcastHeadTD(chain *core.BlockChain, hash common.Hash) *big.Int {
	if header := chain.GetHeaderByHash(hash); header != nil {
		return chain.GetTd(hash, header.Number.Uint64())
	}
	return nil
}
