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

func newStalledHeimdall(t *testing.T) string {
	t.Helper()

	lis, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	srv := grpc.NewServer()
	borTypes.RegisterQueryServer(srv, &stalledBorServer{})
	clerkTypes.RegisterQueryServer(srv, &stalledClerkServer{})

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return lis.Addr().String()
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

			client, err := NewHeimdallGRPCClient(newStalledHeimdall(t), "http://127.0.0.1:1", timeout)
			require.NoError(t, err)
			defer client.Close()

			start := time.Now()
			err = tt.call(t.Context(), client)

			require.Equal(t, codes.DeadlineExceeded, status.Code(err), "unexpected error: %v", err)
			require.Less(t, time.Since(start), 5*time.Second)
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
