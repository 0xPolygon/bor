package p2p

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/netutil"
)

func TestBulkQUICWindowBudget(t *testing.T) {
	server := newTestBulkServer(t)
	t.Cleanup(server.close)
	server.setQUICPort()
	require.Eventually(t, func() bool {
		if !bulkQUICWindows.TryAcquire(bulkQUICBufferLimit) {
			return false
		}
		bulkQUICWindows.Release(bulkQUICBufferLimit)
		return true
	}, time.Second, time.Millisecond)
	var releases []func()
	for range bulkQUICBufferLimit / bulkConnReceiveWindowMax {
		release, err := reserveBulkQUICWindow()
		require.NoError(t, err)
		t.Cleanup(release)
		releases = append(releases, release)
	}
	_, err := reserveBulkQUICWindow()
	require.ErrorIs(t, err, errBulkAdmissionLimit)
	admit := newBulkConnContext(0, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err = admit(ctx, nil)
	require.ErrorIs(t, err, errBulkAdmissionLimit)
	_, err = server.bulk.dialConn(ctx, server.localnode.Node())
	require.ErrorIs(t, err, errBulkAdmissionLimit)
	releases[0]()
	_, err = admit(ctx, nil)
	require.NoError(t, err, "failed reservation must release connection and pending-auth slots")
}

func TestBulkQUICReceiveLimits(t *testing.T) {
	server := newTestBulkServer(t)
	t.Cleanup(server.close)
	config := server.bulk.config
	require.EqualValues(t, bulkConnReceiveWindow, config.InitialConnectionReceiveWindow)
	require.EqualValues(t, bulkConnReceiveWindowMax, config.MaxConnectionReceiveWindow)
	require.EqualValues(t, bulkStreamReceiveWindow, config.InitialStreamReceiveWindow)
	require.EqualValues(t, bulkStreamReceiveWindowMax, config.MaxStreamReceiveWindow)
	require.EqualValues(t, len(bulkChannels)+1, config.MaxIncomingStreams)
	require.EqualValues(t, -1, config.MaxIncomingUniStreams)

	// Auto-tuning only happens when the initial window is below the ceiling.
	require.Less(t, config.InitialConnectionReceiveWindow, config.MaxConnectionReceiveWindow)
	require.Less(t, config.InitialStreamReceiveWindow, config.MaxStreamReceiveWindow)

	require.GreaterOrEqual(t, bulkConnReceiveWindowMax, bulkStreamReceiveWindowMax)

	// The byte budget must not start rejecting connections before the
	// connection-count limit in newBulkConnContext does, or the sidecar would
	// silently stop admitting peers well below MaxPeers.
	const defaultMaxPeers = 50
	require.GreaterOrEqual(t, bulkQUICBufferLimit/bulkConnReceiveWindowMax, defaultMaxPeers+defaultMaxPendingPeers)
}

func TestBulkNetRestrict(t *testing.T) {
	list, err := netutil.ParseNetlist("192.0.2.0/24")
	require.NoError(t, err)
	server := newTestBulkServerWithConfig(t, Config{MaxPeers: 1, NetRestrict: list})
	t.Cleanup(server.close)
	server.setQUICPort()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admit := bulkServerConnContext(server.server)
	_, err = admit(ctx, &quic.ClientInfo{RemoteAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}})
	require.ErrorIs(t, err, errNetRestrict)
	_, err = server.bulk.dialConn(ctx, server.localnode.Node())
	require.ErrorIs(t, err, errNetRestrict)
	_, err = admit(ctx, &quic.ClientInfo{RemoteAddr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1)}})
	require.NoError(t, err)
}
