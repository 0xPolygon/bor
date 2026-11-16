package miner

import (
	"errors"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
)

func TestBaseFeeConversionBounds(t *testing.T) {
	t.Parallel()

	two256 := new(big.Int).Lsh(big.NewInt(1), 256)
	maxUint256 := new(big.Int).Sub(two256, big.NewInt(1))

	tests := []struct {
		name    string
		baseFee *big.Int
		wantErr bool
	}{
		{"nil", nil, false},
		{"256 bits", maxUint256, false},
		{"257 bits", two256, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := &worker{chainConfig: params.TestChainConfig, tip: uint256.NewInt(1)}
			filter, err := w.buildDefaultFilter(tt.baseFee, big.NewInt(1))
			if tt.wantErr {
				require.ErrorContains(t, err, "bitlen 257")
				return
			}
			require.NoError(t, err)
			require.Equal(t, uint256.NewInt(1), filter.MinTip)
			require.Equal(t, uint256.MustFromBig(tt.baseFee), filter.BaseFee)
		})
	}
}

func TestPendingTxSetsBaseFeeBounds(t *testing.T) {
	t.Parallel()

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	signer := types.LatestSigner(params.TestChainConfig)
	tx := types.MustSignNewTx(key, signer, &types.DynamicFeeTx{
		ChainID:   params.TestChainConfig.ChainID,
		GasFeeCap: big.NewInt(100),
		GasTipCap: big.NewInt(1),
		Gas:       params.TxGas,
	})
	from, err := types.Sender(signer, tx)
	require.NoError(t, err)

	tests := []struct {
		name    string
		baseFee *big.Int
		wantTx  bool
	}{
		{"fits", big.NewInt(1), true},
		{"257 bits", new(big.Int).Lsh(big.NewInt(1), 256), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			txs := map[common.Address][]*txpool.LazyTransaction{from: {{
				Hash:      tx.Hash(),
				Tx:        tx,
				GasFeeCap: uint256.MustFromBig(tx.GasFeeCap()),
				GasTipCap: uint256.MustFromBig(tx.GasTipCap()),
				Gas:       tx.Gas(),
			}}}
			plain, blob := pendingTxSets(signer, txs, tt.baseFee, nil)
			next, _ := plain.Peek()
			require.Equal(t, tt.wantTx, next != nil)
			next, _ = blob.Peek()
			require.Nil(t, next)
		})
	}
}

func TestFillTransactionsWideBaseFee(t *testing.T) {
	t.Parallel()

	w := &worker{chainConfig: params.TestChainConfig, tip: uint256.NewInt(1)}
	env := &environment{header: &types.Header{
		Number:  big.NewInt(1),
		BaseFee: new(big.Int).Lsh(big.NewInt(1), 256),
	}}
	require.ErrorContains(t, w.fillTransactions(nil, env, nil), "bitlen 257")
}

// newBaseFeeParentWorker builds a worker on a genesis carrying the given base
// fee and gas usage, so the child header's base fee follows from them.
func newBaseFeeParentWorker(t *testing.T, baseFee *big.Int, gasUsed uint64) *worker {
	t.Helper()

	gspec := &core.Genesis{
		Config:   params.TestChainConfig,
		Alloc:    types.GenesisAlloc{testBankAddress: {Balance: testBankFunds}},
		GasLimit: params.GenesisGasLimit,
		GasUsed:  gasUsed,
		BaseFee:  baseFee,
	}
	engine := ethash.NewFaker()
	chain, err := core.NewBlockChain(rawdb.NewMemoryDatabase(), gspec, engine, core.DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(chain.Stop)

	legacy := legacypool.New(testTxPoolConfig, chain)
	pool, err := txpool.New(testTxPoolConfig.PriceLimit, chain, []txpool.SubPool{legacy})
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })

	backend := &testWorkerBackend{chain: chain, txPool: pool, genesis: gspec}
	w := newWorker(DefaultTestConfig(), params.TestChainConfig, engine, backend, new(event.TypeMux), nil, false, false)
	t.Cleanup(w.close)
	w.setEtherbase(testBankAddress)

	return w
}

