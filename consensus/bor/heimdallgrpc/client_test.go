package heimdallgrpc

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"google.golang.org/grpc/connectivity"

	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
)

func TestNewHeimdallGRPCClientInvalidRESTEndpoint(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	client, err := NewHeimdallGRPCClient("localhost:9090", "h3:///status-only", time.Second)
	require.ErrorContains(t, err, "empty host")
	require.Nil(t, client)
}

func TestHeimdallGRPCClientClose(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	for _, scheme := range []string{"http", "h3"} {
		t.Run(scheme, func(t *testing.T) {
			client, err := NewHeimdallGRPCClient("localhost:9090", scheme+"://127.0.0.1:1317", time.Second)
			require.NoError(t, err)
			t.Cleanup(client.Close)
			var wg sync.WaitGroup
			for range 8 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					client.Close()
				}()
			}
			wg.Wait()
			require.Equal(t, connectivity.Shutdown, client.conn.GetState())
			if scheme == "h3" {
				_, err = client.FetchStatus(t.Context())
				require.ErrorIs(t, err, http3.ErrTransportClosed)
			}
		})
	}
}

func TestHeimdallGRPCClientClosePartial(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	(*HeimdallGRPCClient)(nil).Close()
	new(HeimdallGRPCClient).Close()
	rest, err := heimdall.NewHeimdallClientWithError("h3://127.0.0.1:1317", time.Second)
	require.NoError(t, err)
	client := &HeimdallGRPCClient{client: rest}
	t.Cleanup(client.Close)
	client.Close()
	_, err = client.FetchStatus(t.Context())
	require.ErrorIs(t, err, http3.ErrTransportClosed)

	grpcOnly, err := NewHeimdallGRPCClient("localhost:9090", "http://127.0.0.1:1317", time.Second)
	require.NoError(t, err)
	t.Cleanup(grpcOnly.Close)
	grpcOnly.client.Close()
	grpcOnly.client = nil
	grpcOnly.Close()
	require.Equal(t, connectivity.Shutdown, grpcOnly.conn.GetState())
}

func TestHeimdallGRPCClientClosePendingStatus(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	client, err := NewHeimdallGRPCClient("localhost:9090", "h3://"+listener.LocalAddr().String(), 5*time.Second)
	require.NoError(t, err)
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		_, err := client.FetchStatus(t.Context())
		done <- err
	}()
	require.NoError(t, listener.SetReadDeadline(time.Now().Add(3*time.Second)))
	var packet [2048]byte
	_, _, err = listener.ReadFrom(packet[:])
	require.NoError(t, err)
	client.Close()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("pending HTTP/3 request did not stop on close")
	}
}
