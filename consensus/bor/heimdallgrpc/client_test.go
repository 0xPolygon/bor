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

	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"

	cmtservice "github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
)

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
	require.NotNil(t, client.tmServiceClient)

	client.Close()
	client.Close()
	require.Equal(t, connectivity.Shutdown, client.conn.GetState())
}

// A zero-value client (never produced by the constructor) has neither transport.
func TestFetchStatusNoTransport(t *testing.T) {
	_, err := (*HeimdallGRPCClient)(nil).FetchStatus(t.Context())
	require.ErrorIs(t, err, errNoRESTEndpoint)
	_, err = new(HeimdallGRPCClient).FetchStatus(t.Context())
	require.ErrorIs(t, err, errNoRESTEndpoint)
}

type stubSyncServer struct {
	cmtservice.UnimplementedServiceServer
	syncing bool
	err     error
	block   chan struct{} // when set, GetSyncing blocks until it or the RPC ctx is done
	probed  chan struct{} // non-blocking signal on each request arrival
}

func (s *stubSyncServer) GetSyncing(ctx context.Context, _ *cmtservice.GetSyncingRequest) (*cmtservice.GetSyncingResponse, error) {
	if s.probed != nil {
		select {
		case s.probed <- struct{}{}:
		default:
		}
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	return &cmtservice.GetSyncingResponse{Syncing: s.syncing}, nil
}

func startStubHeimdallGRPC(t *testing.T, srv cmtservice.ServiceServer) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := grpc.NewServer()
	cmtservice.RegisterServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func TestFetchStatusGRPCOnly(t *testing.T) {
	opt := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, opt) })

	for _, tc := range []struct {
		name    string
		syncing bool
	}{
		{"synced", false},
		{"catching up", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := startStubHeimdallGRPC(t, &stubSyncServer{syncing: tc.syncing})
			client, err := NewHeimdallGRPCClient(addr, "", time.Second)
			require.NoError(t, err)
			t.Cleanup(client.Close)

			status, err := client.FetchStatus(t.Context())
			require.NoError(t, err)
			require.Equal(t, tc.syncing, status.CatchingUp)
		})
	}
}

func TestFetchStatusGRPCError(t *testing.T) {
	opt := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, opt) })

	// FailedPrecondition is not in the retry set, so it surfaces promptly and the
	// endpoint is reported unhealthy on a genuine failure rather than hidden.
	addr := startStubHeimdallGRPC(t, &stubSyncServer{err: status.Error(codes.FailedPrecondition, "boom")})
	client, err := NewHeimdallGRPCClient(addr, "", time.Second)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	info, err := client.FetchStatus(t.Context())
	require.Error(t, err)
	require.Nil(t, info)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// A deadline-less caller must still be bounded by the configured status timeout
// when a reachable server never answers.
func TestFetchStatusGRPCConfiguredTimeout(t *testing.T) {
	opt := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, opt) })

	probed := make(chan struct{}, 1)
	addr := startStubHeimdallGRPC(t, &stubSyncServer{block: make(chan struct{}), probed: probed})
	client, err := NewHeimdallGRPCClient(addr, "", 200*time.Millisecond)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	done := make(chan error, 1)
	go func() { _, e := client.FetchStatus(context.Background()); done <- e }()
	requireRecv(t, probed, "request never reached server")
	select {
	case e := <-done:
		require.Equal(t, codes.DeadlineExceeded, status.Code(e))
	case <-time.After(5 * time.Second):
		t.Fatal("FetchStatus did not time out")
	}
}

// Cancelling after the request is in flight aborts it promptly; a tighter parent
// deadline wins over the configured status timeout.
func TestFetchStatusGRPCCancelInFlight(t *testing.T) {
	opt := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, opt) })

	probed := make(chan struct{}, 1)
	addr := startStubHeimdallGRPC(t, &stubSyncServer{block: make(chan struct{}), probed: probed})
	client, err := NewHeimdallGRPCClient(addr, "", time.Minute)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := client.FetchStatus(ctx); done <- e }()
	requireRecv(t, probed, "request never reached server")
	cancel()
	select {
	case e := <-done:
		require.Equal(t, codes.Canceled, status.Code(e))
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight FetchStatus did not cancel")
	}
}

// With multiple gRPC-only endpoints the failover registry's startup probe cycle
// reaches every endpoint over GetSyncing, so none is stuck unhealthy.
func TestFetchStatusGRPCOnlyFailoverProbesAll(t *testing.T) {
	opt := goleak.IgnoreCurrent()
	t.Cleanup(func() { goleak.VerifyNone(t, opt) })

	probed0 := make(chan struct{}, 4)
	probed1 := make(chan struct{}, 4)
	c0, err := NewHeimdallGRPCClient(startStubHeimdallGRPC(t, &stubSyncServer{probed: probed0}), "", time.Second)
	require.NoError(t, err)
	c1, err := NewHeimdallGRPCClient(startStubHeimdallGRPC(t, &stubSyncServer{probed: probed1}), "", time.Second)
	require.NoError(t, err)

	fc, err := heimdall.NewMultiHeimdallClient(c0, c1)
	require.NoError(t, err)
	t.Cleanup(fc.Close) // closes c0 and c1

	// A foreground call returns real status and starts the background registry,
	// which immediately probes every endpoint.
	info, err := fc.FetchStatus(t.Context())
	require.NoError(t, err)
	require.False(t, info.CatchingUp)

	requireRecv(t, probed0, "background probe did not reach endpoint 0")
	requireRecv(t, probed1, "background probe did not reach endpoint 1")
}

func requireRecv(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(msg)
	}
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