func TestBlockBuildingWideChildBaseFee(t *testing.T) {
	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	gasTarget := params.GenesisGasLimit / params.TestChainConfig.ElasticityMultiplier()

	tests := []struct {
		name    string
		gasUsed uint64
		wantErr bool
	}{
		{"unchanged fee fits", gasTarget, false},
		{"raised fee exceeds 256 bits", params.GenesisGasLimit, true},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/seal", func(t *testing.T) {
			w := newBaseFeeParentWorker(t, maxUint256, tt.gasUsed)

			var sealed atomic.Int32
			tasks := make(chan *task, 1)
			w.fullTaskHook = func() { sealed.Add(1) }
			w.newTaskHook = func(task *task) { tasks <- task }
			w.skipSealHook = func(*task) bool { return true }
			w.running.Store(true)

			var prefetch atomic.Bool
			w.buildAndCommitBlock(nil, false, &generateParams{coinbase: testBankAddress, timestamp: uint64(time.Now().Unix())}, &prefetch)

			w.currentMu.Lock()
			current := w.current
			w.currentMu.Unlock()

			if tt.wantErr {
				require.Zero(t, sealed.Load())
				require.Nil(t, current)
				require.Empty(t, tasks)
				return
			}
			require.Equal(t, int32(1), sealed.Load())
			require.NotNil(t, current)
			select {
			case task := <-tasks:
				require.Equal(t, maxUint256, task.block.BaseFee())
			case <-time.After(5 * time.Second):
				t.Fatal("built block never reached the seal channel")
			}
		})
		t.Run(tt.name+"/payload", func(t *testing.T) {
			w := newBaseFeeParentWorker(t, maxUint256, tt.gasUsed)

			res := w.generateWork(&generateParams{
				coinbase:   testBankAddress,
				timestamp:  uint64(time.Now().Unix()),
				parentHash: w.chain.Genesis().Hash(),
			}, false)
			if tt.wantErr {
				require.ErrorContains(t, res.err, "bitlen 257")
				require.Nil(t, res.block)
				return
			}
			require.NoError(t, res.err)
			require.Equal(t, maxUint256, res.block.BaseFee())
		})
	}
}

func TestIdleTxProviderBaseFeeBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseFee *big.Int
		wantTx  bool
	}{
		{"fits", big.NewInt(1), true},
		{"257 bits", new(big.Int).Lsh(big.NewInt(1), 256), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w, b, cleanup := newTestWorker(t, DefaultTestConfig(), params.TestChainConfig, ethash.NewFaker(), rawdb.NewMemoryDatabase(), false, 0)
			defer cleanup()
			require.NoError(t, errors.Join(b.txPool.Add([]*types.Transaction{b.newRandomTx(false)}, true)...))

			header := &types.Header{Number: big.NewInt(1), GasLimit: params.GenesisGasLimit, BaseFee: tt.baseFee}
			txsCh := make(chan *types.Transaction, 16)
			var interrupt atomic.Bool
			done := make(chan struct{})
			go func() {
				defer close(done)
				w.runIdleTxProvider(txsCh, header, &generateParams{}, &interrupt)
			}()

			if tt.wantTx {
				select {
				case <-txsCh:
				case <-time.After(5 * time.Second):
					t.Fatal("no pool transaction was streamed")
				}
				interrupt.Store(true)
				<-done
				return
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				interrupt.Store(true)
				<-done
				t.Fatal("provider kept running on a base fee wider than 256 bits")
			}
			require.Empty(t, txsCh)
		})
	}
}

func TestBuilderTxProviderBaseFeeBounds(t *testing.T) {
	t.Parallel()

	w := &worker{chainConfig: params.TestChainConfig, tip: uint256.NewInt(1)}
	header := &types.Header{Number: big.NewInt(1), GasLimit: params.GenesisGasLimit, BaseFee: new(big.Int).Lsh(big.NewInt(1), 256)}

	_, err := w.buildOverflowHeap(header, nil)
	require.Error(t, err)

	planCh := make(chan *types.Transaction)
	defer close(planCh)
	txsCh := make(chan *types.Transaction, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.runBuilderTxProvider(txsCh, header, &generateParams{builderPlanCh: planCh}, new(atomic.Bool))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("builder provider kept running on a base fee wider than 256 bits")
	}
	require.Empty(t, txsCh)
}

func TestTransactionsByPriceAndNonceInterrupted(t *testing.T) {
	t.Parallel()

	var interrupt atomic.Bool
	interrupt.Store(true)
	txs := map[common.Address][]*txpool.LazyTransaction{{1}: {{}}}
	set := newTransactionsByPriceAndNonce(types.LatestSignerForChainID(big.NewInt(1)), txs, uint256.NewInt(1), &interrupt)
	require.NotNil(t, set)
	tx, _ := set.Peek()
	require.Nil(t, tx)
}
