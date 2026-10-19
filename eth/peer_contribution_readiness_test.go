package eth

import (
	"io"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/downloader"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

func newContributionTestHandler(t *testing.T, clock mclock.Clock, fresh bool) *testHandler {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	genesis := &core.Genesis{Config: params.TestChainConfig,
		Alloc: types.GenesisAlloc{testAddr: {Balance: big.NewInt(1000000)}}}
	chain, err := core.NewBlockChain(db, genesis, ethash.NewFaker(), nil)
	require.NoError(t, err)
	pool := newTestTxPool()
	backend, err := newHandler(&handlerConfig{Database: db, Chain: chain, TxPool: pool,
		Network: 1, Sync: downloader.SnapSync, BloomCache: 1})
	require.NoError(t, err)
	backend.peerPolicy = peerpolicy.New(clock)
	h := &testHandler{db: db, chain: chain, txpool: pool, handler: backend}
	if fresh {
		insertContributionHead(t, h, time.Now().Unix())
	}
	backend.Start(1000)
	t.Cleanup(h.close)
	return h
}

func insertContributionHead(t *testing.T, h *testHandler, timestamp int64) {
	t.Helper()
	parent := h.chain.GetBlockByHash(h.chain.CurrentBlock().Hash())
	blocks, _ := core.GenerateChain(h.chain.Config(), parent, h.chain.Engine(), h.db, 1, func(_ int, block *core.BlockGen) {
		block.OffsetTime(timestamp - int64(parent.Time()) - 10)
	})
	_, err := h.chain.InsertChain(blocks, false)
	require.NoError(t, err)
	require.Equal(t, uint64(timestamp), h.chain.CurrentBlock().Time)
}

func updateContributionAt(h *handler, now int64) {
	h.policyMu.Lock()
	defer h.policyMu.Unlock()
	h.updateContributionReadinessLocked(now)
}

func TestPeerContributionHeadFreshness(t *testing.T) {
	fixture := newContributionTestHandler(t, new(mclock.Simulated), true)
	headTime := int64(fixture.chain.CurrentBlock().Time)
	for _, test := range []struct {
		name   string
		now    int64
		chain  *core.BlockChain
		status string
	}{
		{"head timestamp", headTime, fixture.chain, "warming-up"},
		{"at age limit", headTime + 120, fixture.chain, "warming-up"},
		{"past age limit", headTime + 121, fixture.chain, "unknown"},
		{"early head", headTime - 1, fixture.chain, "warming-up"},
		{"at future limit", headTime - 30, fixture.chain, "warming-up"},
		{"past future limit", headTime - 31, fixture.chain, "unknown"},
		{"negative clock", -1, fixture.chain, "unknown"},
		{"no chain", headTime, nil, "unknown"},
		{"no head", headTime, new(core.BlockChain), "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := &handler{chain: test.chain, peerPolicy: peerpolicy.New(new(mclock.Simulated))}
			h.synced.Store(true)
			h.peerPolicy.Connected("peer")
			updateContributionAt(h, test.now)
			require.Equal(t, test.status, h.peerPolicy.Snapshot("peer").Contribution.Status)
		})
	}
}

func TestPeerContributionTimestampBounds(t *testing.T) {
	for _, test := range []struct {
		name string
		head uint64
		now  int64
		want bool
	}{
		{"zero timestamp", 0, 0, true},
		{"maximum timestamp", math.MaxUint64, math.MaxInt64, false},
		{"maximum timestamp from zero", math.MaxUint64, 0, false},
		{"maximum clock", math.MaxInt64, math.MaxInt64, true},
		{"future boundary past signed range", uint64(math.MaxInt64) + 30, math.MaxInt64, true},
		{"future past boundary signed range", uint64(math.MaxInt64) + 31, math.MaxInt64, false},
		{"negative time with zero head", 0, -1, false},
		{"negative time with maximum head", math.MaxUint64, -1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, contributionHeadFresh(test.head, test.now))
		})
	}
}

func TestPeerContributionHeadEventRefreshesReadiness(t *testing.T) {
	clock := new(mclock.Simulated)
	h := newContributionTestHandler(t, clock, false)
	h.handler.peerPolicy.Connected("peer")
	h.handler.enableSyncedFeatures()
	require.Equal(t, "unknown", h.handler.peerPolicy.Snapshot("peer").Contribution.Status)
	clock.Run(10 * time.Minute)
	insertContributionHead(t, h, time.Now().Unix())
	require.Eventually(t, func() bool {
		return h.handler.peerPolicy.Snapshot("peer").Contribution.Status == "warming-up"
	}, time.Second, time.Millisecond, "head event must refresh readiness without admin inspection")
	clock.Run(10 * time.Minute)
	require.Equal(t, "idle", h.handler.peerPolicy.Snapshot("peer").Contribution.Status)
}

