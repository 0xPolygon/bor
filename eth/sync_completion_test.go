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
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
)

func TestEnableSyncedFeaturesAnnouncesExpiredPending(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, tx := syncCompletionRecoveryFixture(t)
		peer, sink := syncCompletionPeer(t, h, eth.ETH69, 1)
		h.syncTransactions(peer)
		expectSyncCompletionAnnouncement(t, sink, tx)
		pool := h.txpool.(*txpool.TxPool)
		events := make(chan core.StuckTxsEvent, 1)
		sub := pool.SubscribeRebroadcastTransactionsWithAcknowledgement(events)
		defer sub.Unsubscribe()
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case event := <-events:
			if len(event.Txs) != 1 || event.Txs[0].Hash() != tx.Hash() {
				t.Fatal("unexpected pending transaction selected")
			}
			if h.rebroadcastStuckTransactions(event.Txs, pool.RebroadcastAcknowledgement(event.Txs)) {
				t.Fatal("initial sync must suppress periodic rebroadcast")
			}
			// Model an actual queue offer before its retry window expires during sync.
			h.txAnnouncementOnly = true
			h.peers.ForgetTransactions([]common.Hash{tx.Hash()})
			if !h.broadcastTransactions(event.Txs, pool.RebroadcastAcknowledgement(event.Txs), pool.RebroadcastAttempt(event.Txs)) {
				t.Fatal("fixture must offer the transaction to a peer queue")
			}
			expectSyncCompletionAnnouncement(t, sink, tx)
		default:
			t.Fatal("missing first rebroadcast attempt")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		select {
		case <-events:
			t.Fatal("expired attempt must not remain eligible for periodic rebroadcast")
		default:
		}
		if !pool.Has(tx.Hash()) || !peer.KnownTransaction(tx.Hash()) {
			t.Fatal("test requires a retained transaction already announced on this connection")
		}
		h.enableSyncedFeatures()
		expectSyncCompletionAnnouncement(t, sink, tx)
	})
}

func TestEnableSyncedFeaturesPendingFilters(t *testing.T) {
	for _, test := range []struct {
		name, remove string
		disabled     bool
		want         bool
	}{
		{name: "public", want: true},
		{name: "filtered only", remove: "public"},
		{name: "empty", remove: "all"},
		{name: "disabled", disabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, pool, txs := syncCompletionFixture()
				h.disableTxPropagation = test.disabled
				if test.remove == "public" {
					delete(pool.pool, txs[0].Hash())
				} else if test.remove == "all" {
					clear(pool.pool)
				}
				peer, sink := syncCompletionPeer(t, h, eth.ETH69, 1)
				h.snapSync.Store(true)
				h.enableSyncedFeatures()
				synctest.Wait()
				if !h.synced.Load() || h.snapSync.Load() {
					t.Fatal("sync completion did not preserve the sync state transition")
				}
				if peer.KnownTransaction(txs[0].Hash()) != test.want || peer.KnownTransaction(txs[1].Hash()) || peer.KnownTransaction(txs[2].Hash()) {
					t.Fatal("sync completion announced an unexpected transaction set")
				}
				if test.want {
					expectSyncCompletionAnnouncement(t, sink, txs[0])
				} else {
					expectNoSyncCompletionAnnouncement(t, sink, txs[0])
				}
				if test.disabled && pool.snapshots.Load() != 0 {
					t.Fatal("disabled propagation read the pending pool")
				}
			})
		})
	}
}

func TestEnableSyncedFeaturesSharesSnapshotOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, pool, txs := syncCompletionFixture()
		var sinks []*p2p.MsgPipeRW
		for i, protocol := range eth.ProtocolVersions {
			_, sink := syncCompletionPeer(t, h, protocol, byte(i+1))
			sinks = append(sinks, sink)
		}
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(h.enableSyncedFeatures)
		}
		wg.Wait()
		for _, sink := range sinks {
			expectSyncCompletionAnnouncement(t, sink, txs[0])
		}
		h.snapSync.Store(true)
		h.enableSyncedFeatures()
		if pool.snapshots.Load() != 1 || h.snapSync.Load() {
			t.Fatal("repeated sync completion must preserve snap state without another pool snapshot")
		}
		for _, sink := range sinks {
			expectNoSyncCompletionAnnouncement(t, sink, txs[0])
		}
	})
}

