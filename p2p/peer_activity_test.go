package p2p

import (
	"bytes"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/naoina/toml"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

const peerIdleTimeout = peerpolicy.IdleTimeout

func TestPeerActivityDoesNotRetainConnection(t *testing.T) {
	tracker, released := retainedActivityRecord(t)
	deadline := time.After(3 * time.Second)
	for {
		runtime.GC()
		select {
		case <-released:
			if tracker.Snapshot("peer").Contribution.DisconnectReason != "idle" {
				t.Fatal("connection collection lost the retained activity")
			}
			return
		case <-deadline:
			t.Fatal("retained activity keeps the disconnected peer alive")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func retainedActivityRecord(t *testing.T) (*peerpolicy.Tracker, <-chan struct{}) {
	t.Helper()
	clock := new(mclock.Simulated)
	tracker := peerpolicy.New(clock)
	peer := NewPeer(uintID(1), "fixture", nil)
	if err := peer.rw.fd.Close(); err != nil {
		t.Fatal(err)
	}
	peer.activity.Start(clock.Now())
	tracker.BindConnection("peer", tracker.Connected("peer"), peer.ConnectionActivity())
	peer.activity.Close()
	peer.activity.MarkIdleDisconnect()
	released := make(chan struct{})
	runtime.SetFinalizer(peer, func(*Peer) { close(released) })
	return tracker, released
}

func activityTestPeer(t *testing.T, id uint16, clock mclock.Clock) *Peer {
	t.Helper()
	p := NewPeer(uintID(id), "fixture", nil)
	t.Cleanup(func() { p.rw.fd.Close() })
	p.clock = clock
	p.activity.Start(clock.Now())
	p.disc = make(chan DiscReason, 1)
	p.closed = make(chan struct{})
	return p
}

func activityTestServer(t *testing.T, count, inbound int) (*Server, map[enode.ID]*Peer, *mclock.Simulated) {
	t.Helper()
	clock := new(mclock.Simulated)
	srv := &Server{Config: Config{MaxPeers: 20, PeerIdleEviction: true, clock: clock}}
	srv.peerJail = newPeerJail(DefaultPeerIdleReconnectCooldown, clock)
	policy := peerpolicy.New(clock)
	policy.SetReady(true)
	srv.SetPeerPolicy(policy)
	peers := make(map[enode.ID]*Peer)
	for i := range count {
		p := activityTestPeer(t, uint16(i+1), clock)
		p.rw.set(inboundConn, i < inbound)
		p.rw.set(trustedConn, true)
		policy.BindConnection(p.ID().String(), policy.Connected(p.ID().String()), p.activity)
		peers[p.ID()] = p
	}
	return srv, peers, clock
}

func TestPeerIdleEvictionEligibility(t *testing.T) {
	for _, tc := range []struct {
		name           string
		count, inbound int
		age            time.Duration
		change         func(*Server, *Peer)
		want           bool
	}{
		{"grace", 20, 14, 10*time.Minute - time.Nanosecond, nil, false},
		{"idle-at-boundary", 20, 14, 10 * time.Minute, nil, true},
		{"spare-slots", 8, 4, peerIdleTimeout, nil, false},
		{"inbound-full", 13, 13, peerIdleTimeout, nil, true},
		{"outbound-full", 6, 0, peerIdleTimeout, nil, true},
		{"total-full", 18, 17, peerIdleTimeout, nil, true},
		{"other-direction-full", 14, 13, peerIdleTimeout, nil, false},
		{"minimum-peers", 5, 0, peerIdleTimeout, func(s *Server, _ *Peer) { s.MaxPeers = 5 }, false},
		{"disabled", 20, 14, peerIdleTimeout, func(s *Server, _ *Peer) { s.PeerIdleEviction = false }, false},
		{"trusted", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { p.rw.set(trustedConn, true) }, false},
		{"static", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { p.rw.set(staticConn, true) }, false},
		{"recent-activity", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { p.activity.Observe(p.clock.Now(), false) }, false},
		{"already-closing", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { p.activity.Close() }, false},
		{"protocol-not-started", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { p.activity = new(peerpolicy.ConnectionActivity) }, false},
		{"already-disconnected", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { close(p.closed) }, false},
		{"syncing", 20, 14, peerIdleTimeout, func(s *Server, _ *Peer) { s.peerPolicy.Load().SetReady(false) }, false},
		{"reputation-disabled", 20, 14, peerIdleTimeout, func(s *Server, _ *Peer) { s.SetPeerPolicy(nil) }, false},
		{"no-jail", 20, 14, peerIdleTimeout, func(s *Server, _ *Peer) { s.peerJail = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, peers, clock := activityTestServer(t, tc.count, tc.inbound)
			p := peers[uintID(uint16(tc.count))]
			p.rw.set(trustedConn, false)
			clock.Run(tc.age)
			if tc.change != nil {
				tc.change(srv, p)
			}
			srv.expireIdlePeer(peers)
			select {
			case reason := <-p.disc:
				if !tc.want || reason != DiscJailed || !srv.peerJail.IsJailed(p.ID()) {
					t.Fatalf("unexpected disconnect: %v", reason)
				}
			default:
				if tc.want {
					t.Fatal("idle peer was retained")
				}
			}
		})
	}
}

func TestPeerIdleEvictionLiveFloor(t *testing.T) {
	for _, live := range []int{5, 6} {
		for _, closed := range []bool{false, true} {
			t.Run(fmt.Sprintf("live=%d/closed=%v", live, closed), func(t *testing.T) {
				srv, peers, clock := activityTestServer(t, 20, 14)
				for i := 1; i <= 20-live; i++ {
					p := peers[uintID(uint16(i))]
					if closed {
						close(p.closed)
					} else {
						p.activity.Close()
					}
				}
				p := peers[uintID(20)]
				p.rw.set(trustedConn, false)
				clock.Run(peerIdleTimeout)
				srv.expireIdlePeer(peers)
				if got := len(p.disc) != 0; got != (live == 6) {
					t.Fatalf("disconnect = %v with %d live peers", got, live)
				}
			})
		}
	}
}

func TestPeerIdleEvictionClosingSlots(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	for i := 11; i <= 20; i++ {
		close(peers[uintID(uint16(i))].closed)
	}
	p := peers[uintID(1)]
	p.rw.set(trustedConn, false)
	clock.Run(peerIdleTimeout)
	srv.expireIdlePeer(peers)
	if len(p.disc) != 0 {
		t.Fatal("connections waiting for shutdown caused unnecessary eviction")
	}
}

func TestPeerIdleEvictionSelectsOneOldest(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	oldest, newer := peers[uintID(1)], peers[uintID(2)]
	oldest.rw.set(trustedConn, false)
	newer.rw.set(trustedConn, false)
	clock.Run(time.Minute)
	newer.activity.Observe(newer.clock.Now(), false)
	clock.Run(peerIdleTimeout)
	before := idlePeerDisconnects.Snapshot().Count()
	policy := srv.peerPolicy.Load()
	if got := policy.Snapshot(oldest.ID().String()); got.Risk != 0 || got.Contribution.Risk != 20 || got.Contribution.Status != "idle" {
		t.Fatalf("eviction candidate disagrees with reputation: %+v", got)
	}
	srv.expireIdlePeer(peers)
	if idlePeerDisconnects.Snapshot().Count() != before+1 {
		t.Fatal("disconnect metric did not increase")
	}
	if len(oldest.disc) != 1 || len(newer.disc) != 0 {
		t.Fatal("expected only the oldest idle peer to be removed")
	}
	if got := policy.Snapshot(oldest.ID().String()).Contribution; got.DisconnectReason != "idle" {
		t.Fatalf("eviction missing from activity record: %+v", got)
	}
	oldest.activity.Observe(oldest.clock.Now(), false)
	if _, live := oldest.activity.Last(); live {
		t.Fatal("activity reopened a closing session")
	}
	clock.Run(5*time.Minute - time.Nanosecond)
	if !srv.peerJail.IsJailed(oldest.ID()) {
		t.Fatal("cooldown expired early")
	}
	clock.Run(2 * time.Nanosecond)
	if srv.peerJail.IsJailed(oldest.ID()) {
		t.Fatal("cooldown did not expire")
	}
}

func TestPeerIdleGraceStartsAtConnection(t *testing.T) {
	p := NewPeer(uintID(1), "fixture", nil)
	p.closed = make(chan struct{})
	p.activity.Start(p.created)
	t.Cleanup(func() { p.rw.fd.Close() })
	if last, live := p.activity.Last(); !live || p.created.Add(peerIdleTimeout-time.Nanosecond).Sub(last) >= peerIdleTimeout {
		t.Fatal("new connection did not receive a grace period")
	}
}

func TestPeerIdleRotationKeepsRecentContributor(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	contributor, quiet := peers[uintID(1)], peers[uintID(2)]
	contributor.rw.set(trustedConn, false)
	quiet.rw.set(trustedConn, false)
	policy := srv.peerPolicy.Load()
	policy.Observe(contributor.ID().String(), peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{UsefulBlock: true}})
	clock.Run(peerIdleTimeout)
	srv.expireIdlePeer(peers)
	if len(contributor.disc) != 0 || len(quiet.disc) != 1 {
		t.Fatal("expected only the idle peer with no useful deliveries to be removed")
	}
}

func TestPeerIdleRotationPrefersLowerScore(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	older, lowerScore := peers[uintID(1)], peers[uintID(2)]
	older.rw.set(trustedConn, false)
	lowerScore.rw.set(trustedConn, false)
	clock.Run(time.Minute)
	lowerScore.activity.Observe(clock.Now(), false)
	clock.Run(peerIdleTimeout)
	policy := srv.peerPolicy.Load()
	policy.Observe(lowerScore.ID().String(), peerpolicy.Evidence{Reason: peerpolicy.InvalidBlock})
	srv.expireIdlePeer(peers)
	if len(older.disc) != 0 || len(lowerScore.disc) != 1 {
		t.Fatal("expected rotation to prefer the lower score despite its newer activity")
	}
}

func TestPeerProtocolActivity(t *testing.T) {
	for _, tc := range []struct {
		name, protocol string
		version        uint
		code           uint64
		active         bool
	}{
		{"ping", "eth", 68, pingMsg, false},
		{"pong", "eth", 69, pongMsg, false},
		{"base-message", "eth", 69, 15, false},
		{"eth68-request", "eth", 68, 16 + 3, true},
		{"eth69-response", "eth", 69, 16 + 4, true},
		{"eth-announcement", "eth", 69, 16 + 8, true},
		{"snap-request", "snap", 1, 16, true},
		{"snap-response", "snap", 1, 17, true},
		{"wit-response", "wit", 2, 17, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := new(mclock.Simulated)
			p := activityTestPeer(t, 1, clock)
			p.running[tc.protocol] = &protoRW{Protocol: Protocol{Name: tc.protocol, Version: tc.version, Length: 17}, offset: 16, in: make(chan Msg, 1)}
			clock.Run(time.Minute)
			if err := p.handle(Msg{Code: tc.code, Payload: bytes.NewReader(nil)}); err != nil {
				t.Fatal(err)
			}
			last, live := p.activity.Last()
			if got := live && last == clock.Now(); got != tc.active {
				t.Fatalf("protocol activity = %v, want %v", got, tc.active)
			}
		})
	}
}

func TestPeerIdlePressureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		count, limit int
		want         bool
	}{{15, 20, true}, {14, 20, false}, {8, 10, true}, {7, 10, false}, {1, 0, false}, {1, -1, false}} {
		if got := peerSlotsScarce(tc.count, tc.limit, 75); got != tc.want {
			t.Fatalf("pressure(%d, %d) = %v", tc.count, tc.limit, got)
		}
	}
	for limit := 1; limit < 10; limit++ {
		threshold := (3*limit + 3) / 4
		if peerSlotsScarce(threshold-1, limit, 75) || !peerSlotsScarce(threshold, limit, 75) {
			t.Fatalf("incorrect rounding of 75%% for limit %d", limit)
		}
	}
}

