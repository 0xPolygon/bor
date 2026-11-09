package p2p

import (
	"context"
	"net"
	"testing"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/netutil"
)

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
