package ethconfig

import (
	"context"
	"math/big"
	"testing"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	stakeTypes "github.com/0xPolygon/heimdall-v2/x/stake/types"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/checkpoint"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdallws"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// mockHeimdallClient implements bor.IHeimdallClient for testing
type mockHeimdallClient struct{}

func (m *mockHeimdallClient) Close() {}
func (m *mockHeimdallClient) StateSyncEvents(context.Context, uint64, int64) ([]*clerk.EventRecordWithTime, error) {
	return nil, nil
}
func (m *mockHeimdallClient) GetSpan(_ context.Context, spanID uint64) (*borTypes.Span, error) {
	return &borTypes.Span{
		Id: spanID, StartBlock: 0, EndBlock: 255,
		ValidatorSet: stakeTypes.ValidatorSet{
			Validators: []*stakeTypes.Validator{{ValId: 1, Signer: "0x96C42C56fdb78294F96B0cFa33c92bed7D75F96a", VotingPower: 100}},
		},
	}, nil
}
func (m *mockHeimdallClient) GetLatestSpan(ctx context.Context) (*borTypes.Span, error) {
	return m.GetSpan(ctx, 0)
}
func (m *mockHeimdallClient) FetchCheckpoint(context.Context, int64) (*checkpoint.Checkpoint, error) {
	return nil, nil
}
func (m *mockHeimdallClient) FetchCheckpointCount(context.Context) (int64, error) { return 0, nil }
func (m *mockHeimdallClient) FetchMilestone(context.Context) (*milestone.Milestone, error) {
	return nil, nil
}
func (m *mockHeimdallClient) FetchMilestoneCount(context.Context) (int64, error) { return 0, nil }
func (m *mockHeimdallClient) FetchStatus(context.Context) (*ctypes.SyncInfo, error) {
	return &ctypes.SyncInfo{CatchingUp: false}, nil
}

// newTestBorChainConfig creates a minimal Bor chain config for testing
func newTestBorChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:             big.NewInt(137),
		HomesteadBlock:      big.NewInt(0),
		EIP150Block:         big.NewInt(0),
		EIP155Block:         big.NewInt(0),
		EIP158Block:         big.NewInt(0),
		ByzantiumBlock:      big.NewInt(0),
		ConstantinopleBlock: big.NewInt(0),
		PetersburgBlock:     big.NewInt(0),
		IstanbulBlock:       big.NewInt(0),
		MuirGlacierBlock:    big.NewInt(0),
		BerlinBlock:         big.NewInt(0),
		LondonBlock:         big.NewInt(0),
		Bor: &params.BorConfig{
			Period:                map[string]uint64{"0": 2},
			ProducerDelay:         map[string]uint64{"0": 4},
			Sprint:                map[string]uint64{"0": 64},
			BackupMultiplier:      map[string]uint64{"0": 2},
			ValidatorContract:     "0x0000000000000000000000000000000000001000",
			StateReceiverContract: "0x0000000000000000000000000000000000001001",
		},
	}
}

