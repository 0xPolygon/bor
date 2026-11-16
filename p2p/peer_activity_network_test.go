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
)

func activityProtocol(version uint, received chan<- enode.ID) Protocol {
	return Protocol{Name: "eth", Version: version, Length: 17, Run: func(p *Peer, rw MsgReadWriter) error {
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
			if p.protocolStarted.Load() {
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

func advanceActivityClock(t *testing.T, clock *mclock.Simulated, elapsed time.Duration) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	// This fixture disables discovery and dialing, leaving only the policy timer.
	for clock.ActiveTimers() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("policy timer was not rearmed")
		}
		time.Sleep(time.Millisecond)
	}
	clock.Run(elapsed)
}

func TestPeerIdleEvictionRLPX(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enabled=%v", enabled), func(t *testing.T) {
			testPeerIdleEvictionRLPX(t, enabled)
		})
	}
}

func TestPeerIdleStartupGrace(t *testing.T) {
	clock := new(mclock.Simulated)
	ready := make(chan *Peer, 1)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	srv := &Server{Config: Config{
		PrivateKey: newkey(), MaxPeers: 9, NoDial: true, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", PeerIdleEviction: true, clock: clock,
		Protocols: []Protocol{activityProtocol(68, make(chan enode.ID, 1))},
	}, newPeerHook: func(p *Peer) { ready <- p; <-release }}
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
	advanceActivityClock(t, clock, 20*time.Minute)
	if _, idle := peer.idleSince(clock.Now()); idle {
		t.Fatal("peer was considered idle before protocols started")
	}
	unblock()
	waitActivityPeers(t, srv, 1)
	if _, idle := peer.idleSince(clock.Now().Add(10*time.Minute - time.Nanosecond)); idle {
		t.Fatal("peer lost its grace period during local startup delay")
	}
}

func testPeerIdleEvictionRLPX(t *testing.T, enabled bool) {
	clock := new(mclock.Simulated)
	received := make(chan enode.ID, 10)
	srv := &Server{Config: Config{
		PrivateKey: newkey(), MaxPeers: 9, NoDial: true, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", PeerIdleEviction: enabled, clock: clock,
		Protocols: []Protocol{activityProtocol(68, received), activityProtocol(69, received)},
	}}
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
	advanceActivityClock(t, clock, peerIdleTimeout-time.Second)
	keepActivityPeer(t, clients[0], received)
	advanceActivityClock(t, clock, time.Second)
	if !enabled {
		advanceActivityClock(t, clock, time.Hour)
		waitActivityPeers(t, srv, 9)
		return
	}
	advanceActivityClock(t, clock, peerIdleCheckInterval)
	waitActivityPeers(t, srv, 8)
	checkActivityCooldown(t, srv, keys, clock)
	waitActivityPeers(t, srv, 9)
	advanceActivityClock(t, clock, time.Minute)
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

func checkActivityCooldown(t *testing.T, srv *Server, keys []*ecdsa.PrivateKey, clock *mclock.Simulated) {
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
	advanceActivityClock(t, clock, peerIdleCooldown+time.Nanosecond)
	if _, err := connectActivityPeer(t, srv, jailed, 68); err != nil {
		t.Fatalf("reconnect after cooldown: %v", err)
	}
}
