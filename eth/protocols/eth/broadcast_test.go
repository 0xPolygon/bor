// Copyright 2024 The go-ethereum Authors
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
	"io"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestTxPropagationRetention(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		queued, incoming, want int
		failed                 bool
	}{
		{"empty", 0, 0, 0, false},
		{"available", 0, 3, 3, false},
		{"oversized", 0, 6, 4, false},
		{"partial", 2, 3, 2, false},
		{"full", 4, 1, 0, false},
		{"lowered limit", 6, 1, 0, false},
		{"failed", 0, 2, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previous := []common.Hash{{1}, {2}, {3}, {4}, {5}, {6}}[:tc.queued]
			hashes := []common.Hash{{5}, {6}, {7}, {8}, {9}, {10}}[:tc.incoming]
			batch := &txPropagation{hashes: hashes, retained: make(chan []common.Hash, 1)}
			queue := retainTxPropagation(slices.Clone(previous), batch, 4, tc.failed)
			retained := <-batch.retained
			if !slices.Equal(retained, hashes[:tc.want]) || !slices.Equal(queue, append(slices.Clone(previous), retained...)) {
				t.Fatal("retention discarded queued hashes or acknowledged rejected hashes")
			}
		})
	}
}

func TestPeerStaticAnnouncementCapacity(t *testing.T) {
	stop := make(chan struct{})
	remote := limitsTestServer(t, nil, nil, func(*p2p.Peer, p2p.MsgReadWriter) error { <-stop; return nil })
	connected := make(chan *p2p.Peer, 1)
	local := limitsTestServer(t, nil, nil, func(p *p2p.Peer, _ p2p.MsgReadWriter) error {
		connected <- p
		<-stop
		return nil
	})
	t.Cleanup(func() { close(stop) })
	remote.AddPeer(local.Self())
	var connection *p2p.Peer
	select {
	case connection = <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("inbound peer did not connect")
	}
	source, sink := p2p.MsgPipe()
	defer source.Close()
	defer sink.Close()
	pool := &broadcastTestPool{tx: types.NewTx(&types.LegacyTx{})}
	peer := NewPeer(ETH68, connection, source, pool)
	defer peer.Close()
	if len(peer.QueuePooledTransactionHashes([]common.Hash{pool.tx.Hash()})) != 1 {
		t.Fatal("initial announcement was not retained")
	}
	local.AddPeer(remote.Self())
	hashes := make([]common.Hash, maxQueuedTxAnns+1)
	if retained := peer.QueuePooledTransactionHashes(hashes); len(retained) != len(hashes) {
		t.Fatalf("runtime static peer retained %d of %d announcements", len(retained), len(hashes))
	}
}

type broadcastTestPool struct {
	TxPool
	tx *types.Transaction
}

func (p *broadcastTestPool) Get(common.Hash) *types.Transaction { return p.tx }
func (p *broadcastTestPool) GetMetadata(common.Hash) *txpool.TxMetadata {
	return &txpool.TxMetadata{Type: p.tx.Type(), Size: p.tx.Size()}
}

func TestTxPropagationRejectsFailedWriter(t *testing.T) {
	for _, announce := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			pool := &broadcastTestPool{tx: types.NewTx(&types.LegacyTx{})}
			peer := NewPeer(ETH68, p2p.NewPeer(enode.ID{1}, "", nil), &replyTestRW{err: io.ErrClosedPipe}, pool)
			defer peer.Close()
			send := peer.QueueTransactions
			if announce {
				send = peer.QueuePooledTransactionHashes
			}
			first := pool.tx.Hash()
			if got := send([]common.Hash{first}); !slices.Equal(got, []common.Hash{first}) {
				t.Fatal("initial propagation was not retained")
			}
			synctest.Wait()
			rejected := common.Hash{2}
			if len(send([]common.Hash{rejected})) != 0 || peer.KnownTransaction(rejected) {
				t.Fatal("failed writer accepted or marked the rejected transaction known")
			}
		})
	}
}

func TestTxPropagationAfterClose(t *testing.T) {
	peer := &Peer{term: make(chan struct{})}
	peer.Close()
	if len(peer.QueueTransactions([]common.Hash{{1}})) != 0 || len(peer.QueuePooledTransactionHashes([]common.Hash{{1}})) != 0 {
		t.Fatal("closed peer accepted transactions")
	}
}
