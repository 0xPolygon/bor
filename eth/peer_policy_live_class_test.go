package eth

import (
	"net/http/httptest"
	"testing"
	"time"

	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/metrics/prometheus"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/stretchr/testify/require"
)

func liveClassTestPeer(t *testing.T) (*handler, *ethproto.Peer, *p2p.Server, *enode.Node) {
	t.Helper()
	backend := newTestHandlerWithConfig(func(c *handlerConfig) *handlerConfig {
		c.peerReputation = true
		return c
	})
	t.Cleanup(backend.close)
	stop := make(chan struct{})
	remote := policyTestServer(t, nil, func(*p2p.Peer, p2p.MsgReadWriter) error { <-stop; return nil })
	connected := make(chan *p2p.Peer, 1)
	local := policyTestServer(t, nil, func(p *p2p.Peer, _ p2p.MsgReadWriter) error {
		connected <- p
		<-stop
		return nil
	})
	t.Cleanup(func() { close(stop) })
	remote.AddPeer(local.Self())
	var conn *p2p.Peer
	select {
	case conn = <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("peer connection timed out")
	}
	peer := ethproto.NewPeer(ethproto.ETH68, conn, policyFaultRW{}, nil)
	t.Cleanup(peer.Close)
	require.NoError(t, backend.handler.peers.registerPeer(peer, nil, nil))
	backend.handler.observeProtocolPeer(peer)
	return backend.handler, peer, local, remote.Self()
}

func TestPeerPolicyQuietTrustChanges(t *testing.T) {
	for _, firstRead := range []string{"admin", "metrics"} {
		t.Run(firstRead, func(t *testing.T) {
			h, peer, server, remote := liveClassTestPeer(t)
			registry := metrics.NewRegistry()
			// The handler already owns a collector when metrics are globally enabled.
			h.peerPolicy.UnregisterPeerMetrics()
			require.NoError(t, h.peerPolicy.RegisterPeerMetrics(registry))
			defer h.peerPolicy.UnregisterPeerMetrics()
			scrape := func() string {
				response := httptest.NewRecorder()
				prometheus.Handler(registry).ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
				return response.Body.String()
			}
			info := func() *peerpolicy.Snapshot {
				return (*ethHandler)(h).PeerInfo(peer.Peer.ID()).(*ethPeerInfo).Reputation
			}
			protectedScrape := func(want string) {
				t.Helper()
				got := scrape()
				require.Contains(t, got, `eth_peerpolicy_peer_risk{peer_id="`+peer.ID()+`"} 0`)
				require.Contains(t, got, `eth_peerpolicy_peer_protected{peer_id="`+peer.ID()+`"} `+want)
			}
			h.observePeer(peer.ID(), peerpolicy.InvalidBlock)
			require.Contains(t, scrape(), `eth_peerpolicy_peer_risk{peer_id="`+peer.ID()+`"} 60`)

			server.AddTrustedPeer(remote)
			require.Eventually(t, peer.Trusted, time.Second, time.Millisecond)
			if firstRead == "metrics" {
				protectedScrape("1")
			}
			got := info()
			require.True(t, got.Contribution.Protected)
			require.Zero(t, got.Risk)
			protectedScrape("1")

			server.RemoveTrustedPeer(remote)
			require.Eventually(t, func() bool { return !peer.Trusted() }, time.Second, time.Millisecond)
			if firstRead == "metrics" {
				protectedScrape("0")
			}
			got = info()
			require.False(t, got.Contribution.Protected)
			require.Zero(t, got.Risk)
			require.Zero(t, got.Contribution.ReceivedMessages, "administrative changes are not peer traffic")

			// Removing trust cannot remove the independent static exemption.
			server.AddPeer(remote)
			require.True(t, peer.Static())
			require.True(t, info().Contribution.Protected)
			server.AddTrustedPeer(remote)
			require.Eventually(t, peer.Trusted, time.Second, time.Millisecond)
			server.RemoveTrustedPeer(remote)
			require.Eventually(t, func() bool { return !peer.Trusted() }, time.Second, time.Millisecond)
			require.True(t, info().Contribution.Protected)
		})
	}
}

func TestPeerPolicyQuietTrustRetainedAtDisconnect(t *testing.T) {
	h, peer, server, remote := liveClassTestPeer(t)
	h.observePeer(peer.ID(), peerpolicy.InvalidBlock)
	server.AddTrustedPeer(remote)
	require.Eventually(t, peer.Trusted, time.Second, time.Millisecond)
	// No evidence, admin query, or metrics scrape runs between trust and teardown.
	h.unregisterPeer(peer.ID())
	got := h.peerPolicy.Snapshot(peer.ID())
	require.True(t, got.Contribution.Protected)
	require.Zero(t, got.Risk)
	h.observePeer(peer.ID(), peerpolicy.InvalidBlock)
	require.Zero(t, h.peerPolicy.Snapshot(peer.ID()).Risk, "late disconnected outcomes must retain the final class")
}
