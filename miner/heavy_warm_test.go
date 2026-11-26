package miner

import (
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
)

const testHeavyGas = 1_000_000

func testWarmConfig() heavyWarmConfig {
	return heavyWarmConfig{
		gas:       testHeavyGas,
		workers:   1,
		queue:     8,
		perSender: 8,
		deferMax:  100 * time.Millisecond,
		maxRun:    time.Second,
	}
}

func warmTestTx(t *testing.T, key *ecdsa.PrivateKey, nonce, gas uint64, feeCap, tip int64) *types.Transaction {
	t.Helper()

	return types.MustSignNewTx(key, types.LatestSigner(params.TestChainConfig), &types.DynamicFeeTx{
		ChainID:   params.TestChainConfig.ChainID,
		Nonce:     nonce,
		To:        &testUserAddress,
		Gas:       gas,
		GasFeeCap: big.NewInt(feeCap),
		GasTipCap: big.NewInt(tip),
	})
}

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)

	return key
}

// offerTest queues tx with base fee 100 and min tip 10.
func offerTest(w *heavyWarmer, tx *types.Transaction) {
	w.offer(tx, big.NewInt(100), types.LatestSigner(params.TestChainConfig), uint256.NewInt(10))
}

func queuedHashes(w *heavyWarmer) map[common.Hash]bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	out := make(map[common.Hash]bool, len(w.queued))
	for h := range w.queued {
		out[h] = true
	}

	return out
}

func TestHeavyWarmShouldDefer(t *testing.T) {
	t.Parallel()

	key := newKey(t)
	heavy := warmTestTx(t, key, 0, testHeavyGas, 200, 20)
	light := warmTestTx(t, key, 1, params.TxGas, 200, 20)

	var disabled *heavyWarmer
	require.False(t, disabled.shouldDefer(heavy), "nil warmer never defers")
	disabled.stop() // nil-safe

	w := newHeavyWarmer(testWarmConfig(), params.TestChainConfig, nil, nil, nil)

	require.False(t, w.shouldDefer(heavy), "no warm: apply now")

	offerTest(w, heavy)
	require.True(t, queuedHashes(w)[heavy.Hash()])
	require.False(t, w.shouldDefer(heavy), "queued warm: apply now")

	w.mu.Lock()
	w.running[heavy.Hash()] = time.Now()
	w.running[light.Hash()] = time.Now()
	w.mu.Unlock()
	require.True(t, w.shouldDefer(heavy), "running warm under deferMax: skip")
	require.False(t, w.shouldDefer(light), "light tx is never deferred")

	w.mu.Lock()
	w.running[heavy.Hash()] = time.Now().Add(-200 * time.Millisecond)
	w.mu.Unlock()
	require.False(t, w.shouldDefer(heavy), "running warm past deferMax: apply cold")

	w.mu.Lock()
	w.running[heavy.Hash()] = time.Now()
	w.mu.Unlock()
	w.cfg.deferMax = 0
	require.False(t, w.shouldDefer(heavy), "deferMax 0 disables the rule")
}

func TestHeavyWarmAdmission(t *testing.T) {
	t.Parallel()

	w := newHeavyWarmer(testWarmConfig(), params.TestChainConfig, nil, nil, nil)
	key := newKey(t)

	lowCap := warmTestTx(t, key, 0, testHeavyGas, 90, 20) // fee cap < base fee 100
	lowTip := warmTestTx(t, key, 1, testHeavyGas, 200, 5) // tip 5 < min tip 10
	good := warmTestTx(t, key, 2, testHeavyGas, 200, 20)

	offerTest(w, lowCap)
	offerTest(w, lowTip)
	offerTest(w, good)
	offerTest(w, good) // duplicate

	q := queuedHashes(w)
	require.Len(t, q, 1)
	require.True(t, q[good.Hash()])
	require.Equal(t, 1, w.senders[crypto.PubkeyToAddress(key.PublicKey)])
}

func TestHeavyWarmPerSenderCap(t *testing.T) {
	t.Parallel()

	cfg := testWarmConfig()
	cfg.perSender = 2
	w := newHeavyWarmer(cfg, params.TestChainConfig, nil, nil, nil)

	key, other := newKey(t), newKey(t)
	for nonce := uint64(0); nonce < 3; nonce++ {
		offerTest(w, warmTestTx(t, key, nonce, testHeavyGas, 200, 20))
	}
	offerTest(w, warmTestTx(t, other, 0, testHeavyGas, 200, 20))

	require.Len(t, queuedHashes(w), 3, "2 from the capped sender + 1 from another")
	require.Equal(t, 2, w.senders[crypto.PubkeyToAddress(key.PublicKey)])
}

