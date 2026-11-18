package p2p

import (
	"bytes"
	"fmt"
	"testing"
	"testing/synctest"
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
				ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1, ServedBytes: 1024,
			}})
		}
	}
	for _, peer := range peers {
		srv.peerPolicy.Load().Observe(peer.ID().String(), peerpolicy.Evidence{Reason: peerpolicy.ServingVolume})
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
		}, true},
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
	srv.PeerIdleMaxRemovals = 3
	for _, peer := range peers {
		peer.rw.set(trustedConn, false)
	}
	prepareRequestOnlyPeers(t, srv, peers, clock)
	before := requestOnlyPeerDisconnects.Snapshot().Count()
	srv.expireIdlePeer(peers)
	if got := requestOnlyPeerDisconnects.Snapshot().Count() - before; got != 3 {
		t.Fatalf("removal counter increased by %d, want 3", got)
	}
	removed := 0
	for _, peer := range peers {
		if len(peer.disc) > 0 {
			removed++
		}
	}
	if removed != 3 {
		t.Fatalf("removed %d peers, want 3", removed)
	}
	if srv.inboundCapacityRefusedUntil != 0 {
		t.Fatal("reused a capacity refusal after rotation")
	}
	srv.expireIdlePeer(peers)
	if requestOnlyPeerDisconnects.Snapshot().Count()-before != 3 {
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
	peer.activity.ObserveData(clock.Now())
	if srv.retireActivityPeer(peer, last, true, srv.peerPolicy.Load(), time.Minute) {
		t.Fatal("reported a removal after new traffic arrived")
	}
	if len(peer.disc) != 0 || srv.peerJail.IsJailed(peer.ID()) {
		t.Fatal("disconnected or jailed a peer after its eligibility changed")
	}
}

func observePeerRequest(srv *Server, peer *Peer, clock *mclock.Simulated) {
	peer.activity.Observe(clock.Now(), false)
	srv.peerPolicy.Load().Observe(peer.ID().String(), peerpolicy.Evidence{Reason: peerpolicy.ServingVolume, Activity: peerpolicy.ActivityEvent{
		ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1,
	}})
}

func TestRequestOnlyRotationRate(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 21, 15)
	prepareRequestOnlyPeers(t, srv, peers, clock)
	policy := srv.peerPolicy.Load()
	first, second := peers[uintID(1)], peers[uintID(2)]
	first.rw.set(trustedConn, false)
	second.rw.set(trustedConn, false)
	srv.expireIdlePeer(peers)
	if len(first.disc) == 0 {
		first, second = second, first
	}
	if len(first.disc) != 1 || len(second.disc) != 0 {
		t.Fatal("first rotation failed")
	}
	for _, elapsed := range []time.Duration{0, requestOnlyRotationInterval - time.Nanosecond} {
		clock.Run(elapsed)
		observePeerRequest(srv, second, clock)
		srv.inboundCapacityRefusedUntil = clock.Now().Add(time.Minute)
		last, ok := policy.RequestOnlySince(second.ID().String(), second.activity)
		if !ok {
			t.Fatal("second peer lost eligibility")
		}
		count, inbound, _ := peerCounts(peers)
		if srv.requestOnlyPressure(count, inbound) || srv.retireActivityPeer(second, last, true, policy, time.Minute) {
			t.Fatal("repeated refusals bypassed the rotation interval")
		}
	}
	clock.Run(time.Nanosecond)
	observePeerRequest(srv, second, clock)
	srv.expireIdlePeer(peers)
	if len(second.disc) != 1 {
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
	peer.rw.set(trustedConn, false)
	observePeerRequest(srv, peer, clock)
	srv.inboundCapacityRefusedUntil = clock.Now().Add(time.Minute)
	srv.expireIdlePeer(peers)
	if len(peer.disc) != 1 {
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
	srv.peerPolicy.Load().Observe(requester.ID().String(), peerpolicy.Evidence{Reason: peerpolicy.ServingVolume})
	srv.expireIdlePeer(peers)
	if len(idle.disc) != 1 || len(requester.disc) != 0 {
		t.Fatal("request-only rotation displaced idle cleanup")
	}
	if srv.requestOnlyRotatedUntil != 0 {
		t.Fatal("idle cleanup consumed request-only allowance")
	}
}

func TestRequestOnlyWireActivity(t *testing.T) {
	for _, tc := range []struct {
		protocol string
		code     uint64
	}{{"eth", 3}, {"eth", 1}, {"snap", 7}, {"wit", 7}} {
		t.Run(fmt.Sprintf("%s/%d", tc.protocol, tc.code), func(t *testing.T) {
			protocol := tc.protocol
			srv, peers, clock := activityTestServer(t, 20, 14)
			peer := peers[uintID(1)]
			prepareRequestOnlyPeers(t, srv, peers, clock)
			peer.running[protocol] = &protoRW{Protocol: Protocol{Name: protocol, Length: 17, CanContribute: func(code uint64) bool { return code == 2 || code == 4 || code == 6 || code == 7 || code == 10 }}, offset: 16, in: make(chan Msg, 1)}
			clock.Run(time.Second)
			if err := peer.handle(Msg{Code: 16 + tc.code, Payload: bytes.NewReader(nil)}); err != nil {
				t.Fatal(err)
			}
			msg := <-peer.running[protocol].in
			if msg.ReceivedSequence == 0 {
				t.Fatal("missing wire receipt sequence")
			}
			policy := srv.peerPolicy.Load()
			policy.Observe(peer.ID().String(), peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{ReceivedMessages: 1, MessageSequence: msg.ReceivedSequence}})
			clock.Run(time.Nanosecond)
			observePeerRequest(srv, peer, clock)
			_, eligible := policy.RequestOnlySince(peer.ID().String(), peer.activity)
			if eligible != (protocol == "eth") {
				t.Fatalf("%s eligibility = %v", protocol, eligible)
			}
		})
	}
}

