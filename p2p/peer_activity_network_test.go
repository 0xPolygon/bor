package p2p

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func activityProtocol(version uint, received chan<- enode.ID, policy *peerpolicy.Tracker) Protocol {
	return Protocol{Name: "eth", Version: version, Length: 17, Run: func(p *Peer, rw MsgReadWriter) error {
		policy.BindConnection(p.ID().String(), policy.Connected(p.ID().String()), p.ConnectionActivity())
		for {
			msg, err := rw.ReadMsg()
			if err != nil {
				return err
			}
			if err := msg.Discard(); err != nil {
				return err
			}
			received <- p.ID()
		}
	}}
}

func connectActivityPeer(t *testing.T, srv *Server, key *ecdsa.PrivateKey, version uint) (transport, error) {
	t.Helper()
	fd, err := net.DialTimeout("tcp", srv.ListenAddr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fd.Close() })
	client := newRLPX(fd, srv.Self().Pubkey())
	if _, err := client.doEncHandshake(key); err != nil {
		return nil, err
	}
	_, err = client.doProtoHandshake(&protoHandshake{
		Version: baseProtocolVersion, Name: "fixture", Caps: []Cap{{Name: "eth", Version: version}},
		ID: crypto.FromECDSAPub(&key.PublicKey)[1:],
	})
	return client, err
}

func waitActivityPeers(t *testing.T, srv *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		peers := srv.Peers()
		started := 0
		for _, p := range peers {
			if _, live := p.activity.Last(); live && srv.peerPolicy.Load().Snapshot(p.ID().String()).Contribution.ConnectionKnown {
				started++
			}
		}
		if len(peers) == want && started == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("connected peers = %d, want %d", srv.PeerCount(), want)
}

func advanceActivityClock(t *testing.T, srv *Server, clock *mclock.Simulated, elapsed time.Duration) {
	t.Helper()
	waitForTimer := func() {
		deadline := time.Now().Add(time.Second)
		// This fixture disables discovery and dialing, leaving only the policy timer.
		for clock.ActiveTimers() != 1 {
			if time.Now().After(deadline) {
				t.Fatal("policy timer was not rearmed")
			}
			time.Sleep(time.Millisecond)
		}
		srv.doPeerOp(func(map[enode.ID]*Peer) {})
	}
	waitForTimer()
	clock.Run(elapsed)
	waitForTimer()
}

func TestPeerIdleEvictionRLPX(t *testing.T) {
	for _, tc := range []struct {
		enabled  bool
		cooldown time.Duration
	}{{false, 0}, {true, 0}, {true, 90 * time.Second}, {true, 7 * time.Minute}} {
		t.Run(fmt.Sprintf("enabled=%v/cooldown=%v", tc.enabled, tc.cooldown), func(t *testing.T) {
			testPeerIdleEvictionRLPX(t, tc.enabled, tc.cooldown)
		})
	}
}

func TestPeerIdleConfiguredRLPX(t *testing.T) {
	clock := new(mclock.Simulated)
	policy := peerpolicy.New(clock)
	policy.SetReady(true)
	srv := &Server{Config: Config{
		PrivateKey: newkey(), MaxPeers: 9, NoDial: true, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", PeerIdleEviction: true, clock: clock,
		PeerIdleGracePeriod: 3 * time.Minute, PeerIdleMaxRemovals: 2, PeerIdleOccupancyThreshold: 80,
		Protocols: []Protocol{activityProtocol(68, make(chan enode.ID, 1), policy), activityProtocol(69, make(chan enode.ID, 1), policy)},
	}}
	srv.SetPeerPolicy(policy)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	for i := range 9 {
		if _, err := connectActivityPeer(t, srv, newkey(), 68+uint(i%2)); err != nil {
			t.Fatal(err)
		}
	}
	waitActivityPeers(t, srv, 9)
	advanceActivityClock(t, srv, clock, 3*time.Minute-time.Second)
	srv.doPeerOp(func(map[enode.ID]*Peer) {})
	waitActivityPeers(t, srv, 9)
	advanceActivityClock(t, srv, clock, time.Minute)
	waitActivityPeers(t, srv, 7)
	advanceActivityClock(t, srv, clock, time.Minute)
	waitActivityPeers(t, srv, 7)
	advanceActivityClock(t, srv, clock, time.Hour)
	srv.doPeerOp(func(map[enode.ID]*Peer) {})
	waitActivityPeers(t, srv, 7)
}

