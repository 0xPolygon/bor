package eth

import (
	"bytes"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/stateless"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/fetcher"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// forgedPreStateFixture is one valid block, its honest witness, and a copy of
// that witness whose parent header carries a forged state root.
type forgedPreStateFixture struct {
	gspec  *core.Genesis
	block  *types.Block
	honest *stateless.Witness
	forged *stateless.Witness
}

func newForgedPreStateFixture(t *testing.T) *forgedPreStateFixture {
	t.Helper()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	gspec := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc:  types.GenesisAlloc{sender: {Balance: big.NewInt(10_000_000_000_000_000)}},
	}
	_, blocks, _ := core.GenerateChainWithGenesis(gspec, ethash.NewFaker(), 1, func(_ int, b *core.BlockGen) {
		b.SetCoinbase(common.Address{1})
		tx, signErr := types.SignTx(types.NewTransaction(0, common.HexToAddress("0x1234"), big.NewInt(1000), 21_000, big.NewInt(2_000_000_000), nil), types.HomesteadSigner{}, key)
		require.NoError(t, signErr)
		b.AddTx(tx)
	})
	producer, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), gspec, ethash.NewFaker(), nil)
	require.NoError(t, err)
	honest, err := producer.InsertBlockWithoutSetHead(blocks[0], true)
	require.NoError(t, err)
	producer.Stop()

	forged := honest.Copy()
	parent := types.CopyHeader(forged.Headers[0])
	parent.Root = common.HexToHash("0xdeadbeef")
	forged.Headers[0] = parent
	return &forgedPreStateFixture{gspec: gspec, block: blocks[0], honest: honest, forged: forged}
}

// statelessVictim is a stateless node at the parent of the fixture block, wired
// with the real handler, witness manager, block fetcher and stateless import.
type statelessVictim struct {
	db      ethdb.Database
	chain   *core.BlockChain
	h       *handler
	fetcher *fetcher.BlockFetcher
	imports atomic.Int32
	fetches atomic.Int32
	stop    chan struct{} // closed at cleanup so stub fetch goroutines exit

	mu       sync.Mutex
	struck   []string // peers struck, in order
	excluded []string // peers excluded as a source, in order
}

func newStatelessVictim(t *testing.T, fx *forgedPreStateFixture) *statelessVictim {
	t.Helper()
	cfg := core.DefaultConfig()
	cfg.Stateless = true
	db := rawdb.NewMemoryDatabase()
	chain, err := core.NewBlockChain(db, fx.gspec, ethash.NewFaker(), cfg)
	require.NoError(t, err)
	t.Cleanup(chain.Stop)

	h, err := newHandler(&handlerConfig{
		Database:   db,
		Chain:      chain,
		TxPool:     newTestTxPool(),
		Network:    1,
		Sync:       downloader.SnapSync,
		BloomCache: 1,
	})
	require.NoError(t, err)

	v := &statelessVictim{db: db, chain: chain, h: h, stop: make(chan struct{})}
	insertChain := func(blocks types.Blocks, witnesses []*stateless.Witness) (int, error) {
		v.imports.Add(1)
		return chain.InsertChainStateless(blocks, witnesses)
	}
	getBlock := func(hash common.Hash) *types.Block { return chain.GetBlockByHash(hash) }
	v.fetcher = fetcher.NewBlockFetcher(false, nil, getBlock, func(*types.Header) error { return nil },
		func(*types.Block, *stateless.Witness, bool) {}, func() uint64 { return chain.CurrentHeader().Number.Uint64() }, chain.CurrentHeader,
		nil, insertChain, func(string) {}, false, true, 30_000_000, h.lookupSignedWitnessHash, nil)
	v.fetcher.SetWitnessServerStriker(func(id string) {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.struck = append(v.struck, id)
	})
	v.fetcher.SetWitnessSourceExcluder(func(id string, _ common.Hash) {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.excluded = append(v.excluded, id)
	})
	h.blockFetcher = v.fetcher
	h.Start(1000)
	t.Cleanup(h.Stop)
	t.Cleanup(func() { close(v.stop) }) // runs before h.Stop
	return v
}

