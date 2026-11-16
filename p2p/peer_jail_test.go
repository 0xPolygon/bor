package p2p

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestPeerJailReconnect(t *testing.T) {
	testPeerJailReconnect(t, 5*time.Minute, func(srv *Server, id enode.ID) { srv.JailPeer(id) })
}

func TestPeerJailReconnectAfterCustomPeriod(t *testing.T) {
	testPeerJailReconnect(t, 2*time.Minute, func(srv *Server, id enode.ID) { srv.JailPeerFor(id, 2*time.Minute) })
}

func TestPeerJailReconnectAtCapacity(t *testing.T) {
	testPeerJailReconnect(t, 2*time.Minute, func(srv *Server, id enode.ID) {
		for i := range maxPeerJailEntries {
			var existing enode.ID
			binary.BigEndian.PutUint64(existing[:], uint64(i))
			srv.peerJail.JailPeer(existing)
		}
		srv.JailPeerFor(id, 2*time.Minute)
		if len(srv.peerJail.jailed) != maxPeerJailEntries {
			t.Fatal("replacement exceeded jail capacity")
		}
	})
}

func testPeerJailReconnect(t *testing.T, period time.Duration, jail func(*Server, enode.ID)) {
	t.Helper()
	clock := new(mclock.Simulated)
	srv := &Server{
		Config: Config{PrivateKey: newkey(), MaxPeers: 10, NoDial: true, NoDiscovery: true, clock: clock},
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	node := enode.NewV4(&newkey().PublicKey, nil, 0, 0)
	dial := &dialScheduler{dialConfig: dialConfig{jailedUntil: srv.peerJail.JailedUntil}}
	jail(srv, node.ID())
	if err := dial.checkDial(node); !errors.Is(err, errJailed) {
		t.Fatalf("jailed outbound dial: %v", err)
	}
	for _, flags := range []connFlag{inboundConn, dynDialedConn, staticDialedConn} {
		if err := srv.postHandshakeChecks(nil, 0, &conn{node: node, flags: flags}); !errors.Is(err, DiscJailed) {
			t.Fatalf("jailed connection %v: %v", flags, err)
		}
	}
	clock.Run(period - time.Nanosecond)
	if !srv.peerJail.IsJailed(node.ID()) {
		t.Fatal("jail expired early")
	}
	clock.Run(2 * time.Nanosecond)
	if err := dial.checkDial(node); err != nil {
		t.Fatalf("outbound dial after expiry: %v", err)
	}
	if err := srv.postHandshakeChecks(nil, 0, &conn{node: node, flags: inboundConn}); err != nil {
		t.Fatalf("inbound connection after expiry: %v", err)
	}
}

func TestPeerJailDoesNotShortenExistingPeriod(t *testing.T) {
	clock := new(mclock.Simulated)
	jail := newPeerJail(5*time.Minute, clock)
	id := enode.ID{1}
	jail.JailPeer(id)
	jail.JailPeerFor(id, 2*time.Minute)
	clock.Run(3 * time.Minute)
	if !jail.IsJailed(id) {
		t.Fatal("shorter backoff replaced the existing jail")
	}
}

func TestPeerJailIgnoresNonPositivePeriod(t *testing.T) {
	clock := new(mclock.Simulated)
	srv := &Server{
		Config: Config{PrivateKey: newkey(), MaxPeers: 10, NoDial: true, NoDiscovery: true, clock: clock},
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	id := enode.ID{1}
	srv.JailPeerFor(id, 0)
	srv.JailPeerFor(id, -time.Second)
	if srv.peerJail.IsJailed(id) {
		t.Fatal("peer jailed for a non-positive period")
	}
}

func TestPeerJailCapacity(t *testing.T) {
	clock := new(mclock.Simulated)
	jail := newPeerJail(5*time.Minute, clock)
	for i := range maxPeerJailEntries {
		var id enode.ID
		binary.BigEndian.PutUint64(id[:], uint64(i))
		period := 5 * time.Minute
		if i < 2 {
			period = time.Duration(i+1) * time.Minute
		}
		jail.JailPeerFor(id, period)
	}
	jail.JailPeer(enode.ID{})
	clock.Run(30 * time.Second)
	var replacement enode.ID
	binary.BigEndian.PutUint64(replacement[:], maxPeerJailEntries)
	jail.JailPeerFor(replacement, 2*time.Minute)
	if len(jail.jailed) != maxPeerJailEntries {
		t.Fatalf("jail size: got %d, want %d", len(jail.jailed), maxPeerJailEntries)
	}
	for i := range maxPeerJailEntries {
		var id enode.ID
		binary.BigEndian.PutUint64(id[:], uint64(i))
		if jail.IsJailed(id) != (i != 1) {
			t.Fatalf("incorrect retention for peer %d", i)
		}
	}
	if !jail.IsJailed(replacement) {
		t.Fatal("replacement peer was not jailed")
	}
	clock.Run(2 * time.Minute)
	if !jail.IsJailed(replacement) {
		t.Fatal("replacement jail expired early")
	}
	clock.Run(time.Nanosecond)
	if jail.IsJailed(replacement) {
		t.Fatal("replacement jail did not expire")
	}
}

func TestPeerJailReclaimsExpiredCapacity(t *testing.T) {
	for _, expired := range []int{maxPeerJailEntries / 2, maxPeerJailEntries} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			clock := new(mclock.Simulated)
			jail := newPeerJail(2*time.Minute, clock)
			for i := range maxPeerJailEntries {
				var id enode.ID
				binary.BigEndian.PutUint64(id[:], uint64(i))
				period := 2 * time.Minute
				if i >= expired {
					period = 5 * time.Minute
				}
				jail.JailPeerFor(id, period)
			}
			clock.Run(2*time.Minute + time.Nanosecond)
			var replacement enode.ID
			binary.BigEndian.PutUint64(replacement[:], maxPeerJailEntries)
			jail.JailPeer(replacement)
			if want := maxPeerJailEntries - expired + 1; len(jail.jailed) != want {
				t.Fatalf("jail size after expiry: got %d, want %d", len(jail.jailed), want)
			}
			for i := expired; i < maxPeerJailEntries; i++ {
				var id enode.ID
				binary.BigEndian.PutUint64(id[:], uint64(i))
				if !jail.IsJailed(id) {
					t.Fatalf("active peer %d was evicted despite expired capacity", i)
				}
			}
			if !jail.IsJailed(replacement) {
				t.Fatal("replacement did not use reclaimed capacity")
			}
		})
	}
}

