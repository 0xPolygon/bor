package p2p

import (
	"math"
	"math/big"
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
	srv := &Server{Config: Config{PrivateKey: newkey(), MaxPeers: 50, NoDiscovery: true, PeerIdleEviction: true}}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	srv.SetPeerPolicy(peerpolicy.New(srv.clock))
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
		srv.doPeerOp(func(peers map[enode.ID]*Peer) { srv.expireIdlePeer(peers) })
		inbound, outbound := srv.peerPressure(16, 0)
		if inbound || !outbound {
			t.Fatalf("pressure inbound=%v outbound=%v", inbound, outbound)
		}
	}
	wg.Wait()
}

func TestPeerIdlePressurePercentages(t *testing.T) {
	for _, percent := range []int{1, 50, 75, 80, 99, 100} {
		for _, limit := range []int{1, 3, 4, 99, 100, 101, math.MaxInt - 1, math.MaxInt} {
			product := new(big.Int).Mul(big.NewInt(int64(limit)), big.NewInt(int64(percent)))
			product.Add(product, big.NewInt(99))
			threshold := int(product.Quo(product, big.NewInt(100)).Int64())
			if peerSlotsScarce(threshold-1, limit, percent) || !peerSlotsScarce(threshold, limit, percent) {
				t.Fatalf("incorrect boundary for limit=%d percent=%d", limit, percent)
			}
		}
	}
}

func TestPeerIdleConfiguredPressure(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		percent, count, inbound   int
		wantInbound, wantOutbound bool
	}{
		{"default", 0, 75, 50, true, true},
		{"default-below", 0, 74, 50, false, false},
		{"negative-native", -1, 75, 50, true, true},
		{"total", 80, 80, 54, true, true},
		{"below-total", 80, 79, 53, false, false},
		{"inbound", 80, 54, 54, true, false},
		{"below-inbound", 80, 53, 53, false, false},
		{"outbound", 80, 27, 0, false, true},
		{"below-outbound", 80, 26, 0, false, false},
		{"minimum", 1, 1, 0, true, true},
		{"maximum", 100, 100, 67, true, true},
		{"below-maximum", 100, 98, 66, false, false},
		{"bounded-native", 101, 100, 67, true, true},
		{"below-native", 101, 90, 60, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{Config: Config{MaxPeers: 100, PeerIdleOccupancyThreshold: tc.percent}}
			inbound, outbound := srv.peerPressure(tc.count, tc.inbound)
			if inbound != tc.wantInbound || outbound != tc.wantOutbound {
				t.Fatalf("inbound=%v outbound=%v", inbound, outbound)
			}
		})
	}
}

func TestPeerIdleBatchPressureThreshold(t *testing.T) {
	for _, tc := range []struct{ percent, remaining int }{
		{0, 10}, {90, 12}, {100, 13}, {50, 6}, {1, 5},
	} {
		t.Run(strconv.Itoa(tc.percent), func(t *testing.T) {
			srv, peers, clock := activityTestServer(t, 14, 14)
			srv.PeerIdleOccupancyThreshold, srv.PeerIdleMaxRemovals = tc.percent, 64
			for _, peer := range peers {
				peer.rw.set(trustedConn, false)
			}
			clock.Run(peerIdleTimeout)
			srv.expireIdlePeer(peers)
			_, _, live := peerCounts(peers)
			if live != tc.remaining {
				t.Fatalf("remaining=%d want=%d", live, tc.remaining)
			}
		})
	}
}
