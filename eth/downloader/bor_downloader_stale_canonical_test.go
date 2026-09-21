package downloader

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/downloader/whitelist"
	"github.com/ethereum/go-ethereum/event"
)

// newWhitelistedTester builds a downloadTester whose blockchain runs the real
// whitelist service as its chain validator and hands the chain back to the
// service, mirroring the production wiring in eth/backend.go. newTester does
// not set core.BlockChainConfig.Checker, so InsertChain never consults the
// whitelist there.
func newWhitelistedTester(t *testing.T) (*downloadTester, *whitelist.Service) {
	t.Helper()

	freezer := t.TempDir()
	db, err := rawdb.NewDatabaseWithFreezer(rawdb.NewMemoryDatabase(), freezer, "", false, false, false, false, false, false)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	service := whitelist.NewService(db, false, 0)

	cfg := core.DefaultConfig()
	cfg.Checker = service
	chain, err := core.NewBlockChain(db, testGspec, ethash.NewFaker(), cfg)
	require.NoError(t, err)
	service.SetBlockchain(chain)

	tester := &downloadTester{
		freezer: freezer,
		chain:   chain,
		peers:   make(map[string]*downloadTesterPeer),
	}
	//nolint: staticcheck
	tester.downloader = New(db, new(event.TypeMux), chain, nil, tester.dropPeer, nil, service, 0, false)

	return tester, service
}

// fetchResultsFor turns already-built blocks into the results the downloader
// queue would hand to importBlockResults once their bodies arrived.
func fetchResultsFor(blocks []*types.Block) []*fetchResult {
	results := make([]*fetchResult, 0, len(blocks))
	for _, b := range blocks {
		results = append(results, &fetchResult{
			Header:       b.Header(),
			Uncles:       b.Uncles(),
			Transactions: b.Transactions(),
			Withdrawals:  b.Withdrawals(),
		})
	}
	return results
}

// TestImportBlockResultsStaleCanonicalReimport is the INC-192 regression at the
// downloader boundary. The block fetcher has already imported the chain up to
// the head and Heimdall has whitelisted a milestone above the blocks a slow
// peer finally delivers bodies for. importBlockResults re-inserts those
// already-canonical blocks; core.insertChain runs the whitelist check before
// the known-block trim, so this used to surface as errInvalidChain wrapping
// whitelist.ErrMismatch, and the sync master was struck for a "whitelist
// mismatch" it never caused.
func TestImportBlockResultsStaleCanonicalReimport(t *testing.T) {
	t.Parallel()

	tester, service := newWhitelistedTester(t)
	defer tester.terminate()

	base := testChainBase.blocks // base[n] is block number n, base[0] the genesis

	// The block fetcher imported everything up to block 64.
	_, err := tester.chain.InsertChain(base[1:65], false)
	require.NoError(t, err)
	require.Equal(t, uint64(64), tester.chain.CurrentBlock().Number.Uint64())

	// Heimdall whitelisted block 60 while the downloader was still waiting for bodies.
	service.ProcessMilestone(60, base[60].Hash())

	// The late bodies for blocks 30-31 arrive and the downloader re-imports them.
	err = tester.downloader.importBlockResults(fetchResultsFor(base[30:32]))
	require.NoError(t, err, "re-import of already-canonical blocks below the milestone must not be a whitelist mismatch")
	require.Equal(t, uint64(64), tester.chain.CurrentBlock().Number.Uint64(), "head must be untouched")

	// A real fork below the milestone is still rejected, wrapped for the
	// downloader as errInvalidChain, and still classified as a whitelist
	// mismatch by the peer-response policy.
	fork30 := types.CopyHeader(base[30].Header())
	fork30.Extra = []byte("inc-192 fork")
	fork31 := types.CopyHeader(base[31].Header())
	fork31.ParentHash = fork30.Hash()
	fork31.Extra = []byte("inc-192 fork")
	forkResults := []*fetchResult{
		{Header: fork30, Uncles: base[30].Uncles(), Transactions: base[30].Transactions(), Withdrawals: base[30].Withdrawals()},
		{Header: fork31, Uncles: base[31].Uncles(), Transactions: base[31].Transactions(), Withdrawals: base[31].Withdrawals()},
	}
	err = tester.downloader.importBlockResults(forkResults)
	require.ErrorIs(t, err, errInvalidChain)
	require.ErrorIs(t, err, whitelist.ErrMismatch)
	reason, ok := classifySyncFailure(err)
	require.True(t, ok)
	require.Equal(t, peerFailureWhitelistMismatch, reason)
	require.Equal(t, uint64(64), tester.chain.CurrentBlock().Number.Uint64(), "head must be untouched")
}