func TestPeerIdleStartupGrace(t *testing.T) {
	clock := new(mclock.Simulated)
	policy := peerpolicy.New(clock)
	policy.SetReady(true)
	ready := make(chan *Peer, 1)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	srv := &Server{Config: Config{
		PrivateKey: newkey(), MaxPeers: 9, NoDial: true, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", PeerIdleEviction: true, clock: clock,
		Protocols: []Protocol{activityProtocol(68, make(chan enode.ID, 1), policy)},
	}, newPeerHook: func(p *Peer) { ready <- p; <-release }}
	srv.SetPeerPolicy(policy)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	t.Cleanup(unblock)
	if _, err := connectActivityPeer(t, srv, newkey(), 68); err != nil {
		t.Fatal(err)
	}
	var peer *Peer
	select {
	case peer = <-ready:
	case <-time.After(time.Second):
		t.Fatal("peer did not reach startup hook")
	}
	advanceActivityClock(t, srv, clock, 20*time.Minute)
	if _, live := peer.activity.Last(); live {
		t.Fatal("peer was considered idle before protocols started")
	}
	unblock()
	waitActivityPeers(t, srv, 1)
	if last, live := peer.activity.Last(); !live || clock.Now().Add(peerIdleTimeout-time.Nanosecond).Sub(last) >= peerIdleTimeout {
		t.Fatal("peer lost its grace period during local startup delay")
	}
}

func testPeerIdleEvictionRLPX(t *testing.T, enabled bool, cooldown time.Duration) {
	clock := new(mclock.Simulated)
	policy := peerpolicy.New(clock)
	policy.SetReady(true)
	received := make(chan enode.ID, 10)
	srv := &Server{Config: Config{
		PrivateKey: newkey(), MaxPeers: 9, NoDial: true, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", PeerIdleEviction: enabled, clock: clock,
		PeerIdleReconnectCooldown: cooldown,
		Protocols:                 []Protocol{activityProtocol(68, received, policy), activityProtocol(69, received, policy)},
	}}
	srv.SetPeerPolicy(policy)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	keys := make([]*ecdsa.PrivateKey, 9)
	clients := make([]transport, 9)
	for i := range clients {
		keys[i] = newkey()
		var err error
		clients[i], err = connectActivityPeer(t, srv, keys[i], 68+uint(i%2))
		if err != nil {
			t.Fatal(err)
		}
	}
	waitActivityPeers(t, srv, 9)
	advanceActivityClock(t, srv, clock, peerIdleTimeout-time.Second)
	keepActivityPeer(t, clients[0], received)
	advanceActivityClock(t, srv, clock, time.Second)
	if !enabled {
		advanceActivityClock(t, srv, clock, time.Hour)
		waitActivityPeers(t, srv, 9)
		return
	}
	advanceActivityClock(t, srv, clock, peerIdleCheckInterval)
	waitActivityPeers(t, srv, 8)
	srv.SetMaxPeers(20)
	if cooldown == 0 {
		cooldown = 5 * time.Minute
	}
	checkActivityCooldown(t, srv, keys, clock, cooldown)
	waitActivityPeers(t, srv, 9)
	srv.SetMaxPeers(9)
	advanceActivityClock(t, srv, clock, time.Minute)
	waitActivityPeers(t, srv, 8)
}

