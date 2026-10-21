package eth

import (
	"bytes"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/downloader"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/metrics/prometheus"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

func policyTestServer(t *testing.T, trusted []*enode.Node, run func(*p2p.Peer, p2p.MsgReadWriter) error) *p2p.Server {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	server := &p2p.Server{Config: p2p.Config{PrivateKey: key, MaxPeers: 4, NoDiscovery: true, ListenAddr: "127.0.0.1:0", TrustedNodes: trusted, Protocols: []p2p.Protocol{{Name: "policy", Version: 1, Length: 1, Run: run}}}}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	return server
}

func policyTestConnection(t *testing.T, mode string) *p2p.Peer {
	t.Helper()
	stop := make(chan struct{})
	remote := policyTestServer(t, nil, func(*p2p.Peer, p2p.MsgReadWriter) error { <-stop; return nil })
	var trusted []*enode.Node
	if mode == "trusted" {
		trusted = append(trusted, remote.Self())
	}
	connected := make(chan *p2p.Peer, 1)
	local := policyTestServer(t, trusted, func(p *p2p.Peer, _ p2p.MsgReadWriter) error { connected <- p; <-stop; return nil })
	t.Cleanup(func() { close(stop) })
	if mode == "static" {
		local.AddPeer(remote.Self())
	} else {
		remote.AddPeer(local.Self())
	}
	select {
	case peer := <-connected:
		return peer
	case <-time.After(5 * time.Second):
		t.Fatal("peer connection timed out")
		return nil
	}
}

type policyFaultRW struct{}

func (policyFaultRW) ReadMsg() (p2p.Msg, error) {
	return p2p.Msg{Code: 255, Payload: bytes.NewReader(nil)}, nil
}
func (policyFaultRW) WriteMsg(msg p2p.Msg) error { return msg.Discard() }

func TestPeerPolicyConnectionClasses(t *testing.T) {
	for _, mode := range []string{"ordinary", "trusted", "static"} {
		t.Run(mode, func(t *testing.T) {
			h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated)), peers: newPeerSet()}
			peer := ethproto.NewPeer(ethproto.ETH68, policyTestConnection(t, mode), policyFaultRW{}, nil)
			defer peer.Close()
			if err := h.peers.registerPeer(peer, nil, nil); err != nil {
				t.Fatal(err)
			}
			want := uint64(0)
			if mode == "ordinary" {
				want = 60
			}
			h.peerPolicy.Observe(peer.ID(), peerpolicy.Evidence{Reason: peerpolicy.InvalidBlock})
			h.observeProtocolPeer(peer)
			if got := h.peerPolicy.Snapshot(peer.ID()); got.Risk != want {
				t.Fatalf("initial classification: %+v", got)
			}
			h.peerPolicy = peerpolicy.New(new(mclock.Simulated))
			if err := ethproto.Handle(nil, peer); err == nil {
				t.Fatal("invalid framing accepted")
			}
			if got := h.peerPolicy.Snapshot(peer.ID()); got.Risk != want {
				t.Fatalf("protocol observation: %+v", got)
			}
			h.peerPolicy = peerpolicy.New(new(mclock.Simulated))
			h.observePeer(peer.ID(), peerpolicy.InvalidBlock)
			if got := h.peerPolicy.Snapshot(peer.ID()); got.Risk != want {
				t.Fatalf("async observation: %+v", got)
			}
		})
	}
}

func TestPeerContributionEvictionPreservesConnectionClass(t *testing.T) {
	for _, mode := range []string{"ordinary", "trusted", "static"} {
		t.Run(mode, func(t *testing.T) {
			clock := new(mclock.Simulated)
			h := &handler{peerPolicy: peerpolicy.New(clock), peers: newPeerSet()}
			h.peerPolicy.SetReady(true)
			peer := ethproto.NewPeer(ethproto.ETH68, policyTestConnection(t, mode), policyFaultRW{}, nil)
			defer peer.Close()
			if err := h.peers.registerPeer(peer, nil, nil); err != nil {
				t.Fatal(err)
			}
			h.observeProtocolPeer(peer)
			clock.Run(10 * time.Minute)
			for i := range 1024 {
				h.peerPolicy.Connected(fmt.Sprint(i))
			}
			if err := ethproto.Handle(nil, peer); err == nil {
				t.Fatal("invalid framing accepted")
			}
			got := h.peerPolicy.Snapshot(peer.ID())
			want := uint64(0)
			if mode == "ordinary" {
				want = 60
			}
			if got.Risk != want || got.Contribution.Protected != (mode != "ordinary") || got.Contribution.Status != "warming-up" {
				t.Fatalf("evicted connection class/coverage: %+v, %+v", got, got.Contribution)
			}
			require.True(t, got.Contribution.ConnectionKnown)
			require.Equal(t, uint64(600), got.Contribution.ConnectionSeconds)
			require.Zero(t, got.Contribution.ObservedSeconds)
		})
	}
}

