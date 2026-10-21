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
	"testing/synctest"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/eth/relay"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/params"
)

func TestEnableSyncedFeaturesRetriesFullQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, target := syncCompletionRecoveryFixture(t)
		prepareSyncRetryShutdown(t, h)
		filler, err := types.SignTx(types.NewTransaction(1, testAddr, big.NewInt(1), 21000,
			big.NewInt(params.BorDefaultTxPoolPriceLimit), nil), types.HomesteadSigner{}, testKey)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.txpool.Add(types.Transactions{filler}, true)[0]; err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		synctest.Wait()
		slow, sink := syncCompletionPeer(t, h, eth.ETH69, 1)
		fillSyncAnnouncementQueue(t, slow, filler.Hash())
		healthy, _ := syncCompletionPeer(t, h, eth.ETH69, 2)
		h.enableSyncedFeatures()
		synctest.Wait()
		if slow.KnownTransaction(target.Hash()) || !healthy.KnownTransaction(target.Hash()) {
			t.Fatal("queue rejection was not isolated to the saturated peer")
		}
		seen := readSyncRetryTarget(sink, target.Hash())
		time.Sleep(3 * syncAnnouncementRetry)
		synctest.Wait()
		select {
		case err := <-seen:
			if err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("expired pending transaction was lost after the queue recovered")
		}
		h.wg.Wait()
	})
}

func TestEnableSyncedFeaturesRetryStops(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "peer departure", true: "shutdown"}[shutdown], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, pool, txs := syncCompletionFixture()
				prepareSyncRetryShutdown(t, h)
				filler := types.NewTx(&types.LegacyTx{Nonce: 99})
				pool.pool[filler.Hash()] = filler
				peer, _ := syncCompletionPeer(t, h, eth.ETH69, 1)
				fillSyncAnnouncementQueue(t, peer, filler.Hash())
				h.enableSyncedFeatures()
				synctest.Wait()
				if shutdown {
					close(h.quitSync)
				} else if err := h.peers.unregisterPeer(peer.ID()); err != nil {
					t.Fatal(err)
				}
				time.Sleep(syncAnnouncementRetry)
				h.wg.Wait()
				if peer.KnownTransaction(txs[0].Hash()) {
					t.Fatal("rejected transaction was marked accepted")
				}
			})
		})
	}
}

func TestEnableSyncedFeaturesRetryRefilters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, pool, txs := syncCompletionFixture()
		prepareSyncRetryShutdown(t, h)
		private := relay.NewPrivateTxStore()
		defer private.Close()
		private.Add(txs[2].Hash())
		h.privateTxGetter = private
		filler := types.NewTx(&types.LegacyTx{Nonce: 99})
		pool.pool[filler.Hash()] = filler
		peer, _ := syncCompletionPeer(t, h, eth.ETH69, 1)
		fillSyncAnnouncementQueue(t, peer, filler.Hash())
		h.enableSyncedFeatures()
		synctest.Wait()
		private.Add(txs[0].Hash())
		pool.lock.Lock()
		delete(pool.pool, filler.Hash())
		pool.lock.Unlock()
		time.Sleep(syncAnnouncementRetry)
		h.wg.Wait()
		if peer.KnownTransaction(txs[0].Hash()) {
			t.Fatal("retry announced a transaction that became private")
		}
	})
}

func TestEnableSyncedFeaturesRetryPartialBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, pool, txs := syncCompletionFixture()
		filler := types.NewTx(&types.LegacyTx{Nonce: 99})
		last := types.NewTx(&types.LegacyTx{Nonce: 100})
		pool.pool[filler.Hash()], pool.pool[last.Hash()] = filler, last
		peer, sink := syncCompletionPeer(t, h, eth.ETH69, 1)
		fillSyncAnnouncementQueue(t, peer, filler.Hash())
		expectSyncCompletionAnnouncement(t, sink, filler)
		synctest.Wait()
		// The next packet now owns the previous full queue. Leave one slot
		// in the replacement queue for the first eligible snapshot entry.
		fill := make([]common.Hash, 16383)
		for i := range fill {
			fill[i] = filler.Hash()
		}
		if len(peer.QueuePooledTransactionHashes(fill)) != len(fill) {
			t.Fatal("could not leave exactly one queue slot")
		}
		hashes := []common.Hash{txs[1].Hash(), txs[0].Hash(), txs[2].Hash(), last.Hash()}
		if offset := h.queuePendingAnnouncements(h.peers.peer(peer.ID()), hashes); offset != 3 {
			t.Fatalf("partial acceptance advanced by %d, want first rejected index 3", offset)
		}
		if !peer.KnownTransaction(txs[0].Hash()) || peer.KnownTransaction(last.Hash()) || peer.KnownTransaction(txs[2].Hash()) {
			t.Fatal("partial acceptance marked the wrong transactions known")
		}
	})
}

func TestEnableSyncedFeaturesRetryBatchLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, pool, _ := syncCompletionFixture()
		peer, _ := syncCompletionPeer(t, h, eth.ETH69, 1)
		hashes := make([]common.Hash, syncAnnouncementBatch+1)
		for i := range hashes {
			tx := types.NewTx(&types.LegacyTx{Nonce: uint64(i + 10)})
			pool.pool[tx.Hash()] = tx
			hashes[i] = tx.Hash()
		}
		offset := h.queuePendingAnnouncements(h.peers.peer(peer.ID()), hashes)
		if offset != 256 || peer.KnownTransaction(hashes[256]) {
			t.Fatal("one retry exceeded the per-peer batch bound")
		}
		if next := h.queuePendingAnnouncements(h.peers.peer(peer.ID()), hashes[offset:]); next != 1 || !peer.KnownTransaction(hashes[256]) {
			t.Fatal("next batch did not announce the remainder")
		}
	})
}

func prepareSyncRetryShutdown(t *testing.T, h *handler) {
	t.Helper()
	h.quitSync = make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-h.quitSync:
		default:
			close(h.quitSync)
		}
		h.wg.Wait()
	})
}

func fillSyncAnnouncementQueue(t *testing.T, peer *eth.Peer, filler common.Hash) {
	t.Helper()
	if len(peer.QueuePooledTransactionHashes([]common.Hash{filler})) != 1 {
		t.Fatal("initial announcement was rejected")
	}
	synctest.Wait()
	hashes := make([]common.Hash, 65536)
	for i := range hashes {
		hashes[i] = filler
	}
	accepted := peer.QueuePooledTransactionHashes(hashes)
	if len(accepted) == 0 || len(accepted) == len(hashes) || len(peer.QueuePooledTransactionHashes(hashes[:1])) != 0 {
		t.Fatal("failed to saturate the announcement queue")
	}
}

func readSyncRetryTarget(sink *p2p.MsgPipeRW, target common.Hash) <-chan error {
	seen := make(chan error, 1)
	go func() {
		for {
			msg, err := sink.ReadMsg()
			if err != nil {
				seen <- err
				return
			}
			var packet eth.NewPooledTransactionHashesPacket
			if err := msg.Decode(&packet); err != nil {
				seen <- err
				return
			}
			for _, hash := range packet.Hashes {
				if hash == target {
					seen <- nil
					return
				}
			}
		}
	}()
	return seen
}