func TestPeerContributionPeerInfoRefreshesFreshness(t *testing.T) {
	clock := new(mclock.Simulated)
	h := newContributionTestHandler(t, clock, false)
	peer, cleanup := registerEthWitPeer(t, h, wit.WIT2)
	defer cleanup()
	h.handler.observeProtocolPeer(h.handler.peers.peer(peer.ID()).Peer)
	h.handler.synced.Store(true)
	h.handler.snapSync.Store(false)
	h.handler.peerPolicy.SetReady(true)
	clock.Run(10 * time.Minute)
	require.Equal(t, "idle", h.handler.peerPolicy.Snapshot(peer.ID()).Contribution.Status)
	snapshot := func() *peerpolicy.ContributionSnapshot {
		return (*ethHandler)(h.handler).PeerInfo(peer.Peer.ID()).(*ethPeerInfo).Reputation.Contribution
	}
	require.Equal(t, "unknown", snapshot().Status, "PeerInfo must refresh a now-stale head")
	clock.Run(time.Minute)
	insertContributionHead(t, h, time.Now().Unix())
	require.Equal(t, "warming-up", snapshot().Status, "fresh block must restore readiness on inspection")
	clock.Run(10 * time.Minute)
	require.Equal(t, "idle", snapshot().Status)
}

type contributionSyncRW struct{ requests chan uint64 }

func (r *contributionSyncRW) ReadMsg() (p2p.Msg, error) { return p2p.Msg{}, io.EOF }

func (r *contributionSyncRW) WriteMsg(msg p2p.Msg) error {
	if err := msg.Discard(); err != nil {
		return err
	}
	r.requests <- msg.Code
	return nil
}

func TestPeerContributionSyncModes(t *testing.T) {
	for _, test := range []struct {
		mode     downloader.SyncMode
		status   string
		observed uint64
	}{
		{downloader.FullSync, "contributing", 660},
		{downloader.SnapSync, "unknown", 600},
		{downloader.StatelessSync, "unknown", 600},
	} {
		t.Run(test.mode.String(), func(t *testing.T) {
			testPeerContributionSyncMode(t, test.mode, test.status, test.observed)
		})
	}
}

func testPeerContributionSyncMode(t *testing.T, mode downloader.SyncMode, status string, observed uint64) {
	t.Helper()
	clock := new(mclock.Simulated)
	h := newContributionTestHandler(t, clock, true).handler
	rw := &contributionSyncRW{requests: make(chan uint64, 1)}
	peer := ethproto.NewPeer(ethproto.ETH69, p2p.NewPeer(enode.ID{1}, "sync-peer", nil), rw, nil)
	defer peer.Close()
	require.NoError(t, h.downloader.RegisterPeer(peer.ID(), peer.Version(), &ethPeer{Peer: peer}))
	defer func() { require.NoError(t, h.downloader.UnregisterPeer(peer.ID())) }()
	defer h.downloader.Cancel()
	head := h.chain.CurrentBlock()
	td := h.chain.GetTd(head.Hash(), head.Number.Uint64())
	peer.SetHead(head.Hash(), td)
	h.peerPolicy.Connected(peer.ID())
	h.enableSyncedFeatures()
	clock.Run(10 * time.Minute)
	h.peerPolicy.Observe(peer.ID(), peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{AcceptedTransactions: 1, StartedAt: clock.Now()}})
	result := make(chan error, 1)
	go func() {
		result <- h.doSync(&chainSyncOp{mode: mode, peer: peer, head: head.Hash(), td: td})
	}()
	select {
	case code := <-rw.requests:
		require.Equal(t, uint64(ethproto.GetBlockHeadersMsg), code)
	case <-time.After(time.Second):
		t.Fatal("sync did not request a header")
	}
	clock.Run(time.Minute)
	h.enableSyncedFeatures()
	got := h.peerPolicy.Snapshot(peer.ID()).Contribution
	require.Equal(t, status, got.Status)
	require.Equal(t, uint64(1), got.AcceptedTransactions)
	require.Equal(t, observed, got.ObservedSeconds)
	h.downloader.Cancel()
	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("sync did not cancel")
	}
	restoredStatus := "warming-up"
	if mode == downloader.FullSync {
		restoredStatus = "contributing"
	}
	require.Equal(t, restoredStatus, h.peerPolicy.Snapshot(peer.ID()).Contribution.Status)
}
