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
	"context"
	"log/slog"
	"math/big"
	"slices"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/params"
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
	sub := h.subscribeRebroadcastTransactions(make(chan core.StuckTxsEvent, 1))
	sub.Unsubscribe()
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

func TestRebroadcastEventsExcludeNonGossipableTransactions(t *testing.T) {
	stack, err := node.New(&node.Config{DataDir: t.TempDir(), P2P: p2p.Config{NoDiscovery: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stack.Close(); err != nil {
			t.Error(err)
		}
	})
	config := ethconfig.Defaults
	config.Genesis = loadBorTestGenesis(t)
	config.Genesis.Alloc[testAddr] = types.Account{Balance: big.NewInt(1e18)}
	config.WithoutHeimdall, config.AcceptPrivateTx = true, true
	config.TxPool.Journal, config.TxPool.NoLocals = "", true
	config.TxPool.RebroadcastInterval, config.TxPool.RebroadcastBatchSize = time.Second, 2
	backend, err := New(stack, &config)
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.Start(); err != nil {
		t.Fatal(err)
	}
	ch := make(chan core.StuckTxsEvent, 10)
	sub := backend.txPool.SubscribeRebroadcastTransactionsWithAcknowledgement(ch)
	defer sub.Unsubscribe()
	var txs types.Transactions
	for nonce := uint64(0); nonce < 3; nonce++ {
		tx, err := types.SignTx(types.NewTransaction(nonce, testAddr, big.NewInt(1), 21000, big.NewInt(params.BorDefaultTxPoolPriceLimit), nil), types.LatestSigner(config.Genesis.Config), testKey)
		if err != nil {
			t.Fatal(err)
		}
		tx.SetTime(time.Now().Add(-time.Hour))
		txs = append(txs, tx)
	}
	backend.APIBackend.RecordPrivateTx(txs[0].Hash())
	txs[1].PutOptions(new(types.OptionsPIP15))
	for _, err := range backend.txPool.Add(txs, true) {
		if err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case event := <-ch:
			if len(event.Txs) != 1 || event.Txs[0].Hash() != txs[2].Hash() {
				t.Fatal("rebroadcast event must contain only the public transaction")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the public rebroadcast candidate")
		}
	}
}

func TestRebroadcastEmptyQueueAcknowledgement(t *testing.T) {
	h, _ := rebroadcastDeliveryFixture(t, true)
	for _, announce := range []bool{false, true} {
		peers := map[*ethPeer][]common.Hash{h.peers.all()[0]: nil}
		count := queueTransactions(peers, announce, func([]common.Hash) {
			t.Error("empty batch must not acknowledge a rebroadcast")
		})
		if count != 0 {
			t.Fatal("empty batch counted as a rebroadcast")
		}
	}
}

type rebroadcastLogCapture struct {
	slog.Handler
	records chan slog.Record
}

func (h *rebroadcastLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (h *rebroadcastLogCapture) Handle(_ context.Context, record slog.Record) error {
	if record.Message == "Distributed transactions" {
		h.records <- record.Clone()
	}
	return nil
}

func TestRebroadcastDistributionCounts(t *testing.T) {
	h, txs := rebroadcastDeliveryFixture(t, false)
	txs[1].PutOptions(new(types.OptionsPIP15))
	txs = append(txs, types.NewTx(&types.BlobTx{}), types.NewTx(&types.LegacyTx{Data: make([]byte, txMaxBroadcastSize+1)}))
	capture := &rebroadcastLogCapture{Handler: log.DiscardHandler(), records: make(chan slog.Record, 1)}
	previous := log.Root()
	log.SetDefault(log.NewLogger(capture))
	defer log.SetDefault(previous)
	if h.broadcastTransactions(txs, nil) {
		t.Fatal("broadcast reported delivery without peers")
	}
	select {
	case record := <-capture.records:
		counts := make(map[string]int64)
		record.Attrs(func(attr slog.Attr) bool {
			counts[attr.Key] = attr.Value.Int64()
			return true
		})
		for key, want := range map[string]int64{"plaintxs": 1, "blobtxs": 1, "largetxs": 1, "conditionaltxs": 1, "bcastcount": 0, "anncount": 0} {
			if got, ok := counts[key]; !ok || got != want {
				t.Errorf("distribution count %s: got %d (present %v), want %d", key, got, ok, want)
			}
		}
	default:
		t.Fatal("transaction distribution counts were not logged")
	}
}

func TestRebroadcastQueueDeliveryMode(t *testing.T) {
	for _, announce := range []bool{false, true} {
		t.Run(map[bool]string{false: "bodies", true: "announcements"}[announce], func(t *testing.T) {
			h, txs := rebroadcastDeliveryFixture(t, false)
			source, sink := p2p.MsgPipe()
			peer := eth.NewPeer(eth.ETH69, p2p.NewPeer(enode.ID{1}, "", nil), source, h.txpool)
			defer peer.Close()
			defer source.Close()
			defer sink.Close()
			hashes := []common.Hash{txs[0].Hash(), txs[1].Hash()}
			var acknowledged []common.Hash
			count := queueTransactions(map[*ethPeer][]common.Hash{{Peer: peer}: hashes}, announce, func(retained []common.Hash) {
				acknowledged = append(acknowledged, retained...)
			})
			if count != len(hashes) || !slices.Equal(acknowledged, hashes) {
				t.Fatal("queued hashes were not acknowledged")
			}
			if announce {
				packet := eth.NewPooledTransactionHashesPacket{Hashes: hashes}
				for _, tx := range txs {
					packet.Types = append(packet.Types, tx.Type())
					packet.Sizes = append(packet.Sizes, uint32(tx.Size()))
				}
				if err := p2p.ExpectMsg(sink, eth.NewPooledTransactionHashesMsg, packet); err != nil {
					t.Fatal(err)
				}
			} else if err := p2p.ExpectMsg(sink, eth.TransactionsMsg, txs); err != nil {
				t.Fatal(err)
			}
		})
	}
}