// serve answers a stub witness request from peer, unless the request is
// cancelled or the test ends first.
func (v *statelessVictim) serve(peer string, w *stateless.Witness, sink chan *ethproto.Response) *ethproto.Request {
	req := &ethproto.Request{Peer: peer, Cancel: make(chan struct{})}
	res := &ethproto.Response{Req: req, Res: []*stateless.Witness{w}, Time: time.Millisecond, Done: make(chan error, 1)}
	go func() {
		select {
		case sink <- res:
		case <-req.Cancel:
		case <-v.stop:
		}
	}()
	return req
}

// honestFetcher serves the honest witness when the fetcher re-fetches.
func (v *statelessVictim) honestFetcher(honest *stateless.Witness) func(common.Hash, chan *ethproto.Response) (*ethproto.Request, error) {
	return func(_ common.Hash, sink chan *ethproto.Response) (*ethproto.Request, error) {
		v.fetches.Add(1)
		return v.serve("honest-witness-server", honest, sink), nil
	}
}

func signAnnouncement(t *testing.T, block *types.Block, body []byte) wit.SignedWitnessAnnouncement {
	t.Helper()
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	ann := wit.SignedWitnessAnnouncement{
		BlockHash:   block.Hash(),
		BlockNumber: block.NumberU64(),
		WitnessHash: stateless.WitnessCommitHash(body),
		WitnessSize: uint64(len(body)),
	}
	digest := wit.WitnessAnnouncementSigningHash(ann.BlockHash, ann.BlockNumber, ann.WitnessHash, ann.WitnessSize)
	ann.Signature, err = crypto.Sign(digest.Bytes(), key)
	require.NoError(t, err)
	return ann
}

func encodeWitness(t *testing.T, w *stateless.Witness) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, w.EncodeRLP(&buf))
	return buf.Bytes()
}

// requireRecovered checks that the block imported with the honest witness after
// the forged one failed, that exactly the attacker was excluded, and that it was
// struck only when its bytes contradicted a verified BP commitment (struck).
func (v *statelessVictim) requireRecovered(t *testing.T, attacker string, struck bool) {
	t.Helper()
	require.Eventually(t, func() bool { return v.chain.CurrentBlock().Number.Uint64() == 1 }, 5*time.Second, 10*time.Millisecond,
		"the valid block must import with the honest witness after the forged one fails")
	require.GreaterOrEqual(t, v.imports.Load(), int32(2), "the forged witness import must fail first")
	require.Positive(t, v.fetches.Load(), "the honest witness must be re-fetched")
	v.mu.Lock()
	defer v.mu.Unlock()
	if struck {
		require.Equal(t, []string{attacker}, v.struck, "only the peer that chose the forged witness is struck")
	} else {
		require.Empty(t, v.struck, "unproven bytes may be the BP's, relayed by an honest peer: never struck")
	}
	require.Equal(t, []string{attacker}, v.excluded, "only the peer that chose the forged witness is excluded for the block")
}

// TestForgedPreStateWitnessDeferredMatchIsChargedAndRetried: before the block
// header is local, a peer signs a commitment to its own forged witness with any
// key and pushes the matching bytes. The failed import must exclude the pusher
// and re-fetch an honest witness, not forget the block. The pusher is not
// struck: a deferred commitment is unverified, so the bytes may be the BP's.
func TestForgedPreStateWitnessDeferredMatchIsChargedAndRetried(t *testing.T) {
	fx := newForgedPreStateFixture(t)
	v := newStatelessVictim(t, fx)
	witH := (*witHandler)(v.h)
	attacker, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	ann := signAnnouncement(t, fx.block, encodeWitness(t, fx.forged))
	require.NoError(t, witH.handleSignedWitnessAnnouncements(attacker, []wit.SignedWitnessAnnouncement{ann}))
	require.True(t, v.h.deferredAnnounces.has(fx.block.Hash()))

	require.NoError(t, witH.handleWitnessBroadcast(attacker, fx.forged))
	require.NoError(t, v.fetcher.InjectBlockWithWitnessRequirement("block-peer", fx.block, v.honestFetcher(fx.honest)))
	v.requireRecovered(t, attacker.ID(), false)
}

