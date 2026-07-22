// Copyright 2024 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package miner

import (
	"bytes"
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/txpool"
	"github.com/ethereum/go-ethereum/core/txpool/legacypool"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/program"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/trie"
)

// exerciserAddr is the fixed address at which the shared-cache exerciser
// runtime is predeployed in the differential test's genesis.
var exerciserAddr = common.HexToAddress("0x000000000000000000000000000000000000E7E7")

// buildExerciserRuntime returns EVM runtime bytecode that, on every CALL:
//   - hashes seven distinct-length memory regions (32..200 bytes) with
//     KECCAK256 — exercising the widened KeccakStore path (variable-length
//     keccak) added in Tasks 1/3, not just the legacy 64B fast path;
//   - STATICCALLs the ECRECOVER precompile (0x01) over a *valid* signature
//     (signed here in Go over a fixed message hash with the given key), so the
//     call recovers an address rather than failing closed — exercising the
//     EcrecoverCache fast/slow path, not just its early-return guard.
//
// The runtime CALLDATACOPYs the call's own calldata into memory before hashing
// so distinct calls still vary the hashed tail deterministically. This mirrors
// core/blockchain_test.go's buildPrecompileCacheExerciserInitCode, but returns
// runtime code (for direct genesis predeploy) rather than init code.
func buildExerciserRuntime(key *ecdsa.PrivateKey) []byte {
	msgHash := crypto.Keccak256Hash([]byte("miner: shared VM result cache - build path differential"))

	sig, err := crypto.Sign(msgHash.Bytes(), key)
	if err != nil {
		panic(err)
	}

	// ecrecover precompile input: hash(32) || v(32, right-aligned) || r(32) || s(32).
	ecrecoverInput := make([]byte, 128)
	copy(ecrecoverInput[0:32], msgHash.Bytes())
	ecrecoverInput[63] = sig[64] + 27 // recovery id -> Ethereum v (27/28)
	copy(ecrecoverInput[64:96], sig[0:32])
	copy(ecrecoverInput[96:128], sig[32:64])

	runtime := program.New().
		// mem[0:128) = ecrecover input.
		Mstore(ecrecoverInput, 0).
		// mem[128:200) = the transaction's own calldata (varies the hashed
		// tail below without needing more PUSH/MSTORE bytecode).
		Push(72).Push(0).Push(128).Op(vm.CALLDATACOPY)
	for _, size := range []int{32, 64, 88, 100, 128, 150, 200} {
		runtime.Push(size).Push(0).Op(vm.KECCAK256).Op(vm.POP)
	}
	runtime.StaticCall(nil, 1, 0, 128, 224, 32).Op(vm.POP)
	runtime.Op(vm.STOP)

	return runtime.Bytes()
}

// newExerciserWorker builds a fresh ethash-faker worker whose genesis predeploys
// the shared-cache exerciser runtime at exerciserAddr and funds testBankAddress.
// When enableCache is true the chain's base vm.Config has EnablePrecompileCache
// set, matching what a production node would carry.
func newExerciserWorker(t *testing.T, enableCache bool) (*worker, *testWorkerBackend) {
	t.Helper()

	chainConfig := new(params.ChainConfig)
	*chainConfig = *params.TestChainConfig

	engine := ethash.NewFaker()
	t.Cleanup(func() { engine.Close() })
	db := rawdb.NewMemoryDatabase()

	gspec := &core.Genesis{
		Config:   chainConfig,
		BaseFee:  big.NewInt(params.InitialBaseFee),
		GasLimit: params.GenesisGasLimit,
		Alloc: types.GenesisAlloc{
			testBankAddress: {Balance: new(big.Int).Set(testBankFunds)},
			exerciserAddr:   {Balance: big.NewInt(0), Code: buildExerciserRuntime(testBankKey)},
		},
	}

	chain, err := core.NewBlockChain(db, gspec, engine, core.DefaultConfig())
	if err != nil {
		t.Fatalf("core.NewBlockChain: %v", err)
	}
	t.Cleanup(chain.Stop)

	// Thread the flag through the chain's base VM config exactly as a real node
	// would: commitWork/makeEnv/runPrefetcher all read w.chain.GetVMConfig().
	if enableCache {
		chain.GetVMConfig().EnablePrecompileCache = true
	}

	pool := legacypool.New(testTxPoolConfig, chain)
	pl, _ := txpool.New(testTxPoolConfig.PriceLimit, chain, []txpool.SubPool{pool})

	backend := &testWorkerBackend{
		db:      db,
		chain:   chain,
		txPool:  pl,
		genesis: gspec,
	}

	// DefaultTestConfig leaves NewPayloadTimeout at 0, which makes
	// generateWork's interrupt timer fire immediately and flakily drop pending
	// txs before they are committed. Give it a real budget so sealing is
	// deterministic.
	config := DefaultTestConfig()
	config.NewPayloadTimeout = 2 * time.Second

	w := newWorker(config, chainConfig, engine, backend, new(event.TypeMux), nil, false, false)
	t.Cleanup(w.close)
	w.setEtherbase(testBankAddress)

	return w, backend
}