func TestRequestOnlyBatchLimit(t *testing.T) {
	for _, tc := range []struct {
		name                                      string
		count, inbound, capacity, limit, removals int
	}{
		{"default", 100, 67, 100, 0, 1},
		{"sixteen", 100, 67, 100, 16, 16},
		{"below-full", 100, 67, 120, 16, 0},
		{"pressure-cleared", 100, 67, 100, 64, 26},
		{"hard-limit", 500, 334, 500, 1000, 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, peers, clock := activityTestServer(t, tc.count, tc.inbound)
			srv.MaxPeers, srv.PeerIdleMaxRemovals = tc.count, tc.limit
			prepareRequestOnlyPeers(t, srv, peers, clock)
			srv.MaxPeers = tc.capacity
			for _, peer := range peers {
				peer.rw.set(trustedConn, false)
			}
			before := requestOnlyPeerDisconnects.Snapshot().Count()
			srv.expireIdlePeer(peers)
			if got := requestOnlyPeerDisconnects.Snapshot().Count() - before; got != int64(tc.removals) {
				t.Fatalf("removed %d, want %d", got, tc.removals)
			}
			for _, peer := range peers {
				if !peer.Inbound() && len(peer.disc) != 0 {
					t.Fatal("batch removed an outbound peer")
				}
			}
		})
	}
}

func TestRequestOnlyPartialBatchRate(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	srv.PeerIdleMaxRemovals = 16
	prepareRequestOnlyPeers(t, srv, peers, clock)
	first, second := peers[uintID(1)], peers[uintID(2)]
	first.rw.set(trustedConn, false)
	second.rw.set(trustedConn, false)
	clock.Run(time.Nanosecond)
	observePeerRequest(srv, second, clock)
	second.disc <- DiscQuitting
	srv.expireIdlePeer(peers)
	if len(first.disc) != 1 || srv.peerJail.IsJailed(second.ID()) {
		t.Fatal("incorrect partial batch result")
	}
	if srv.requestOnlyRotatedUntil != clock.Now().Add(requestOnlyRotationInterval) {
		t.Fatal("partial batch did not start the rotation interval")
	}
}

func TestRequestOnlyMixedBatch(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	srv.PeerIdleMaxRemovals = 3
	for id := uint16(1); id <= 4; id++ {
		peers[uintID(id)].rw.set(trustedConn, false)
	}
	for range 11 {
		clock.Run(time.Minute)
		observePeerRequest(srv, peers[uintID(3)], clock)
		observePeerRequest(srv, peers[uintID(4)], clock)
	}
	srv.inboundCapacityRefusedUntil = clock.Now().Add(time.Minute)
	before := requestOnlyPeerDisconnects.Snapshot().Count()
	srv.expireIdlePeer(peers)
	if len(peers[uintID(1)].disc) != 1 || len(peers[uintID(2)].disc) != 1 {
		t.Fatal("mixed batch did not prioritize idle peers")
	}
	if requestOnlyPeerDisconnects.Snapshot().Count()-before != 1 {
		t.Fatal("mixed batch did not share its removal budget")
	}
	if srv.requestOnlyRotatedUntil != clock.Now().Add(requestOnlyRotationInterval) {
		t.Fatal("mixed batch did not start the rotation interval")
	}
}

func TestRequestOnlyPendingWireMessages(t *testing.T) {
	for _, tc := range []struct {
		name   string
		codes  []uint64
		remove bool
	}{
		{"request", []uint64{3}, true},
		{"queued-requests", []uint64{3, 3}, true},
		{"block-announcement", []uint64{1}, true},
		{"tx-announcement", []uint64{8}, true},
		{"range-update", []uint64{17}, true},
		{"request-then-announcement", []uint64{3, 8}, true},
		{"announcement-then-request", []uint64{8, 3}, true},
		{"block", []uint64{7}, false},
		{"transactions", []uint64{2}, false},
		{"pooled-transactions", []uint64{10}, false},
		{"bodies", []uint64{6}, false},
		{"headers", []uint64{4}, false},
		{"request-then-headers", []uint64{3, 4}, false},
		{"headers-then-request", []uint64{4, 3}, false},
		{"block-then-request", []uint64{7, 3}, false},
		{"request-then-block", []uint64{3, 7}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				srv, peers, clock := activityTestServer(t, 20, 14)
				peer := peers[uintID(1)]
				prepareRequestOnlyPeers(t, srv, peers, clock)
				peer.rw.set(trustedConn, false)
				wire := &protoRW{Protocol: Protocol{Name: "eth", Length: 18, CanContribute: func(code uint64) bool { return code == 2 || code == 4 || code == 6 || code == 7 || code == 10 }}, offset: 16, in: make(chan Msg)}
				peer.running["eth"] = wire
				for i, code := range tc.codes {
					clock.Run(time.Nanosecond)
					done := make(chan error, 1)
					go func() { done <- peer.handle(Msg{Code: 16 + code, Payload: bytes.NewReader(nil)}) }()
					synctest.Wait()
					if i < len(tc.codes)-1 {
						<-wire.in
						if err := <-done; err != nil {
							t.Fatal(err)
						}
						continue
					}
					srv.expireIdlePeer(peers)
					if got := srv.peerJail.IsJailed(peer.ID()); got != tc.remove {
						t.Errorf("peer entered cooldown = %v, want %v", got, tc.remove)
					}
					<-wire.in
					if err := <-done; err != nil {
						t.Fatal(err)
					}
				}
			})
		})
	}
}
