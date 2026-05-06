package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	protobor "github.com/0xPolygon/polyproto/bor"
	protoutil "github.com/0xPolygon/polyproto/utils"
)

func TestServer_DeveloperMode(t *testing.T) {
	t.Parallel()

	// get the default config
	config := DefaultConfig()

	// enable developer mode
	config.Developer.Enabled = true
	config.Developer.Period = 2 // block time

	// start the mock server
	server, err := CreateMockServer(config)
	assert.NoError(t, err)

	defer CloseMockServer(server)

	// record the initial block number
	blockNumber := server.backend.BlockChain().CurrentBlock().Number.Int64()

	var i int64
	for i = 0; i < 3; i++ {
		// We expect the node to mine blocks every `config.Developer.Period` time period
		time.Sleep(time.Duration(config.Developer.Period) * time.Second)

		currBlock := server.backend.BlockChain().CurrentBlock().Number.Int64()
		expected := blockNumber + i + 1

		if res := assert.Equal(t, expected, currBlock); res == false {
			break
		}
	}
}

// TestPerformHealthChecks_NoConfig short-circuits when health config is absent.
// All gRPC-related telemetry knobs default to "off" and the function must
// return StatusOK without dereferencing the nil config.
func TestPerformHealthChecks_NoConfig(t *testing.T) {
	t.Parallel()

	srv := &Server{config: nil}
	got := srv.performHealthChecks(map[string]any{})
	require.Equal(t, StatusOK, got.Level)
	require.Equal(t, 0, got.Code)
	require.Equal(t, "", got.Message)
}

// TestPerformHealthChecks_NilHealthSection: same as above but config exists
// without a [health] block — must take the early-return path, not panic.
func TestPerformHealthChecks_NilHealthSection(t *testing.T) {
	t.Parallel()

	srv := &Server{config: &Config{Health: nil}}
	got := srv.performHealthChecks(map[string]any{
		"system": map[string]any{"goroutines_count": float64(99999)},
	})
	require.Equal(t, StatusOK, got.Level)
}

// TestPerformHealthChecks_GoroutinesThresholds drives the goroutine-count
// branches: under both, only over warn, over critical (which wins over warn),
// plus the "0 = disabled" sentinel.
func TestPerformHealthChecks_GoroutinesThresholds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		warn    int
		max     int
		count   float64
		wantLvl HealthStatusLevel
		wantMsg string
	}{
		{"under both", 1000, 5000, 200, StatusOK, ""},
		{"over warn only", 100, 5000, 200, StatusWarn, "above the warning threshold"},
		{"over critical wins", 100, 1000, 5000, StatusCritical, "above the maximum threshold"},
		{"thresholds disabled by 0", 0, 0, 1_000_000, StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := &Server{config: &Config{Health: &HealthConfig{
				MaxGoRoutineThreshold:  tc.max,
				WarnGoRoutineThreshold: tc.warn,
			}}}
			resp := map[string]any{
				"system": map[string]any{"goroutines_count": tc.count},
			}
			got := srv.performHealthChecks(resp)
			require.Equal(t, tc.wantLvl, got.Level)
			if tc.wantMsg != "" {
				require.Contains(t, got.Message, tc.wantMsg)
			}
		})
	}
}

// TestPerformHealthChecks_PeerThresholds drives the peer-count branches.
// peer_count is read as int (not float64 like goroutine count).
func TestPerformHealthChecks_PeerThresholds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		warn    int
		min     int
		count   int
		wantLvl HealthStatusLevel
		wantMsg string
	}{
		{"healthy", 5, 1, 50, StatusOK, ""},
		{"below warn only", 10, 1, 5, StatusWarn, "below the warning threshold"},
		{"below critical wins", 5, 10, 1, StatusCritical, "below the minimum threshold"},
		{"thresholds disabled by 0", 0, 0, 0, StatusOK, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := &Server{config: &Config{Health: &HealthConfig{
				MinPeerThreshold:  tc.min,
				WarnPeerThreshold: tc.warn,
			}}}
			resp := map[string]any{
				"node_info": map[string]any{"peer_count": tc.count},
			}
			got := srv.performHealthChecks(resp)
			require.Equal(t, tc.wantLvl, got.Level)
			if tc.wantMsg != "" {
				require.Contains(t, got.Message, tc.wantMsg)
			}
		})
	}
}

