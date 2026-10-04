package p2p

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enr"
)

func TestBulkDialAfterShutdown(t *testing.T) {
	local, remote := newTestBulkServer(t), newTestBulkServer(t)
	t.Cleanup(local.close)
	t.Cleanup(remote.close)
	remote.setQUICPort()
	remote.setPeer(newTestTrackedPeer(local.localnode.Node()))
	local.bulk.Close()
	conn, err := local.bulk.dialConn(t.Context(), remote.localnode.Node())
	if conn != nil {
		require.NoError(t, conn.CloseWithError(0, "test complete"))
	}
	require.Error(t, err)
	require.Nil(t, conn)
}

func TestBulkShutdownDuringDial(t *testing.T) {
	for _, phase := range []string{"handshake", "authentication"} {
		t.Run(phase, func(t *testing.T) { testBulkShutdownDuringDial(t, phase) })
	}
}

func testBulkShutdownDuringDial(t *testing.T, phase string) {
	t.Helper()
	local, remote := newTestBulkServer(t), newTestBulkServer(t)
	t.Cleanup(local.close)
	t.Cleanup(remote.close)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	defer udp.Close()
	transport := &quic.Transport{Conn: udp}
	defer transport.Close()
	var listener *quic.Listener
	if phase == "authentication" {
		listener, err = transport.Listen(remote.bulk.tls, remote.bulk.config)
		require.NoError(t, err)
	}
	remote.localnode.Set(enr.QUIC(udp.LocalAddr().(*net.UDPAddr).Port))
	result := make(chan error, 1)
	go func() {
		conn, err := local.bulk.dialConn(ctx, remote.localnode.Node())
		if conn != nil {
			conn.CloseWithError(0, "test complete")
		}
		result <- err
	}()
	if listener == nil {
		require.NoError(t, udp.SetReadDeadline(time.Now().Add(time.Second)))
		_, _, err = udp.ReadFromUDP(make([]byte, 1500))
	} else {
		var conn *quic.Conn
		conn, err = listener.Accept(ctx)
		require.NoError(t, err)
		_, err = conn.AcceptStream(ctx)
	}
	require.NoError(t, err)
	local.bulk.Close()
	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(time.Second):
		cancel()
		<-result
		t.Fatal("sidecar shutdown did not stop the outstanding dial")
	}
}

func TestBulkSessionMeterBothDirections(t *testing.T) {
	left, right := newTestBulkServer(t), newTestBulkServer(t)
	t.Cleanup(left.close)
	t.Cleanup(right.close)
	if bytes.Compare(left.bulk.localID[:], right.bulk.localID[:]) > 0 {
		left, right = right, left
	}
	left.setQUICPort()
	right.setQUICPort()
	lp, rp := newTestTrackedPeer(right.localnode.Node()), newTestTrackedPeer(left.localnode.Node())
	left.setPeer(lp)
	right.setPeer(rp)
	before := bulkSidecarSessionMeter.Snapshot().Count()
	_, err := left.bulk.OpenChannel(lp, "eth-bulk")
	require.NoError(t, err)
	_, err = right.bulk.OpenChannel(rp, "eth-bulk")
	require.NoError(t, err)
	require.Equal(t, before+2, bulkSidecarSessionMeter.Snapshot().Count())
	session := right.bulk.peerSession(rp.Node(), rp)
	conn, err := session.ensureConn(t.Context())
	require.NoError(t, err)
	require.Nil(t, right.bulk.adoptConn(rp, conn))
	_, err = left.bulk.OpenChannel(lp, "eth-bulk")
	require.NoError(t, err)
	require.Equal(t, before+2, bulkSidecarSessionMeter.Snapshot().Count())
	require.Equal(t, left.bulk.Addr().String(), conn.RemoteAddr().String())
}
