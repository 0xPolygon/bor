package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

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