func TestPeerIdleEvictionPendingDisconnect(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	p := peers[uintID(20)]
	p.rw.set(trustedConn, false)
	p.disc <- DiscQuitting
	clock.Run(peerIdleTimeout)
	done := make(chan struct{})
	go func() {
		srv.expireIdlePeer(peers)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		<-p.disc
		<-done
		t.Fatal("pending disconnect blocked maintenance")
	}
	if srv.peerJail.IsJailed(p.ID()) {
		t.Fatal("peer already leaving was jailed")
	}
	if p.isLive() {
		t.Fatal("pending disconnect still counted as a live connection")
	}
	if got := srv.peerPolicy.Load().Snapshot(p.ID().String()).Contribution; got.DisconnectReason != "" {
		t.Fatal("pending disconnect attributed to idle policy")
	}
}

func TestPeerIdleDisconnectBeforeRun(t *testing.T) {
	p := NewPeer(uintID(1), "fixture", nil)
	p.closed = make(chan struct{})
	t.Cleanup(func() { p.rw.fd.Close() })
	done := make(chan struct{})
	go func() {
		p.Disconnect(DiscQuitting)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(p.closed)
		<-done
		t.Fatal("disconnect blocked before peer loop started")
	}
}

func TestPeerIdleEvictionDirectionalPressure(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 22, 6)
	srv.MaxPeers = 50
	p := peers[uintID(22)]
	p.rw.set(trustedConn, false)
	clock.Run(peerIdleTimeout)
	srv.expireIdlePeer(peers)
	if len(p.disc) != 1 {
		t.Fatal("full outbound quota did not trigger rotation")
	}
}

