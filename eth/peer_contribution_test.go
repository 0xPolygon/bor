package eth

import (
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/eth/fetcher"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/stretchr/testify/require"
)

func TestPeerContributionAdminSnapshot(t *testing.T) {
	clock := new(mclock.Simulated)
	h := newContributionTestHandler(t, clock, true)
	peer, cleanup := registerEthWitPeer(t, h, wit.WIT2)
	defer cleanup()
	ethPeer := h.handler.peers.peer(peer.ID())
	h.handler.observeProtocolPeer(ethPeer.Peer)
	h.handler.enableSyncedFeatures()
	snapshot := func() *peerpolicy.Snapshot {
		return (*ethHandler)(h.handler).PeerInfo(peer.Peer.ID()).(*ethPeerInfo).Reputation
	}
	require.Equal(t, "warming-up", snapshot().Contribution.Status)
	clock.Run(10 * time.Minute)
	require.Equal(t, "idle", snapshot().Contribution.Status)
	for i := uint64(1); i <= 3; i++ {
		require.NoError(t, ethPeer.ReplyReceiptsRLP(i, []rlp.RawValue{{0xc0}}))
		require.Eventually(t, func() bool {
			return snapshot().Contribution.ServedResponses == i
		}, time.Second, time.Millisecond)
		clock.Run(time.Minute)
	}
	got := snapshot()
	require.Equal(t, "request-only", got.Contribution.Status)
	require.Equal(t, uint64(3), got.Contribution.ServedMinutes)
	require.Positive(t, got.Contribution.ServedBytes)
	require.Zero(t, got.Risk)
	require.Equal(t, "none", got.Action)
	h.handler.observePeer(peer.ID(), peerpolicy.InvalidTransaction)
	h.handler.observeTransactionDelivery(peer.ID(), fetcher.UnsolicitedDelivery, 1, 100, false, 1, true, clock.Now())
	got = snapshot()
	require.Equal(t, "contributing", got.Contribution.Status)
	require.Equal(t, uint64(1), got.Contribution.AcceptedTransactions)
	require.Equal(t, uint64(60), got.Risk)
	require.NotNil(t, h.handler.peers.peer(peer.ID()), "activity must not disconnect a peer")
}

func TestPeerContributionTransactionOutcomes(t *testing.T) {
	for _, test := range []struct {
		name       string
		provenance fetcher.DeliveryProvenance
		accepted   uint64
		complete   bool
		status     string
	}{
		{"accepted broadcast", fetcher.UnsolicitedDelivery, 1, true, "contributing"},
		{"accepted requested reply", fetcher.RequestedDelivery, 1, true, "contributing"},
		{"pool rejection", fetcher.UnsolicitedDelivery, 0, true, "request-only"},
		{"unknown provenance", fetcher.UnknownDelivery, 1, true, "unknown"},
		{"incomplete admission", fetcher.UnsolicitedDelivery, 1, false, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := new(mclock.Simulated)
			h := &handler{peerPolicy: peerpolicy.New(clock)}
			h.peerPolicy.Connected("peer")
			h.peerPolicy.SetReady(true)
			clock.Run(10 * time.Minute)
			for range 3 {
				h.peerPolicy.Observe("peer", peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{ServedResponses: 1}})
				clock.Run(time.Minute)
			}
			h.observeTransactionDelivery("peer", test.provenance, 1, 100, false, test.accepted, test.complete, clock.Now())
			got := h.peerPolicy.Snapshot("peer")
			require.Equal(t, test.status, got.Contribution.Status)
			require.Zero(t, got.Risk)
		})
	}
}

func TestPeerContributionLocalReadiness(t *testing.T) {
	clock := new(mclock.Simulated)
	h := newContributionTestHandler(t, clock, true).handler
	h.peerPolicy.Connected("peer")
	h.updateContributionReadiness()
	clock.Run(30 * time.Minute)
	require.Equal(t, "unknown", h.peerPolicy.Snapshot("peer").Contribution.Status)
	h.enableSyncedFeatures()
	require.Equal(t, "warming-up", h.peerPolicy.Snapshot("peer").Contribution.Status)
	clock.Run(10 * time.Minute)
	require.Equal(t, "idle", h.peerPolicy.Snapshot("peer").Contribution.Status)
	h.setContributionSyncing(true)
	require.Equal(t, "unknown", h.peerPolicy.Snapshot("peer").Contribution.Status)
	h.enableSyncedFeatures()
	require.Equal(t, "unknown", h.peerPolicy.Snapshot("peer").Contribution.Status)
	clock.Run(time.Minute)
	h.setContributionSyncing(false)
	require.Equal(t, "warming-up", h.peerPolicy.Snapshot("peer").Contribution.Status)
	h.snapSync.Store(true)
	h.updateContributionReadiness()
	require.Equal(t, "unknown", h.peerPolicy.Snapshot("peer").Contribution.Status)
	clock.Run(time.Minute)
	h.enableSyncedFeatures()
	require.Equal(t, "warming-up", h.peerPolicy.Snapshot("peer").Contribution.Status)
	h.policyMu.Lock()
	h.disableTxPropagation = true
	h.policyMu.Unlock()
	h.updateContributionReadiness()
	require.Equal(t, "unknown", h.peerPolicy.Snapshot("peer").Contribution.Status)
	h.policyMu.Lock()
	h.disableTxPropagation = false
	h.policyMu.Unlock()
	h.statelessSync.Store(true)
	h.updateContributionReadiness()
	require.Equal(t, "unknown", h.peerPolicy.Snapshot("peer").Contribution.Status)
}

func TestPeerContributionConcurrentReadiness(t *testing.T) {
	clock := new(mclock.Simulated)
	h := newContributionTestHandler(t, clock, true).handler
	h.peerPolicy.Connected("peer")
	h.enableSyncedFeatures()
	for range 20 {
		h.setContributionSyncing(false)
		clock.Run(10 * time.Minute)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 20 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				h.updateContributionReadiness()
			}()
		}
		close(start)
		h.setContributionSyncing(true)
		wg.Wait()
		require.Equal(t, "unknown", h.peerPolicy.Snapshot("peer").Contribution.Status)
	}
}
