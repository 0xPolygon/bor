package p2p

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/p2p/enr"
)

func shortenBulkFallbackGrace(t *testing.T) {
	t.Helper()
	prev := bulkFallbackGrace
	bulkFallbackGrace = 150 * time.Millisecond
	t.Cleanup(func() { bulkFallbackGrace = prev })
}

func bulkFallbackServers(t *testing.T, primaryLowerID bool) (primary, fallback *testBulkServer, primaryPeer, fallbackPeer *Peer) {
	t.Helper()
	primary, fallback = newTestBulkServer(t), newTestBulkServer(t)
	t.Cleanup(primary.close)
	t.Cleanup(fallback.close)
	if (bytes.Compare(primary.bulk.localID[:], fallback.bulk.localID[:]) < 0) != primaryLowerID {
		primary, fallback = fallback, primary
	}
	// Only the primary listens for QUIC; the fallback must dial it.
	primary.setQUICPort()

	// The primary knows the fallback's identity but not its QUIC endpoint.
	fallbackNode := fallback.localnode.Node()
	identityOnly := enode.NewV4(fallbackNode.Pubkey(), nil, 0, 0)
	_, hasQUIC := identityOnly.QUICEndpoint()
	require.False(t, hasQUIC, "primary must not know the fallback endpoint")
	primaryPeer = newTestTrackedPeer(identityOnly)
	primaryPeer.rw.flags = dynDialedConn

	// The fallback holds the primary's full record, including its QUIC endpoint.
	fallbackPeer = newTestTrackedPeer(primary.localnode.Node())
	fallbackPeer.rw.flags = inboundConn
	_, hasQUIC = fallbackPeer.Node().QUICEndpoint()
	require.True(t, hasQUIC, "fallback must know the primary endpoint")

	primary.setPeer(primaryPeer)
	fallback.setPeer(fallbackPeer)
	return primary, fallback, primaryPeer, fallbackPeer
}

func openBulkLanes(t *testing.T, ctx context.Context, primary, fallback *testBulkServer, primaryPeer, fallbackPeer *Peer, lanes []string) {
	t.Helper()
	opened := make(chan error, 2*len(lanes))
	for _, lane := range lanes {
		go func() {
			_, err := primary.bulk.OpenChannelContext(ctx, primaryPeer, lane)
			opened <- err
		}()
		go func() {
			_, err := fallback.bulk.OpenChannelContext(ctx, fallbackPeer, lane)
			opened <- err
		}()
	}
	for range cap(opened) {
		require.NoError(t, <-opened)
	}
}

func TestBulkFallbackOnlyAcceptorHasEndpoint(t *testing.T) {
	for _, primaryLowerID := range []bool{false, true} {
		name := "primaryHigherID"
		if primaryLowerID {
			name = "primaryLowerID"
		}
		t.Run(name, func(t *testing.T) {
			shortenBulkFallbackGrace(t)
			primary, fallback, primaryPeer, fallbackPeer := bulkFallbackServers(t, primaryLowerID)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			// First wave establishes the connection via the fallback dial.
			openBulkLanes(t, ctx, primary, fallback, primaryPeer, fallbackPeer, []string{"eth-control"})
			require.Equal(t, 1, primary.bulk.Status().ActiveSessions)
			require.Equal(t, 1, fallback.bulk.Status().ActiveSessions)

			// Remaining lanes join after the fallback connection is admitted.
			rest := make([]string, 0, len(bulkChannels)-1)
			for lane := range bulkChannels {
				if lane != "eth-control" {
					rest = append(rest, lane)
				}
			}
			openBulkLanes(t, ctx, primary, fallback, primaryPeer, fallbackPeer, rest)

			for _, server := range []*testBulkServer{primary, fallback} {
				status := server.bulk.Status()
				require.Equal(t, 1, status.ActiveSessions)
				require.Equal(t, len(bulkChannels), status.ActiveChannels)
			}
			assertBulkDirectionTraffic(t,
				primary.bulk.peerSession(primaryPeer.Node(), primaryPeer),
				fallback.bulk.peerSession(fallbackPeer.Node(), fallbackPeer))
		})
	}
}

func TestBulkPrimaryRefusesFallbackWhileDialing(t *testing.T) {
	primary, fallback := newTestBulkServer(t), newTestBulkServer(t)
	t.Cleanup(primary.close)
	t.Cleanup(fallback.close)
	primary.setQUICPort()
	fallback.setQUICPort()
	primaryPeer := newTestTrackedPeer(fallback.localnode.Node())
	primaryPeer.rw.flags = dynDialedConn
	fallbackPeer := newTestTrackedPeer(primary.localnode.Node())
	fallbackPeer.rw.flags = inboundConn
	primary.setPeer(primaryPeer)
	fallback.setPeer(fallbackPeer)

	session := primary.bulk.peerSession(primaryPeer.Node(), primaryPeer)
	require.True(t, session.shouldDial(primaryPeer), "primary must be the authority")

	// Pin the primary mid preferred-dial and dial it from the fallback.
	session.lock.Lock()
	session.preferredDialing = true
	session.lock.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := fallback.bulk.dialConn(ctx, primary.localnode.Node())
	require.ErrorIs(t, err, errBulkAdmissionRefused, "a fallback dial must be refused while dialing preferred")
	require.Zero(t, primary.bulk.Status().ActiveSessions, "the refused fallback must not be committed")
	require.False(t, primary.bulk.leases.holds(fallback.localnode.ID()), "no lease for a refused connection")

	// With the preferred dial settled, the same fallback dial is now admitted.
	session.lock.Lock()
	session.preferredDialing = false
	session.lock.Unlock()
	conn, err := fallback.bulk.dialConn(ctx, primary.localnode.Node())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseWithError(0, "test complete") })
	require.Eventually(t, func() bool {
		return primary.bulk.leases.holds(fallback.localnode.ID())
	}, time.Second, time.Millisecond, "the accepted fallback must hold the lease")
}