func TestPeerActivityConcurrentEviction(t *testing.T) {
	clock := new(mclock.Simulated)
	p := activityTestPeer(t, 1, clock)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			p.activity.Observe(p.clock.Now(), false)
		}
	}()
	p.activity.Close()
	<-done
	if _, live := p.activity.Last(); live {
		t.Fatal("closing session was reopened")
	}
}

func TestPeerIdleSparseTraffic(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 14)
	peer := peers[uintID(20)]
	peer.rw.set(trustedConn, false)
	for interval := range 20 {
		clock.Run(3 * time.Minute)
		srv.expireIdlePeer(peers)
		if len(peer.disc) != 0 || srv.peerJail.IsJailed(peer.ID()) {
			t.Fatalf("active peer retired at interval %d", interval+1)
		}
		peer.activity.Observe(clock.Now(), false)
	}
	clock.Run(10*time.Minute - time.Nanosecond)
	srv.expireIdlePeer(peers)
	if len(peer.disc) != 0 || srv.peerJail.IsJailed(peer.ID()) {
		t.Fatal("peer retired before the renewed grace expired")
	}
	clock.Run(time.Nanosecond)
	srv.expireIdlePeer(peers)
	if len(peer.disc) != 1 {
		t.Fatal("peer was not eligible after becoming silent")
	}
}

