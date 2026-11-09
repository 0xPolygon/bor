package p2p

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enr"
)

func TestBulkFallbackAfterPreferredTimeout(t *testing.T) {
	for _, lower := range []bool{false, true} {
		name := "primaryHigherID"
		if lower {
			name = "primaryLowerID"
		}
		t.Run(name, func(t *testing.T) {
			primary, fallback, primaryPeer, fallbackPeer := bulkFallbackServers(t, lower)
			blackhole, err := net.ListenPacket("udp", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, blackhole.Close()) })
			// A bound UDP port avoids an immediate ICMP refusal and forces real dial timeouts.
			fallback.localnode.Set(enr.QUIC(blackhole.LocalAddr().(*net.UDPAddr).Port))
			primaryPeer.rw.node = fallback.localnode.Node()
			_, hasEndpoint := primaryPeer.Node().QUICEndpoint()
			require.True(t, hasEndpoint)
			primary.bulk.config = primary.bulk.config.Clone()
			primary.bulk.config.HandshakeIdleTimeout = time.Second
			previousGrace := bulkFallbackGrace
			bulkFallbackGrace = 0
			t.Cleanup(func() { bulkFallbackGrace = previousGrace })

			ctx, cancel := context.WithTimeout(t.Context(), bulkConnectionSetupTimeout+bulkChannelOpenTimeout)
			defer cancel()
			var workers sync.WaitGroup
			t.Cleanup(func() { cancel(); workers.Wait() })
			type openedLane struct {
				name string
				side int
				rw   MsgReadWriter
				err  error
			}
			opened := make(chan openedLane, 2*len(bulkChannels))
			open := func(side int, server *testBulkServer, peer *Peer) {
				for lane := range bulkChannels {
					workers.Add(1)
					go func() {
						defer workers.Done()
						rw, err := server.bulk.OpenChannelContext(ctx, peer, lane)
						opened <- openedLane{lane, side, rw, err}
					}()
				}
			}
			session := primary.bulk.peerSession(primaryPeer.Node(), primaryPeer)
			open(0, primary, primaryPeer)
			require.Eventually(t, func() bool {
				session.lock.Lock()
				defer session.lock.Unlock()
				return session.preferredDialing
			}, time.Second, time.Millisecond)
			open(1, fallback, fallbackPeer)

			lanes := make(map[string][2]MsgReadWriter)
			for range cap(opened) {
				select {
				case result := <-opened:
					require.NoError(t, result.err, "side %d lane %s", result.side, result.name)
					pair := lanes[result.name]
					pair[result.side] = result.rw
					lanes[result.name] = pair
				case <-ctx.Done():
					t.Fatal("reverse setup did not recover after the preferred dial failed")
				}
			}
			for _, pair := range lanes {
				assertBulkLiveTraffic(t, pair)
			}
			for _, server := range []*testBulkServer{primary, fallback} {
				require.Equal(t, 1, server.bulk.Status().ActiveSessions)
				require.Equal(t, len(bulkChannels), server.bulk.Status().ActiveChannels)
			}
		})
	}
}