func TestPeerIdleOperatorOverride(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(fmt.Sprintf("trusted=%v", trusted), func(t *testing.T) {
			srv, peers, clock := activityTestServer(t, 15, 15)
			srv.PrivateKey, srv.NoDial, srv.NoDiscovery = newkey(), true, true
			srv.PeerIdleReconnectCooldown = 24 * time.Hour
			if err := srv.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(srv.Stop)
			peer := peers[uintID(1)]
			peer.rw.set(trustedConn, false)
			clock.Run(peerIdleTimeout)
			srv.doPeerOp(func(map[enode.ID]*Peer) { srv.expireIdlePeer(peers) })
			if !srv.peerJail.IsJailed(peer.ID()) {
				t.Fatal("silent peer was not placed in cooldown")
			}
			node := newNode(peer.ID(), "127.0.0.1:30303")
			if trusted {
				srv.AddTrustedPeer(node)
			} else {
				srv.AddPeer(node)
			}
			srv.doPeerOp(func(map[enode.ID]*Peer) {})
			if srv.peerJail.IsJailed(peer.ID()) {
				t.Fatal("operator peering did not clear the idle cooldown")
			}
			dial := &dialScheduler{dialConfig: dialConfig{jailedUntil: srv.peerJail.JailedUntil}}
			if err := dial.checkDial(node); err != nil {
				t.Fatalf("manual outbound dial: %v", err)
			}
			if err := srv.postHandshakeChecks(nil, 0, &conn{node: node, flags: inboundConn}); err != nil {
				t.Fatalf("manual inbound connection: %v", err)
			}
		})
	}
}

