package p2p

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enode"
)

func TestBulkAdmissionPendingLimit(t *testing.T) {
	for _, limit := range []int{0, -1, 2} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			admit := newBulkConnContext(limit)
			if limit <= 0 {
				limit = defaultMaxPendingPeers
			}
			for i := range limit {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				_, err := admit(ctx, bulkTestClientInfo(byte(i+1)))
				require.NoError(t, err)
			}
			_, err := admit(context.Background(), bulkTestClientInfo(1))
			require.ErrorIs(t, err, errBulkAdmissionLimit)
		})
	}
}

func TestBulkAdmissionReleasesOnClose(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "authenticated"}[authenticated], func(t *testing.T) {
			admit := newBulkConnContext(1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx, err := admit(ctx, bulkTestClientInfo(1))
			require.NoError(t, err)
			if authenticated {
				releaseBulkPendingAuth(ctx)
			}
			cancel()
			next, nextCancel := context.WithCancel(context.Background())
			defer nextCancel()
			require.Eventually(t, func() bool {
				_, err := admit(next, bulkTestClientInfo(1))
				return err == nil
			}, time.Second, time.Millisecond)
		})
	}
	releaseBulkPendingAuth(context.Background())
}

func TestBulkSidecarAdmission(t *testing.T) {
	server := newTestBulkServerWithConfig(t, Config{MaxPeers: 1, MaxPendingPeers: 1})
	defer server.close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, first.CloseWithError(0, "test complete")) })

	second, err := quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
	if second != nil {
		require.NoError(t, second.CloseWithError(0, "test complete"))
	}
	var transportErr *quic.TransportError
	require.ErrorAs(t, err, &transportErr)
	require.Equal(t, quic.ConnectionRefused, transportErr.ErrorCode)

	stream, err := first.OpenStreamSync(ctx)
	require.NoError(t, err)
	require.NoError(t, writeBulkControl(stream, bulkAuthHello{Version: bulkSidecarVersion, To: server.bulk.localID}))
	select {
	case <-first.Context().Done():
	case <-ctx.Done():
		t.Fatal("authentication failure did not close connection")
	}
	var next *quic.Conn
	require.Eventually(t, func() bool {
		next, err = quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
		return err == nil
	}, time.Second, time.Millisecond)
	require.NoError(t, next.CloseWithError(0, "test complete"))
}

func TestBulkSidecarAuthenticatedAdmission(t *testing.T) {
	server := newTestBulkServerWithConfig(t, Config{MaxPeers: 1, MaxPendingPeers: 1})
	defer server.close()
	client := newTestBulkServer(t)
	defer client.close()
	server.setPeer(newTestTrackedPeer(client.localnode.Node()))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
	require.NoError(t, err)
	defer first.CloseWithError(0, "test complete")
	require.NoError(t, client.bulk.initiateAuth(first, server.localnode.Node()))
	var second *quic.Conn
	require.Eventually(t, func() bool {
		second, err = quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
		return err == nil
	}, time.Second, time.Millisecond)
	defer second.CloseWithError(0, "test complete")
	third, err := quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
	if third != nil {
		require.NoError(t, third.CloseWithError(0, "test complete"))
	}
	var transportErr *quic.TransportError
	require.ErrorAs(t, err, &transportErr)
	require.Equal(t, quic.ConnectionRefused, transportErr.ErrorCode)
}

func bulkTestClientInfo(ip byte) *quic.ClientInfo {
	return &quic.ClientInfo{RemoteAddr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, ip), Port: 30303}}
}

// The sidecar admits one connection per live RLPx identity, both directions. A
// second connection for an identity that already holds a lease is rejected, and
// closing the first frees the lease for a reconnect.
func TestBulkSidecarAuthenticatedPeerLimit(t *testing.T) {
	for _, outgoing := range []bool{false, true} {
		t.Run(map[bool]string{false: "incoming", true: "outgoing"}[outgoing], func(t *testing.T) {
			server := newTestBulkServerWithConfig(t, Config{MaxPeers: 1, MaxPendingPeers: 2})
			t.Cleanup(server.close)
			server.setQUICPort()
			client := newTestBulkServer(t)
			t.Cleanup(client.close)
			client.setQUICPort()
			serverPeer := newTestTrackedPeer(client.localnode.Node())
			clientPeer := newTestTrackedPeer(server.localnode.Node())
			serverPeer.rw.flags, clientPeer.rw.flags = inboundConn, dynDialedConn
			if outgoing {
				serverPeer.rw.flags, clientPeer.rw.flags = dynDialedConn, inboundConn
			}
			server.setPeer(serverPeer)
			client.setPeer(clientPeer)
			left, right := openTestBulkChannel(t, server, client, serverPeer, clientPeer, "eth-bulk")
			session := server.bulk.peerSession(serverPeer.Node(), serverPeer)
			session.lock.Lock()
			first := session.conn
			session.lock.Unlock()
			require.NotNil(t, first)
			require.True(t, server.bulk.leases.holds(client.localnode.ID()))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()

			dup, err := quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
			require.NoError(t, err)
			t.Cleanup(func() { dup.CloseWithError(0, "test complete") })
			require.ErrorIs(t, client.bulk.initiateAuth(dup, server.localnode.Node()), errBulkAdmissionRefused)
			require.NoError(t, dup.CloseWithError(0, "admission refused"))
			require.Equal(t, 1, server.bulk.leases.active())
			assertBulkLiveTraffic(t, [2]MsgReadWriter{left, right})

			require.NoError(t, first.CloseWithError(0, "release lease"))
			require.Eventually(t, func() bool {
				return !server.bulk.leases.holds(client.localnode.ID())
			}, time.Second, time.Millisecond)
			recovered, err := quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
			require.NoError(t, err)
			t.Cleanup(func() { recovered.CloseWithError(0, "test complete") })
			require.NoError(t, client.bulk.initiateAuth(recovered, server.localnode.Node()))
			require.Eventually(t, func() bool {
				return server.bulk.leases.holds(client.localnode.ID())
			}, time.Second, time.Millisecond)
		})
	}
}

