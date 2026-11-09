package p2p

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestBulkAdmissionPendingLimit(t *testing.T) {
	for _, limit := range []int{0, -1, 2} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			admit := newBulkConnContext(3, limit, newBulkQUICWindows(3, limit))
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

func TestBulkAdmissionTransportLimit(t *testing.T) {
	for _, peers := range []int{-1, 0, 2} {
		t.Run(strconv.Itoa(peers), func(t *testing.T) {
			admit := newBulkConnContext(peers, 1, newBulkQUICWindows(peers, 1))
			for range max(0, peers) + 1 {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				ctx, err := admit(ctx, bulkTestClientInfo(1))
				require.NoError(t, err)
				releaseBulkPendingAuth(ctx)
				releaseBulkPendingAuth(ctx)
			}
			for range 2 {
				_, err := admit(context.Background(), bulkTestClientInfo(1))
				require.ErrorIs(t, err, errBulkAdmissionLimit)
			}
		})
	}
}

func TestBulkAdmissionReleasesOnClose(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "authenticated"}[authenticated], func(t *testing.T) {
			admit := newBulkConnContext(0, 1, newBulkQUICWindows(0, 1))
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

func TestBulkSidecarAuthenticatedPeerLimit(t *testing.T) {
	for _, outgoing := range []bool{false, true} {
		t.Run(map[bool]string{false: "incoming", true: "outgoing"}[outgoing], func(t *testing.T) {
			server := newTestBulkServerWithConfig(t, Config{MaxPeers: 1, MaxPendingPeers: 1})
			t.Cleanup(server.close)
			server.setQUICPort()
			firstClient := newTestBulkServer(t)
			t.Cleanup(firstClient.close)
			firstClient.setQUICPort()
			secondClient := newTestBulkServer(t)
			t.Cleanup(secondClient.close)
			server.setPeer(newTestTrackedPeer(firstClient.localnode.Node()))
			server.setPeer(newTestTrackedPeer(secondClient.localnode.Node()))
			firstClient.setPeer(newTestTrackedPeer(server.localnode.Node()))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var first *quic.Conn
			var err error
			if outgoing {
				first, err = server.bulk.dialConn(ctx, firstClient.localnode.Node())
			} else {
				first, err = firstClient.bulk.dialConn(ctx, server.localnode.Node())
			}
			require.NoError(t, err)
			defer first.CloseWithError(0, "test complete")
			// Pending admission opens only after the first incoming connection is active.
			var second *quic.Conn
			require.Eventually(t, func() bool {
				second, err = quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
				return err == nil
			}, time.Second, time.Millisecond)
			defer second.CloseWithError(0, "test complete")
			require.NoError(t, secondClient.bulk.initiateAuth(second, server.localnode.Node()))
			select {
			case <-second.Context().Done():
				var appErr *quic.ApplicationError
				require.ErrorAs(t, context.Cause(second.Context()), &appErr)
				require.Equal(t, errBulkAdmissionLimit.Error(), appErr.ErrorMessage)
			case <-ctx.Done():
				t.Fatal("pending connection exceeded the authenticated peer limit")
			}
			require.NoError(t, first.CloseWithError(0, "release active slot"))
			require.Eventually(t, func() bool {
				if !server.bulk.activeConnections.TryAcquire(1) {
					return false
				}
				server.bulk.activeConnections.Release(1)
				return true
			}, time.Second, time.Millisecond)
			var recovered *quic.Conn
			require.Eventually(t, func() bool {
				recovered, err = quic.DialAddr(ctx, server.bulk.Addr().String(), newBulkSidecarVerifiedTLSConfig(), nil)
				return err == nil
			}, time.Second, time.Millisecond)
			defer recovered.CloseWithError(0, "test complete")
			require.NoError(t, secondClient.bulk.initiateAuth(recovered, server.localnode.Node()))
			require.Eventually(t, func() bool {
				server.bulk.lock.Lock()
				session := server.bulk.sessions[secondClient.localnode.ID()]
				server.bulk.lock.Unlock()
				if session == nil {
					return false
				}
				session.lock.Lock()
				defer session.lock.Unlock()
				return session.liveConnLocked() != nil
			}, time.Second, time.Millisecond)
		})
	}
}

func TestBulkAuthenticatedAdmissionCapacity(t *testing.T) {
	for _, limit := range []int{0, 1, 2} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			server := newTestBulkServerWithConfig(t, Config{MaxPeers: limit, MaxPendingPeers: 1})
			t.Cleanup(server.close)
			admit := newBulkConnContext(limit, 1, newBulkQUICWindows(limit, 1))
			for range limit {
				ctx, cancel := context.WithCancel(t.Context())
				t.Cleanup(cancel)
				pending, err := admit(ctx, bulkTestClientInfo(1))
				require.NoError(t, err)
				require.NoError(t, server.bulk.admitAuthenticatedConn(pending))
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			pending, err := admit(ctx, bulkTestClientInfo(1))
			require.NoError(t, err, "active connections must not hold pending slots")
			require.ErrorIs(t, server.bulk.admitAuthenticatedConn(pending), errBulkAdmissionLimit)
			_, err = admit(t.Context(), bulkTestClientInfo(2))
			require.ErrorIs(t, err, errBulkAdmissionLimit, "a rejected connection retains its pending reservation until close")
		})
	}
	server := newTestBulkServer(t)
	t.Cleanup(server.close)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, server.bulk.admitAuthenticatedConn(ctx), context.Canceled)
	require.True(t, server.bulk.activeConnections.TryAcquire(int64(server.server.MaxPeers)))
	server.bulk.activeConnections.Release(int64(server.server.MaxPeers))
}

func TestBulkSidecarOutboundAdmissionLimit(t *testing.T) {
	server := newTestBulkServerWithConfig(t, Config{MaxPeers: 1, MaxPendingPeers: 1})
	t.Cleanup(server.close)
	firstRemote := newTestBulkServer(t)
	t.Cleanup(firstRemote.close)
	firstRemote.setQUICPort()
	firstRemote.setPeer(newTestTrackedPeer(server.localnode.Node()))
	secondRemote := newTestBulkServer(t)
	t.Cleanup(secondRemote.close)
	secondRemote.setQUICPort()
	secondRemote.setPeer(newTestTrackedPeer(server.localnode.Node()))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	first, err := server.bulk.dialConn(ctx, firstRemote.localnode.Node())
	require.NoError(t, err)
	defer first.CloseWithError(0, "test complete")
	second, err := server.bulk.dialConn(ctx, secondRemote.localnode.Node())
	if second != nil {
		defer second.CloseWithError(0, "test complete")
	}
	require.ErrorIs(t, err, errBulkAdmissionLimit)
	require.Nil(t, second)
}
