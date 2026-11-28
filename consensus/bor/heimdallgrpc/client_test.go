package heimdallgrpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	clerkTypes "github.com/0xPolygon/heimdall-v2/x/clerk/types"
)

type stalledBorServer struct {
	borTypes.UnimplementedQueryServer
	responseErr error
}

func (s stalledBorServer) GetSpanById(ctx context.Context, _ *borTypes.QuerySpanByIdRequest) (*borTypes.QuerySpanByIdResponse, error) {
	if s.responseErr != nil {
		return nil, s.responseErr
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s stalledBorServer) GetLatestSpan(ctx context.Context, _ *borTypes.QueryLatestSpanRequest) (*borTypes.QueryLatestSpanResponse, error) {
	if s.responseErr != nil {
		return nil, s.responseErr
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

type stalledClerkServer struct {
	clerkTypes.UnimplementedQueryServer
	responseErr error
}

func (s stalledClerkServer) GetRecordListWithTime(ctx context.Context, _ *clerkTypes.RecordListWithTimeRequest) (*clerkTypes.RecordListWithTimeResponse, error) {
	if s.responseErr != nil {
		return nil, s.responseErr
	}
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

func newCancelingHeimdall(t *testing.T) string {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	responseErr := status.Error(codes.Canceled, "server canceled")
	srv := grpc.NewServer()
	borTypes.RegisterQueryServer(srv, &stalledBorServer{responseErr: responseErr})
	clerkTypes.RegisterQueryServer(srv, &stalledClerkServer{responseErr: responseErr})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return lis.Addr().String()
}

type grpcClientCall struct {
	name string
	call func(context.Context, *HeimdallGRPCClient) error
}

func grpcClientCalls() []grpcClientCall {
	return []grpcClientCall{
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
}

func TestRequestsHonorConfiguredTimeout(t *testing.T) {
	t.Parallel()

	const timeout = 200 * time.Millisecond

	tests := grpcClientCalls()
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

func TestCallerCancellationIsNormalized(t *testing.T) {
	t.Parallel()

	tests := grpcClientCalls()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			addr, entered := newStalledHeimdall(t)
			client, err := NewHeimdallGRPCClient(addr, "http://127.0.0.1:1", 5*time.Second)
			require.NoError(t, err)
			defer client.Close()

			ctx, cancel := context.WithCancel(t.Context())
			errCh := make(chan error, 1)
			go func() { errCh <- tt.call(ctx, client) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("request did not reach the server")
			}
			cancel()

			select {
			case err := <-errCh:
				require.Error(t, err)
				require.True(t, errors.Is(err, context.Canceled), "caller cancellation was not preserved: %v", err)
				require.Equal(t, codes.Canceled, status.Code(err), "original gRPC status was not preserved: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("canceled request did not return")
			}
		})
	}
}

func TestServerCancellationIsNotCallerCancellation(t *testing.T) {
	t.Parallel()

	for _, tt := range grpcClientCalls() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewHeimdallGRPCClient(newCancelingHeimdall(t), "http://127.0.0.1:1", 5*time.Second)
			require.NoError(t, err)
			defer client.Close()

			err = tt.call(t.Context(), client)
			require.Error(t, err)
			require.False(t, errors.Is(err, context.Canceled), "server cancellation was attributed to the live caller: %v", err)
			require.Equal(t, codes.Canceled, status.Code(err))
		})
	}
}

func TestConnectionCloseIsNotCallerCancellation(t *testing.T) {
	t.Parallel()

	for _, tt := range grpcClientCalls() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			addr, entered := newStalledHeimdall(t)
			client, err := NewHeimdallGRPCClient(addr, "http://127.0.0.1:1", 5*time.Second)
			require.NoError(t, err)
			defer client.Close()

			errCh := make(chan error, 1)
			go func() { errCh <- tt.call(t.Context(), client) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("request did not reach the server")
			}
			client.Close()

			select {
			case err := <-errCh:
				require.Error(t, err)
				require.False(t, errors.Is(err, context.Canceled), "connection close was attributed to the live caller: %v", err)
				require.Equal(t, codes.Canceled, status.Code(err))
			case <-time.After(5 * time.Second):
				t.Fatal("request did not return after connection close")
			}
		})
	}
}

func TestCallerCancellationLeavesOtherFailuresUnchanged(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, code := range []codes.Code{codes.Internal, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			rpcErr := status.Error(code, "request failed")
			err := callerCancellation(ctx, rpcErr)
			require.Same(t, rpcErr, err)
			require.False(t, errors.Is(err, context.Canceled))
			require.Equal(t, code, status.Code(err))
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
