//go:build integration

package bor

import (
	"bytes"
	"context"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	borTypes "github.com/0xPolygon/heimdall-v2/x/bor/types"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
	"google.golang.org/grpc"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdall"
	borSpan "github.com/ethereum/go-ethereum/consensus/bor/heimdall/span"
	"github.com/ethereum/go-ethereum/consensus/bor/heimdallgrpc"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/ethconfig"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/miner"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
)

func initShutdownTestNode(t *testing.T, genesis *core.Genesis, heimdallClient bor.IHeimdallClient) (*node.Node, *eth.Ethereum, error) {
	stack, err := node.New(&node.Config{
		Name:    "geth",
		DataDir: t.TempDir(),
		P2P: p2p.Config{
			ListenAddr:  "0.0.0.0:0",
			NoDiscovery: true,
			MaxPeers:    25,
		},
		UseLightweightKDF: true,
	})
	if err != nil {
		return nil, nil, err
	}
	ethereum, err := eth.New(stack, &ethconfig.Config{
		Genesis:                genesis,
		NetworkId:              genesis.Config.ChainID.Uint64(),
		SyncMode:               downloader.FullSync,
		StateScheme:            rawdb.HashScheme,
		DatabaseCache:          256,
		DatabaseHandles:        256,
		TxPool:                 legacypool.DefaultConfig,
		GPO:                    ethconfig.Defaults.GPO,
		Miner:                  miner.Config{Etherbase: addr, GasCeil: genesis.GasLimit * 11 / 10, Recommit: time.Second},
		OverrideHeimdallClient: heimdallClient,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := importValidatorKey(stack, ethereum, key); err != nil {
		return nil, nil, err
	}
	return stack, ethereum, stack.Start()
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type blockingHeimdallClient struct {
	bor.IHeimdallClient
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func newBlockingHeimdallClient(inner bor.IHeimdallClient) *blockingHeimdallClient {
	return &blockingHeimdallClient{
		IHeimdallClient: inner,
		started:         make(chan struct{}),
		closed:          make(chan struct{}),
	}
}

func (c *blockingHeimdallClient) StateSyncEvents(ctx context.Context, _ uint64, _ int64) ([]*clerk.EventRecordWithTime, error) {
	c.startOnce.Do(func() { close(c.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, heimdall.ErrShutdownDetected
	}
}

func (c *blockingHeimdallClient) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.IHeimdallClient.Close()
	})
}

type stalledSpanServer struct {
	borTypes.UnimplementedQueryServer
	entered chan struct{}
	once    sync.Once
}

func (s *stalledSpanServer) GetSpanById(ctx context.Context, _ *borTypes.QuerySpanByIdRequest) (*borTypes.QuerySpanByIdResponse, error) {
	s.once.Do(func() { close(s.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func newStalledSpanServer(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &stalledSpanServer{entered: make(chan struct{})}
	grpcServer := grpc.NewServer()
	borTypes.RegisterQueryServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)
	return listener.Addr().String(), server.entered
}

func TestUncachedSpanShutdownAbortDoesNotReportBadBlock(t *testing.T) {
	var output synchronizedBuffer
	previousLogger := log.Root()
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(&output, log.LevelInfo, true)))
	defer log.SetDefault(previousLogger)

	genesis := InitGenesis(t, keys, "./testdata/genesis.json", sprintSize)
	genesis.Config.Bor.RioBlock = big.NewInt(int64(sprintSize + 1))
	genesis.Config.Bor.StateSyncConfirmationDelay = map[string]uint64{"0": 128}
	span0 := createMockSpan(addr, genesis.Config.ChainID.String())
	span1 := loadSpanFromFile(t)
	heimdallClient := createMockHeimdall(gomock.NewController(t), &span0, span1)
	heimdallClient.EXPECT().StateSyncEvents(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]*clerk.EventRecordWithTime{getSampleEventRecord(t)}, nil).AnyTimes()
	stack, ethereum, err := initShutdownTestNode(t, genesis, heimdallClient)
	require.NoError(t, err)
	closed := false
	defer func() {
		if !closed {
			_ = stack.Close()
		}
	}()

	chain := ethereum.BlockChain()
	engine := ethereum.Engine().(*bor.Bor)
	validators := borSpan.ConvertHeimdallValSetToBorValSet(span0.ValidatorSet).Validators
	workingSpanner := getMockedSpanner(t, validators)
	engine.SetSpanner(workingSpanner)
	parent := genesis.ToBlock()
	signer := crypto.FromECDSA(key)
	for number := uint64(1); number < sprintSize; number++ {
		parent = buildNextBlock(t, engine, chain, parent, signer, genesis.Config.Bor, nil, validators, true, nil, nil)
		insertNewBlock(t, chain, parent)
	}
	sprintBlock := buildNextBlock(t, engine, chain, parent, signer, genesis.Config.Bor, nil, validators, true, nil, nil)

	address, entered := newStalledSpanServer(t)
	grpcClient, err := heimdallgrpc.NewHeimdallGRPCClient(address, "http://127.0.0.1:1", 15*time.Second)
	require.NoError(t, err)
	defer grpcClient.Close()
	shutdownSpanner := bor.NewMockSpanner(gomock.NewController(t))
	shutdownSpanner.EXPECT().GetCurrentSpan(gomock.Any(), gomock.Any(), gomock.Any()).Return(&borTypes.Span{Id: 42}, nil).AnyTimes()
	shutdownSpanner.EXPECT().GetCurrentValidatorsByHash(gomock.Any(), gomock.Any(), gomock.Any()).Return(validators, nil).AnyTimes()
	shutdownSpanner.EXPECT().GetCurrentValidatorsByBlockNrOrHash(gomock.Any(), gomock.Any(), gomock.Any()).Return(validators, nil).AnyTimes()
	engine.SetSpanner(shutdownSpanner)
	engine.SetHeimdallClient(grpcClient)
	importDone := make(chan error, 1)
	go func() {
		_, err := chain.InsertChain([]*types.Block{sprintBlock}, false)
		importDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("sprint-start import did not reach GetSpan")
	}
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- stack.Close() }()
	select {
	case err := <-shutdownDone:
		require.NoError(t, err)
		closed = true
	case <-time.After(15 * time.Second):
		t.Fatal("node shutdown remained blocked behind GetSpan")
	}
	select {
	case err := <-importDone:
		require.ErrorIs(t, err, consensus.ErrEngineStopped)
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(15 * time.Second):
		t.Fatal("aborted import did not return")
	}
	require.NotContains(t, output.String(), "BAD BLOCK")
	require.Equal(t, parent.Hash(), chain.CurrentBlock().Hash())

	db := reopenValidationDatabase(t, stack)
	defer db.Close()
	require.Nil(t, rawdb.ReadBadBlock(db, sprintBlock.Hash()))
	restartedEngine := bor.New(genesis.Config, db, nil, workingSpanner, heimdallClient, nil, engine.GenesisContractsClient, false, 0, vm.Config{})
	defer restartedEngine.Close()
	restartedChain, err := core.NewBlockChain(db, genesis, restartedEngine, core.DefaultConfig().WithStateScheme(rawdb.HashScheme))
	require.NoError(t, err)
	defer restartedChain.Stop()
	_, err = restartedChain.InsertChain([]*types.Block{sprintBlock}, false)
	require.NoError(t, err)
	require.Equal(t, sprintBlock.Hash(), restartedChain.CurrentBlock().Hash())
}

// TestShutdownAbortDoesNotReportBadBlock drives a real sprint-start import into
// StateSyncEvents, then closes the node while that call is blocked. The block
// was built and signed with a working Heimdall client, so cancellation is the
// only reason its import can fail.
func TestShutdownAbortDoesNotReportBadBlock(t *testing.T) {
	var output synchronizedBuffer
	previousLogger := log.Root()
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(&output, log.LevelInfo, true)))
	defer log.SetDefault(previousLogger)

	genesis := InitGenesis(t, keys, "./testdata/genesis.json", sprintSize)
	genesis.Config.Bor.StateSyncConfirmationDelay = map[string]uint64{"0": 128}
	span0 := createMockSpan(addr, genesis.Config.ChainID.String())
	span1 := loadSpanFromFile(t)
	ctrl := gomock.NewController(t)
	heimdallClient := createMockHeimdall(ctrl, &span0, span1)
	heimdallClient.EXPECT().StateSyncEvents(gomock.Any(), gomock.Any(), gomock.Any()).
		Return([]*clerk.EventRecordWithTime{getSampleEventRecord(t)}, nil).AnyTimes()

	stack, ethereum, err := initShutdownTestNode(t, genesis, heimdallClient)
	require.NoError(t, err)
	closed := false
	defer func() {
		if !closed {
			_ = stack.Close()
		}
	}()

	chain := ethereum.BlockChain()
	engine := ethereum.Engine().(*bor.Bor)
	validators := borSpan.ConvertHeimdallValSetToBorValSet(span0.ValidatorSet).Validators
	engine.SetSpanner(getMockedSpanner(t, validators))

	parent := genesis.ToBlock()
	signer := crypto.FromECDSA(key)
	for number := uint64(1); number < sprintSize; number++ {
		parent = buildNextBlock(t, engine, chain, parent, signer, genesis.Config.Bor, nil, validators, true, nil, nil)
		insertNewBlock(t, chain, parent)
	}
	sprintBlock := buildNextBlock(t, engine, chain, parent, signer, genesis.Config.Bor, nil, validators, true, nil, nil)

	blocked := newBlockingHeimdallClient(heimdallClient)
	engine.SetHeimdallClient(blocked)
	defer blocked.Close()
	importDone := make(chan error, 1)
	go func() {
		_, err := chain.InsertChain([]*types.Block{sprintBlock}, false)
		importDone <- err
	}()

	select {
	case <-blocked.started:
	case <-time.After(15 * time.Second):
		t.Fatal("sprint-start import did not reach StateSyncEvents")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- stack.Close() }()
	select {
	case err := <-shutdownDone:
		require.NoError(t, err)
		closed = true
	case <-time.After(15 * time.Second):
		t.Fatal("node shutdown remained blocked behind StateSyncEvents")
	}

	var importErr error
	select {
	case importErr = <-importDone:
		require.Error(t, importErr)
		require.ErrorIs(t, importErr, consensus.ErrEngineStopped)
		require.ErrorIs(t, importErr, context.Canceled)
	case <-time.After(15 * time.Second):
		t.Fatal("aborted import did not return")
	}
	require.NotContains(t, output.String(), "BAD BLOCK")
	require.Equal(t, parent.Hash(), chain.CurrentBlock().Hash())
	require.Nil(t, chain.GetBlockByHash(sprintBlock.Hash()))

	// Reopen the actual chain database after node shutdown. A shutdown abort
	// must not leave a bad-block record, and a fresh engine must accept the
	// same valid block without rebuilding or mutating it.
	kvdb, err := pebble.New(stack.ResolvePath("chaindata"), 16, 16, "", false)
	require.NoError(t, err)
	db, err := rawdb.Open(kvdb, rawdb.OpenOptions{
		Ancient: stack.ResolvePath("chaindata/ancient/chain"),
		Era:     stack.ResolvePath("chaindata/ancient/chain/era"),
	})
	require.NoError(t, err)
	defer db.Close()
	require.Nil(t, rawdb.ReadBadBlock(db, sprintBlock.Hash()))

	restartedEngine := bor.New(genesis.Config, db, nil, engine.GetSpanner(), heimdallClient, nil, engine.GenesisContractsClient, false, 0, vm.Config{})
	defer restartedEngine.Close()
	restartedChain, err := core.NewBlockChain(db, genesis, restartedEngine, core.DefaultConfig().WithStateScheme(rawdb.HashScheme))
	require.NoError(t, err)
	defer restartedChain.Stop()
	_, err = restartedChain.InsertChain([]*types.Block{sprintBlock}, false)
	require.NoError(t, err)
	require.Equal(t, sprintBlock.Hash(), restartedChain.CurrentBlock().Hash())
}
