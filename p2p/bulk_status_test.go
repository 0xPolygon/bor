package p2p

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestBulkSidecarStatus(t *testing.T) {
	require.Equal(t, BulkSidecarStatus{}, (*BulkSidecar)(nil).Status())
	left, right := newTestBulkServer(t), newTestBulkServer(t)
	t.Cleanup(left.close)
	t.Cleanup(right.close)
	left.setQUICPort()
	right.setQUICPort()
	lp, rp := newTestTrackedPeer(right.localnode.Node()), newTestTrackedPeer(left.localnode.Node())
	left.setPeer(lp)
	right.setPeer(rp)
	result := make(chan error, 1)
	go func() {
		_, err := right.bulk.OpenChannel(rp, "eth-bulk")
		result <- err
	}()
	_, err := left.bulk.OpenChannel(lp, "eth-bulk")
	require.NoError(t, err)
	require.NoError(t, <-result)
	for range 3 {
		remote := enode.NewV4(&newkey().PublicKey, nil, 0, 0)
		session := left.bulk.session(remote)
		session.dialing = true
	}
	status := left.bulk.Status()
	require.True(t, status.Enabled)
	require.Equal(t, left.bulk.Addr().String(), status.ListenAddr)
	require.Equal(t, 1, status.ActiveSessions)
	require.Equal(t, 1, status.ActiveChannels)
	require.Len(t, status.Peers, 4)
	ids := make([]string, 0, len(status.Peers))
	for _, peer := range status.Peers {
		ids = append(ids, peer.PeerID)
		if peer.Connected {
			require.Equal(t, []string{"eth-bulk"}, peer.Channels)
			require.False(t, peer.Dialing)
		} else {
			require.True(t, peer.Dialing)
			require.Empty(t, peer.Channels)
		}
	}
	require.True(t, slices.IsSorted(ids))
	require.Positive(t, status.Counters.SessionsEstablished)
}

func TestBulkSidecarStatusSnapshot(t *testing.T) {
	session := newHelloTestSession()
	session.channels["snap-trie"] = &closeTrackingRW{}
	session.channels["eth-bulk"] = &closeTrackingRW{}
	status := session.status()
	require.Equal(t, []string{"eth-bulk", "snap-trie"}, status.Channels)
	status.Channels[0] = "changed"
	require.Equal(t, []string{"eth-bulk", "snap-trie"}, session.status().Channels)
	stats := &bulkSidecarStatsBook{channels: make(map[string]*BulkSidecarChannelCounters)}
	stats.markChannelOpened("eth-bulk")
	first, _, _ := stats.snapshot()
	delete(first.Channels, "eth-bulk")
	second, _, _ := stats.snapshot()
	require.Equal(t, uint64(1), second.Channels["eth-bulk"].Opened)
	trace := stats.newConnectionTrace()
	require.True(t, trace.SupportsSchemas("unknown"))
	require.NoError(t, trace.AddProducer().Close())
}
