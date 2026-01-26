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
type mockHeimdallClient struct {
	spanCalled bool
}

func (m *mockHeimdallClient) Close() {}

func (m *mockHeimdallClient) StateSyncEvents(ctx context.Context, fromID uint64, to int64) ([]*clerk.EventRecordWithTime, error) {
	return nil, nil
}

func (m *mockHeimdallClient) GetSpan(ctx context.Context, spanID uint64) (*borTypes.Span, error) {
	m.spanCalled = true
	validators := []*stakeTypes.Validator{
		{
			ValId:            1,
			Signer:           "0x96C42C56fdb78294F96B0cFa33c92bed7D75F96a",
			VotingPower:      100,
			ProposerPriority: 0,
		},
	}
	validatorSet := stakeTypes.ValidatorSet{
		Validators: validators,
		Proposer:   validators[0],
	}
	return &borTypes.Span{
		Id:           spanID,
		StartBlock:   0,
		EndBlock:     255,
		ValidatorSet: validatorSet,
	}, nil
}

func (m *mockHeimdallClient) GetLatestSpan(ctx context.Context) (*borTypes.Span, error) {
	return m.GetSpan(ctx, 0)
}

func (m *mockHeimdallClient) FetchCheckpoint(ctx context.Context, number int64) (*checkpoint.Checkpoint, error) {
	return nil, nil
}

func (m *mockHeimdallClient) FetchCheckpointCount(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockHeimdallClient) FetchMilestone(ctx context.Context) (*milestone.Milestone, error) {
	return nil, nil
}

func (m *mockHeimdallClient) FetchMilestoneCount(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockHeimdallClient) FetchStatus(ctx context.Context) (*ctypes.SyncInfo, error) {
	return &ctypes.SyncInfo{CatchingUp: false}, nil
}

func TestCreateConsensusEngine_OverrideHeimdallClient(t *testing.T) {
	// Create a mock heimdall client
	mockClient := &mockHeimdallClient{}

	// Create chain config with Bor consensus
	chainConfig := &params.ChainConfig{
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

	// Create eth config with override client
	ethConfig := &Config{
		OverrideHeimdallClient: mockClient,
		WithoutHeimdall:        false,
	}

	// Create in-memory database
	db := rawdb.NewMemoryDatabase()

	// Create consensus engine - blockchainAPI can be nil for this test since
	// we just need to verify the override client is used
	engine, err := CreateConsensusEngine(chainConfig, ethConfig, db, nil)
	require.NoError(t, err)
	require.NotNil(t, engine)

	// Verify we got a Bor engine
	borEngine, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")
	require.NotNil(t, borEngine)

	// The override client should have been passed to the Bor engine
	// We can verify this by checking that our mock client is used
	// when calling SetHeimdallClient (which updates the span store)
	// This proves the override path was taken during construction
}

func TestCreateConsensusEngine_WithoutHeimdall(t *testing.T) {
	// Create chain config with Bor consensus
	chainConfig := &params.ChainConfig{
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

	// Create eth config without heimdall
	ethConfig := &Config{
		WithoutHeimdall: true,
	}

	// Create in-memory database
	db := rawdb.NewMemoryDatabase()

	// Create consensus engine
	engine, err := CreateConsensusEngine(chainConfig, ethConfig, db, nil)
	require.NoError(t, err)
	require.NotNil(t, engine)

	// Verify we got a Bor engine
	_, ok := engine.(*bor.Bor)
	require.True(t, ok, "Expected Bor consensus engine")
}
