package heimdallgrpc

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	clerkTypes "github.com/0xPolygon/heimdall-v2/x/clerk/types"

	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
)

type stalledBorServer struct {
	borTypes.UnimplementedQueryServer
}

func (stalledBorServer) GetSpanById(ctx context.Context, _ *borTypes.QuerySpanByIdRequest) (*borTypes.QuerySpanByIdResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (stalledBorServer) GetLatestSpan(ctx context.Context, _ *borTypes.QueryLatestSpanRequest) (*borTypes.QueryLatestSpanResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type stalledClerkServer struct {
	clerkTypes.UnimplementedQueryServer
}

func (stalledClerkServer) GetRecordListWithTime(ctx context.Context, _ *clerkTypes.RecordListWithTimeRequest) (*clerkTypes.RecordListWithTimeResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// newStalledHeimdall also returns the time left on the deadline of the
// first request it receives, so tests can check which timeout was applied.
func newStalledHeimdall(t *testing.T) (string, <-chan time.Duration) {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	remaining := make(chan time.Duration, 1)
	srv := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if deadline, ok := ctx.Deadline(); ok {
			select {
			case remaining <- time.Until(deadline):
			default:
			}
		}
		return handler(ctx, req)
	}))
	borTypes.RegisterQueryServer(srv, &stalledBorServer{})
	clerkTypes.RegisterQueryServer(srv, &stalledClerkServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return lis.Addr().String(), remaining
}

func TestRequestsHonorConfiguredTimeout(t *testing.T) {
	t.Parallel()

	const timeout = 200 * time.Millisecond

	tests := []struct {
		name string
		call func(context.Context, *HeimdallGRPCClient) error
	}{
		{"GetSpan", func(ctx context.Context, c *HeimdallGRPCClient) error {
			_, err := c.GetSpan(ctx, 1)
			return err
		}},
		{"GetLatestSpan", func(ctx context.Context, c *HeimdallGRPCClient) error {
			_, err := c.GetLatestSpan(ctx)
			return err
		}},
		{"StateSyncEvents", func(ctx context.Context, c *HeimdallGRPCClient) error {
			_, err := c.StateSyncEvents(ctx, 1, time.Now().Unix())
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			addr, remaining := newStalledHeimdall(t)
			client, err := NewHeimdallGRPCClient(addr, "http://127.0.0.1:1", timeout)
			require.NoError(t, err)
			defer client.Close()

			err = tt.call(t.Context(), client)
			require.Equal(t, codes.DeadlineExceeded, status.Code(err), "unexpected error: %v", err)

			// The server must see the configured deadline, not the 30s default.
			select {
			case left := <-remaining:
				require.Positive(t, left)
				require.LessOrEqual(t, left, timeout)
			default:
				t.Fatal("request reached the server without a deadline")
			}
		})
	}
}

func TestUnsetTimeoutFallsBackToDefault(t *testing.T) {
	t.Parallel()

	client, err := NewHeimdallGRPCClient("127.0.0.1:1", "http://127.0.0.1:1", 0)
	require.NoError(t, err)
	defer client.Close()

	require.Equal(t, defaultTimeout, client.timeout)
}

func TestNewHeimdallGRPCClientInvalidRESTEndpoint(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	client, err := NewHeimdallGRPCClient("localhost:9090", "h3:///status-only", time.Second)
	require.ErrorContains(t, err, "empty host")
	require.Nil(t, client)
}

func TestNewHeimdallGRPCClientNonEmptyInvalidRESTEndpoint(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	client, err := NewHeimdallGRPCClient("localhost:9090", "ftp://127.0.0.1:1317", time.Second)
	require.ErrorContains(t, err, "unsupported Heimdall endpoint scheme")
	require.Nil(t, client)
}

func TestNewHeimdallGRPCClientNonEmptyValidRESTEndpoint(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	client, err := NewHeimdallGRPCClient("localhost:9090", "http://127.0.0.1:1317", time.Second)
	require.NoError(t, err)
	defer client.Close()
	require.NotNil(t, client.client)
}

func TestNewHeimdallGRPCClientGRPCOnly(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	client, err := NewHeimdallGRPCClient("localhost:9090", "", time.Second)
	require.NoError(t, err)
	require.NotNil(t, client)
	t.Cleanup(client.Close)
	require.Nil(t, client.client)

	status, err := client.FetchStatus(t.Context())
	require.ErrorIs(t, err, errNoRESTEndpoint)
	require.Nil(t, status)

	client.Close()
	client.Close()
	require.Equal(t, connectivity.Shutdown, client.conn.GetState())
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
