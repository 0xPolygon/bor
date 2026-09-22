package eth

import (
	"bytes"
	"fmt"
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
	"github.com/ethereum/go-ethereum/eth/downloader"
	"github.com/ethereum/go-ethereum/eth/fetcher"
	ethproto "github.com/ethereum/go-ethereum/eth/protocols/eth"
	"github.com/ethereum/go-ethereum/eth/protocols/wit"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/require"
)

// TestHandleWitnessBroadcastDivergentBodyImportFailureChargesPusher drives the
// push path end to end from the wire handler: a BP-signed announcement on
// file, a within-band body with another hash pushed by NewWitness (cached
// before its block), the block injected, the import failing with a
// witness-attributable error — and the PUSHER struck and excluded as a source
// for the block, the witness fetched from another peer, the block imported.
// It pins the provenance bit handleWitnessBroadcast hands to InjectWitness:
// without it the failure would be forgotten for free, as it was before.
func TestHandleWitnessBroadcastDivergentBodyImportFailureChargesPusher(t *testing.T) {
	// Same construction as newTestHandler, but the block fetcher is swapped
	// for one with a controllable insertChain BEFORE the handler starts, so
	// the chain syncer starts and stops the swapped fetcher itself.
	db := rawdb.NewMemoryDatabase()
	gspec := &core.Genesis{
		Config: params.TestChainConfig,
		Alloc:  types.GenesisAlloc{testAddr: {Balance: big.NewInt(1000000)}},
	}
	chain, err := core.NewBlockChain(db, gspec, ethash.NewFaker(), nil)
	require.NoError(t, err)
	defer chain.Stop()
	hh, err := newHandler(&handlerConfig{
		Database:   db,
		Chain:      chain,
		TxPool:     newTestTxPool(),
		Network:    1,
		Sync:       downloader.SnapSync,
		BloomCache: 1,
	})
	require.NoError(t, err)

	head := chain.CurrentHeader()
	header := &types.Header{
		ParentHash: head.Hash(),
		Number:     new(big.Int).Add(head.Number, big.NewInt(1)),
		GasLimit:   head.GasLimit,
		Time:       head.Time + 2,
	}
	hash := header.Hash()
	block := types.NewBlockWithHeader(header)

	var (
		mu       sync.Mutex
		strikes  []string
		excluded []string
		imports  atomic.Int32
		fetches  atomic.Int32
	)
	// First import fails with an error the witness could have caused; the
	// second (with the re-fetched witness) succeeds.
	insertChain := func(blocks types.Blocks, _ []*stateless.Witness) (int, error) {
		if imports.Add(1) == 1 {
			return 0, fmt.Errorf("%w (cross: 01 local: 02)", core.ErrStatelessStateRootMismatch)
		}
		return len(blocks), nil
	}
	getBlock := func(bh common.Hash) *types.Block {
		if bh == head.Hash() {
			return chain.GetBlockByHash(bh) // the parent is known, the block is not
		}
		return nil
	}
	f := fetcher.NewBlockFetcher(false, nil, getBlock, func(*types.Header) error { return nil },
		func(*types.Block, *stateless.Witness, bool) {}, func() uint64 { return head.Number.Uint64() }, chain.CurrentHeader,
		nil, insertChain, func(string) {}, false, true, 30_000_000, nil, nil)
	f.SetWitnessServerStriker(func(id string) {
		mu.Lock()
		strikes = append(strikes, id)
		mu.Unlock()
	})
	f.SetWitnessSourceExcluder(func(peer string, bh common.Hash) {
		if bh != hash {
			t.Errorf("exclusion for unexpected block %s", bh)
		}
		mu.Lock()
		excluded = append(excluded, peer)
		mu.Unlock()
	})
	hh.blockFetcher = f
	hh.Start(1000)
	defer hh.Stop()

	witH := (*witHandler)(hh)
	pusher, cleanup := newTestWit2PeerWithReader()
	defer cleanup()

	witness, err := stateless.NewWitness(header, nil)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, witness.EncodeRLP(&buf))
	// Signed commitment the pushed body is within band of but does not hash to.
	hh.signedWitnesses.putIfNewer(wit.SignedWitnessAnnouncement{
		BlockHash:   hash,
		BlockNumber: header.Number.Uint64(),
		WitnessHash: common.HexToHash("0xdeadbeef"),
		WitnessSize: uint64(buf.Len()),
		Signature:   make([]byte, wit.SignatureLength),
	})
	// The push is processed on the witness manager loop before the block
	// injection below is even received (unbuffered channels, one loop).
	require.NoError(t, witH.handleWitnessBroadcast(pusher, witness))

	fetchWitness := func(_ common.Hash, sink chan *ethproto.Response) (*ethproto.Request, error) {
		n := fetches.Add(1)
		req := &ethproto.Request{Peer: fmt.Sprintf("server-%d", n), Cancel: make(chan struct{})}
		go func() {
			w, err := stateless.NewWitness(header, nil)
			if err != nil {
				return
			}
			sink <- &ethproto.Response{Req: req, Res: []*stateless.Witness{w}, Time: time.Millisecond, Done: make(chan error, 1)}
		}()
		return req, nil
	}
	require.NoError(t, f.InjectBlockWithWitnessRequirement("origin", block, fetchWitness))

	deadline := time.Now().Add(10 * time.Second)
	for imports.Load() < 2 {
		if time.Now().After(deadline) {
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("block never re-imported after the pushed witness failed (imports=%d fetches=%d strikes=%v excluded=%v)",
				imports.Load(), fetches.Load(), strikes, excluded)
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{pusher.ID()}, strikes, "the pusher of the divergent body must be struck exactly once")
	require.Equal(t, []string{pusher.ID()}, excluded, "the pusher must be excluded as a witness source for the block")
	require.Equal(t, int32(1), fetches.Load(), "exactly one re-fetch, from another peer, after the pushed body failed import")
}
