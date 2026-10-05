package p2p

import (
	"encoding/binary"
	"errors"
	"fmt"
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
	dial := &dialScheduler{dialConfig: dialConfig{jailChecker: srv.peerJail.IsJailed}}
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