func TestHeavyWarmQueueEvictsLowestFee(t *testing.T) {
	t.Parallel()

	cfg := testWarmConfig()
	cfg.queue = 2
	w := newHeavyWarmer(cfg, params.TestChainConfig, nil, nil, nil)

	tip20 := warmTestTx(t, newKey(t), 0, testHeavyGas, 1000, 20)
	tip30 := warmTestTx(t, newKey(t), 0, testHeavyGas, 1000, 30)
	tip10 := warmTestTx(t, newKey(t), 0, testHeavyGas, 1000, 10)
	tip40 := warmTestTx(t, newKey(t), 0, testHeavyGas, 1000, 40)

	offerTest(w, tip20)
	offerTest(w, tip30)
	offerTest(w, tip10) // full, pays less than everything queued: dropped
	require.Equal(t, map[common.Hash]bool{tip20.Hash(): true, tip30.Hash(): true}, queuedHashes(w))

	offerTest(w, tip40) // full, pays more: evicts tip20
	require.Equal(t, map[common.Hash]bool{tip30.Hash(): true, tip40.Hash(): true}, queuedHashes(w))
	require.Len(t, w.senders, 2, "evicted sender released")

	// Highest fee runs first.
	w.mu.Lock()
	first := w.queue[0].tx.Hash()
	w.mu.Unlock()
	require.Equal(t, tip40.Hash(), first)
}

func TestHeavyWarmWarmsPoolTx(t *testing.T) {
	t.Parallel()

	backend := newTestWorkerBackend(t, ethashChainConfig, ethash.NewFaker(), rawdb.NewMemoryDatabase())
	defer backend.chain.Stop()

	cfg := testWarmConfig()
	cfg.gas = params.TxGas // every tx is heavy in this test

	w := newHeavyWarmer(cfg, ethashChainConfig, backend.chain, backend.txPool, func() *uint256.Int { return uint256.NewInt(0) })
	warmed := make(chan common.Hash, 4)
	w.onWarmed = func(h common.Hash) { warmed <- h }
	w.start()
	defer w.stop()

	tx := backend.newRandomTx(false)
	errs := backend.txPool.Add([]*types.Transaction{tx}, true)
	require.NoError(t, errs[0])

	select {
	case h := <-warmed:
		require.Equal(t, tx.Hash(), h)
	case <-time.After(5 * time.Second):
		t.Fatal("pool tx was not warmed")
	}

	_, running := w.runningFor(tx.Hash())
	require.False(t, running)
	w.mu.Lock()
	require.Empty(t, w.senders)
	require.Empty(t, w.queued)
	w.mu.Unlock()

	w.stop()
	w.stop() // idempotent
}

func TestCommitTransactionsDefersRunningHeavyWarm(t *testing.T) {
	t.Parallel()

	w, backend, cleanup := newTestWorker(t, DefaultTestConfig(), ethashChainConfig, ethash.NewFaker(), rawdb.NewMemoryDatabase(), false, 0)
	defer cleanup()

	cfg := testWarmConfig()
	cfg.gas = params.TxGas
	cfg.deferMax = time.Hour
	w.heavyWarm = newHeavyWarmer(cfg, ethashChainConfig, backend.chain, backend.txPool, w.minerTip)

	tx := pendingTxs[0]
	commit := func() *environment {
		parent := backend.chain.CurrentBlock()
		statedb, _, prefetchReader, processReader, err := backend.chain.StateAtWithReaders(parent.Root)
		require.NoError(t, err)
		work, err := w.prepareWork(&generateParams{
			timestamp:      parent.Time + 1,
			coinbase:       testBankAddress,
			parentHash:     parent.Hash(),
			statedb:        statedb,
			prefetchReader: prefetchReader,
			processReader:  processReader,
		}, false)
		require.NoError(t, err)

		signer := types.MakeSigner(ethashChainConfig, work.header.Number, work.header.Time)
		lazy := map[common.Address][]*txpool.LazyTransaction{testBankAddress: {{
			Hash:      tx.Hash(),
			Tx:        tx,
			Time:      tx.Time(),
			GasFeeCap: uint256.MustFromBig(tx.GasFeeCap()),
			GasTipCap: uint256.MustFromBig(tx.GasTipCap()),
			Gas:       tx.Gas(),
		}}}
		var baseFee *uint256.Int
		if work.header.BaseFee != nil {
			baseFee = uint256.MustFromBig(work.header.BaseFee)
		}
		plain := newTransactionsByPriceAndNonce(signer, lazy, baseFee, nil)
		blobs := newTransactionsByPriceAndNonce(signer, nil, baseFee, nil)
		require.NoError(t, w.commitTransactions(work, plain, blobs, nil, nil))

		return work
	}

	w.heavyWarm.mu.Lock()
	w.heavyWarm.running[tx.Hash()] = time.Now()
	w.heavyWarm.mu.Unlock()
	require.Equal(t, 0, commit().tcount, "tx skipped while its warm runs")

	w.heavyWarm.mu.Lock()
	delete(w.heavyWarm.running, tx.Hash())
	w.heavyWarm.mu.Unlock()
	require.Equal(t, 1, commit().tcount, "tx applied once the warm is done")
}
