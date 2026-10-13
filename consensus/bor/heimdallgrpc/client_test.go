package heimdallgrpc

import (
	"context"
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