func TestPeerContributionEvictedStaleSessionCannotRestore(t *testing.T) {
	clock := new(mclock.Simulated)
	h := &handler{peerPolicy: peerpolicy.New(clock), peers: newPeerSet()}
	h.peerPolicy.SetReady(true)
	conn := p2p.NewPeer(enode.ID{1}, "", nil)
	old := ethproto.NewPeer(ethproto.ETH68, conn, policyFaultRW{}, nil)
	t.Cleanup(old.Close)
	require.NoError(t, h.peers.registerPeer(old, nil, nil))
	h.observeProtocolPeer(old)
	require.NoError(t, h.peers.unregisterPeer(old.ID()))
	clock.Run(time.Second)
	current := ethproto.NewPeer(ethproto.ETH68, conn, policyFaultRW{}, nil)
	t.Cleanup(current.Close)
	require.NoError(t, h.peers.registerPeer(current, nil, nil))
	h.observeProtocolPeer(current)
	for i := range 1024 {
		h.peerPolicy.Connected(fmt.Sprint(i))
	}
	clock.Run(time.Second)
	require.Error(t, ethproto.Handle(nil, old))
	got := h.peerPolicy.Snapshot(old.ID())
	require.False(t, got.Contribution.ConnectionKnown)
	require.Equal(t, uint64(60), got.AbuseRisk)
	require.Error(t, ethproto.Handle(nil, current))
	got = h.peerPolicy.Snapshot(current.ID())
	require.True(t, got.Contribution.ConnectionKnown)
	require.Equal(t, uint64(1), got.Contribution.ConnectionSeconds)
	require.Zero(t, got.Contribution.ObservedSeconds)
	require.Equal(t, uint64(1), got.Contribution.ReceivedMessages)
}

func TestPeerContributionMissingPeerSetRetainsEvidence(t *testing.T) {
	h := &handler{peerPolicy: peerpolicy.New(new(mclock.Simulated))}
	h.peerPolicy.SetReady(true)
	peer := ethproto.NewPeer(ethproto.ETH68, p2p.NewPeer(enode.ID{1}, "", nil), policyFaultRW{}, nil)
	t.Cleanup(peer.Close)
	h.observeProtocolPeer(peer)
	for i := range 1024 {
		h.peerPolicy.Connected(fmt.Sprint(i))
	}
	var err error
	require.NotPanics(t, func() { err = ethproto.Handle(nil, peer) })
	require.Error(t, err)
	got := h.peerPolicy.Snapshot(peer.ID())
	require.False(t, got.Contribution.ConnectionKnown)
	require.Equal(t, "unknown", got.Contribution.Status)
	require.Equal(t, uint64(1), got.Contribution.ReceivedMessages)
	require.Equal(t, uint64(60), got.AbuseRisk)
	require.Equal(t, uint64(1), got.Windows[peerpolicy.InvalidEncoding.String()])
}

func newPolicyAdmissionHandler(t *testing.T, clock mclock.Clock, maxPeers int) *testHandler {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	chain, err := core.NewBlockChain(db, &core.Genesis{Config: params.TestChainConfig}, ethash.NewFaker(), nil)
	require.NoError(t, err)
	pool := newTestTxPool()
	backend, err := newHandler(&handlerConfig{Database: db, Chain: chain, TxPool: pool,
		Network: 1, Sync: downloader.FullSync, BloomCache: 1})
	require.NoError(t, err)
	backend.peerPolicy = peerpolicy.New(clock)
	backend.peerPolicy.SetClassResolver(backend.peerPolicyClass)
	h := &testHandler{db: db, chain: chain, txpool: pool, handler: backend}
	backend.Start(maxPeers)
	t.Cleanup(h.close)
	return h
}

func policyAdmissionPeers(t *testing.T, h *testHandler, version uint, id enode.ID) (*ethproto.Peer, *ethproto.Peer, *p2p.MsgPipeRW) {
	t.Helper()
	remoteRW, localRW := p2p.MsgPipe()
	t.Cleanup(func() { require.NoError(t, remoteRW.Close()) })
	remote := ethproto.NewPeer(version, p2p.NewPeerPipe(enode.ID{255}, "", nil, remoteRW), remoteRW, h.txpool)
	local := ethproto.NewPeer(version, p2p.NewPeerPipe(id, "", nil, localRW), localRW, h.txpool)
	t.Cleanup(remote.Close)
	t.Cleanup(local.Close)
	return remote, local, remoteRW
}

func policyAdmissionResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("ETH admission did not finish")
		return nil
	}
}

