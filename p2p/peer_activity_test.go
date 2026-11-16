package p2p

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

func activityTestPeer(t *testing.T, id uint16, clock mclock.Clock) *Peer {
	t.Helper()
	p := NewPeer(uintID(id), "fixture", nil)
	t.Cleanup(func() { p.rw.fd.Close() })
	p.clock = clock
	p.lastProtocolActivity.Store(int64(clock.Now()))
	p.protocolStarted.Store(true)
	p.disc = make(chan DiscReason, 1)
	p.closed = make(chan struct{})
	return p
}

func activityTestServer(t *testing.T, count, inbound int) (*Server, map[enode.ID]*Peer, *mclock.Simulated) {
	t.Helper()
	clock := new(mclock.Simulated)
	srv := &Server{Config: Config{MaxPeers: 20, PeerIdleEviction: true, clock: clock}}
	srv.peerJail = newPeerJail(peerIdleCooldown, clock)
	peers := make(map[enode.ID]*Peer)
	for i := range count {
		p := activityTestPeer(t, uint16(i+1), clock)
		p.rw.set(inboundConn, i < inbound)
		p.rw.set(trustedConn, true)
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
		{"grace", 20, 14, peerIdleTimeout - 1, nil, false},
		{"idle-at-boundary", 20, 14, peerIdleTimeout, nil, true},
		{"spare-slots", 10, 5, peerIdleTimeout, nil, false},
		{"inbound-full", 13, 13, peerIdleTimeout, nil, true},
		{"outbound-full", 6, 0, peerIdleTimeout, nil, true},
		{"total-full", 18, 17, peerIdleTimeout, nil, true},
		{"other-direction-full", 14, 13, peerIdleTimeout, nil, false},
		{"minimum-peers", 5, 0, peerIdleTimeout, func(s *Server, _ *Peer) { s.MaxPeers = 5 }, false},
		{"disabled", 20, 14, peerIdleTimeout, func(s *Server, _ *Peer) { s.PeerIdleEviction = false }, false},
		{"trusted", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { p.rw.set(trustedConn, true) }, false},
		{"static", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { p.rw.set(staticConn, true) }, false},
		{"recent-activity", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { p.recordProtocolActivity() }, false},
		{"already-closing", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { p.lastProtocolActivity.Store(peerActivityClosing) }, false},
		{"protocol-not-started", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { p.protocolStarted.Store(false) }, false},
		{"already-disconnected", 20, 14, peerIdleTimeout, func(_ *Server, p *Peer) { close(p.closed) }, false},
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
						p.lastProtocolActivity.Store(peerActivityClosing)
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
	newer.recordProtocolActivity()
	clock.Run(peerIdleTimeout)
	before := idlePeerDisconnects.Snapshot().Count()
	srv.expireIdlePeer(peers)
	if idlePeerDisconnects.Snapshot().Count() != before+1 {
		t.Fatal("disconnect metric did not increase")
	}
	if len(oldest.disc) != 1 || len(newer.disc) != 0 {
		t.Fatal("expected only the oldest idle peer to be removed")
	}
	oldest.recordProtocolActivity()
	if oldest.lastProtocolActivity.Load() != peerActivityClosing {
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
	p.protocolStarted.Store(true)
	t.Cleanup(func() { p.rw.fd.Close() })
	if _, idle := p.idleSince(p.created.Add(10*time.Minute - time.Nanosecond)); idle {
		t.Fatal("new connection did not receive a grace period")
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
			if got := p.lastProtocolActivity.Load() == int64(clock.Now()); got != tc.active {
				t.Fatalf("protocol activity = %v, want %v", got, tc.active)
			}
		})
	}
}

func TestPeerIdlePressureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		count, limit int
		want         bool
	}{{18, 20, true}, {17, 20, false}, {9, 10, true}, {8, 10, false}, {1, 0, false}, {1, -1, false}} {
		if got := peerSlotsScarce(tc.count, tc.limit); got != tc.want {
			t.Fatalf("pressure(%d, %d) = %v", tc.count, tc.limit, got)
		}
	}
	for limit := 1; limit < 10; limit++ {
		if peerSlotsScarce(limit-1, limit) || !peerSlotsScarce(limit, limit) {
			t.Fatalf("incorrect rounding of 90%% for limit %d", limit)
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
			p.recordProtocolActivity()
		}
	}()
	for {
		last := p.lastProtocolActivity.Load()
		if p.lastProtocolActivity.CompareAndSwap(last, peerActivityClosing) {
			break
		}
	}
	<-done
	if p.lastProtocolActivity.Load() != peerActivityClosing {
		t.Fatal("closing session was reopened")
	}
}
