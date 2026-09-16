package p2p

import (
	"encoding/binary"
	"errors"
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
	jail := newPeerJail(2*time.Minute, clock)
	var overflow enode.ID
	for i := 0; i <= maxPeerJailEntries; i++ {
		var id enode.ID
		binary.BigEndian.PutUint64(id[:], uint64(i+1))
		jail.JailPeer(id)
		overflow = id
	}
	if len(jail.jailed) != maxPeerJailEntries {
		t.Fatalf("jail size: got %d, want %d", len(jail.jailed), maxPeerJailEntries)
	}
	if want := mclock.AbsTime(2 * time.Minute); jail.nextExpiry != want {
		t.Fatalf("next expiry: got %v, want %v", jail.nextExpiry, want)
	}
	if jail.IsJailed(overflow) {
		t.Fatal("peer added beyond jail capacity")
	}

	clock.Run(2 * time.Minute)
	jail.JailPeer(overflow)
	if jail.IsJailed(overflow) {
		t.Fatal("peer added before existing jails expired")
	}

	clock.Run(time.Nanosecond)
	var id enode.ID
	binary.BigEndian.PutUint64(id[:], maxPeerJailEntries+2)
	jail.JailPeer(id)
	if len(jail.jailed) != 1 {
		t.Fatalf("jail size after expiry: got %d, want 1", len(jail.jailed))
	}
}
