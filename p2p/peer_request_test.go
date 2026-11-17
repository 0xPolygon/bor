package p2p

import (
	"bytes"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func prepareRequestOnlyPeers(t *testing.T, srv *Server, peers map[enode.ID]*Peer, clock *mclock.Simulated) {
	t.Helper()
	for range 44 {
		clock.Run(15 * time.Second)
		for _, peer := range peers {
			peer.activity.Observe(clock.Now(), false)
			srv.peerPolicy.Load().Observe(peer.ID().String(), peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{
				ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1, ServedBytes: 1024, MessageAt: clock.Now(),
			}})
		}
	}
	srv.log = log.Root()
	_, inbound, _ := peerCounts(peers)
	c := &conn{node: enode.SignNull(new(enr.Record), uintID(1000)), flags: inboundConn}
	if err := srv.postHandshakeChecks(peers, inbound, c); err != DiscTooManyPeers {
		t.Fatalf("expected capacity refusal, got %v", err)
	}
}

func TestRequestOnlyCapacityRefusal(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		flags                 connFlag
		count, inbound, limit int
		duplicate, jailed     bool
		want                  bool
	}{
		{"total-full", inboundConn, 20, 5, 20, false, false, true},
		{"inbound-full", inboundConn, 14, 14, 20, false, false, true},
		{"spare-slots", inboundConn, 15, 10, 20, false, false, false},
		{"outbound-refusal", dynDialedConn, 20, 14, 20, false, false, false},
		{"duplicate", inboundConn, 20, 14, 20, true, false, false},
		{"jailed", inboundConn, 20, 14, 20, false, true, false},
		{"trusted", inboundConn | trustedConn, 20, 14, 20, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, peers, _ := activityTestServer(t, tc.count, tc.inbound)
			srv.MaxPeers, srv.log = tc.limit, log.Root()
			db, err := enode.OpenDB("")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(db.Close)
			srv.localnode = enode.NewLocalNode(db, newkey())
			id := uintID(1000)
			if tc.duplicate {
				id = uintID(1)
			}
			c := &conn{node: enode.SignNull(new(enr.Record), id), flags: tc.flags}
			if tc.jailed {
				srv.peerJail.JailPeerFor(id, time.Minute)
			}
			err = srv.postHandshakeChecks(peers, tc.inbound, c)
			if tc.want && err != DiscTooManyPeers {
				t.Fatalf("expected capacity refusal, got %v", err)
			}
			if got := srv.requestOnlyPressure(tc.count, tc.inbound); got != tc.want {
				t.Fatalf("request-only pressure = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequestOnlyAtSeventyFivePercent(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	peer := peers[uintID(1)]
	peer.rw.set(trustedConn, false)
	prepareRequestOnlyPeers(t, srv, peers, clock)
	for _, id := range []uint16{10, 11, 12, 13, 20} {
		delete(peers, uintID(id))
	}
	srv.expireIdlePeer(peers)
	if len(peer.disc) != 0 || srv.peerJail.IsJailed(peer.ID()) {
		t.Fatal("rotated a downloader at 75 percent occupancy")
	}
}

func TestRequestOnlyRefusalClearedOnAdmission(t *testing.T) {
	clock := new(mclock.Simulated)
	policy := peerpolicy.New(clock)
	policy.SetReady(true)
	srv := &Server{Config: Config{
		PrivateKey: newkey(), MaxPeers: 9, NoDial: true, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", PeerIdleEviction: true, clock: clock,
		Protocols: []Protocol{activityProtocol(69, make(chan enode.ID, 1), policy)},
	}}
	srv.SetPeerPolicy(policy)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	srv.doPeerOp(func(map[enode.ID]*Peer) { srv.inboundCapacityRefusedUntil = clock.Now().Add(time.Minute) })
	if _, err := connectActivityPeer(t, srv, newkey(), 69); err != nil {
		t.Fatal(err)
	}
	waitActivityPeers(t, srv, 1)
	srv.doPeerOp(func(map[enode.ID]*Peer) {
		if srv.inboundCapacityRefusedUntil != 0 {
			t.Error("retained old refusal after admitting a peer")
		}
	})
}

func TestRequestOnlyEvictionEligibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Server, *Peer)
		want   bool
	}{
		{"enabled-inbound", nil, true},
		{"no-refusal", func(s *Server, _ *Peer) { s.inboundCapacityRefusedUntil = 0 }, false},
		{"expired-refusal", func(s *Server, p *Peer) { s.inboundCapacityRefusedUntil = p.clock.Now() }, false},
		{"only-75-percent-pressure", func(s *Server, _ *Peer) { s.MaxPeers = 26 }, false},
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
			prepareRequestOnlyPeers(t, srv, peers, clock)
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
	srv.PeerIdleMaxRemovals = 64
	for _, peer := range peers {
		peer.rw.set(trustedConn, false)
	}
	prepareRequestOnlyPeers(t, srv, peers, clock)
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
	if srv.inboundCapacityRefusedUntil != 0 {
		t.Fatal("reused a capacity refusal after rotation")
	}
	srv.expireIdlePeer(peers)
	if requestOnlyPeerDisconnects.Snapshot().Count()-before != 1 {
		t.Fatal("removed another peer without a new capacity refusal")
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
	prepareRequestOnlyPeers(t, srv, peers, clock)
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
	prepareRequestOnlyPeers(t, srv, peers, clock)
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

func observePeerRequest(srv *Server, peer *Peer, clock *mclock.Simulated) {
	peer.activity.Observe(clock.Now(), false)
	srv.peerPolicy.Load().Observe(peer.ID().String(), peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{
		ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1, MessageAt: clock.Now(),
	}})
}

func TestRequestOnlyRotationRate(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	prepareRequestOnlyPeers(t, srv, peers, clock)
	policy := srv.peerPolicy.Load()
	first, second := peers[uintID(1)], peers[uintID(2)]
	last, ok := policy.RequestOnlySince(first.ID().String(), first.activity)
	if !ok || !srv.retireActivityPeer(first, last, true, policy, time.Minute) {
		t.Fatal("first rotation failed")
	}
	for _, elapsed := range []time.Duration{0, requestOnlyRotationInterval - time.Nanosecond} {
		clock.Run(elapsed)
		observePeerRequest(srv, second, clock)
		srv.inboundCapacityRefusedUntil = clock.Now().Add(time.Minute)
		last, ok = policy.RequestOnlySince(second.ID().String(), second.activity)
		if !ok {
			t.Fatal("second peer lost eligibility")
		}
		if srv.requestOnlyPressure(20, 14) || srv.retireActivityPeer(second, last, true, policy, time.Minute) {
			t.Fatal("repeated refusals bypassed the rotation interval")
		}
	}
	clock.Run(time.Nanosecond)
	observePeerRequest(srv, second, clock)
	last, ok = policy.RequestOnlySince(second.ID().String(), second.activity)
	if !ok || !srv.requestOnlyPressure(20, 14) || !srv.retireActivityPeer(second, last, true, policy, time.Minute) {
		t.Fatal("rotation did not resume at the interval boundary")
	}
}

func TestRequestOnlyLongerRotationInterval(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	srv.PeerIdleGracePeriod = 20 * time.Minute
	srv.SetPeerPolicy(srv.peerPolicy.Load())
	prepareRequestOnlyPeers(t, srv, peers, clock)
	clock.Run(9 * time.Minute)
	peer := peers[uintID(1)]
	observePeerRequest(srv, peer, clock)
	policy := srv.peerPolicy.Load()
	last, ok := policy.RequestOnlySince(peer.ID().String(), peer.activity)
	if !ok || !srv.retireActivityPeer(peer, last, true, policy, time.Minute) {
		t.Fatal("rotation failed")
	}
	if srv.requestOnlyRotatedUntil != clock.Now().Add(20*time.Minute) {
		t.Fatal("ignored longer configured grace")
	}
}

func TestRequestOnlyPreservesIdlePriority(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	idle, requester := peers[uintID(1)], peers[uintID(2)]
	idle.rw.set(trustedConn, false)
	requester.rw.set(trustedConn, false)
	for range 11 {
		clock.Run(time.Minute)
		observePeerRequest(srv, requester, clock)
	}
	srv.inboundCapacityRefusedUntil = clock.Now().Add(time.Minute)
	srv.expireIdlePeer(peers)
	if len(idle.disc) != 1 || len(requester.disc) != 0 {
		t.Fatal("request-only rotation displaced idle cleanup")
	}
	if srv.requestOnlyRotatedUntil != 0 {
		t.Fatal("idle cleanup consumed request-only allowance")
	}
}

func TestRequestOnlyWireActivity(t *testing.T) {
	for _, protocol := range []string{"eth", "snap", "wit"} {
		t.Run(protocol, func(t *testing.T) {
			srv, peers, clock := activityTestServer(t, 20, 14)
			peer := peers[uintID(1)]
			prepareRequestOnlyPeers(t, srv, peers, clock)
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
