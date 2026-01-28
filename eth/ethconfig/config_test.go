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
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/checkpoint"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall/milestone"
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

func TestCreateConsensusEngine_WithoutHeimdall(t *testing.T) {
	t.Parallel()
	ethConfig := &Config{WithoutHeimdall: true}

	engine, err := CreateConsensusEngine(newTestBorChainConfig(), ethConfig, rawdb.NewMemoryDatabase(), nil)
	require.NoError(t, err)
	defer engine.Close()

	_, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")
}