func TestPeerIdleRemovalBudget(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		count, limit, budget, want int
	}{
		{"default", 20, 20, 0, 1},
		{"batch", 20, 20, 3, 3},
		{"pressure-clears", 20, 20, 64, 10},
		{"live-floor", 6, 6, 5, 1},
		{"bounded-native-config", 200, 200, 1000, 64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, peers, clock := activityTestServer(t, tc.count, tc.count)
			srv.MaxPeers, srv.PeerIdleMaxRemovals = tc.limit, tc.budget
			for _, peer := range peers {
				peer.rw.set(trustedConn, false)
			}
			clock.Run(peerIdleTimeout)
			srv.expireIdlePeer(peers)
			removed := 0
			for _, peer := range peers {
				removed += len(peer.disc)
			}
			if removed != tc.want {
				t.Fatalf("removed %d peers, want %d", removed, tc.want)
			}
		})
	}
}

func TestPeerIdleBatchPreservesProtectedPeers(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 20)
	srv.PeerIdleMaxRemovals = 5
	peers[uintID(1)].rw.set(trustedConn, false)
	peers[uintID(2)].rw.set(trustedConn, false)
	peers[uintID(2)].rw.set(staticConn, true)
	clock.Run(peerIdleTimeout)
	srv.expireIdlePeer(peers)
	for id, peer := range peers {
		if got, want := len(peer.disc) != 0, id == uintID(1); got != want {
			t.Fatalf("peer %s disconnected=%t want=%t", id, got, want)
		}
	}
}