// sealExerciserBlock adds TWO calls into the predeployed exerciser to the pool
// (byte-identical calldata, same sender, sequential nonces) and seals a single
// block on top of the current head via getSealingBlock. When vmCaches is
// non-nil it is threaded onto the generateParams, exactly as commitWork does
// when EnablePrecompileCache is on — so makeEnv wires the shared caches into
// the sealing EVM.
//
// Because both calls carry identical calldata, the exerciser's CALLDATACOPY
// produces an identical memory tail on both invocations, so the SECOND call's
// seven KECCAK256 hashes are all cache HITS against entries the FIRST call's
// KeccakStore.Store wrote — including sizes other than the legacy 64B fast
// path, so the widened-length store path is actually read back, not just
// written. The ECRECOVER input is independent of calldata (it's a fixed,
// pre-signed message baked into the bytecode by buildExerciserRuntime), so it
// is byte-identical across every call regardless of calldata; the second
// call's STATICCALL to 0x01 is therefore also a cache HIT against the
// EcrecoverCache entry the first call populated. Both hits occur within the
// SAME sealed block because makeEnv calls vmCaches.ApplyTo(&vmCfg) exactly
// once per block build and every included transaction's EVM shares that one
// vm.Config (see miner/worker.go's makeEnv and the mirrored wiring in the
// build prefetcher). A wrong hit (bad keying/aliasing/stale reuse) would
// therefore make the flag-ON sealed block diverge from the flag-OFF one,
// which sees no cache at all and recomputes everything, and the equality
// assertions below would fail.
func sealExerciserBlock(t *testing.T, w *worker, backend *testWorkerBackend, vmCaches *vm.SharedResultCaches) (*types.Block, types.Receipts) {
	t.Helper()

	callData := make([]byte, 72)
	for i := range callData {
		callData[i] = byte(i * 7)
	}

	signer := types.LatestSigner(w.chainConfig)
	gasPrice := big.NewInt(26 * params.InitialBaseFee)

	const numCalls = 2
	txs := make([]*types.Transaction, numCalls)
	for i := 0; i < numCalls; i++ {
		tx, err := types.SignTx(
			types.NewTransaction(uint64(i), exerciserAddr, big.NewInt(0), 1_000_000, gasPrice, callData),
			signer, testBankKey,
		)
		if err != nil {
			t.Fatalf("sign exerciser call tx %d: %v", i, err)
		}
		txs[i] = tx
	}
	if errs := backend.txPool.Add(txs, true); errs[0] != nil || errs[1] != nil {
		t.Fatalf("add exerciser call txs to pool: %v / %v", errs[0], errs[1])
	}

	// Give the pool a beat to surface both txs as pending before sealing.
	require.Eventually(t, func() bool {
		return countPendingTransactions(backend) >= numCalls
	}, 2*time.Second, 10*time.Millisecond, "exerciser txs never became pending")

	genParams := &generateParams{
		parentHash: w.chain.CurrentBlock().Hash(),
		timestamp:  uint64(time.Now().Unix()),
		coinbase:   testBankAddress,
		forceTime:  true,
		noTxs:      false,
		vmCaches:   vmCaches,
	}

	r := w.getSealingBlock(genParams)
	require.NoError(t, r.err, "getSealingBlock returned an error")
	require.NotNil(t, r.block, "getSealingBlock produced no block")

	return r.block, r.receipts
}

