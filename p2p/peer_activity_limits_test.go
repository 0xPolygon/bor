package p2p

import (
	"strconv"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/peerpolicy"
)

func TestPeerIdlePendingSlots(t *testing.T) {
	for _, live := range []int{5, 10} {
		t.Run(strconv.Itoa(live), func(t *testing.T) {
			srv, peers, clock := activityTestServer(t, 20, 20)
			srv.NoDial = true
			for i := live + 1; i <= 20; i++ {
				peers[uintID(uint16(i))].activity = new(peerpolicy.ConnectionActivity)
			}
			target := peers[uintID(1)]
			target.rw.set(trustedConn, false)
			clock.Run(peerIdleTimeout)
			srv.expireIdlePeer(peers)
			if got := len(target.disc) != 0; got != (live > peerIdleMinimumPeers) {
				t.Fatalf("rotation=%v with %d live and %d starting peers", got, live, 20-live)
			}
		})
	}
}

func TestPeerIdleRuntimeLimits(t *testing.T) {
	srv, peers, clock := activityTestServer(t, 16, 0)
	srv.MaxPeers = 50
	srv.PrivateKey = newkey()
	srv.NoDiscovery = true
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	srv.SetMaxPeers(100)
	if got := srv.dialsched.maxDialPeers; got != 16 {
		t.Fatalf("dial capacity = %d", got)
	}
	if got := srv.MaxInboundConns(); got != 67 {
		t.Fatalf("inbound limit = %d", got)
	}
	target := peers[uintID(16)]
	target.rw.set(trustedConn, false)
	clock.Run(peerIdleTimeout)
	srv.doPeerOp(func(map[enode.ID]*Peer) { srv.expireIdlePeer(peers) })
	if len(target.disc) != 1 {
		t.Fatal("full startup dial capacity did not trigger rotation")
	}
}

func TestPeerLimitsConcurrentUpdate(t *testing.T) {
	srv := &Server{Config: Config{PrivateKey: newkey(), MaxPeers: 50, NoDiscovery: true}}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 200 {
			srv.SetMaxPeers(50 + i%2*50)
		}
	})
	for range 200 {
		total, dialed := srv.peerLimits()
		if dialed != total/3 {
			t.Fatalf("inconsistent limits total=%d dialed=%d", total, dialed)
		}
		srv.MaxInboundConns()
		srv.MaxDialedConns()
		inbound, outbound := srv.peerPressure(16, 0)
		if inbound || !outbound {
			t.Fatalf("pressure inbound=%v outbound=%v", inbound, outbound)
		}
	}
	wg.Wait()
}
