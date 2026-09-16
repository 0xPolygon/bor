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
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

// Hide the optional acknowledgment capability to exercise existing pool implementations.
type legacyRebroadcastPool struct{ txPool }

var _ interface{ BroadcastTransactions(types.Transactions) } = (*handler)(nil)

func TestRebroadcastAcknowledgementLegacyPool(t *testing.T) {
	h, txs := rebroadcastDeliveryFixture(t, false)
	h.txpool = &legacyRebroadcastPool{h.txpool}
	if h.rebroadcastAcknowledgement(txs) != nil {
		t.Fatal("legacy pools must remain usable without the optional callback")
	}
}

func TestRebroadcastPeerAssignment(t *testing.T) {
	for _, mode := range []string{"direct", "announce", "known"} {
		t.Run(mode, func(t *testing.T) {
			h, txs := rebroadcastDeliveryFixture(t, true)
			peers := h.peers.all()
			peer, hash := peers[0], txs[0].Hash()
			direct := make(map[*ethPeer]struct{})
			if mode == "direct" || mode == "known" {
				direct[peer] = struct{}{}
			}
			if mode == "known" {
				peer.AsyncSendTransactions([]common.Hash{hash})
			}
			bodies, annos := make(map[*ethPeer][]common.Hash), make(map[*ethPeer][]common.Hash)
			assignTransactionPeers(hash, peers, direct, bodies, annos)
			if (len(bodies) == 1) != (mode == "direct") || (len(annos) == 1) != (mode == "announce") {
				t.Fatal("peer assignment did not respect body selection and known hashes")
			}
			for _, set := range []map[*ethPeer][]common.Hash{bodies, annos} {
				if hashes := set[peer]; len(hashes) > 0 && (len(hashes) != 1 || hashes[0] != hash) {
					t.Fatal("peer assignment changed the transaction hash")
				}
			}
		})
	}
}

func rebroadcastDeliveryFixture(t *testing.T, withPeer bool) (*handler, types.Transactions) {
	t.Helper()
	h, cleanup := newChainSyncerTestHandler(t)
	t.Cleanup(cleanup)
	h.enableSyncedFeatures()
	var txs types.Transactions
	pool := h.txpool.(*testTxPool)
	for i := uint64(0); i < 2; i++ {
		tx, err := types.SignTx(types.NewTransaction(i, testAddr, big.NewInt(1), 21000, big.NewInt(1), nil), types.HomesteadSigner{}, testKey)
		if err != nil {
			t.Fatal(err)
		}
		pool.pool[tx.Hash()] = tx
		txs = append(txs, tx)
	}
	if withPeer {
		app, net := p2p.MsgPipe()
		peer := eth.NewPeer(eth.ETH69, p2p.NewPeer(enode.ID{1}, "test", nil), net, pool)
		t.Cleanup(func() { peer.Close(); app.Close(); net.Close() })
		if err := h.peers.registerPeer(peer, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	return h, txs
}

func TestRebroadcastAcknowledgesOnlyQueuedTransactions(t *testing.T) {
	for _, mode := range []string{"suppressed", "no peers", "private", "conditional", "bodies", "announcements", "mixed"} {
		t.Run(mode, func(t *testing.T) {
			h, txs := rebroadcastDeliveryFixture(t, mode != "no peers")
			want := len(txs)
			switch mode {
			case "suppressed":
				h.synced.Store(false)
				want = 0
			case "no peers":
				want = 0
			case "private":
				h.privateTxGetter = &PrivateTxStore{store: map[common.Hash]struct{}{txs[0].Hash(): {}, txs[1].Hash(): {}}}
				want = 0
			case "conditional":
				for _, tx := range txs {
					tx.PutOptions(new(types.OptionsPIP15))
				}
				want = 0
			case "mixed":
				h.privateTxGetter = &PrivateTxStore{store: map[common.Hash]struct{}{txs[1].Hash(): {}}}
				want = 1
			case "announcements":
				h.txAnnouncementOnly = true
			}
			var acknowledged []common.Hash
			sent := h.rebroadcastStuckTransactions(txs, func(hashes []common.Hash) {
				acknowledged = append(acknowledged, hashes...)
			})
			if sent != (want > 0) || len(acknowledged) != want {
				t.Fatalf("sent = %v, acknowledged = %d, want %d", sent, len(acknowledged), want)
			}
			for i, hash := range acknowledged {
				if hash != txs[i].Hash() {
					t.Fatal("acknowledged the wrong transaction")
				}
			}
		})
	}
}
