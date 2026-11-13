package p2p

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBulkAdmissionFollowsLivePeerLimits(t *testing.T) {
	for _, outbound := range []bool{false, true} {
		name := "incoming"
		if outbound {
			name = "outgoing"
		}
		t.Run(name, func(t *testing.T) {
			server := newBulkLiveServer(t, 1)
			ordinary := newBulkLiveServer(t, 10)
			trusted := newBulkLiveServer(t, 10)
			added := newBulkLiveServer(t, 10)

			connectBulkLivePeers(t, server, ordinary, outbound)
			openBulkLiveLane(t, server, ordinary)
			require.Equal(t, 1, server.PeerCount())

			server.AddTrustedPeer(trusted.Self())
			connectBulkLivePeers(t, server, trusted, outbound)
			trustedLanes := openBulkLiveLane(t, server, trusted)
			require.True(t, server.Peer(trusted.Self().ID()).Info().Network.Trusted)
			require.Equal(t, 2, server.PeerCount())
			require.Equal(t, 2, server.bulk.Status().ActiveSessions)

			server.SetMaxPeers(3)
			connectBulkLivePeers(t, server, added, outbound)
			openBulkLiveLane(t, server, added)
			require.Equal(t, 3, server.PeerCount())
			require.Equal(t, 3, server.bulk.Status().ActiveSessions)

			server.SetMaxPeers(0)
			require.Eventually(t, func() bool {
				return server.PeerCount() == 1 && server.bulk.Status().ActiveSessions == 1
			}, 3*time.Second, time.Millisecond)
			require.Nil(t, server.Peer(ordinary.Self().ID()))
			require.Nil(t, server.Peer(added.Self().ID()))
			require.NotNil(t, server.Peer(trusted.Self().ID()))
			assertBulkLiveTraffic(t, trustedLanes)
		})
	}
}

func newBulkLiveServer(t *testing.T, maxPeers int) *Server {
	t.Helper()
	server := &Server{Config: Config{
		PrivateKey: newkey(), MaxPeers: maxPeers, MaxPendingPeers: 8,
		NoDiscovery: true, NoDial: true, ListenAddr: "127.0.0.1:0",
		EnableBulkSidecar: true, BulkListenAddr: "127.0.0.1:0",
		Protocols: []Protocol{{Name: "eth", Version: 68, Length: 17,
			Run: func(peer *Peer, _ MsgReadWriter) error {
				<-peer.Done()
				return io.EOF
			},
		}},
	}}
	require.NoError(t, server.Start())
	t.Cleanup(server.Stop)
	return server
}

func connectBulkLivePeers(t *testing.T, server, remote *Server, outbound bool) {
	t.Helper()
	dialer, acceptor := remote, server
	if outbound {
		dialer, acceptor = server, remote
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	fd, err := (&net.Dialer{}).DialContext(ctx, "tcp", acceptor.ListenAddr)
	require.NoError(t, err)
	require.NoError(t, dialer.SetupConn(fd, dynDialedConn, acceptor.Self()))
	require.Eventually(t, func() bool {
		return server.Peer(remote.Self().ID()) != nil && remote.Peer(server.Self().ID()) != nil
	}, time.Second, time.Millisecond)
}

func openBulkLiveLane(t *testing.T, left, right *Server) [2]MsgReadWriter {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type result struct {
		side int
		rw   MsgReadWriter
		err  error
	}
	results := make(chan result, 2)
	for i, pair := range [][2]*Server{{left, right}, {right, left}} {
		go func() {
			rw, err := pair[0].bulk.OpenChannelContext(ctx, pair[0].Peer(pair[1].Self().ID()), "eth-bulk")
			results <- result{i, rw, err}
		}()
	}
	var lanes [2]MsgReadWriter
	for range lanes {
		r := <-results
		require.NoError(t, r.err)
		lanes[r.side] = r.rw
	}
	assertBulkLiveTraffic(t, lanes)
	return lanes
}

func assertBulkLiveTraffic(t *testing.T, lanes [2]MsgReadWriter) {
	t.Helper()
	for _, pair := range [][2]MsgReadWriter{{lanes[0], lanes[1]}, {lanes[1], lanes[0]}} {
		sent := make(chan error, 1)
		go func() { sent <- SendItems(pair[0], 1, uint64(42)) }()
		msg := readRoutedTestMessage(t, pair[1])
		var payload []uint64
		require.NoError(t, msg.Decode(&payload))
		require.Equal(t, []uint64{42}, payload)
		require.NoError(t, <-sent)
	}
}