// TestBuild_FlagDifferential proves that wiring the per-building-cycle shared VM
// result caches into the sealing EVM (miner.makeEnv, Task 6) behind
// EnablePrecompileCache is consensus-safe: sealing a block that includes TWO
// byte-identical exerciser calls — so the second call's ECRECOVER and
// widened-length KECCAK256 results are served from the cache the first call
// populated, not recomputed — produces a byte-identical sealed block — state
// root, tx set, gas used, and per-receipt status/gas/bloom (hence receipts
// hash) — whether the caches are wired or not. See sealExerciserBlock's doc
// comment for exactly how the hit is guaranteed.
//
// The sealing EVM (env.evm) is the sole determinant of the produced block, so a
// cache bug here (aliasing, stale reuse) would either diverge these fields from
// the flag-off run or produce an invalid block. Either is caught below.
func TestBuild_FlagDifferential(t *testing.T) {
	// Flag OFF: no shared caches on the sealing EVM.
	wOff, backendOff := newExerciserWorker(t, false)
	blockOff, receiptsOff := sealExerciserBlock(t, wOff, backendOff, nil)

	// Flag ON: chain carries EnablePrecompileCache and the generateParams carries
	// a shared cache set, exactly as commitWork constructs it.
	wOn, backendOn := newExerciserWorker(t, true)
	blockOn, receiptsOn := sealExerciserBlock(t, wOn, backendOn, vm.NewSharedResultCaches(true))

	// Guard against a vacuous pass: both exerciser calls must actually be in the
	// block and must have succeeded, otherwise the caches were never populated
	// (call 1) nor read back as a hit (call 2).
	require.Len(t, receiptsOff, 2, "flag-off: expected both exerciser call receipts")
	require.Len(t, receiptsOn, 2, "flag-on: expected both exerciser call receipts")
	for i := 0; i < 2; i++ {
		require.Equal(t, types.ReceiptStatusSuccessful, receiptsOff[i].Status, "flag-off: exerciser call %d must succeed", i)
		require.Equal(t, types.ReceiptStatusSuccessful, receiptsOn[i].Status, "flag-on: exerciser call %d must succeed", i)
	}
	require.Equal(t, exerciserAddr, *blockOff.Transactions()[0].To(), "block must include the exerciser call")
	require.Equal(t, exerciserAddr, *blockOff.Transactions()[1].To(), "block must include the second exerciser call")

	// Consensus-critical equalities.
	require.Equal(t, blockOff.Root(), blockOn.Root(), "state root diverged")
	require.Equal(t, blockOff.GasUsed(), blockOn.GasUsed(), "block gas used diverged")
	require.Equal(t, len(blockOff.Transactions()), len(blockOn.Transactions()), "tx count diverged")
	for i, tx := range blockOff.Transactions() {
		require.Equal(t, tx.Hash(), blockOn.Transactions()[i].Hash(), "tx[%d] diverged", i)
	}
	require.Equal(t,
		types.DeriveSha(receiptsOff, trie.NewStackTrie(nil)),
		types.DeriveSha(receiptsOn, trie.NewStackTrie(nil)),
		"receipts hash diverged",
	)
	require.Equal(t, len(receiptsOff), len(receiptsOn), "receipt count diverged")
	for i := range receiptsOff {
		require.Equal(t, receiptsOff[i].GasUsed, receiptsOn[i].GasUsed, "receipt[%d].GasUsed diverged", i)
		require.Equal(t, receiptsOff[i].CumulativeGasUsed, receiptsOn[i].CumulativeGasUsed, "receipt[%d].CumulativeGasUsed diverged", i)
		require.Equal(t, receiptsOff[i].Status, receiptsOn[i].Status, "receipt[%d].Status diverged", i)
		require.True(t, bytes.Equal(receiptsOff[i].Bloom.Bytes(), receiptsOn[i].Bloom.Bytes()), "receipt[%d].Bloom diverged", i)
	}
}

// TestBuild_SharedCacheRaceFree drives the real block-builder path
// (commitWork → concurrent runPrefetcher + sealing EVM) with
// EnablePrecompileCache ON, so the build prefetcher goroutine and the sealer
// share the single per-cycle *vm.SharedResultCaches instance that commitWork
// creates before launching the prefetcher. It mines blocks that include both
// value transfers (ECRECOVER) and contract creations (KECCAK256). Each mined
// block is committed to the worker's own chain via WriteBlockAndSetHead, so
// chain advancement proves the shared-cache sealer produced a self-consistent,
// committable block; run under `-race` it proves the concurrent prefetcher↔
// sealer cache sharing is race-free. (Giugliano must be active for the build
// prefetcher to launch at all — see the IsGiugliano gate in commitWork.)
func TestBuild_SharedCacheRaceFree(t *testing.T) {
	chainConfig := borUnittestChainConfigWithGiugliano()
	engine, ctrl := getFakeBorFromConfig(t, chainConfig)
	defer engine.Close()
	defer ctrl.Finish()

	db := rawdb.NewMemoryDatabase()
	backend := newTestWorkerBackend(t, chainConfig, engine, db)
	backend.txPool.Add(pendingTxs, false)

	// Turn on the shared VM result caches on the chain's base config; commitWork
	// reads this to decide whether to build the per-cycle cache set.
	backend.chain.GetVMConfig().EnablePrecompileCache = true

	config := DefaultTestConfig()
	config.EnablePrefetch = true
	config.PrefetchGasLimitPercent = 50

	w := newWorker(config, chainConfig, engine, backend, new(event.TypeMux), nil, false, false)
	defer w.close()
	w.setEtherbase(testBankAddress)

	sub := w.mux.Subscribe(core.NewMinedBlockEvent{})
	defer sub.Unsubscribe()

	w.start()

	const wantBlocks = 3
	sawTxs := false
	for i := 0; i < wantBlocks; i++ {
		// Alternate a contract creation (keccak-heavy) and a transfer (ecrecover)
		// so both the widened-keccak and ecrecover caches are shared concurrently.
		tx := backend.newRandomTxWithNonce(i%2 == 0, uint64(i))
		if errs := backend.txPool.Add([]*types.Transaction{tx}, false); errs[0] != nil {
			t.Fatalf("add tx %d: %v", i, errs[0])
		}

		select {
		case ev := <-sub.Chan():
			block := ev.Data.(core.NewMinedBlockEvent).Block
			if len(block.Transactions()) > 0 {
				sawTxs = true
			}
		case <-time.After(8 * time.Second):
			t.Fatalf("timed out waiting for mined block %d", i)
		}
	}

	w.stop()
	require.GreaterOrEqual(t, w.chain.CurrentBlock().Number.Uint64(), uint64(wantBlocks),
		"worker chain did not advance to the expected height — sealed blocks failed to commit")
	require.True(t, sawTxs, "no mined block included any transaction; caches were never exercised")
}