func TestPeerIdleConfiguredGrace(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 20, 20)
	srv.PeerIdleGracePeriod = 4 * time.Minute
	policy := srv.peerPolicy.Load()
	srv.SetPeerPolicy(policy)
	peer := peers[uintID(1)]
	peer.rw.set(trustedConn, false)
	clock.Run(4*time.Minute - time.Nanosecond)
	srv.expireIdlePeer(peers)
	before := policy.Snapshot(peer.ID().String()).Contribution
	if len(peer.disc) != 0 || before.Risk != 0 || before.IdleGraceSeconds != 240 {
		t.Fatalf("configured grace was not respected: %+v", before)
	}
	clock.Run(time.Nanosecond)
	if got := policy.Snapshot(peer.ID().String()).Contribution; got.Status != "idle" || got.Risk != 20 {
		t.Fatalf("configured score boundary was not respected: %+v", got)
	}
	srv.expireIdlePeer(peers)
	if len(peer.disc) != 1 {
		t.Fatal("peer was not removed at configured grace boundary")
	}
}

func TestPeerIdleConfigRoundTrip(t *testing.T) {
	original := Config{PeerIdleEviction: true, PeerIdleGracePeriod: 3 * time.Minute, PeerIdleMaxRemovals: 5, PeerIdleReconnectCooldown: 7 * time.Minute, PeerIdleOccupancyThreshold: 85}
	encoded, err := toml.Marshal(struct{ P2P Config }{original})
	if err != nil {
		t.Fatal(err)
	}
	var restored struct{ P2P Config }
	if err := toml.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.P2P.PeerIdleGracePeriod != original.PeerIdleGracePeriod || restored.P2P.PeerIdleMaxRemovals != original.PeerIdleMaxRemovals || restored.P2P.PeerIdleReconnectCooldown != original.PeerIdleReconnectCooldown || restored.P2P.PeerIdleOccupancyThreshold != original.PeerIdleOccupancyThreshold {
		t.Fatal("native TOML lost idle policy settings")
	}
}

func TestPeerIdleConfiguredCooldown(t *testing.T) {
	for _, tc := range []struct {
		name             string
		configured, want time.Duration
	}{
		{"default", 0, 5 * time.Minute},
		{"negative-native", -time.Second, 5 * time.Minute},
		{"minimum", time.Second, time.Second},
		{"shorter", 90 * time.Second, 90 * time.Second},
		{"longer", 7 * time.Minute, 7 * time.Minute},
		{"maximum", 24 * time.Hour, 24 * time.Hour},
		{"bounded-native", 48 * time.Hour, 24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, peers, clock := activityTestServer(t, 20, 20)
			srv.PeerIdleReconnectCooldown = tc.configured
			peer := peers[uintID(1)]
			peer.rw.set(trustedConn, false)
			clock.Run(peerIdleTimeout)
			srv.expireIdlePeer(peers)
			until, jailed := srv.peerJail.JailedUntil(peer.ID())
			if !jailed || until.Sub(clock.Now()) != tc.want {
				t.Fatalf("cooldown=%v jailed=%v want=%v", until.Sub(clock.Now()), jailed, tc.want)
			}
			clock.Run(tc.want - time.Nanosecond)
			if !srv.peerJail.IsJailed(peer.ID()) {
				t.Fatal("reconnect cooldown expired early")
			}
			clock.Run(2 * time.Nanosecond)
			if srv.peerJail.IsJailed(peer.ID()) {
				t.Fatal("reconnect cooldown did not expire")
			}
		})
	}
}
