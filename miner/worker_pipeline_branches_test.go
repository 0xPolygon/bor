package miner

import (
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/blockstm"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
)

func TestWorkerPipelineControlBranches(t *testing.T) {
	t.Run("speculative handler stops after setup failure", func(t *testing.T) {
		w, _, req := newPipelineRequestFixture(t, nil)
		req.parentRoot = common.HexToHash("0xdead")
		w.handleSpeculativeWork(req)
		require.Zero(t, w.pendingWorkBlock.Load())
	})

	t.Run("missing parent state is reported", func(t *testing.T) {
		w, _, _ := newPipelineRequestFixture(t, nil)
		header := &types.Header{
			ParentHash: common.HexToHash("0xdead"),
			Number:     big.NewInt(2),
		}
		params := &generateParams{}
		statedb, err := w.resolveStateFor(header, params)
		require.EqualError(t, err, "parent block not found")
		require.Nil(t, statedb)

		env, err := w.makeEnv(header, common.Address{}, false, params)
		require.EqualError(t, err, "parent block not found")
		require.Nil(t, env)
	})

	t.Run("nil interrupt timer is a no-op", func(t *testing.T) {
		stop := createInterruptTimer(1, time.Now(), nil, nil, true)
		require.NotPanics(t, stop)
	})
}

func TestWorkerDependencyMetadataBranches(t *testing.T) {
	w, _, _ := newPipelineRequestFixture(t, nil)
	coinbase := common.HexToAddress("0x1234")
	burnt := common.HexToAddress(w.chainConfig.Bor.CalculateBurntContract(1))

	newHeaderExtra := func(t *testing.T) []byte {
		t.Helper()
		extra, err := rlp.EncodeToBytes(types.BlockExtraData{})
		require.NoError(t, err)
		result := append(make([]byte, types.ExtraVanityLength), extra...)
		return append(result, make([]byte, types.ExtraSealLength)...)
	}
	newEnv := func() *environment {
		return &environment{
			header: &types.Header{
				Number: big.NewInt(1),
				Extra:  newHeaderExtra(t),
			},
			coinbase: coinbase,
		}
	}

	t.Run("missing dependency graph omits metadata", func(t *testing.T) {
		env := newEnv()
		require.Nil(t, w.buildTxDependencyArray(env, nil))

		env.mvReadMapList = []map[blockstm.Key]blockstm.ReadDescriptor{{}}
		require.NoError(t, w.updateTxDependencyMetadata(env))
	})

	t.Run("dependency graph is projected", func(t *testing.T) {
		env := newEnv()
		env.mvReadMapList = []map[blockstm.Key]blockstm.ReadDescriptor{{}, {}}
		deps := map[int]map[int]bool{
			0: {7: true},
			1: {0: true},
		}
		require.Equal(t, [][]uint64{{7}, {0}}, w.buildTxDependencyArray(env, deps))
	})

	t.Run("coinbase and burn reads suppress metadata", func(t *testing.T) {
		for _, address := range []common.Address{coinbase, burnt} {
			env := newEnv()
			env.mvReadMapList = []map[blockstm.Key]blockstm.ReadDescriptor{
				{},
				{blockstm.NewSubpathKey(address, state.BalancePath): {}},
			}
			deps := map[int]map[int]bool{0: {}, 1: {0: true}}
			require.Nil(t, w.buildTxDependencyArray(env, deps))
		}
	})

	t.Run("malformed extra data is rejected", func(t *testing.T) {
		env := newEnv()
		env.header.Extra = append(make([]byte, types.ExtraVanityLength), 0xff)
		env.header.Extra = append(env.header.Extra, make([]byte, types.ExtraSealLength)...)
		require.Error(t, w.updateTxDependencyMetadata(env))
	})
}

func TestCommitTransactionsInitializesPipelineDependencyState(t *testing.T) {
	w, _, req := newPipelineRequestFixture(t, func(config *params.ChainConfig) {
		config.ShanghaiBlock = big.NewInt(0)
		config.CancunBlock = big.NewInt(0)
	})
	w.running.Store(true)
	env := req.blockNEnv
	env.txs = nil
	env.receipts = nil
	env.gasPool = nil

	plain := newTransactionsByPriceAndNonce(env.signer, nil, env.header.BaseFee, nil)
	blob := newTransactionsByPriceAndNonce(env.signer, nil, env.header.BaseFee, nil)
	require.NoError(t, w.commitTransactions(env, plain, blob, nil, nil))
	require.NotNil(t, env.depsBuilder)

	env.depsBuilder = nil
	env.depsFailed = false
	env.gasPool = nil
	env.buildInterrupt = newBuildInterruptState()
	env.buildInterrupt.flagSetAt.Store(time.Now().UnixNano())
	env.buildInterrupt.timedOut.Store(true)
	require.NoError(t, w.commitTransactions(env, plain, blob, new(atomic.Int32), nil))
}
