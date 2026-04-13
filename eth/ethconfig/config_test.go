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
	"github.com/stretchr/testify/assert"
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

func TestCreateConsensusEngine_CommaSeparatedHeimdallURL(t *testing.T) {
	t.Parallel()
	ethConfig := &Config{
		HeimdallURL: "http://primary:1317,http://secondary:1317",
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")

	_, ok = borEngine.HeimdallClient.(*heimdall.MultiHeimdallClient)
	require.True(t, ok, "Expected HeimdallClient to be wrapped in MultiHeimdallClient")
}

func TestCreateConsensusEngine_SingleHeimdallURL(t *testing.T) {
	t.Parallel()
	ethConfig := &Config{
		HeimdallURL: "http://primary:1317",
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")

	// Single URL should NOT produce a MultiHeimdallClient
	_, ok = borEngine.HeimdallClient.(*heimdall.MultiHeimdallClient)
	require.False(t, ok, "Expected no MultiHeimdallClient for single URL")
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

func TestCreateConsensusEngine_CommaSeparatedGRPC(t *testing.T) {
	t.Parallel()
	ethConfig := &Config{
		HeimdallURL:         "http://primary:1317,http://secondary:1317",
		HeimdallgRPCAddress: "localhost:50051,localhost:50052",
	}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")

	_, ok = borEngine.HeimdallClient.(*heimdall.MultiHeimdallClient)
	require.True(t, ok, "Expected MultiHeimdallClient with multiple gRPC endpoints")
}

func TestCreateConsensusEngine_GRPCInitFailsFallsBackToHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		heimdallURL    string
		grpcAddress    string
		expectFailover bool
	}{
		{
			// gRPC uses unsupported scheme → NewHeimdallGRPCClient fails.
			// Fallback appends HTTP client for httpURLs[0]; httpURLs[1] also
			// gets an HTTP client via the else-if branch → 2 clients → failover.
			name:           "with HTTP URL available",
			heimdallURL:    "http://a:1317,http://b:1317",
			grpcAddress:    "ftp://invalid:50051",
			expectFailover: true,
		},
		{
			// gRPC[0] succeeds (localhost is allowed), gRPC[1] fails (bad scheme).
			// i=1 >= len(httpURLs)=1 so no HTTP fallback is added → only 1 client.
			name:           "without HTTP URL at that index",
			heimdallURL:    "http://a:1317",
			grpcAddress:    "localhost:50051,ftp://invalid:50052",
			expectFailover: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ethConfig := &Config{
				HeimdallURL:         tt.heimdallURL,
				HeimdallgRPCAddress: tt.grpcAddress,
			}

			engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
			require.NoError(t, err)
			defer engine.Close()

			borEngine, ok := engine.(*bor.Bor)
			require.True(t, ok, "Expected Bor consensus engine")

			_, ok = borEngine.HeimdallClient.(*heimdall.MultiHeimdallClient)
			require.Equal(t, tt.expectFailover, ok)
		})
	}
}

func TestCreateConsensusEngine_WSAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr string
	}{
		{"comma-separated", "ws://localhost:26657,ws://secondary:26657"},
		{"primary only", "ws://localhost:26657"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ethConfig := &Config{
				OverrideHeimdallClient: &mockHeimdallClient{},
				HeimdallWSAddress:      tt.addr,
			}

			engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
			require.NoError(t, err)
			defer engine.Close()

			borEngine, ok := engine.(*bor.Bor)
			require.True(t, ok, "Expected Bor consensus engine")

			require.NotNil(t, borEngine.HeimdallWSClient, "Expected non-nil HeimdallWSClient")

			_, ok = borEngine.HeimdallWSClient.(*heimdallws.HeimdallWSClient)
			require.True(t, ok, "Expected HeimdallWSClient type")
		})
	}
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

func TestParseURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{"empty string", "", nil},
		{"single URL", "http://localhost:1317", []string{"http://localhost:1317"}},
		{"two URLs", "http://a:1317,http://b:1317", []string{"http://a:1317", "http://b:1317"}},
		{"three URLs", "http://a:1317,http://b:1317,http://c:1317", []string{"http://a:1317", "http://b:1317", "http://c:1317"}},
		{"whitespace trimmed", " http://a:1317 , http://b:1317 ", []string{"http://a:1317", "http://b:1317"}},
		{"trailing comma", "http://a:1317,", []string{"http://a:1317"}},
		{"leading comma", ",http://a:1317", []string{"http://a:1317"}},
		{"empty entries filtered", "http://a:1317,,http://b:1317", []string{"http://a:1317", "http://b:1317"}},
		{"only commas", ",,,", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := parseURLs(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
