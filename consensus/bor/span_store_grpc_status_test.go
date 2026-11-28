package bor

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/ethereum/go-ethereum/consensus/bor/heimdallgrpc"

	cmtservice "github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
)

type stubSyncServer struct {
	cmtservice.UnimplementedServiceServer
	syncing bool
}

func (s *stubSyncServer) GetSyncing(context.Context, *cmtservice.GetSyncingRequest) (*cmtservice.GetSyncingResponse, error) {
	return &cmtservice.GetSyncingResponse{Syncing: s.syncing}, nil
}

func startStubHeimdallGRPC(t *testing.T, syncing bool) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := grpc.NewServer()
	cmtservice.RegisterServiceServer(s, &stubSyncServer{syncing: syncing})
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

func TestWaitUntilHeimdallIsSyncedGRPCOnly(t *testing.T) {
	addr := startStubHeimdallGRPC(t, false)
	client, err := heimdallgrpc.NewHeimdallGRPCClient(addr, "", time.Second)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	store := &SpanStore{heimdallClient: client}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		store.waitUntilHeimdallIsSynced(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("waitUntilHeimdallIsSynced did not return for a synced gRPC-only client")
	}
	require.NoError(t, ctx.Err())
	require.NotNil(t, store.heimdallStatus.Load())
	require.False(t, store.heimdallStatus.Load().CatchingUp)
}
