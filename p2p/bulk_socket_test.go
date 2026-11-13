package p2p

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBulkSocketBufferSizes(t *testing.T) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer conn.Close()
	status := configureBulkSidecarSocketBuffers(conn)
	require.Equal(t, bulkSocketReadBufferSize, status.ConfiguredReadBuffer)
	require.Equal(t, bulkSocketWriteBufferSize, status.ConfiguredWriteBuffer)
	require.Positive(t, status.ActualReadBuffer)
	require.Positive(t, status.ActualWriteBuffer)
	require.Zero(t, socketBufferSize(conn, -1))
}
