package downloader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

type gatedHeaderVerifier struct {
	consensus.Engine
	enabled atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (e *gatedHeaderVerifier) VerifyHeaders(chain consensus.ChainHeaderReader, headers []*types.Header) (chan<- struct{}, <-chan error) {
	if e.enabled.Load() {
		e.once.Do(func() { close(e.entered) })
		<-e.release
	}
	return e.Engine.VerifyHeaders(chain, headers)
}

func newShutdownTestSkeleton(t *testing.T, head *types.Header) *skeleton {
	t.Helper()
	db := rawdb.NewMemoryDatabase()
	t.Cleanup(func() { _ = db.Close() })
	rawdb.WriteSkeletonHeader(db, head)
	progress, err := json.Marshal(&skeletonProgress{Subchains: []*subchain{{Head: 0, Tail: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	rawdb.WriteSkeletonSyncStatus(db, progress)
	return &skeleton{db: db}
}

func TestFullSyncPrunedSideChainStopInsert(t *testing.T) {
	engine := ethash.NewFaker()
	genesis := &core.Genesis{Config: params.AllEthashProtocolChanges, BaseFee: big.NewInt(params.InitialBaseFee)}
	generationDB, blocks, _ := core.GenerateChainWithGenesis(genesis, engine, 2*state.TriesInMemory, nil)
	sideBlocks, _ := core.GenerateChain(genesis.Config, blocks[4], engine, generationDB, 3, func(_ int, block *core.BlockGen) {
		block.SetCoinbase(common.Address{1})
	})
	gate := &gatedHeaderVerifier{Engine: engine, entered: make(chan struct{}), release: make(chan struct{})}
	chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), genesis, gate, core.DefaultConfig().WithStateScheme(rawdb.HashScheme))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(chain.Stop)
	if _, err := chain.InsertChain(blocks, false); err != nil {
		t.Fatal(err)
	}
	if chain.HasBlockAndState(blocks[4].Hash(), blocks[4].NumberU64()) {
		t.Fatal("test ancestor was not pruned")
	}

	gate.enabled.Store(true)
	insertDone := make(chan error, 1)
	go func() {
		_, err := chain.InsertChain(sideBlocks, false)
		insertDone <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("sidechain import did not reach header verification")
	}
	chain.StopInsert()
	close(gate.release)
	if err := <-insertDone; !errors.Is(err, core.ErrInsertionInterrupted) {
		t.Fatalf("pruned-sidechain interruption = %v, want %v", err, core.ErrInsertionInterrupted)
	}
}

type insertErrorChain struct {
	*core.BlockChain
	insertErr    error
	beforeReturn func()
}

func (c *insertErrorChain) InsertChain(types.Blocks, bool) (int, error) {
	if c.beforeReturn != nil {
		c.beforeReturn()
	}
	return 0, c.insertErr
}

func TestFullSyncInsertionInterruptDoesNotReportBadBlock(t *testing.T) {
	engine := ethash.NewFaker()
	genesis := &core.Genesis{Config: params.AllEthashProtocolChanges, BaseFee: big.NewInt(params.InitialBaseFee)}
	_, blocks, _ := core.GenerateChainWithGenesis(genesis, engine, 1, nil)
	chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), genesis, engine, core.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(chain.Stop)
	reported := make(chan *types.Header, 1)
	d := &Downloader{
		blockchain: &insertErrorChain{BlockChain: chain, insertErr: core.ErrInsertionInterrupted},
		quitCh:     make(chan struct{}),
		skeleton:   newShutdownTestSkeleton(t, chain.CurrentBlock()),
		badBlock:   func(invalid *types.Header, _ *types.Header) { reported <- invalid },
	}
	err = d.importBlockResults(fetchResultsFor(blocks))
	if !errors.Is(err, core.ErrInsertionInterrupted) || !errors.Is(err, errInvalidChain) {
		t.Fatalf("unexpected import error: %v", err)
	}
	select {
	case header := <-reported:
		t.Fatalf("local insertion interruption reported block %d as BAD BLOCK", header.Number)
	default:
	}
}

func TestFullSyncInvalidDataReportsDuringShutdown(t *testing.T) {
	engine := ethash.NewFaker()
	genesis := &core.Genesis{Config: params.AllEthashProtocolChanges, BaseFee: big.NewInt(params.InitialBaseFee)}
	_, blocks, _ := core.GenerateChainWithGenesis(genesis, engine, 1, nil)
	chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), genesis, engine, core.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(chain.Stop)
	insertChain := &insertErrorChain{BlockChain: chain, insertErr: core.ErrReceiptRootMismatch}
	reported := make(chan *types.Header, 1)
	dropped := make(chan string, 1)
	d := &Downloader{
		blockchain: insertChain,
		quitCh:     make(chan struct{}),
		skeleton:   newShutdownTestSkeleton(t, chain.CurrentBlock()),
		badBlock:   func(invalid *types.Header, _ *types.Header) { reported <- invalid },
		peers:      newPeerSet(),
		dropPeer:   func(id string) { dropped <- id },
	}
	insertChain.beforeReturn = func() { close(d.quitCh) }
	err = d.importBlockResults(fetchResultsFor(blocks))
	if !errors.Is(err, core.ErrReceiptRootMismatch) || !errors.Is(err, errInvalidChain) {
		t.Fatalf("unexpected invalid-data error: %v", err)
	}
	select {
	case header := <-reported:
		if header.Hash() != blocks[0].Hash() {
			t.Fatalf("reported wrong block %s", header.Hash())
		}
	default:
		t.Fatal("genuine invalid data was not reported during shutdown")
	}
	peer := newPeerConnection("peer", eth.ETH69, nil, log.New())
	if err := d.peers.Register(peer); err != nil {
		t.Fatal(err)
	}
	if !d.handleSyncFailure(peer, peer.id, err) {
		t.Fatal("genuine invalid data did not reach peer response handling")
	}
	select {
	case id := <-dropped:
		if id != peer.id {
			t.Fatalf("dropped wrong peer %q", id)
		}
	default:
		t.Fatal("genuine invalid data did not drop its supplier")
	}
}

func TestEngineShutdownAbortHasNoPeerPenalty(t *testing.T) {
	dropped := make(chan string, 1)
	d := &Downloader{peers: newPeerSet(), dropPeer: func(id string) { dropped <- id }}
	peer := newPeerConnection("peer", eth.ETH69, nil, log.New())
	if err := d.peers.Register(peer); err != nil {
		t.Fatal(err)
	}
	abort := fmt.Errorf("import: %w: %w", core.ErrStateSyncProcessing, consensus.NewEngineStoppedError(context.Canceled))
	if reason, ok := classifySyncFailure(abort); ok {
		t.Fatalf("shutdown abort classified for peer response %q", reason)
	}
	if d.handleSyncFailure(peer, peer.id, abort) {
		t.Fatal("shutdown abort reached peer response handling")
	}
	if peer.backedOff() {
		t.Fatal("shutdown abort backed off the supplier")
	}
	select {
	case id := <-dropped:
		t.Fatalf("shutdown abort dropped supplier %q", id)
	default:
	}
}

func TestInvalidChainStillDropsWhenDownloaderIsStopping(t *testing.T) {
	dropped := make(chan string, 1)
	d := &Downloader{peers: newPeerSet(), quitCh: make(chan struct{}), dropPeer: func(id string) { dropped <- id }}
	peer := newPeerConnection("peer", eth.ETH69, nil, log.New())
	if err := d.peers.Register(peer); err != nil {
		t.Fatal(err)
	}
	close(d.quitCh)
	failure := fmt.Errorf("%w: %w", errInvalidChain, core.ErrStateSyncMismatch)
	if !d.handleSyncFailure(peer, peer.id, failure) {
		t.Fatal("genuine invalid-chain failure was not handled")
	}
	select {
	case id := <-dropped:
		if id != peer.id {
			t.Fatalf("dropped wrong peer %q", id)
		}
	default:
		t.Fatal("genuine invalid-chain failure was hidden by shutdown")
	}
}

func TestSnapHeaderStopInsertIsNotInvalidChain(t *testing.T) {
	engine := ethash.NewFaker()
	genesis := &core.Genesis{Config: params.AllEthashProtocolChanges, BaseFee: big.NewInt(params.InitialBaseFee)}
	_, blocks, _ := core.GenerateChainWithGenesis(genesis, engine, 1, nil)
	chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), genesis, engine, core.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(chain.Stop)

	chain.StopInsert()
	originalHead := chain.CurrentHeader()
	var output bytes.Buffer
	previousLogger := log.Root()
	log.SetDefault(log.NewLogger(log.JSONHandler(&output)))
	defer log.SetDefault(previousLogger)

	dropped := make(chan string, 1)
	d := &Downloader{
		blockchain:   chain,
		lightchain:   chain,
		headerProcCh: make(chan *headerTask, 1),
		cancelCh:     make(chan struct{}),
		quitCh:       make(chan struct{}),
		peers:        newPeerSet(),
		dropPeer:     func(id string) { dropped <- id },
	}
	d.mode.Store(uint32(SnapSync))
	header := blocks[0].Header()
	d.headerProcCh <- &headerTask{headers: []*types.Header{header}, hashes: []common.Hash{header.Hash()}}
	err = d.processHeaders(0, big.NewInt(0), big.NewInt(1000000), false)
	if !errors.Is(err, core.ErrInsertionInterrupted) {
		t.Fatalf("insertion interruption = %v, want %v", err, core.ErrInsertionInterrupted)
	}
	if errors.Is(err, consensus.ErrEngineStopped) {
		t.Fatalf("local insertion interruption became engine shutdown: %v", err)
	}
	if bytes.Contains(output.Bytes(), []byte("Invalid header encountered")) {
		t.Fatalf("insertion interruption logged as invalid peer data: %s", output.String())
	}
	if head := chain.CurrentHeader(); head.Hash() != originalHead.Hash() {
		t.Fatalf("head changed from %s to %s", originalHead.Hash(), head.Hash())
	}

	peer := newPeerConnection("peer", eth.ETH69, nil, log.New())
	if err := d.peers.Register(peer); err != nil {
		t.Fatal(err)
	}
	if d.handleSyncFailure(peer, peer.id, err) {
		t.Fatal("insertion interruption reached peer response handling")
	}
	if peer.backedOff() {
		t.Fatal("insertion interruption backed off the supplier")
	}
	select {
	case id := <-dropped:
		t.Fatalf("insertion interruption dropped supplier %q", id)
	default:
	}
}

