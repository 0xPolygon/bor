package p2p

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"
)

func TestBulkAdmissionPendingLimit(t *testing.T) {
	for _, limit := range []int{0, -1, 2} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			admit := newBulkConnContext(3, limit)
			if limit <= 0 {
				limit = defaultMaxPendingPeers
			}
			for range limit {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				_, err := admit(ctx, nil)
				require.NoError(t, err)
			}
			_, err := admit(context.Background(), nil)
			require.ErrorIs(t, err, errBulkAdmissionLimit)
		})
	}
}

func TestBulkAdmissionConnectionLimit(t *testing.T) {
	for _, peers := range []int{-1, 0, 2} {
		t.Run(strconv.Itoa(peers), func(t *testing.T) {
			admit := newBulkConnContext(peers, 1)
			for range max(0, peers) + 1 {
				ctx, cancel := context.WithCancel(context.Background())
				t.Cleanup(cancel)
				ctx, err := admit(ctx, nil)
				require.NoError(t, err)
				releaseBulkPendingAuth(ctx)
				releaseBulkPendingAuth(ctx)
			}
			for range 2 {
				_, err := admit(context.Background(), nil)
				require.ErrorIs(t, err, errBulkAdmissionLimit)
			}
		})
	}
}

func TestBulkAdmissionReleasesOnClose(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "authenticated"}[authenticated], func(t *testing.T) {
			admit := newBulkConnContext(0, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx, err := admit(ctx, nil)
			require.NoError(t, err)
			if authenticated {
				releaseBulkPendingAuth(ctx)
			}
			cancel()
			next, nextCancel := context.WithCancel(context.Background())
			defer nextCancel()
			require.Eventually(t, func() bool {
				_, err := admit(next, nil)
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