func keepActivityPeer(t *testing.T, client transport, received <-chan enode.ID) {
	t.Helper()
	if err := Send(client, baseProtocolLength+3, []uint64{1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("application message was not received")
	}
}

func checkActivityCooldown(t *testing.T, srv *Server, keys []*ecdsa.PrivateKey, clock *mclock.Simulated, cooldown time.Duration) {
	t.Helper()
	var jailed *ecdsa.PrivateKey
	for i, key := range keys {
		if !srv.peerJail.IsJailed(enode.PubkeyToIDV4(&key.PublicKey)) {
			continue
		}
		if i == 0 || jailed != nil {
			t.Fatal("evicted active peer or more than one peer")
		}
		jailed = key
	}
	if jailed == nil {
		t.Fatal("no reconnect cooldown recorded")
	}
	if _, err := connectActivityPeer(t, srv, jailed, 68); !errors.Is(err, DiscJailed) {
		t.Fatalf("reconnect error = %v, want DiscJailed", err)
	}
	advanceActivityClock(t, srv, clock, cooldown+time.Nanosecond)
	if _, err := connectActivityPeer(t, srv, jailed, 68); err != nil {
		t.Fatalf("reconnect after cooldown: %v", err)
	}
}

func TestPeerIdleManualReconnectRLPX(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		for _, penalty := range []bool{false, true} {
			for _, version := range []uint{68, 69} {
				t.Run(fmt.Sprintf("trusted=%v/penalty=%v/eth%d", trusted, penalty, version), func(t *testing.T) {
					testPeerIdleManualReconnectRLPX(t, trusted, penalty, version)
				})
			}
		}
	}
}

func testPeerIdleManualReconnectRLPX(t *testing.T, trusted, penalty bool, version uint) {
	t.Helper()
	clock := new(mclock.Simulated)
	policy := peerpolicy.New(clock)
	srv := &Server{Config: Config{
		PrivateKey: newkey(), MaxPeers: 10, NoDial: true, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", clock: clock,
		Protocols: []Protocol{activityProtocol(version, make(chan enode.ID, 1), policy)},
	}}
	srv.SetPeerPolicy(policy)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	key := newkey()
	node := enode.NewV4(&key.PublicKey, nil, 0, 0)
	srv.peerJail.cooldownPeer(node.ID(), 24*time.Hour)
	if _, err := connectActivityPeer(t, srv, key, version); !errors.Is(err, DiscJailed) {
		t.Fatalf("connection during cooldown: %v", err)
	}
	if penalty {
		srv.JailPeerFor(node.ID(), time.Hour)
	}
	if trusted {
		srv.AddTrustedPeer(node)
	} else {
		srv.AddPeer(node)
	}
	srv.doPeerOp(func(map[enode.ID]*Peer) {})
	_, err := connectActivityPeer(t, srv, key, version)
	if penalty {
		until, jailed := srv.peerJail.JailedUntil(node.ID())
		if !errors.Is(err, DiscJailed) || !jailed || until != mclock.AbsTime(time.Hour) {
			t.Fatalf("misconduct jail changed: err=%v until=%v jailed=%v", err, until, jailed)
		}
		return
	}
	if err != nil {
		t.Fatalf("operator reconnect: %v", err)
	}
	waitActivityPeers(t, srv, 1)
	info := srv.Peers()[0].Info().Network
	if info.Trusted != trusted || info.Static != !trusted {
		t.Fatalf("incorrect operator protection: %+v", info)
	}
}

func TestPeerIdleManualOutboundRLPX(t *testing.T) {
	servers := make([]*Server, 2)
	for i := range servers {
		servers[i] = &Server{Config: Config{
			PrivateKey: newkey(), MaxPeers: 10, NoDiscovery: true, ListenAddr: "127.0.0.1:0",
		}}
		if err := servers[i].Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(servers[i].Stop)
	}
	srv, remote := servers[0], servers[1]
	srv.peerJail.cooldownPeer(remote.Self().ID(), 24*time.Hour)
	if !syncAddPeer(srv, remote.Self()) {
		t.Fatal("adding a static peer did not restore the outbound connection")
	}
	peer := srv.Peers()[0]
	if peer.ID() != remote.Self().ID() || peer.Inbound() || !peer.Info().Network.Static {
		t.Fatalf("incorrect outbound peer: %+v", peer.Info())
	}
}