func TestLegacySyncSkipsRetryWarningForInsertionInterrupt(t *testing.T) {
	interrupted := fmt.Errorf("sync: %w", core.ErrInsertionInterrupted)
	d := &Downloader{
		peers: newPeerSet(),
		synchroniseMock: func(string, common.Hash) error {
			return interrupted
		},
	}
	var output bytes.Buffer
	previousLogger := log.Root()
	log.SetDefault(log.NewLogger(log.JSONHandler(&output)))
	defer log.SetDefault(previousLogger)

	err := d.LegacySync("peer", common.Hash{}, big.NewInt(0), nil, SnapSync)
	if !errors.Is(err, core.ErrInsertionInterrupted) {
		t.Fatalf("sync error = %v, want insertion interruption", err)
	}
	if bytes.Contains(output.Bytes(), []byte("Synchronisation failed, retrying")) {
		t.Fatalf("insertion interruption emitted a retry warning: %s", output.String())
	}
}

func TestInvalidHeaderChainErrorDoesNotExposeDeadline(t *testing.T) {
	header := &types.Header{Number: big.NewInt(1)}
	err := invalidHeaderChainError([]*types.Header{header}, []common.Hash{header.Hash()}, 0, context.DeadlineExceeded)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("non-shutdown deadline newly exposed through invalid chain: %v", err)
	}
	if reason, ok := classifySyncFailure(err); !ok || reason != peerFailureInvalidChain {
		t.Fatalf("deadline classification = %q, %t, want invalid-chain, true", reason, ok)
	}
}