type syncCompletionPool struct {
	*testTxPool
	snapshots atomic.Int32
}

func (p *syncCompletionPool) Pending(filter txpool.PendingFilter, interrupt *atomic.Bool) map[common.Address][]*txpool.LazyTransaction {
	p.snapshots.Add(1)
	return p.testTxPool.Pending(filter, interrupt)
}

func syncCompletionFixture() (*handler, *syncCompletionPool, types.Transactions) {
	pool := &syncCompletionPool{testTxPool: newTestTxPool()}
	var txs types.Transactions
	for nonce := range uint64(3) {
		tx := types.NewTx(&types.LegacyTx{Nonce: nonce})
		pool.pool[tx.Hash()] = tx
		txs = append(txs, tx)
	}
	txs[1].PutOptions(new(types.OptionsPIP15))
	h := &handler{txpool: pool, peers: newPeerSet(), privateTxGetter: &PrivateTxStore{
		store: map[common.Hash]struct{}{txs[2].Hash(): {}},
	}}
	return h, pool, txs
}

func syncCompletionRecoveryFixture(t *testing.T) (*handler, *types.Transaction) {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	genesis := &core.Genesis{Config: params.TestChainConfig, Alloc: types.GenesisAlloc{
		testAddr: {Balance: big.NewInt(1e18)},
	}}
	chain, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(chain.Stop)
	config := legacypool.DefaultConfig
	config.Journal, config.NoLocals = "", true
	config.RebroadcastInterval, config.RebroadcastMaxAge = time.Second, time.Hour
	pool, err := txpool.New(1, chain, []txpool.SubPool{legacypool.New(config, chain)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pool.Close(); err != nil {
			t.Error(err)
		}
	})
	tx, err := types.SignTx(types.NewTransaction(0, testAddr, big.NewInt(1), 21000, big.NewInt(params.BorDefaultTxPoolPriceLimit), nil), types.HomesteadSigner{}, testKey)
	if err != nil {
		t.Fatal(err)
	}
	tx.SetTime(time.Now().Add(-2 * time.Hour))
	if err := pool.Add(types.Transactions{tx}, true)[0]; err != nil {
		t.Fatal(err)
	}
	return &handler{txpool: pool, peers: newPeerSet(), chain: chain}, tx
}

func syncCompletionPeer(t *testing.T, h *handler, protocol uint, id byte) (*eth.Peer, *p2p.MsgPipeRW) {
	t.Helper()
	source, sink := p2p.MsgPipe()
	peer := eth.NewPeer(protocol, p2p.NewPeer(enode.ID{id}, "", nil), source, h.txpool)
	t.Cleanup(func() {
		peer.Close()
		source.Close()
		sink.Close()
	})
	if err := h.peers.registerPeer(peer, nil, nil); err != nil {
		t.Fatal(err)
	}
	return peer, sink
}

func syncCompletionAnnouncement(sink *p2p.MsgPipeRW, tx *types.Transaction) <-chan error {
	result := make(chan error, 1)
	go func() {
		result <- p2p.ExpectMsg(sink, eth.NewPooledTransactionHashesMsg, &eth.NewPooledTransactionHashesPacket{
			Types: []byte{tx.Type()}, Sizes: []uint32{uint32(tx.Size())}, Hashes: []common.Hash{tx.Hash()},
		})
	}()
	return result
}

func expectSyncCompletionAnnouncement(t *testing.T, sink *p2p.MsgPipeRW, tx *types.Transaction) {
	t.Helper()
	result := syncCompletionAnnouncement(sink, tx)
	synctest.Wait()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("missing pending announcement on the existing connection")
	}
}

func expectNoSyncCompletionAnnouncement(t *testing.T, sink *p2p.MsgPipeRW, tx *types.Transaction) {
	t.Helper()
	result := syncCompletionAnnouncement(sink, tx)
	synctest.Wait()
	select {
	case err := <-result:
		t.Fatalf("unexpected pending announcement: %v", err)
	default:
	}
}
