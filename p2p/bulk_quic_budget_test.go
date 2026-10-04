package p2p

import (
	"context"
	"math"
	"net"
	"testing"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/netutil"
)

func TestBulkQUICWindowBudget(t *testing.T) {
	server := newTestBulkServer(t)
	t.Cleanup(server.close)
	server.setQUICPort()
	windows := server.bulk.windows
	var releases []func()
	for range bulkQUICWindowSlots(server.server.MaxPeers, server.server.MaxPendingPeers) {
		release, err := reserveBulkQUICWindow(windows)
		require.NoError(t, err)
		t.Cleanup(release)
		releases = append(releases, release)
	}
	_, err := reserveBulkQUICWindow(windows)
	require.ErrorIs(t, err, errBulkAdmissionLimit)
	admit := newBulkConnContext(0, 1, windows)
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
}

// The byte budget must track the operator's configured peer limits. A fixed
// ceiling would silently stop granting windows — pushing peers back onto RLPx —
// as soon as MaxPeers was raised past whatever that constant assumed.
func TestBulkQUICWindowsScaleWithPeerLimits(t *testing.T) {
	for _, test := range []struct {
		name                 string
		maxPeers, maxPending int
		wantSlots            int
	}{
		{"default", 50, 50, 100},
		{"raised", 200, 50, 250},
		{"unset", 0, 0, defaultMaxPendingPeers},
		{"negative", -1, -1, defaultMaxPendingPeers},
	} {
		t.Run(test.name, func(t *testing.T) {
			slots := bulkQUICWindowSlots(test.maxPeers, test.maxPending)
			require.Equal(t, test.wantSlots, slots)
			windows := newBulkQUICWindows(test.maxPeers, test.maxPending)
			for range slots {
				_, err := reserveBulkQUICWindow(windows)
				require.NoError(t, err)
			}
			_, err := reserveBulkQUICWindow(windows)
			require.ErrorIs(t, err, errBulkAdmissionLimit)
		})
	}
}

func TestBulkQUICWindowCapacityBounds(t *testing.T) {
	slots := bulkQUICWindowSlots(math.MaxInt, math.MaxInt)
	require.Positive(t, slots)
	require.LessOrEqual(t, int64(slots), int64(math.MaxInt64/bulkConnReceiveWindowMax))
	windows := newBulkQUICWindows(math.MaxInt, math.MaxInt)
	bytes := int64(slots) * bulkConnReceiveWindowMax
	require.True(t, windows.TryAcquire(bytes))
	_, err := reserveBulkQUICWindow(windows)
	require.ErrorIs(t, err, errBulkAdmissionLimit)
	windows.Release(bytes)
	release, err := reserveBulkQUICWindow(windows)
	require.NoError(t, err)
	release()
	release()
	require.True(t, windows.TryAcquire(bytes))
	windows.Release(bytes)
}

func TestBulkNetRestrict(t *testing.T) {
	list, err := netutil.ParseNetlist("192.0.2.0/24")
	require.NoError(t, err)
	server := newTestBulkServerWithConfig(t, Config{MaxPeers: 1, NetRestrict: list})
	t.Cleanup(server.close)
	server.setQUICPort()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	admit := bulkServerConnContext(server.server, server.bulk.windows)
	_, err = admit(ctx, &quic.ClientInfo{RemoteAddr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}})
	require.ErrorIs(t, err, errNetRestrict)
	_, err = server.bulk.dialConn(ctx, server.localnode.Node())
	require.ErrorIs(t, err, errNetRestrict)
	_, err = admit(ctx, &quic.ClientInfo{RemoteAddr: &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1)}})
	require.NoError(t, err)
}
