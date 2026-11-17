package p2p

import (
	"bytes"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func serveRequestOnly(srv *Server, peers map[enode.ID]*Peer, clock *mclock.Simulated) {
	for range 44 {
		clock.Run(15 * time.Second)
		for _, peer := range peers {
			peer.activity.Observe(clock.Now(), false)
			srv.peerPolicy.Load().Observe(peer.ID().String(), peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{
				ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1, ServedBytes: 1024, MessageAt: clock.Now(),
			}})
		}
	}
}

func TestRequestOnlyEvictionEligibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Server, *Peer)
		want   bool
	}{
		{"enabled-inbound", nil, true},
		{"disabled", func(s *Server, _ *Peer) { s.PeerIdleEviction = false }, false},
		{"outbound", func(_ *Server, p *Peer) { p.rw.set(inboundConn, false) }, false},
		{"trusted", func(_ *Server, p *Peer) { p.rw.set(trustedConn, true) }, false},
		{"static", func(_ *Server, p *Peer) { p.rw.set(staticConn, true) }, false},
		{"syncing", func(s *Server, _ *Peer) { s.peerPolicy.Load().SetReady(false) }, false},
		{"longer-grace", func(s *Server, _ *Peer) { s.peerPolicy.Load().SetIdleGrace(20 * time.Minute) }, false},
		{"spare-capacity", func(s *Server, _ *Peer) { s.MaxPeers = 100 }, false},
		{"accepted-transaction", func(s *Server, p *Peer) {
			s.peerPolicy.Load().Observe(p.ID().String(), peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{AcceptedTransactions: 1}})
		}, false},
		{"announcement", func(s *Server, p *Peer) {
			s.peerPolicy.Load().Observe(p.ID().String(), peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{ReceivedMessages: 1}})
		}, false},
		{"block-delivery", func(s *Server, p *Peer) {
			s.peerPolicy.Load().Observe(p.ID().String(), peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{UnclassifiedMessages: 1}})
		}, false},
		{"auxiliary-protocol", func(_ *Server, p *Peer) { p.activity.Observe(p.clock.Now(), true) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, peers, clock := activityTestServer(t, 20, 14)
			peer := peers[uintID(1)]
			peer.rw.set(trustedConn, false)
			serveRequestOnly(srv, peers, clock)
			if tc.change != nil {
				tc.change(srv, peer)
			}
			srv.expireIdlePeer(peers)
			if got := len(peer.disc) != 0; got != tc.want {
				t.Fatalf("removed = %v, want %v", got, tc.want)
			}
			if tc.want {
				if !srv.peerJail.IsJailed(peer.ID()) {
					t.Fatal("missing reconnect cooldown")
				}
				if srv.peerPolicy.Load().Snapshot(peer.ID().String()).Contribution.DisconnectReason != "request-only" {
					t.Fatal("wrong removal reason")
				}
			}
		})
	}
}

func TestRequestOnlyEvictionBoundAndCooldown(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	for _, peer := range peers {
		peer.rw.set(trustedConn, false)
	}
	serveRequestOnly(srv, peers, clock)
	before := requestOnlyPeerDisconnects.Snapshot().Count()
	srv.expireIdlePeer(peers)
	if got := requestOnlyPeerDisconnects.Snapshot().Count() - before; got != 1 {
		t.Fatalf("removal counter increased by %d, want 1", got)
	}
	removed := 0
	for _, peer := range peers {
		if len(peer.disc) > 0 {
			removed++
		}
	}
	if removed != 1 {
		t.Fatalf("removed %d peers, want 1", removed)
	}
	clock.Run(DefaultPeerIdleReconnectCooldown + time.Nanosecond)
	for _, peer := range peers {
		if srv.peerJail.IsJailed(peer.ID()) {
			t.Fatal("cooldown did not expire")
		}
	}
}

func TestRequestOnlyPendingDisconnect(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	peer := peers[uintID(1)]
	peer.rw.set(trustedConn, false)
	serveRequestOnly(srv, peers, clock)
	peer.disc <- DiscQuitting
	last, ok := srv.peerPolicy.Load().RequestOnlySince(peer.ID().String(), peer.activity)
	if !ok {
		t.Fatal("fixture is not eligible")
	}
	if srv.retireActivityPeer(peer, last, true, srv.peerPolicy.Load(), time.Minute) {
		t.Fatal("reported a removal while another disconnect was already queued")
	}
	if srv.peerJail.IsJailed(peer.ID()) {
		t.Fatal("applied cooldown without requesting a disconnect")
	}
}

func TestRequestOnlyChangedAfterSelection(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	peer := peers[uintID(1)]
	serveRequestOnly(srv, peers, clock)
	last, ok := srv.peerPolicy.Load().RequestOnlySince(peer.ID().String(), peer.activity)
	if !ok {
		t.Fatal("fixture is not eligible")
	}
	clock.Run(time.Second)
	peer.activity.Observe(clock.Now(), false)
	if srv.retireActivityPeer(peer, last, true, srv.peerPolicy.Load(), time.Minute) {
		t.Fatal("reported a removal after new traffic arrived")
	}
	if len(peer.disc) != 0 || srv.peerJail.IsJailed(peer.ID()) {
		t.Fatal("disconnected or jailed a peer after its eligibility changed")
	}
}

func TestRequestOnlyWireActivity(t *testing.T) {
	for _, protocol := range []string{"eth", "snap", "wit"} {
		t.Run(protocol, func(t *testing.T) {
			srv, peers, clock := activityTestServer(t, 20, 14)
			peer := peers[uintID(1)]
			serveRequestOnly(srv, peers, clock)
			peer.running[protocol] = &protoRW{Protocol: Protocol{Name: protocol, Length: 17}, offset: 16, in: make(chan Msg, 1)}
			clock.Run(time.Second)
			if err := peer.handle(Msg{Code: 19, Payload: bytes.NewReader(nil)}); err != nil {
				t.Fatal(err)
			}
			msg := <-peer.running[protocol].in
			if msg.ReceivedAtMonotonic != clock.Now() {
				t.Fatal("missing wire receipt timestamp")
			}
			policy := srv.peerPolicy.Load()
			policy.Observe(peer.ID().String(), peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{ReceivedMessages: 1, ReceivedRequests: 1, MessageAt: msg.ReceivedAtMonotonic}})
			_, eligible := policy.RequestOnlySince(peer.ID().String(), peer.activity)
			if eligible != (protocol == "eth") {
				t.Fatalf("%s eligibility = %v", protocol, eligible)
			}
		})
	}
}