// TestPerformHealthChecks_CombinedFailures verifies that both checks compose:
// a critical goroutines reading and a warn-level peer count must surface as
// Critical with both messages joined.
func TestPerformHealthChecks_CombinedFailures(t *testing.T) {
	t.Parallel()

	srv := &Server{config: &Config{Health: &HealthConfig{
		MaxGoRoutineThreshold:  1000,
		WarnGoRoutineThreshold: 500,
		MinPeerThreshold:       1,
		WarnPeerThreshold:      10,
	}}}
	resp := map[string]any{
		"system":    map[string]any{"goroutines_count": float64(5000)},
		"node_info": map[string]any{"peer_count": 5},
	}
	got := srv.performHealthChecks(resp)
	require.Equal(t, StatusCritical, got.Level)
	require.Contains(t, got.Message, "goroutines")
	require.Contains(t, got.Message, "peers")
}

// TestGetGrpcAddr returns the configured gRPC bind address verbatim.
func TestGetGrpcAddr(t *testing.T) {
	t.Parallel()

	srv := &Server{config: &Config{GRPC: &GRPCConfig{Addr: "127.0.0.1:3131"}}}
	require.Equal(t, "127.0.0.1:3131", srv.GetGrpcAddr())
}

// TestServer_GRPCHandlersHappyPath spins up a real mock server in dev mode,
// mines a few blocks, then exercises every gRPC handler that needs a live
// backend. Heavy test (~10s), but it's the only way to cover the actual
// proto-marshaling success paths in api_service.go.
func TestServer_GRPCHandlersHappyPath(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Developer.Enabled = true
	cfg.Developer.Period = 1

	srv, err := CreateMockServer(cfg)
	require.NoError(t, err)
	defer CloseMockServer(srv)

	// Mine a couple blocks so the latest is greater than genesis, and we have a non-genesis
	// header to query (GetAuthor / GetBlockInfoInBatch's author path requires blockNum > 0).
	require.Eventually(t, func() bool {
		return srv.GetLatestBlockNumber().Uint64() >= 2
	}, 10*time.Second, 100*time.Millisecond, "no blocks mined within 10s")

	ctx := context.Background()

	t.Run("HeaderByNumber latest tag", func(t *testing.T) {
		resp, err := srv.HeaderByNumber(ctx, &protobor.GetHeaderByNumberRequest{Number: "latest"})
		require.NoError(t, err)
		require.NotNil(t, resp)
		require.NotNil(t, resp.Header)
		require.GreaterOrEqual(t, resp.Header.Number, uint64(1))
	})

	t.Run("HeaderByNumber hex zero returns genesis", func(t *testing.T) {
		resp, err := srv.HeaderByNumber(ctx, &protobor.GetHeaderByNumberRequest{Number: "0x0"})
		require.NoError(t, err)
		require.Equal(t, uint64(0), resp.Header.Number)
	})

	t.Run("BlockByNumber latest", func(t *testing.T) {
		resp, err := srv.BlockByNumber(ctx, &protobor.GetBlockByNumberRequest{Number: "latest"})
		require.NoError(t, err)
		require.NotNil(t, resp.Block)
		require.NotNil(t, resp.Block.Header)
	})

	t.Run("GetAuthor latest", func(t *testing.T) {
		resp, err := srv.GetAuthor(ctx, &protobor.GetAuthorRequest{Number: "latest"})
		require.NoError(t, err)
		require.NotNil(t, resp.Author)
	})

	t.Run("GetTdByNumber latest", func(t *testing.T) {
		resp, err := srv.GetTdByNumber(ctx, &protobor.GetTdByNumberRequest{Number: "latest"})
		require.NoError(t, err)
		require.GreaterOrEqual(t, resp.TotalDifficulty, uint64(1))
	})

	t.Run("GetTdByHash for known block", func(t *testing.T) {
		// First fetch the latest header so we have a valid hash, then look up TD by it.
		hr, err := srv.HeaderByNumber(ctx, &protobor.GetHeaderByNumberRequest{Number: "latest"})
		require.NoError(t, err)

		// Reconstruct the proto H256 from the header's parent hash; using
		// parent guarantees the hash exists on chain (non-zero TD).
		req := &protobor.GetTdByHashRequest{Hash: hr.Header.ParentHash}
		resp, err := srv.GetTdByHash(ctx, req)
		require.NoError(t, err)
		require.GreaterOrEqual(t, resp.TotalDifficulty, uint64(1))
	})

	// Asserting the engine-rejected error path here:
	t.Run("GetRootHash on ethash mock returns engine error", func(t *testing.T) {
		_, err := srv.GetRootHash(ctx, &protobor.GetRootHashRequest{StartBlockNumber: 0, EndBlockNumber: 1})
		require.Error(t, err)
	})

	t.Run("GetVoteOnHash on ethash mock returns engine error", func(t *testing.T) {
		_, err := srv.GetVoteOnHash(ctx, &protobor.GetVoteOnHashRequest{
			StartBlockNumber: 0, EndBlockNumber: 1,
			Hash: "0x0", MilestoneId: "test",
		})
		require.Error(t, err)
	})

	t.Run("GetBlockInfoInBatch for [0,2]", func(t *testing.T) {
		resp, err := srv.GetBlockInfoInBatch(ctx, &protobor.GetBlockInfoInBatchRequest{
			StartBlockNumber: 0,
			EndBlockNumber:   2,
		})
		require.NoError(t, err)
		require.Len(t, resp.Blocks, 3)
		require.NotNil(t, resp.Blocks[0].Header)
		// Block 0 (genesis) has no author per fetchBlockInfo's contract.
		require.Nil(t, resp.Blocks[0].Author, "genesis must have nil author")
		require.NotNil(t, resp.Blocks[1].Author, "non-genesis must have non-nil author")
	})

	t.Run("HeaderByNumber far-future returns NotFound", func(t *testing.T) {
		_, err := srv.HeaderByNumber(ctx, &protobor.GetHeaderByNumberRequest{
			Number: fmt.Sprintf("0x%x", uint64(1_000_000_000)),
		})
		st, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.NotFound, st.Code())
	})

	t.Run("TransactionReceipt for unknown hash returns NotFound", func(t *testing.T) {
		// Random hash that won't match any tx; protoHashToCommon passes, then
		// the backend lookup misses → NotFound.
		hash := protoutil.ConvertHashToH256(common.HexToHash("0xdeadbeef"))
		_, err := srv.TransactionReceipt(ctx, &protobor.ReceiptRequest{Hash: hash})
		st, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.NotFound, st.Code())
	})

	t.Run("BorBlockReceipt for unknown hash returns NotFound", func(t *testing.T) {
		hash := protoutil.ConvertHashToH256(common.HexToHash("0xfeedface"))
		_, err := srv.BorBlockReceipt(ctx, &protobor.ReceiptRequest{Hash: hash})
		st, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.NotFound, st.Code())
	})

	t.Run("GetTdByHash for unknown hash returns NotFound", func(t *testing.T) {
		hash := protoutil.ConvertHashToH256(common.HexToHash("0xbaddcafe"))
		_, err := srv.GetTdByHash(ctx, &protobor.GetTdByHashRequest{Hash: hash})
		st, ok := status.FromError(err)
		require.True(t, ok)
		require.Equal(t, codes.NotFound, st.Code())
	})

	t.Run("GetLatestBlockNumber and GetGrpcAddr return server state", func(t *testing.T) {
		require.NotNil(t, srv.GetLatestBlockNumber())
		require.GreaterOrEqual(t, srv.GetLatestBlockNumber().Int64(), int64(1))
		require.NotEmpty(t, srv.GetGrpcAddr())
	})

	t.Run("getBorInfo populates expected fields", func(t *testing.T) {
		info := srv.getBorInfo()
		require.Contains(t, info, "chain_id")
		require.Contains(t, info, "latest_block_hash")
		require.Contains(t, info, "latest_block_number")
		require.Contains(t, info, "latest_block_timestamp")
		require.Contains(t, info, "peer_count")
		require.Contains(t, info, "sync_mode")
		require.Contains(t, info, "catching_up")
	})

	t.Run("customHealthServiceHandler responds with composed JSON", func(t *testing.T) {
		require.NoError(t, srv.setupHealthService())
		handler := srv.customHealthServiceHandler()

		req := httptest.NewRequest("GET", "/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		require.Equal(t, 200, rec.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Contains(t, body, "node_info")
		require.Contains(t, body, "status")
		require.Equal(t, false, body["error"])
	})
}

// TestWithGRPCListener: option function should wire the server's grpcServer
// using the provided listener. We feed it an arbitrary loopback listener,
// confirm the option callback succeeds, and immediately tear down the server
// to avoid leaking the Serve goroutine.
func TestWithGRPCListener(t *testing.T) {
	t.Parallel()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	opt := WithGRPCListener(lis)
	// Server with no [grpc] block: tokenForInterceptor handles the nil case
	// internally, so the option must succeed without manual scaffolding.
	srv := &Server{}
	require.NoError(t, opt(srv, &Config{}))
	require.NotNil(t, srv.grpcServer, "WithGRPCListener must wire grpcServer")
	// GracefulStop drains the in-flight serve goroutine and closes the
	// listener so the test doesn't leak.
	srv.grpcServer.GracefulStop()
}