func TestPeerPolicyRejectedAdmissionPreservesHistory(t *testing.T) {
	for _, version := range []uint{ethproto.ETH68, ethproto.ETH69} {
		for _, rejection := range []string{"handshake", "capacity"} {
			t.Run(fmt.Sprintf("eth%d/%s", version, rejection), func(t *testing.T) {
				h := newPolicyAdmissionHandler(t, new(mclock.Simulated), 1)
				if rejection == "capacity" {
					occupant := ethproto.NewPeer(version, p2p.NewPeer(enode.ID{254}, "", nil), policyFaultRW{}, nil)
					t.Cleanup(occupant.Close)
					require.NoError(t, h.handler.peers.registerPeer(occupant, nil, nil))
				}
				for i := range 1024 {
					h.handler.peerPolicy.Observe(fmt.Sprintf("retained-%d", i), peerpolicy.Evidence{Reason: peerpolicy.RateLimited})
				}
				for attempt := range 3 {
					remote, local, _ := policyAdmissionPeers(t, h, version, enode.ID{byte(attempt + 1)})
					result := make(chan error, 1)
					go func() {
						result <- h.handler.runEthPeer(local, func(*ethproto.Peer) error {
							t.Error("rejected peer entered the message handler")
							return nil
						})
					}()
					networkID := uint64(1)
					if rejection == "handshake" {
						networkID++
					}
					err := remote.Handshake(networkID, h.chain, h.handler.blockRange.currentRange())
					if rejection == "handshake" {
						require.Error(t, err)
						require.ErrorContains(t, policyAdmissionResult(t, result), "network ID mismatch")
					} else {
						require.NoError(t, err)
						require.ErrorIs(t, policyAdmissionResult(t, result), p2p.DiscTooManyPeers)
					}
					require.False(t, h.handler.peerPolicy.Snapshot(local.ID()).Contribution.ConnectionKnown)
					require.Equal(t, uint64(20), h.handler.peerPolicy.Snapshot("retained-0").Risk,
						"rejected connection churn must not evict retained evidence")
				}
			})
		}
	}
}

func TestPeerPolicyRejectedReconnectPreservesContribution(t *testing.T) {
	clock := new(mclock.Simulated)
	h := newPolicyAdmissionHandler(t, clock, 10)
	remote, local, _ := policyAdmissionPeers(t, h, ethproto.ETH69, enode.ID{1})
	h.handler.peerPolicy.Connected(local.ID())
	h.handler.peerPolicy.Observe(local.ID(), peerpolicy.Evidence{
		Reason: peerpolicy.RateLimited, Activity: peerpolicy.ActivityEvent{ReceivedMessages: 7, ServedResponses: 2},
	})
	clock.Run(10 * time.Second)
	before := h.handler.peerPolicy.Snapshot(local.ID())
	result := make(chan error, 1)
	go func() {
		result <- h.handler.runEthPeer(local, func(*ethproto.Peer) error {
			t.Error("rejected reconnect entered the message handler")
			return nil
		})
	}()
	require.Error(t, remote.Handshake(2, h.chain, h.handler.blockRange.currentRange()))
	require.Error(t, policyAdmissionResult(t, result))
	require.Equal(t, before, h.handler.peerPolicy.Snapshot(local.ID()), "failed reconnect must not reset an earlier accepted session")
}

func TestPeerPolicyAcceptedAdmissionObservesFirstMessage(t *testing.T) {
	for _, version := range []uint{ethproto.ETH68, ethproto.ETH69} {
		t.Run(fmt.Sprintf("eth%d", version), func(t *testing.T) {
			clock := new(mclock.Simulated)
			h := newPolicyAdmissionHandler(t, clock, 10)
			remote, local, remoteRW := policyAdmissionPeers(t, h, version, enode.ID{1})
			h.handler.peerPolicy.Connected(local.ID())
			h.handler.peerPolicy.Observe(local.ID(), peerpolicy.Evidence{
				Reason: peerpolicy.RateLimited, Activity: peerpolicy.ActivityEvent{ReceivedMessages: 7},
			})
			clock.Run(10 * time.Second)
			admitted := make(chan peerpolicy.Snapshot, 1)
			result := make(chan error, 1)
			go func() {
				result <- h.handler.runEthPeer(local, func(peer *ethproto.Peer) error {
					admitted <- h.handler.peerPolicy.Snapshot(peer.ID())
					return ethproto.Handle((*ethHandler)(h.handler), peer)
				})
			}()
			require.NoError(t, remote.Handshake(1, h.chain, h.handler.blockRange.currentRange()))
			select {
			case got := <-admitted:
				require.True(t, got.Contribution.ConnectionKnown)
				require.Zero(t, got.Contribution.ReceivedMessages, "accepted reconnect starts a fresh contribution session")
				require.Equal(t, uint64(20), got.Risk, "accepted reconnect retains misconduct history")
			case <-time.After(5 * time.Second):
				t.Fatal("accepted peer did not enter the message handler")
			}
			require.NoError(t, p2p.Send(remoteRW, 255, []byte{}))
			require.Error(t, policyAdmissionResult(t, result))
			got := h.handler.peerPolicy.Snapshot(local.ID())
			require.Equal(t, uint64(80), got.Risk)
			require.Equal(t, uint64(1), got.Contribution.ReceivedMessages)
			require.Equal(t, uint64(1), got.Windows[peerpolicy.InvalidEncoding.String()])
		})
	}
}

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
			require.NoError(t, h.peerPolicy.RegisterPeerMetrics(registry, true))
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
