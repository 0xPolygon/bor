package p2p

import (
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/mclock"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func requestActivityProtocol(version uint, received chan<- enode.ID, policy *peerpolicy.Tracker) Protocol {
	return Protocol{Name: "eth", Version: version, Length: 18, CanContribute: func(code uint64) bool { return code == 7 }, Run: func(peer *Peer, rw MsgReadWriter) error {
		policy.BindConnection(peer.ID().String(), policy.Connected(peer.ID().String()), peer.activity)
		for {
			msg, err := rw.ReadMsg()
			if err != nil {
				return err
			}
			if err := msg.Discard(); err != nil {
				return err
			}
			if err := Send(rw, 4, []uint64{}); err != nil {
				return err
			}
			policy.Observe(peer.ID().String(), peerpolicy.Evidence{Activity: peerpolicy.ActivityEvent{
				ReceivedMessages: 1, ReceivedRequests: 1, ServedResponses: 1, MessageSequence: msg.ReceivedSequence,
			}})
			received <- peer.ID()
		}
	}}
}

func exchangeActivityRequest(t *testing.T, client transport, received <-chan enode.ID) {
	t.Helper()
	if err := Send(client, baseProtocolLength+3, []uint64{}); err != nil {
		t.Fatal(err)
	}
	msg, err := client.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	if err := msg.Discard(); err != nil {
		t.Fatal(err)
	}
	if msg.Code != baseProtocolLength+4 {
		t.Fatalf("unexpected reply: %d", msg.Code)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("request was not observed")
	}
}

func TestRequestOnlyCapacityRLPX(t *testing.T) {
	for _, tc := range []struct {
		name         string
		peers, batch int
	}{{"default", 9, 1}, {"batch", 100, 16}} {
		t.Run(tc.name, func(t *testing.T) { testRequestOnlyCapacityRLPX(t, tc.peers, tc.batch) })
	}
}

func testRequestOnlyCapacityRLPX(t *testing.T, count, batch int) {
	t.Helper()
	clock := new(mclock.Simulated)
	policy := peerpolicy.New(clock)
	policy.SetReady(true)
	received := make(chan enode.ID, count)
	srv := &Server{Config: Config{
		PrivateKey: newkey(), MaxPeers: count, NoDial: true, NoDiscovery: true,
		ListenAddr: "127.0.0.1:0", PeerIdleEviction: true, PeerIdleMaxRemovals: batch, clock: clock,
		Protocols: []Protocol{requestActivityProtocol(68, received, policy), requestActivityProtocol(69, received, policy)},
	}}
	srv.SetPeerPolicy(policy)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	clients := make([]transport, count)
	for i := range clients {
		var err error
		clients[i], err = connectActivityPeer(t, srv, newkey(), 68+uint(i%2))
		if err != nil {
			t.Fatal(err)
		}
	}
	waitActivityPeers(t, srv, count)
	for range 44 {
		advanceActivityClock(t, srv, clock, 15*time.Second)
		for _, client := range clients {
			exchangeActivityRequest(t, client, received)
		}
	}
	waitActivityPeers(t, srv, count)
	checkRequestOnlyCapacityRLPX(t, srv, clock, count, batch)
}

func checkRequestOnlyCapacityRLPX(t *testing.T, srv *Server, clock *mclock.Simulated, count, batch int) {
	t.Helper()
	advanceActivityClock(t, srv, clock, time.Second)
	if _, err := connectActivityPeer(t, srv, newkey(), 69); !errors.Is(err, DiscTooManyPeers) {
		t.Fatalf("expected refusal, got %v", err)
	}
	advanceActivityClock(t, srv, clock, 50*time.Second)
	original := srv.Peers()
	for _, peer := range original[:batch] {
		for range 3 {
			srv.peerPolicy.Load().Observe(peer.ID().String(), peerpolicy.Evidence{Refused: true})
		}
	}
	advanceActivityClock(t, srv, clock, 9*time.Second)
	waitActivityPeers(t, srv, count-batch)
	for i, peer := range original {
		if srv.peerJail.IsJailed(peer.ID()) != (i < batch) {
			t.Fatalf("unexpected rotation decision for peer %s", peer.ID())
		}
	}
	for range batch {
		if _, err := connectActivityPeer(t, srv, newkey(), 68); err != nil {
			t.Fatal(err)
		}
	}
	waitActivityPeers(t, srv, count)
	before := requestOnlyPeerDisconnects.Snapshot().Count()
	advanceActivityClock(t, srv, clock, time.Second)
	if _, err := connectActivityPeer(t, srv, newkey(), 69); !errors.Is(err, DiscTooManyPeers) {
		t.Fatalf("expected second refusal, got %v", err)
	}
	advanceActivityClock(t, srv, clock, 50*time.Second)
	srv.peerPolicy.Load().Observe(original[batch].ID().String(), peerpolicy.Evidence{Reason: peerpolicy.ServingVolume})
	advanceActivityClock(t, srv, clock, 9*time.Second)
	if requestOnlyPeerDisconnects.Snapshot().Count() != before {
		t.Fatal("repeated refusal bypassed the rotation interval")
	}
	waitActivityPeers(t, srv, count)
}
