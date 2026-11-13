package p2p

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/p2p/enr"
)

func TestBulkChannelsRecoverFirstDial(t *testing.T) {
	for _, slow := range []bool{false, true} {
		name := "rejected"
		if slow {
			name = "timed out"
		}
		t.Run(name, func(t *testing.T) { testBulkChannelsRecoverFirstDial(t, slow) })
	}
}

func testBulkChannelsRecoverFirstDial(t *testing.T, slow bool) {
	t.Helper()
	left, right, lp, rp := channelReadyTestServers(t)
	var attempts atomic.Int32
	startBulkDialTestListener(t, right, func(*quic.ClientInfo) (*quic.Config, error) {
		if attempts.Add(1) == 1 {
			if slow {
				select {
				case <-time.After(bulkDialTimeout + 200*time.Millisecond):
				case <-t.Context().Done():
				}
			}
			return nil, errors.New("temporarily unavailable")
		}
		return nil, nil
	})
	require.NoError(t, left.db.UpdateNode(right.localnode.Node()))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	results := make(chan error, 2*len(bulkChannels))
	for channel := range bulkChannels {
		for _, side := range []struct {
			server *testBulkServer
			peer   *Peer
		}{{left, lp}, {right, rp}} {
			go func() {
				_, err := side.server.bulk.OpenChannelContext(ctx, side.peer, channel)
				results <- err
			}()
		}
	}
	for range cap(results) {
		select {
		case err := <-results:
			require.NoError(t, err, "a failed shared dial must not discard all concurrent lanes")
		case <-ctx.Done():
			t.Fatal("concurrent lane setup did not finish")
		}
	}
	require.GreaterOrEqual(t, attempts.Load(), int32(2))
	for _, server := range []*testBulkServer{left, right} {
		status := server.bulk.Status()
		require.Equal(t, 1, status.ActiveSessions)
		require.Equal(t, len(bulkChannels), status.ActiveChannels)
	}
}

func startBulkDialTestListener(t *testing.T, server *testBulkServer, configure func(*quic.ClientInfo) (*quic.Config, error)) {
	t.Helper()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, udp.Close()) })
	transport := &quic.Transport{Conn: udp}
	t.Cleanup(func() { require.NoError(t, transport.Close()) })
	config := server.bulk.config.Clone()
	config.GetConfigForClient = configure
	listener, err := transport.Listen(server.bulk.tls, config)
	require.NoError(t, err)
	server.localnode.Set(enr.QUIC(udp.LocalAddr().(*net.UDPAddr).Port))
	done := make(chan struct{})
	t.Cleanup(func() {
		require.NoError(t, listener.Close())
		<-done
	})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept(t.Context())
			if err != nil {
				return
			}
			server.bulk.startWorker(func() { server.bulk.handleIncomingConn(conn) })
		}
	}()
}