func TestBulkOverlappingPreferredDialWins(t *testing.T) {
	for _, primaryLowerID := range []bool{false, true} {
		name := "primaryHigherID"
		if primaryLowerID {
			name = "primaryLowerID"
		}
		t.Run(name, func(t *testing.T) {
			shortenBulkFallbackGrace(t)
			primary, fallback := newTestBulkServer(t), newTestBulkServer(t)
			t.Cleanup(primary.close)
			t.Cleanup(fallback.close)
			if (bytes.Compare(primary.bulk.localID[:], fallback.bulk.localID[:]) < 0) != primaryLowerID {
				primary, fallback = fallback, primary
			}
			fallbackArrived, allowFallback := make(chan struct{}), make(chan struct{})
			arrived := sync.OnceFunc(func() { close(fallbackArrived) })
			allow := sync.OnceFunc(func() { close(allowFallback) })
			t.Cleanup(allow)
			startBulkDialTestListener(t, primary, func(*quic.ClientInfo) (*quic.Config, error) {
				arrived()
				<-allowFallback
				return nil, nil
			})

			// Gate the primary's preferred dial so the fallback dials first.
			release := make(chan struct{})
			releaseOnce := func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}
			t.Cleanup(releaseOnce)
			startBulkDialTestListener(t, fallback, func(*quic.ClientInfo) (*quic.Config, error) {
				<-release
				return nil, nil
			})
			fallback.localnode.Set(enr.TCP(0)) // bump seq so the gated QUIC record is picked up

			primaryPeer := newTestTrackedPeer(fallback.localnode.Node())
			primaryPeer.rw.flags = dynDialedConn
			fallbackPeer := newTestTrackedPeer(primary.localnode.Node())
			fallbackPeer.rw.flags = inboundConn
			primary.setPeer(primaryPeer)
			fallback.setPeer(fallbackPeer)

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var workers sync.WaitGroup
			t.Cleanup(func() { cancel(); allow(); releaseOnce(); workers.Wait() })
			type laneResult struct {
				name string
				side int
				rw   MsgReadWriter
				err  error
			}
			opened := make(chan laneResult, 2*len(bulkChannels))
			open := func(side int, server *testBulkServer, peer *Peer) {
				for lane := range bulkChannels {
					workers.Add(1)
					go func() {
						defer workers.Done()
						rw, err := server.bulk.OpenChannelContext(ctx, peer, lane)
						opened <- laneResult{lane, side, rw, err}
					}()
				}
			}
			open(0, primary, primaryPeer)
			primarySession := primary.bulk.peerSession(primaryPeer.Node(), primaryPeer)
			require.Eventually(t, func() bool {
				primarySession.lock.Lock()
				defer primarySession.lock.Unlock()
				return primarySession.preferredDialing
			}, 2*time.Second, time.Millisecond, "primary must be mid preferred dial")
			open(1, fallback, fallbackPeer)

			// Let the fallback complete its refused handshake before releasing the preferred dial.
			fallbackSession := fallback.bulk.peerSession(fallbackPeer.Node(), fallbackPeer)
			select {
			case <-fallbackArrived:
			case <-ctx.Done():
				t.Fatal("fallback handshake did not arrive")
			}
			fallbackSession.lock.Lock()
			attemptDone := fallbackSession.dialWait
			fallbackSession.lock.Unlock()
			require.NotNil(t, attemptDone)
			allow()
			select {
			case <-attemptDone:
			case <-ctx.Done():
				t.Fatal("fallback handshake did not finish")
			}
			require.Zero(t, primary.bulk.Status().ActiveSessions)
			require.Zero(t, fallback.bulk.Status().ActiveSessions)
			releaseOnce()

			lanes := make(map[string][2]MsgReadWriter)
			for range cap(opened) {
				select {
				case result := <-opened:
					require.NoError(t, result.err, "side %d lane %s", result.side, result.name)
					pair := lanes[result.name]
					pair[result.side] = result.rw
					lanes[result.name] = pair
				case <-ctx.Done():
					t.Fatal("lanes did not open after the preferred dial won")
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

func TestBulkDuplicateConnectionRefused(t *testing.T) {
	shortenBulkFallbackGrace(t)
	primary, fallback, primaryPeer, fallbackPeer := bulkFallbackServers(t, true)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	// Establish the committed connection and all lanes via the fallback dial.
	allLanes := make([]string, 0, len(bulkChannels))
	for lane := range bulkChannels {
		allLanes = append(allLanes, lane)
	}
	openBulkLanes(t, ctx, primary, fallback, primaryPeer, fallbackPeer, allLanes)
	require.Eventually(t, func() bool {
		return primary.bulk.leases.holds(fallback.localnode.ID())
	}, time.Second, time.Millisecond)

	// A second inbound connection for the same identity is refused.
	dup, err := quic.DialAddr(ctx, primary.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dup.CloseWithError(0, "test complete") })
	err = fallback.bulk.initiateAuth(dup, primary.localnode.Node())
	require.ErrorIs(t, err, errBulkAdmissionRefused, "a duplicate connection must be refused")
	require.Equal(t, 1, primary.bulk.Status().ActiveSessions)
	require.Equal(t, len(bulkChannels), primary.bulk.Status().ActiveChannels, "the committed lanes must be undisturbed")
}