func TestCreateConsensusEngine_OverrideHeimdallClient(t *testing.T) {
	t.Parallel()
	ethConfig := &Config{
		OverrideHeimdallClient: &mockHeimdallClient{},
		WithoutHeimdall:        false,
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	_, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")
}

func TestCreateConsensusEngine_HeimdallSecondaryURL(t *testing.T) {
	t.Parallel()
	ethConfig := &Config{
		OverrideHeimdallClient: &mockHeimdallClient{},
		HeimdallSecondaryURL:   "http://secondary:1317",
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")

	_, ok = borEngine.HeimdallClient.(*heimdall.FailoverHeimdallClient)
	require.True(t, ok, "Expected HeimdallClient to be wrapped in FailoverHeimdallClient")
}

func TestCreateConsensusEngine_WithoutHeimdall(t *testing.T) {
	t.Parallel()
	ethConfig := &Config{WithoutHeimdall: true}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	_, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")
}

func TestCreateConsensusEngine_GRPCSecondaryFailover(t *testing.T) {
	t.Parallel()

	ethConfig := &Config{
		OverrideHeimdallClient:       &mockHeimdallClient{},
		HeimdallgRPCSecondaryAddress: "localhost:50051",
		HeimdallURL:                  "http://localhost:1317",
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")

	// Primary mock gets wrapped in FailoverHeimdallClient with gRPC secondary
	_, ok = borEngine.HeimdallClient.(*heimdall.FailoverHeimdallClient)
	require.True(t, ok, "Expected HeimdallClient to be wrapped in FailoverHeimdallClient")
}

func TestCreateConsensusEngine_GRPCSecondaryError_FallsBackToHTTP(t *testing.T) {
	t.Parallel()

	ethConfig := &Config{
		OverrideHeimdallClient: &mockHeimdallClient{},
		// Invalid scheme causes NewHeimdallGRPCClient to fail
		HeimdallgRPCSecondaryAddress: "ftp://localhost:50051",
		HeimdallSecondaryURL:         "http://secondary:1317",
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")

	// gRPC secondary failed, but HTTP secondary kicks in
	_, ok = borEngine.HeimdallClient.(*heimdall.FailoverHeimdallClient)
	require.True(t, ok, "Expected FailoverHeimdallClient with HTTP fallback after gRPC failure")
}

func TestCreateConsensusEngine_GRPCSecondaryError_NoHTTPFallback(t *testing.T) {
	t.Parallel()

	ethConfig := &Config{
		OverrideHeimdallClient: &mockHeimdallClient{},
		// Invalid scheme causes NewHeimdallGRPCClient to fail
		HeimdallgRPCSecondaryAddress: "ftp://localhost:50051",
		// No HeimdallSecondaryURL — no fallback available
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")

	// No secondary available, so no failover wrapper
	_, ok = borEngine.HeimdallClient.(*heimdall.FailoverHeimdallClient)
	require.False(t, ok, "Expected no FailoverHeimdallClient when both gRPC and HTTP secondary fail/absent")
}

func TestCreateConsensusEngine_GRPCSecondaryUsesSecondaryHTTPURL(t *testing.T) {
	t.Parallel()

	ethConfig := &Config{
		OverrideHeimdallClient:       &mockHeimdallClient{},
		HeimdallURL:                  "http://primary:1317",
		HeimdallSecondaryURL:         "http://secondary:1317",
		HeimdallgRPCSecondaryAddress: "localhost:50051",
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")

	// gRPC secondary should be created successfully and wrap in failover.
	// gRPC takes priority over HTTP secondary when both are available.
	_, ok = borEngine.HeimdallClient.(*heimdall.FailoverHeimdallClient)
	require.True(t, ok, "Expected FailoverHeimdallClient (gRPC secondary takes priority over HTTP)")
}

func TestCreateConsensusEngine_WSWithSecondary(t *testing.T) {
	t.Parallel()

	ethConfig := &Config{
		OverrideHeimdallClient:     &mockHeimdallClient{},
		HeimdallWSAddress:          "ws://localhost:26657",
		HeimdallWSSecondaryAddress: "ws://secondary:26657",
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")

	// WS client should be created
	require.NotNil(t, borEngine.HeimdallWSClient, "Expected non-nil HeimdallWSClient")

	_, ok = borEngine.HeimdallWSClient.(*heimdallws.HeimdallWSClient)
	require.True(t, ok, "Expected HeimdallWSClient type")
}

func TestCreateConsensusEngine_WSPrimaryOnly(t *testing.T) {
	t.Parallel()

	ethConfig := &Config{
		OverrideHeimdallClient: &mockHeimdallClient{},
		HeimdallWSAddress:      "ws://localhost:26657",
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")

	require.NotNil(t, borEngine.HeimdallWSClient, "Expected non-nil HeimdallWSClient")

	_, ok = borEngine.HeimdallWSClient.(*heimdallws.HeimdallWSClient)
	require.True(t, ok, "Expected HeimdallWSClient type")
}

func TestCreateConsensusEngine_NoWSAddress(t *testing.T) {
	t.Parallel()

	ethConfig := &Config{
		OverrideHeimdallClient: &mockHeimdallClient{},
		// No HeimdallWSAddress set
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")

	require.Nil(t, borEngine.HeimdallWSClient, "Expected nil HeimdallWSClient when no WS address configured")
}