func TestPeerJailIdleOverlap(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		idle, penalty, elapsed time.Duration
	}{
		{"idle-only", 24 * time.Hour, 0, 0},
		{"penalty-only", 0, time.Hour, 0},
		{"longer-idle", 24 * time.Hour, time.Hour, 0},
		{"longer-penalty", time.Minute, time.Hour, 0},
		{"expired-penalty", 24 * time.Hour, time.Minute, 2 * time.Minute},
		{"expired-idle", time.Minute, time.Hour, 2 * time.Minute},
		{"boundary", 24 * time.Hour, time.Minute, time.Minute},
		{"unknown", 0, 0, 0},
	} {
		for _, idleFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/idle-first=%v", tc.name, idleFirst), func(t *testing.T) {
				clock := new(mclock.Simulated)
				jail := newPeerJail(time.Minute, clock)
				id := enode.ID{1}
				if idleFirst && tc.idle > 0 {
					jail.cooldownPeer(id, tc.idle)
				}
				if tc.penalty > 0 {
					jail.JailPeerFor(id, tc.penalty)
				}
				if !idleFirst && tc.idle > 0 {
					jail.cooldownPeer(id, tc.idle)
				}
				until, jailed := jail.JailedUntil(id)
				if until != mclock.AbsTime(max(tc.idle, tc.penalty)) || jailed != (tc.idle > 0 || tc.penalty > 0) {
					t.Fatalf("combined expiry=%v jailed=%v", until, jailed)
				}
				clock.Run(tc.elapsed)
				jail.clearIdleCooldown(id)
				until, jailed = jail.JailedUntil(id)
				want := tc.penalty > 0 && tc.elapsed <= tc.penalty
				if jailed != want || (want && until != mclock.AbsTime(tc.penalty)) {
					t.Fatalf("remaining expiry=%v jailed=%v want penalty=%v", until, jailed, tc.penalty)
				}
			})
		}
	}
}

func TestPeerJailConcurrentIdleOverride(t *testing.T) {
	jail := newPeerJail(time.Hour, new(mclock.Simulated))
	id := enode.ID{1}
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 1000 {
			jail.JailPeer(id)
		}
	})
	wg.Go(func() {
		for range 1000 {
			jail.cooldownPeer(id, 24*time.Hour)
			jail.JailedUntil(id)
		}
	})
	for range 1000 {
		jail.clearIdleCooldown(id)
	}
	wg.Wait()
	jail.clearIdleCooldown(id)
	if until, jailed := jail.JailedUntil(id); !jailed || until != mclock.AbsTime(time.Hour) {
		t.Fatalf("operator override erased or extended the penalty: until=%v jailed=%v", until, jailed)
	}
}

func TestPeerJailIdleCapacity(t *testing.T) {
	clock := new(mclock.Simulated)
	jail := newPeerJail(time.Minute, clock)
	for i := range maxPeerJailEntries {
		jail.cooldownPeer(uintID(uint16(i)), time.Hour)
	}
	jail.JailPeer(uintID(0))
	jail.clearIdleCooldown(uintID(0))
	jail.cooldownPeer(uintID(maxPeerJailEntries), 24*time.Hour)
	if len(jail.jailed) != maxPeerJailEntries || jail.IsJailed(uintID(0)) {
		t.Fatal("mixed jail did not evict the earliest expiry at capacity")
	}
	jail.cooldownPeer(uintID(1), 24*time.Hour)
	jail.cooldownPeer(uintID(1), time.Minute)
	if until, jailed := jail.JailedUntil(uintID(1)); !jailed || until != mclock.AbsTime(24*time.Hour) {
		t.Fatalf("shortened repeated cooldown: until=%v jailed=%v", until, jailed)
	}
	clock.Run(time.Hour + time.Nanosecond)
	jail.cooldownPeer(uintID(maxPeerJailEntries+1), time.Minute)
	if len(jail.jailed) != 3 {
		t.Fatalf("expired idle entries retained: %d", len(jail.jailed))
	}
}
