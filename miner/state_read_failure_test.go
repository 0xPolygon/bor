package miner

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
)

var errMissingTrieNode = errors.New("missing trie node")

// failingReader serves the real state except for one account, whose lookup
// fails the way a missing trie node or a flush error would.
type failingReader struct {
	state.Reader
	addr common.Address
}

func (r failingReader) Account(addr common.Address) (*types.StateAccount, error) {
	if addr == r.addr {
		return nil, errMissingTrieNode
	}
	return r.Reader.Account(addr)
}

// stateWithReadFailure opens the state at root and records a read failure
// partway through, as if execution had touched an unreadable account.
func stateWithReadFailure(t *testing.T, chain *core.BlockChain, root common.Hash) *state.StateDB {
	t.Helper()

	db := chain.StateCache()
	reader, err := db.Reader(root)
	require.NoError(t, err)
	statedb, err := state.NewWithReader(root, db, failingReader{Reader: reader, addr: testUserAddress})
	require.NoError(t, err)

	statedb.GetBalance(testBankAddress)
	require.NoError(t, statedb.Error())
	statedb.GetBalance(testUserAddress)
	require.ErrorIs(t, statedb.Error(), errMissingTrieNode)
	return statedb
}

func TestSealingStopsOnStateReadFailure(t *testing.T) {
	t.Run("commit", func(t *testing.T) {
		w, backend, req := newPipelineRequestFixture(t, nil)
		w.running.Store(true)
		env := req.blockNEnv
		env.state = stateWithReadFailure(t, backend.chain, backend.chain.CurrentBlock().Root)

		err := w.commit(env, nil, false, time.Now(), &generateParams{})
		require.ErrorIs(t, err, errStateReadFailure)
		select {
		case <-w.taskCh:
			t.Fatal("block was handed to the sealer")
		default:
		}
	})

	t.Run("generateWork", func(t *testing.T) {
		w, backend := newPipelineWorkerFixture(t, nil)
		parent := backend.chain.CurrentBlock()

		result := w.generateWork(&generateParams{
			timestamp:  uint64(time.Now().Unix()),
			coinbase:   testBankAddress,
			parentHash: parent.Hash(),
			statedb:    stateWithReadFailure(t, backend.chain, parent.Root),
			noTxs:      true,
		}, false)
		require.ErrorIs(t, result.err, errStateReadFailure)
		require.Nil(t, result.block)
	})

	t.Run("commitPipelined", func(t *testing.T) {
		w, backend, req := newPipelineRequestFixture(t, nil)
		w.running.Store(true)
		env := req.blockNEnv
		env.state = stateWithReadFailure(t, backend.chain, backend.chain.CurrentBlock().Root)

		require.ErrorIs(t, w.commitPipelined(env, time.Now()), errStateReadFailure)
		select {
		case <-w.speculativeWorkCh:
			t.Fatal("block entered the pipeline")
		default:
		}
	})

	t.Run("fallbackToSequential", func(t *testing.T) {
		w, backend, req := newPipelineRequestFixture(t, nil)
		req.blockNEnv.state = stateWithReadFailure(t, backend.chain, backend.chain.CurrentBlock().Root)

		// The fixture's taskLoop would drain taskCh, so use a bare worker
		// whose taskCh only this test reads.
		bare := &worker{
			chainConfig: w.chainConfig,
			engine:      w.engine,
			chain:       w.chain,
			exitCh:      make(chan struct{}),
			taskCh:      make(chan *task, 1),
		}
		bare.fallbackToSequential(req)
		require.Empty(t, bare.taskCh, "block was handed to the sealer")
	})

	t.Run("finalizeCurrent", func(t *testing.T) {
		_, backend, session := newPipelineSessionFixture(t, nil)
		<-session.initialFillDone
		require.True(t, session.waitForSRCAndSealBlockN())

		session.specState = stateWithReadFailure(t, backend.chain, backend.chain.CurrentBlock().Root)
		_, _, _, ok := session.finalizeCurrent()
		require.False(t, ok)
	})
}