// TestForgedPreStateWitnessWithinSignedBandIsChargedAndRetried: with a verified
// announcement committing to the honest witness, a peer pushes a different,
// smaller forged witness inside the signed size band. The pre-state failure
// must be charged to the pusher and the block re-fetched.
func TestForgedPreStateWitnessWithinSignedBandIsChargedAndRetried(t *testing.T) {
	fx := newForgedPreStateFixture(t)
	v := newStatelessVictim(t, fx)
	witH := (*witHandler)(v.h)
	attacker, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	// A local header lets the announcement verify on this non-Bor test chain.
	rawdb.WriteHeader(v.db, fx.block.Header())
	ann := signAnnouncement(t, fx.block, encodeWitness(t, fx.honest))
	require.NoError(t, witH.handleSignedWitnessAnnouncements(attacker, []wit.SignedWitnessAnnouncement{ann}))
	verified, ok := v.h.signedWitnesses.get(fx.block.Hash())
	require.True(t, ok, "the announcement must be verified")
	require.NotEqual(t, verified.WitnessHash, stateless.WitnessCommitHash(encodeWitness(t, fx.forged)))

	require.NoError(t, witH.handleWitnessBroadcast(attacker, fx.forged))
	require.NoError(t, v.fetcher.InjectBlockWithWitnessRequirement("block-peer", fx.block, v.honestFetcher(fx.honest)))
	v.requireRecovered(t, attacker.ID(), true)
}

// TestForgedPreStateWitnessFetchedWithDeferredAnnounceIsChargedAndRetried: at
// the tip the block's header is not local, so the producer's announcement stays
// deferred and the paged fetch has no verified commitment to check against. A
// server that answers the fetch with a forged witness must still be excluded
// and the block re-fetched from another server. It is not struck: unproven
// bytes may be the BP's, relayed by an honest peer.
func TestForgedPreStateWitnessFetchedWithDeferredAnnounceIsChargedAndRetried(t *testing.T) {
	fx := newForgedPreStateFixture(t)
	v := newStatelessVictim(t, fx)
	witH := (*witHandler)(v.h)
	relayer, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	ann := signAnnouncement(t, fx.block, encodeWitness(t, fx.honest))
	require.NoError(t, witH.handleSignedWitnessAnnouncements(relayer, []wit.SignedWitnessAnnouncement{ann}))
	require.True(t, v.h.deferredAnnounces.has(fx.block.Hash()))
	_, verified := v.h.signedWitnesses.get(fx.block.Hash())
	require.False(t, verified, "the announcement must still be deferred")

	// The first fetch is answered by the attacker, later ones by an honest server.
	honest := v.honestFetcher(fx.honest)
	var served atomic.Bool
	fetch := func(hash common.Hash, sink chan *ethproto.Response) (*ethproto.Request, error) {
		if served.Swap(true) {
			return honest(hash, sink)
		}
		return v.serve("attacker-server", fx.forged, sink), nil
	}
	require.NoError(t, v.fetcher.InjectBlockWithWitnessRequirement("block-peer", fx.block, fetch))
	v.requireRecovered(t, "attacker-server", false)
}

// TestForgedPreStateWitnessUnsignedPushIsChargedAndRetried: with the header
// local and no announcement on file (WIT1 fallback), a pushed forged witness's
// failure must exclude the pusher and re-fetch as well, without a strike.
func TestForgedPreStateWitnessUnsignedPushIsChargedAndRetried(t *testing.T) {
	fx := newForgedPreStateFixture(t)
	v := newStatelessVictim(t, fx)
	witH := (*witHandler)(v.h)
	attacker, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	rawdb.WriteHeader(v.db, fx.block.Header())
	require.False(t, v.h.deferredAnnounces.has(fx.block.Hash()))
	_, signed := v.h.signedWitnesses.get(fx.block.Hash())
	require.False(t, signed)

	require.NoError(t, witH.handleWitnessBroadcast(attacker, fx.forged))
	require.NoError(t, v.fetcher.InjectBlockWithWitnessRequirement("block-peer", fx.block, v.honestFetcher(fx.honest)))
	v.requireRecovered(t, attacker.ID(), false)
}
