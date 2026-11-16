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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

type attemptReportingPool struct {
	txPool
	attempted []common.Hash
}

func (p *attemptReportingPool) RebroadcastAttempt(txs []*types.Transaction) func([]common.Hash) {
	return func(hashes []common.Hash) { p.attempted = append(p.attempted, hashes...) }
}
func TestRebroadcastActualQueueAttempts(t *testing.T) {
	for _, mode := range []string{"suppressed", "disabled", "no peers", "private", "rejected", "accepted"} {
		t.Run(mode, func(t *testing.T) {
			h, txs := rebroadcastDeliveryFixture(t, mode != "no peers" && mode != "rejected")
			pool := &attemptReportingPool{txPool: h.txpool}
			h.txpool = pool
			switch mode {
			case "suppressed":
				h.synced.Store(false)
			case "disabled":
				h.disableTxPropagation = true
			case "private":
				h.privateTxGetter = &PrivateTxStore{store: map[common.Hash]struct{}{txs[0].Hash(): {}, txs[1].Hash(): {}}}
			case "rejected":
				app, net := p2p.MsgPipe()
				peer := ethproto.NewPeer(ethproto.ETH69, p2p.NewPeer(enode.ID{2}, "closed", nil), net, pool)
				require.NoError(t, h.peers.registerPeer(peer, nil, nil))
				peer.Close()
				t.Cleanup(func() { app.Close(); net.Close() })
			}
			accepted := h.rebroadcastStuckTransactions(txs, nil)
			require.Equal(t, mode == "accepted", accepted)
			if mode == "accepted" || mode == "rejected" {
				require.ElementsMatch(t, []common.Hash{txs[0].Hash(), txs[1].Hash()}, pool.attempted)
			} else {
				require.Empty(t, pool.attempted)
			}
			if mode == "suppressed" {
				h.synced.Store(true)
				require.True(t, h.rebroadcastStuckTransactions(txs, nil))
				require.Len(t, pool.attempted, len(txs))
			}
		})
	}
}