type shutdownTestChain struct{ BlockChain }

func (*shutdownTestChain) GetTd(common.Hash, uint64) *big.Int { return big.NewInt(100) }
func (*shutdownTestChain) CurrentBlock() *types.Header {
	return &types.Header{Number: big.NewInt(1)}
}

func (*shutdownTestChain) CurrentHeader() *types.Header {
	return &types.Header{Number: big.NewInt(1)}
}

func (*shutdownTestChain) CurrentSnapBlock() *types.Header {
	return &types.Header{Number: big.NewInt(1)}
}

type shutdownTestLightChain struct {
	LightChain
	insertErr error
}

func (c *shutdownTestLightChain) InsertHeaderChain([]*types.Header) (int, error) {
	return 0, c.insertErr
}

func TestSnapHeaderImportPreservesShutdownClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		insertErr error
		stopped   bool
	}{
		{name: "engine shutdown", insertErr: consensus.NewEngineStoppedError(context.Canceled), stopped: true},
		{name: "invalid receipt", insertErr: core.ErrReceiptRootMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			previousLogger := log.Root()
			log.SetDefault(log.NewLogger(log.JSONHandler(&output)))
			defer log.SetDefault(previousLogger)

			d := &Downloader{
				blockchain:   &shutdownTestChain{},
				lightchain:   &shutdownTestLightChain{insertErr: tc.insertErr},
				headerProcCh: make(chan *headerTask, 1),
				cancelCh:     make(chan struct{}),
				quitCh:       make(chan struct{}),
				peers:        newPeerSet(),
			}
			close(d.quitCh) // A genuine validation failure must survive an in-progress stop.
			d.mode.Store(uint32(SnapSync))
			header := &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(1)}
			d.headerProcCh <- &headerTask{headers: []*types.Header{header}, hashes: []common.Hash{header.Hash()}}
			err := d.processHeaders(0, big.NewInt(0), big.NewInt(1000000), false)
			if err == nil {
				t.Fatal("missing insertion failure")
			}
			if errors.Is(err, consensus.ErrEngineStopped) != tc.stopped {
				t.Errorf("shutdown classification = %v, want %v: %v", errors.Is(err, consensus.ErrEngineStopped), tc.stopped, err)
			}
			if tc.stopped && bytes.Contains(output.Bytes(), []byte("Invalid header encountered")) {
				t.Errorf("engine shutdown logged as invalid peer data: %s", output.String())
			}
			if !tc.stopped && !bytes.Contains(output.Bytes(), []byte("Invalid header encountered")) {
				t.Errorf("genuine validation failure lost its invalid-header warning during stop: %s", output.String())
			}

			dropped := make(chan string, 1)
			d.dropPeer = func(id string) { dropped <- id }
			peer := newPeerConnection("peer", eth.ETH69, nil, log.New())
			if err := d.peers.Register(peer); err != nil {
				t.Fatal(err)
			}
			punished := d.handleSyncFailure(peer, peer.id, err)
			if punished == tc.stopped {
				t.Errorf("peer punishment = %v for engine shutdown = %v", punished, tc.stopped)
			}
			select {
			case id := <-dropped:
				if tc.stopped {
					t.Errorf("engine shutdown dropped supplier %q", id)
				}
			default:
				if !tc.stopped {
					t.Error("invalid-chain failure did not drop supplier")
				}
			}
		})
	}
}