// admitAuthenticatedConn is a lease: one per identity, distinct identities admit
// independently, a cancelled context is refused, and release restores capacity.
func TestBulkAuthenticatedAdmissionCapacity(t *testing.T) {
	server := newTestBulkServer(t)
	t.Cleanup(server.close)
	admit := newBulkConnContext(2)

	ctxA, cancelA := context.WithCancel(t.Context())
	t.Cleanup(cancelA)
	pendingA, err := admit(ctxA, bulkTestClientInfo(1))
	require.NoError(t, err)
	idA, idB := enode.ID{0xa}, enode.ID{0xb}
	require.NoError(t, server.bulk.admitAuthenticatedConn(pendingA, idA))
	require.True(t, server.bulk.leases.holds(idA))

	// A second connection for the same identity is refused.
	ctxDup, cancelDup := context.WithCancel(t.Context())
	t.Cleanup(cancelDup)
	pendingDup, err := admit(ctxDup, bulkTestClientInfo(1))
	require.NoError(t, err, "an admitted connection must not hold a pending slot")
	require.ErrorIs(t, server.bulk.admitAuthenticatedConn(pendingDup, idA), errBulkAdmissionLimit)

	// A distinct identity is admitted independently; no fixed numeric cap.
	ctxB, cancelB := context.WithCancel(t.Context())
	t.Cleanup(cancelB)
	pendingB, err := admit(ctxB, bulkTestClientInfo(2))
	require.NoError(t, err)
	require.NoError(t, server.bulk.admitAuthenticatedConn(pendingB, idB))

	// Releasing idA (context cancel) frees its lease for a reconnect.
	cancelA()
	require.Eventually(t, func() bool { return !server.bulk.leases.holds(idA) }, time.Second, time.Millisecond)
	ctxReuse, cancelReuse := context.WithCancel(t.Context())
	t.Cleanup(cancelReuse)
	pendingReuse, err := admit(ctxReuse, bulkTestClientInfo(1))
	require.NoError(t, err)
	require.NoError(t, server.bulk.admitAuthenticatedConn(pendingReuse, idA))

	// A cancelled context is refused without taking a lease.
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, server.bulk.admitAuthenticatedConn(cancelled, enode.ID{0xc}), context.Canceled)
	require.False(t, server.bulk.leases.holds(enode.ID{0xc}))
}

func TestBulkSidecarOutboundAdmissionLimit(t *testing.T) {
	server := newTestBulkServerWithConfig(t, Config{MaxPeers: 1, MaxPendingPeers: 1})
	t.Cleanup(server.close)
	firstRemote := newTestBulkServer(t)
	t.Cleanup(firstRemote.close)
	firstRemote.setQUICPort()
	serverPeer := newTestTrackedPeer(firstRemote.localnode.Node())
	serverPeer.rw.flags = dynDialedConn
	remotePeer := newTestTrackedPeer(server.localnode.Node())
	remotePeer.rw.flags = inboundConn
	server.setPeer(serverPeer)
	firstRemote.setPeer(remotePeer)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	first, err := server.bulk.peerSession(serverPeer.Node(), serverPeer).ensureConn(ctx)
	require.NoError(t, err)
	defer first.CloseWithError(0, "test complete")
	require.True(t, server.bulk.leases.holds(firstRemote.localnode.ID()))

	second, err := server.bulk.dialConn(ctx, firstRemote.localnode.Node())
	require.Error(t, err)
	require.Nil(t, second)
	require.NoError(t, first.Context().Err())
	require.Equal(t, 1, server.bulk.leases.active())
}
