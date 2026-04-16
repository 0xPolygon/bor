package miner

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/consensus/misc/eip1559"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/params"
)

// Pipelined SRC metrics
var (
	pipelineSpeculativeBlocksCounter = metrics.NewRegisteredCounter("worker/pipelineSpeculativeBlocks", nil)
	pipelineSpeculativeAbortsCounter = metrics.NewRegisteredCounter("worker/pipelineSpeculativeAborts", nil)
	pipelineEIP2935AbortsCounter     = metrics.NewRegisteredCounter("worker/pipelineEIP2935Aborts", nil)
	pipelineSRCTimer                 = metrics.NewRegisteredTimer("worker/pipelineSRCTime", nil)
	pipelineFlatDiffExtractTimer     = metrics.NewRegisteredTimer("worker/pipelineFlatDiffExtractTime", nil)
)

const speculativeEmptyRefillLead = 300 * time.Millisecond

// Refill speculative blocks that are still less than 75% full after the first
// txpool snapshot. This catches the common case where the early snapshot grabs
// a small trickle of txs, but the load ramps up before the slot boundary.
const speculativeLowFillRemainingGasDivisor = 4

// speculativeWorkReq is sent to mainLoop's speculative work channel
// when block N's execution is done and we want to speculatively start N+1.
type speculativeWorkReq struct {
	parentHeader  *types.Header          // block N's header (complete except Root)
	flatDiff      *state.FlatDiff        // block N's state mutations
	parentRoot    common.Hash            // root_{N-1} (last committed trie root)
	blockNEnv     *environment           // block N's execution environment (for assembly later)
	stateSyncData []*types.StateSyncData // from FinalizeForPipeline
}

// placeholderParentHash generates a deterministic placeholder hash for use
// as ParentHash in speculative headers. It must not collide with any real
// block hash.
func placeholderParentHash(blockNumber uint64) common.Hash {
	data := append([]byte("pipelined-src-placeholder:"), new(big.Int).SetUint64(blockNumber).Bytes()...)
	return sha256.Sum256(data)
}

// isPipelineEligible checks whether we can use pipelined SRC for the next
// block. Returns false at sprint boundaries in pre-Rio mode (where
// GetCurrentValidatorsByHash needs a real parent hash).
func (w *worker) isPipelineEligible(currentBlockNumber uint64) bool {
	if !w.config.EnablePipelinedSRC {
		return false
	}
	if w.chainConfig.Bor == nil {
		return false
	}
	if len(w.chainConfig.Bor.Sprint) == 0 {
		return false
	}
	if !w.IsRunning() || w.syncing.Load() {
		return false
	}
	// Pre-Rio: the speculative chain reader provides block N's unsigned header.
	// When snapshot() walks back and calls ecrecover() on this header, it fails
	// because the Extra seal bytes are all zeros (Seal() hasn't run yet).
	// This causes speculative Prepare to always fail with "recovery failed",
	// making the pipeline useless pre-Rio. Skip it entirely.
	nextBlockNumber := currentBlockNumber + 1
	if !w.chainConfig.Bor.IsRio(new(big.Int).SetUint64(nextBlockNumber)) {
		return false
	}
	return true
}

// commitPipelined is the pipelined version of commit(). Instead of calling
// FinalizeAndAssemble (which blocks on IntermediateRoot), it:
//  1. Calls FinalizeForPipeline (state sync, span commits — no IntermediateRoot)
//  2. Extracts FlatDiff
//  3. Sends a speculativeWorkReq to start N+1 execution
//  4. Returns immediately — the SRC goroutine is spawned by commitSpeculativeWork
//     after confirming the speculative Prepare() succeeds. This avoids a trie DB
//     race between the SRC goroutine and the fallback path's inline commit.
func (w *worker) commitPipelined(env *environment, start time.Time) error {
	if !w.IsRunning() {
		return nil
	}

	env = env.copy()

	borEngine, ok := w.engine.(*bor.Bor)
	if !ok {
		log.Error("Pipelined SRC: engine is not Bor")
		return nil
	}

	// Phase 1: Finalize (state sync, span commits) without IntermediateRoot
	stateSyncData, err := borEngine.FinalizeForPipeline(w.chain, env.header, env.state, &types.Body{
		Transactions: env.txs,
	}, env.receipts)
	if err != nil {
		log.Error("Pipelined SRC: FinalizeForPipeline failed", "err", err)
		return err
	}

	// Phase 2: Extract FlatDiff (~1ms, no trie operations)
	flatDiffStart := time.Now()
	flatDiff := env.state.CommitSnapshot(w.chainConfig.IsEIP158(env.header.Number))
	pipelineFlatDiffExtractTimer.Update(time.Since(flatDiffStart))

	// The parent root is root_{N-1}, stored in the parent header.
	parent := w.chain.GetHeader(env.header.ParentHash, env.header.Number.Uint64()-1)
	if parent == nil {
		log.Error("Pipelined SRC: parent not found", "parentHash", env.header.ParentHash)
		return nil
	}
	parentRoot := parent.Root

	w.chain.SetLastFlatDiff(flatDiff, env.header.Number.Uint64(), parentRoot, common.Hash{})
	// Note: this counts block N as "entering the pipeline." If Prepare() fails
	// and fallbackToSequential produces the block inline, the counter is slightly
	// inflated — the block was produced sequentially, not speculatively.
	pipelineSpeculativeBlocksCounter.Inc(1)

	// Phase 3: Send speculative work request for block N+1.
	// The SRC goroutine is NOT spawned here — commitSpeculativeWork spawns it
	// after confirming Prepare() succeeds. If Prepare() fails, fallbackToSequential
	// uses the normal inline FinalizeAndAssemble path (no SRC goroutine).
	select {
	case w.speculativeWorkCh <- &speculativeWorkReq{
		parentHeader:  env.header,
		flatDiff:      flatDiff,
		parentRoot:    parentRoot,
		blockNEnv:     env,
		stateSyncData: stateSyncData,
	}:
	case <-w.exitCh:
		return nil
	}

	return nil
}

// shouldLateRefillSpeculativeBlock reports whether a speculative block should
// take one more txpool snapshot shortly before the slot boundary.
func shouldLateRefillSpeculativeBlock(env *environment) bool {
	if len(env.txs) == 0 {
		return true
	}
	if env.gasPool == nil {
		return true
	}

	// Skip the top-up when the block is already mostly full. Otherwise, give it
	// one late snapshot to catch txs that arrived after the initial early fill.
	return env.gasPool.Gas() > env.header.GasLimit/speculativeLowFillRemainingGasDivisor
}

// fillSpeculativeTransactions snapshots the txpool once immediately, and if
// the speculative block is still underfilled, gives it one more pass shortly
// before the slot boundary. This avoids sealing low/empty speculative blocks
// simply because the initial early snapshot raced ahead of incoming load.
func (w *worker) fillSpeculativeTransactions(env *environment, interrupt *atomic.Int32) time.Duration {
	fillStart := time.Now()
	err := w.fillTransactions(interrupt, env)
	totalFill := time.Since(fillStart)

	if err != nil || !shouldLateRefillSpeculativeBlock(env) {
		return totalFill
	}

	remaining := time.Until(env.header.GetActualTime())
	if remaining <= speculativeEmptyRefillLead {
		return totalFill
	}

	timer := time.NewTimer(remaining - speculativeEmptyRefillLead)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-w.exitCh:
		return totalFill
	}

	refillStart := time.Now()
	_ = w.fillTransactions(interrupt, env)
	totalFill += time.Since(refillStart)

	return totalFill
}

// commitSpeculativeWork handles a speculativeWorkReq: executes block N+1
// speculatively using the FlatDiff overlay, then waits for SRC(N) to complete,
// assembles block N, and sends it for sealing. Then it finalizes N+1 and
// seals it as well.
//
// Returns true when mainLoop should requeue normal work after this function
// returns. This is needed for:
//   - Abort (EIP-2935/BLOCKHASH): the speculative block was discarded, so the
//     block slot must be rebuilt sequentially.
//   - Normal pipeline exit: the last block was sent to sealBlockViaTaskCh, and
//     there is a race where ChainHeadEvent may arrive at newWorkLoop before
//     pendingWorkBlock is cleared, causing the event to be skipped.
//
// Returns false when the pipeline fell back to sequential (fallbackToSequential
// already sealed block N via taskCh → resultLoop → ChainHeadEvent). Retrying
// work in this case creates a tight loop that keeps restarting Seal() with
// fresh timestamps, preventing any block from ever being sealed.
func (w *worker) commitSpeculativeWork(req *speculativeWorkReq) (shouldRetry bool, abortRecovery bool) {
	// Default: retry commitWork after this function returns. This handles the
	// race where ChainHeadEvent from the last pipeline block arrives before
	// pendingWorkBlock is cleared. Fallback paths set shouldRetry = false
	// because they already sealed block N via taskCh (resultLoop handles it).
	shouldRetry = true

	// Ensure pendingWorkBlock is cleared when this function exits, so the
	// next ChainHeadEvent-triggered commitWork can proceed.
	defer w.pendingWorkBlock.Store(0)

	blockNHeader := req.parentHeader
	blockNNumber := blockNHeader.Number.Uint64()
	nextBlockNumber := blockNNumber + 1

	log.Debug("Pipelined SRC: starting speculative execution", "speculativeBlock", nextBlockNumber, "parent", blockNNumber)

	// --- Build speculative header for N+1 ---
	placeholder := placeholderParentHash(blockNNumber)
	specReader := newSpeculativeChainReader(w.chain, blockNHeader, placeholder)
	specContext := newSpeculativeChainContext(specReader, w.engine)

	// Resolve the EVM coinbase the same way the importer does in
	// NewEVMBlockContext(header, chain, nil) — for post-Rio blocks, this
	// uses CalculateCoinbase (from the Bor config), falling back to
	// w.etherbase() if not configured. We must NOT use w.etherbase()
	// directly because the Bor config's Coinbase field may specify a
	// different address (e.g. 0xba5e on some networks).
	var coinbase common.Address
	if w.chainConfig.Bor != nil && w.chainConfig.Bor.IsRio(new(big.Int).SetUint64(nextBlockNumber)) {
		coinbase = common.HexToAddress(w.chainConfig.Bor.CalculateCoinbase(nextBlockNumber))
	}
	if coinbase == (common.Address{}) {
		coinbase = w.etherbase()
	}

	specHeader := &types.Header{
		ParentHash: placeholder,
		Number:     new(big.Int).SetUint64(nextBlockNumber),
		GasLimit:   core.CalcGasLimit(blockNHeader.GasLimit, w.config.GasCeil),
		Time:       blockNHeader.Time + w.chainConfig.Bor.CalculatePeriod(nextBlockNumber),
		Coinbase:   coinbase,
	}
	if w.chainConfig.IsLondon(specHeader.Number) {
		specHeader.BaseFee = eip1559.CalcBaseFee(w.chainConfig, blockNHeader)
	}

	// Call Prepare() via the speculative chain reader.
	// This sets Difficulty, Extra (validator bytes at sprint boundary), and timestamp
	// without sleeping. The timing wait is deferred until after the abort check
	// to avoid wasting a full block period if the speculative block is discarded.
	// NOTE: Prepare() will zero out specHeader.Coinbase. The real coinbase
	// is preserved in the local `coinbase` variable above.
	if err := w.engine.Prepare(specReader, specHeader); err != nil {
		log.Warn("Pipelined SRC: speculative Prepare failed, falling back", "err", err)
		w.fallbackToSequential(req)
		// fallbackToSequential already sealed block N via taskCh. Don't retry —
		// resultLoop will emit ChainHeadEvent which triggers the next commitWork.
		shouldRetry = false
		return
	}

	// Prepare() succeeded — now spawn the background SRC goroutine for block N.
	// This is done HERE (not in commitPipelined) to avoid a trie DB race:
	// if Prepare() fails and we fall back, the fallback path does an inline
	// FinalizeAndAssemble which also commits to the trie. Having both an SRC
	// goroutine AND an inline commit operating on the same parent root causes
	// "missing trie node / layer stale" errors.
	tmpBlock := types.NewBlockWithHeader(req.parentHeader)
	w.chain.SpawnSRCGoroutine(tmpBlock, req.parentRoot, req.flatDiff)

	// --- Open speculative StateDB ---
	specState, err := w.chain.StateAtWithFlatDiff(req.parentRoot, req.flatDiff)
	if err != nil {
		log.Error("Pipelined SRC: failed to open speculative state", "err", err)
		// SRC goroutine is already running — wait for it to finish before
		// fallbackToSequential does IntermediateRoot on the same parent root.
		w.chain.WaitForSRC() //nolint:errcheck
		w.fallbackToSequential(req)
		shouldRetry = false
		return
	}
	specState.StartPrefetcher("miner-speculative", nil, nil)

	// --- Create speculative EVM with SpeculativeGetHashFn ---
	blockN1Header := w.chain.GetHeader(blockNHeader.ParentHash, blockNNumber-1)
	if blockN1Header == nil {
		log.Error("Pipelined SRC: grandparent header not found")
		// SRC goroutine is already running — wait for it to finish before
		// fallbackToSequential does IntermediateRoot on the same parent root.
		w.chain.WaitForSRC() //nolint:errcheck
		w.fallbackToSequential(req)
		shouldRetry = false
		return
	}

	// srcDone is a lazy resolver for block N's hash, used by SpeculativeGetHashFn.
	// Block N's hash isn't known until SRC completes (it depends on the state root).
	// If a tx in the speculative block calls BLOCKHASH(N), SpeculativeGetHashFn
	// calls srcDone() which blocks on WaitForSRC, resolves the hash, and sets the
	// blockhashNAccessed flag to trigger an abort (since the pre-seal hash won't
	// match the final on-chain hash).
	var blockNHash common.Hash
	var blockNHashResolved bool
	var resolveMu sync.Mutex

	srcDone := func() common.Hash {
		resolveMu.Lock()
		defer resolveMu.Unlock()
		if blockNHashResolved {
			return blockNHash
		}
		root, _, err := w.chain.WaitForSRC()
		if err != nil {
			log.Error("Pipelined SRC: SRC failed during BLOCKHASH resolution", "err", err)
			return common.Hash{}
		}
		finalHeader := types.CopyHeader(blockNHeader)
		finalHeader.Root = root
		finalHeader.UncleHash = types.CalcUncleHash(nil)
		blockNHash = finalHeader.Hash()
		blockNHashResolved = true
		return blockNHash
	}

	var blockhashNAccessed atomic.Bool
	specGetHash := core.SpeculativeGetHashFn(blockN1Header, specContext, blockNNumber, srcDone, &blockhashNAccessed)

	evmContext := core.NewEVMBlockContext(specHeader, specContext, &coinbase)
	evmContext.GetHash = specGetHash

	specEnv := &environment{
		signer:         types.MakeSigner(w.chainConfig, specHeader.Number, specHeader.Time),
		state:          specState,
		size:           uint64(specHeader.Size()),
		coinbase:       coinbase,
		buildInterrupt: newBuildInterruptState(),
		header:         specHeader,
		evm:            vm.NewEVM(evmContext, specState, w.chainConfig, vm.Config{}),
	}
	specEnv.evm.SetInterrupt(specEnv.buildInterrupt.timeoutFlag())
	specEnv.tcount = 0

	// NOTE: ProcessParentBlockHash is NOT called during speculative execution.
	// It will be called after block N is written and the real hash is known,
	// before FinalizeAndAssemble for N+1.

	// --- Reset txpool state for speculative execution ---
	specTxPoolState, err := w.chain.StateAtWithFlatDiff(req.parentRoot, req.flatDiff)
	if err != nil {
		log.Error("Pipelined SRC: failed to create txpool speculative state", "err", err)
	} else {
		w.eth.TxPool().ResetSpeculativeState(blockNHeader, specTxPoolState)
	}

	// --- Fill transactions for N+1 (in goroutine) ---
	// fillTransactions runs concurrently with SRC(N) so that sealing block N
	// is not delayed by filling block N+1's transactions.
	initialFillDone := make(chan struct{})
	defer func() { <-initialFillDone }() // ensure goroutine is drained on all return paths
	var eip2935Abort bool

	go func() {
		defer close(initialFillDone)

		specStopFn := createInterruptTimer(
			specHeader.Number.Uint64(),
			specHeader.GetActualTime(),
			specEnv.buildInterrupt,
			true, // pipelinedSRC — no 500ms buffer
		)

		var specInterrupt atomic.Int32
		w.fillSpeculativeTransactions(specEnv, &specInterrupt)
		specStopFn()

		// Check abort conditions (needs fill to be done). The final discard
		// log is emitted in the main loop so each aborted block is logged once.
		if w.chainConfig.IsPrague(specHeader.Number) {
			dangerousSlot := common.BigToHash(new(big.Int).SetUint64(blockNNumber % params.HistoryServeWindow))
			if specState.WasStorageSlotRead(params.HistoryStorageAddress, dangerousSlot) {
				eip2935Abort = true
				pipelineEIP2935AbortsCounter.Inc(1)
			}
		}
	}()

	// --- Wait for SRC(N) to complete ---
	// No longer blocked by fillTransactions — block N is sealed as soon as SRC finishes.
	srcStart := time.Now()
	root, witnessN, err := w.chain.WaitForSRC()
	pipelineSRCTimer.Update(time.Since(srcStart))
	if err != nil {
		log.Error("Pipelined SRC: SRC(N) failed", "block", blockNNumber, "err", err)
		pipelineSpeculativeAbortsCounter.Inc(1)
		return
	}

	// --- Assemble and seal block N ---
	borEngine, ok := w.engine.(*bor.Bor)
	if !ok {
		log.Error("Pipelined SRC: engine is not Bor")
		return
	}

	finalHeaderN := types.CopyHeader(blockNHeader)
	finalHeaderN.Root = root
	blockN, receiptsN, err := borEngine.AssembleBlock(w.chain, finalHeaderN, req.blockNEnv.state, &types.Body{
		Transactions: req.blockNEnv.txs,
	}, req.blockNEnv.receipts, root, req.stateSyncData)
	if err != nil {
		log.Error("Pipelined SRC: AssembleBlock(N) failed", "err", err)
		return
	}

	// Block N uses the pipelined write path to avoid a double CommitWithUpdate
	// from the same parent root (one from the SRC goroutine, one from the normal
	// writeBlockWithState). The SRC goroutine's witness is complete.
	select {
	case w.taskCh <- &task{receipts: receiptsN, state: req.blockNEnv.state, block: blockN, createdAt: time.Now(), pipelined: true, witnessBytes: witnessN}:
		if w.config.PipelinedSRCLogs {
			log.Info("Pipelined SRC: block N sent for sealing", "number", blockN.Number(), "txs", len(blockN.Transactions()), "root", root)
		}
	case <-w.exitCh:
		shouldRetry = false
		return
	}

	// Wait for block N to be written to the chain before sending N+1.
	blockNNum := blockN.NumberU64()
	waitDeadline := time.After(30 * time.Second)
	for {
		if current := w.chain.CurrentBlock(); current != nil && current.Number.Uint64() >= blockNNum {
			break
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-waitDeadline:
			log.Error("Pipelined SRC: timed out waiting for block N to be written", "number", blockNNum)
			return
		case <-w.exitCh:
			shouldRetry = false
			return
		}
	}

	// Get the REAL block N hash from the chain — this is the signed hash
	// written by resultLoop after Seal() modified header.Extra.
	chainHead := w.chain.CurrentBlock()
	if chainHead == nil {
		log.Error("Pipelined SRC: chain head is nil after waiting", "expected", blockNNum)
		return
	}
	if chainHead.Number.Uint64() != blockNNum {
		log.Error("Pipelined SRC: chain head mismatch after waiting", "expected", blockNNum,
			"got", chainHead.Number.Uint64())
		return
	}
	realBlockNHash := chainHead.Hash()
	rootN := root // state root of the last written block

	// Wait for the initial fillTransactions goroutine to finish before entering
	// the loop — the loop's first iteration checks abort conditions from the fill.
	// (The defer also drains this, but we need the results here, not just cleanup.)
	<-initialFillDone

	// --- CONTINUOUS PIPELINE LOOP ---
	// State at this point:
	//   - Block N is written to chain, realBlockNHash is known
	//   - Speculative execution of N+1 is complete (specHeader, specState, specEnv)
	//   - rootN is block N's committed state root
	//   - eip2935Abort and blockhashNAccessed track N+1's abort conditions
	curBlockhashAccessed := &blockhashNAccessed
	var prevDBWriteDone chan struct{}  // tracks the previous iteration's async DB write
	var lastSealedHeader *types.Header // header of the last inline-sealed block (for grandparent lookup)

	for {
		// --- Check abort conditions for current speculative block ---
		shouldAbort := false
		if eip2935Abort {
			log.Warn("Pipelined SRC: discarding speculative block — EIP-2935 slot accessed",
				"block", nextBlockNumber)
			pipelineSpeculativeAbortsCounter.Inc(1)
			shouldAbort = true
		}
		if !shouldAbort && curBlockhashAccessed.Load() {
			log.Warn("Pipelined SRC: discarding speculative block — BLOCKHASH(N) was accessed",
				"block", nextBlockNumber, "pendingBlockN", blockNNumber)
			pipelineSpeculativeAbortsCounter.Inc(1)
			shouldAbort = true
		}
		if shouldAbort {
			// Break out of the loop — mainLoop always calls commitWork after
			// this function returns, which rebuilds the block sequentially.
			// Mark the next commit as abort recovery so Bor.Prepare keeps the
			// original slot instead of pushing late rebuilds into the next one.
			abortRecovery = true
			break
		}

		// --- Wait for previous async DB write before finalize ---
		// FinalizeForPipeline may call into state sync / span commit code that
		// reads block headers and state from the chain DB. If the previous
		// inline-sealed block hasn't been persisted yet, those lookups fail.
		if prevDBWriteDone != nil {
			<-prevDBWriteDone
			prevDBWriteDone = nil
		}

		// --- Finalize current speculative block ---
		finalSpecHeader := types.CopyHeader(specHeader)
		finalSpecHeader.ParentHash = realBlockNHash

		if w.chainConfig.IsPrague(finalSpecHeader.Number) {
			evmCtx := core.NewEVMBlockContext(finalSpecHeader, w.chain, &coinbase)
			vmenv := vm.NewEVM(evmCtx, specState, w.chainConfig, vm.Config{})
			core.ProcessParentBlockHash(realBlockNHash, vmenv)
		}

		specStateSyncData, err := borEngine.FinalizeForPipeline(w.chain, finalSpecHeader, specState, &types.Body{
			Transactions: specEnv.txs,
		}, specEnv.receipts)
		if err != nil {
			log.Error("Pipelined SRC: FinalizeForPipeline failed", "block", nextBlockNumber, "err", err)
			break
		}

		flatDiff := specState.CommitSnapshot(w.chainConfig.IsEIP158(finalSpecHeader.Number))

		// --- Check if we can continue the pipeline for the next block ---
		nextNextBlockNumber := nextBlockNumber + 1
		if !w.isPipelineEligible(nextBlockNumber) || !w.IsRunning() {
			// Last block in the pipeline — seal synchronously via taskCh so that
			// resultLoop emits ChainHeadEvent and normal block production resumes.
			w.sealBlockViaTaskCh(borEngine, finalSpecHeader, specState, specEnv.txs,
				specEnv.receipts, specStateSyncData, rootN, flatDiff, true)
			break
		}

		// --- Build speculative environment for the NEXT block (N+2) ---
		placeholderNext := placeholderParentHash(nextBlockNumber)
		specReaderNext := newSpeculativeChainReader(w.chain, finalSpecHeader, placeholderNext)
		specContextNext := newSpeculativeChainContext(specReaderNext, w.engine)

		var coinbaseNext common.Address
		if w.chainConfig.Bor != nil && w.chainConfig.Bor.IsRio(new(big.Int).SetUint64(nextNextBlockNumber)) {
			coinbaseNext = common.HexToAddress(w.chainConfig.Bor.CalculateCoinbase(nextNextBlockNumber))
		}
		if coinbaseNext == (common.Address{}) {
			coinbaseNext = w.etherbase()
		}

		specHeaderNext := &types.Header{
			ParentHash: placeholderNext,
			Number:     new(big.Int).SetUint64(nextNextBlockNumber),
			GasLimit:   core.CalcGasLimit(finalSpecHeader.GasLimit, w.config.GasCeil),
			Time:       finalSpecHeader.Time + w.chainConfig.Bor.CalculatePeriod(nextNextBlockNumber),
			Coinbase:   coinbaseNext,
		}
		if w.chainConfig.IsLondon(specHeaderNext.Number) {
			specHeaderNext.BaseFee = eip1559.CalcBaseFee(w.chainConfig, finalSpecHeader)
		}

		// Prepare() sets header fields without sleeping.
		// The timing wait is deferred to just before sealing, after the abort check.
		// This avoids wasting a full block period if the speculative block is aborted.
		if err := w.engine.Prepare(specReaderNext, specHeaderNext); err != nil {
			log.Warn("Pipelined SRC: Prepare failed for next block, sealing current",
				"block", nextNextBlockNumber, "err", err)
			w.sealBlockViaTaskCh(borEngine, finalSpecHeader, specState, specEnv.txs,
				specEnv.receipts, specStateSyncData, rootN, flatDiff, true)
			break
		}

		// --- Spawn SRC for current speculative block (overlaps with next block's execution) ---
		srcSpawnTime := time.Now()
		tmpBlockCur := types.NewBlockWithHeader(finalSpecHeader)
		w.chain.SpawnSRCGoroutine(tmpBlockCur, rootN, flatDiff)
		w.chain.SetLastFlatDiff(flatDiff, finalSpecHeader.Number.Uint64(), rootN, common.Hash{})
		if w.config.PipelinedSRCLogs {
			log.Info("Pipelined SRC: spawned SRC, starting speculative exec",
				"srcBlock", nextBlockNumber, "specExecBlock", nextNextBlockNumber)
		}

		// --- Open speculative state for next block ---
		specStateNext, err := w.chain.StateAtWithFlatDiff(rootN, flatDiff)
		if err != nil {
			log.Error("Pipelined SRC: failed to open speculative state for next block",
				"block", nextNextBlockNumber, "err", err)
			// SRC is already running — wait for it and seal current block
			w.sealBlockViaTaskCh(borEngine, finalSpecHeader, specState, specEnv.txs,
				specEnv.receipts, specStateSyncData, rootN, flatDiff, false)
			break
		}
		specStateNext.StartPrefetcher("miner-speculative", nil, nil)

		// --- Build SpeculativeGetHashFn for next block ---
		// Use lastSealedHeader if available (the async DB write may not have
		// persisted it yet), otherwise fall back to the chain DB.
		var grandparentHeader *types.Header
		if lastSealedHeader != nil && lastSealedHeader.Number.Uint64() == blockNNumber {
			grandparentHeader = lastSealedHeader
		} else {
			grandparentHeader = w.chain.GetHeaderByNumber(blockNNumber)
		}
		if grandparentHeader == nil {
			log.Error("Pipelined SRC: grandparent header not found for next block", "number", blockNNumber)
			w.sealBlockViaTaskCh(borEngine, finalSpecHeader, specState, specEnv.txs,
				specEnv.receipts, specStateSyncData, rootN, flatDiff, false)
			break
		}

		var nextBlockHash common.Hash
		var nextBlockHashResolved bool
		var nextResolveMu sync.Mutex

		srcDoneNext := func() common.Hash {
			nextResolveMu.Lock()
			defer nextResolveMu.Unlock()
			if nextBlockHashResolved {
				return nextBlockHash
			}
			rootSpec, _, err := w.chain.WaitForSRC()
			if err != nil {
				log.Error("Pipelined SRC: SRC failed during BLOCKHASH resolution", "err", err)
				return common.Hash{}
			}
			finalH := types.CopyHeader(finalSpecHeader)
			finalH.Root = rootSpec
			finalH.UncleHash = types.CalcUncleHash(nil)
			nextBlockHash = finalH.Hash()
			nextBlockHashResolved = true
			return nextBlockHash
		}

		nextBlockhashAccessed := new(atomic.Bool)
		specGetHashNext := core.SpeculativeGetHashFn(grandparentHeader, specContextNext, nextBlockNumber, srcDoneNext, nextBlockhashAccessed)

		evmContextNext := core.NewEVMBlockContext(specHeaderNext, specContextNext, &coinbaseNext)
		evmContextNext.GetHash = specGetHashNext

		specEnvNext := &environment{
			signer:         types.MakeSigner(w.chainConfig, specHeaderNext.Number, specHeaderNext.Time),
			state:          specStateNext,
			size:           uint64(specHeaderNext.Size()),
			coinbase:       coinbaseNext,
			buildInterrupt: newBuildInterruptState(),
			header:         specHeaderNext,
			evm:            vm.NewEVM(evmContextNext, specStateNext, w.chainConfig, vm.Config{}),
		}
		specEnvNext.evm.SetInterrupt(specEnvNext.buildInterrupt.timeoutFlag())
		specEnvNext.tcount = 0

		// --- Reset txpool and fill transactions for next block (in goroutine) ---
		// fillTransactions runs concurrently with SRC so that sealing block N
		// is not delayed by filling block N+1's transactions.
		specTxPoolStateNext, err := w.chain.StateAtWithFlatDiff(rootN, flatDiff)
		if err != nil {
			log.Error("Pipelined SRC: failed to create txpool state for next block", "err", err)
		} else {
			w.eth.TxPool().ResetSpeculativeState(finalSpecHeader, specTxPoolStateNext)
		}

		fillDone := make(chan struct{})
		var nextEIP2935Abort bool
		var fillElapsed time.Duration

		go func() {
			defer close(fillDone)

			specStopFnNext := createInterruptTimer(
				specHeaderNext.Number.Uint64(),
				specHeaderNext.GetActualTime(),
				specEnvNext.buildInterrupt,
				true, // pipelinedSRC — no 500ms buffer
			)

			var specInterruptNext atomic.Int32
			fillElapsed = w.fillSpeculativeTransactions(specEnvNext, &specInterruptNext)
			specStopFnNext()

			// Check EIP-2935 abort for next block (needs fill to be done
			// so WasStorageSlotRead can inspect accessed slots). The final
			// discard log is emitted in the main loop so each aborted block is
			// logged once.
			if w.chainConfig.IsPrague(specHeaderNext.Number) {
				dangerousSlot := common.BigToHash(new(big.Int).SetUint64(nextBlockNumber % params.HistoryServeWindow))
				if specStateNext.WasStorageSlotRead(params.HistoryStorageAddress, dangerousSlot) {
					nextEIP2935Abort = true
					pipelineEIP2935AbortsCounter.Inc(1)
				}
			}
		}()

		// --- Wait for SRC of current speculative block ---
		// No longer blocked by fillTransactions — SRC result is collected as
		// soon as the goroutine completes, allowing immediate sealing.
		srcWaitStart := time.Now()
		rootSpec, witnessSpec, err := w.chain.WaitForSRC()
		srcWaitElapsed := time.Since(srcWaitStart)
		srcTotalElapsed := time.Since(srcSpawnTime)
		pipelineSRCTimer.Update(srcTotalElapsed)
		if err != nil {
			log.Error("Pipelined SRC: SRC failed", "block", nextBlockNumber, "err", err)
			pipelineSpeculativeAbortsCounter.Inc(1)
			<-fillDone // wait for goroutine before breaking
			break
		}
		if w.config.PipelinedSRCLogs {
			log.Info("Pipelined SRC: SRC completed",
				"block", nextBlockNumber, "srcWait", srcWaitElapsed)
		}

		// --- Assemble current speculative block ---
		blockSpec, receiptsSpec, err := borEngine.AssembleBlock(w.chain, finalSpecHeader, specState, &types.Body{
			Transactions: specEnv.txs,
		}, specEnv.receipts, rootSpec, specStateSyncData)
		if err != nil {
			log.Error("Pipelined SRC: AssembleBlock failed", "block", nextBlockNumber, "err", err)
			<-fillDone // wait for goroutine before breaking
			break
		}

		// Update pendingWorkBlock BEFORE inline write so that newWorkLoop skips
		// the ChainHeadEvent for this block. pendingWorkBlock = nextBlockNumber + 1
		// means "we're working on nextBlockNumber+1, so skip ChainHeadEvent for nextBlockNumber".
		w.pendingWorkBlock.Store(nextBlockNumber + 1)

		// --- Wait for the block's target timestamp before sealing ---
		// Since Prepare() was called without sleeping, we wait here instead.
		// This is AFTER the abort check — if the block was aborted, we skip
		// this wait entirely (zero wasted time).
		if delay := time.Until(finalSpecHeader.GetActualTime()); delay > 0 {
			select {
			case <-time.After(delay):
			case <-w.exitCh:
				<-fillDone // wait for goroutine before returning
				if prevDBWriteDone != nil {
					<-prevDBWriteDone
				}
				shouldRetry = false
				return // defer clears pendingWorkBlock
			}
		}

		// --- Inline seal + broadcast (bypass taskLoop/resultLoop) ---
		// prevDBWriteDone was already awaited before FinalizeForPipeline above.
		// The DB write runs asynchronously — the pipeline proceeds without waiting.
		sealedBlock, dbWriteDone, err := w.inlineSealAndBroadcast(blockSpec, receiptsSpec, specState, witnessSpec)
		if err != nil {
			log.Error("Pipelined SRC: inline seal failed", "block", nextBlockNumber, "err", err)
			<-fillDone // wait for goroutine before breaking
			break
		}

		// Wait for fillTransactions goroutine to finish before next iteration.
		// The abort conditions (EIP-2935, BLOCKHASH) are checked at the top of
		// the next loop iteration, which requires fill to be complete.
		<-fillDone
		prevDBWriteDone = dbWriteDone
		pipelineSpeculativeBlocksCounter.Inc(1)

		if w.config.PipelinedSRCLogs {
			log.Info("Pipelined SRC: block sealed (inline)", "number", sealedBlock.Number(),
				"txs", len(sealedBlock.Transactions()), "root", rootSpec,
				"fillBlock", nextNextBlockNumber, "fillElapsed", fillElapsed)
		}

		// --- Shift variables for next iteration ---
		lastSealedHeader = sealedBlock.Header()
		blockNNumber = nextBlockNumber
		nextBlockNumber = nextNextBlockNumber
		rootN = rootSpec
		realBlockNHash = sealedBlock.Hash()
		specHeader = specHeaderNext
		specState = specStateNext
		specEnv = specEnvNext
		coinbase = coinbaseNext
		eip2935Abort = nextEIP2935Abort
		curBlockhashAccessed = nextBlockhashAccessed
	}

	// Wait for the last async DB write to complete before exiting.
	if prevDBWriteDone != nil {
		<-prevDBWriteDone
	}
	return shouldRetry, abortRecovery
}

// fallbackToSequential computes the state root inline and assembles block N
// without a background SRC goroutine. This avoids trie DB races between
// background and inline commits.
func (w *worker) fallbackToSequential(req *speculativeWorkReq) {
	if w.config.PipelinedSRCLogs {
		log.Info("Pipelined SRC: falling back to sequential execution")
	}
	pipelineSpeculativeAbortsCounter.Inc(1)

	borEngine, ok := w.engine.(*bor.Bor)
	if !ok {
		return
	}

	root := req.blockNEnv.state.IntermediateRoot(w.chainConfig.IsEIP158(req.blockNEnv.header.Number))

	block, receipts, err := borEngine.AssembleBlock(w.chain, req.blockNEnv.header, req.blockNEnv.state, &types.Body{
		Transactions: req.blockNEnv.txs,
	}, req.blockNEnv.receipts, root, req.stateSyncData)
	if err != nil {
		log.Error("Pipelined SRC: AssembleBlock failed during fallback", "err", err)
		return
	}

	select {
	case w.taskCh <- &task{receipts: receipts, state: req.blockNEnv.state, block: block, createdAt: time.Now()}:
		if w.config.PipelinedSRCLogs {
			log.Info("Pipelined SRC: fallback block sealed", "number", block.Number(), "root", root)
		}
	case <-w.exitCh:
	}
}

// sealBlockViaTaskCh spawns SRC (if needed), waits for the root, assembles the
// block, and sends it through the normal taskCh → taskLoop → Seal → resultLoop
// path. Used for the last block in a pipeline run so that resultLoop emits
// ChainHeadEvent and normal block production resumes immediately.
func (w *worker) sealBlockViaTaskCh(
	borEngine *bor.Bor,
	finalHeader *types.Header,
	statedb *state.StateDB,
	txs []*types.Transaction,
	receipts []*types.Receipt,
	stateSyncData []*types.StateSyncData,
	rootN common.Hash,
	flatDiff *state.FlatDiff,
	spawnSRC bool, // false if SRC goroutine is already running
) {
	if spawnSRC {
		tmpBlock := types.NewBlockWithHeader(finalHeader)
		w.chain.SpawnSRCGoroutine(tmpBlock, rootN, flatDiff)
		w.chain.SetLastFlatDiff(flatDiff, finalHeader.Number.Uint64(), rootN, common.Hash{})
	}
	pipelineSpeculativeBlocksCounter.Inc(1)

	rootSpec, witnessSpec, err := w.chain.WaitForSRC()
	if err != nil {
		log.Error("Pipelined SRC: SRC failed", "block", finalHeader.Number, "err", err)
		return
	}

	block, blockReceipts, err := borEngine.AssembleBlock(w.chain, finalHeader, statedb, &types.Body{
		Transactions: txs,
	}, receipts, rootSpec, stateSyncData)
	if err != nil {
		log.Error("Pipelined SRC: AssembleBlock failed", "block", finalHeader.Number, "err", err)
		return
	}

	// Wait for the block's target timestamp before sending to taskCh.
	// Since Prepare() was called without sleeping, we wait here instead.
	if delay := time.Until(finalHeader.GetActualTime()); delay > 0 {
		select {
		case <-time.After(delay):
		case <-w.exitCh:
			return
		}
	}

	select {
	case w.taskCh <- &task{receipts: blockReceipts, state: statedb, block: block, createdAt: time.Now(), pipelined: true, witnessBytes: witnessSpec}:
		if w.config.PipelinedSRCLogs {
			log.Info("Pipelined SRC: block sealed", "number", block.Number(),
				"txs", len(block.Transactions()), "root", rootSpec)
		}
	case <-w.exitCh:
	}
}

// inlineSealAndBroadcast seals a pipelined block using a private channel
// (bypassing taskLoop/resultLoop), broadcasts it to peers immediately, and
// writes to the chain DB asynchronously. This avoids blocking the pipeline
// on the DB write — the next iteration can start as soon as the block is sealed.
//
// Returns the sealed block and a channel that closes when the async DB write
// completes. The caller must wait on writeDone before the node can serve the
// block data from DB, but the pipeline can proceed immediately.
//
// Uses emitHeadEvent=false to avoid a deadlock: mainLoop is blocked in
// commitSpeculativeWork, so chainHeadFeed.Send would eventually block when
// newWorkLoop's channel fills up.
func (w *worker) inlineSealAndBroadcast(block *types.Block, receipts []*types.Receipt, statedb *state.StateDB, witnessBytes []byte) (*types.Block, chan struct{}, error) {
	// Seal the block via a private channel — reuses Seal() without contention
	// on the shared w.resultCh. For primary producers on Bhilai+, delay=0.
	sealCh := make(chan *consensus.NewSealedBlockEvent, 1)
	stopCh := make(chan struct{})

	if err := w.engine.Seal(w.chain, block, nil, sealCh, stopCh); err != nil {
		return nil, nil, fmt.Errorf("seal failed: %w", err)
	}

	var sealedBlock *types.Block
	select {
	case ev := <-sealCh:
		if ev == nil || ev.Block == nil {
			return nil, nil, errors.New("nil sealed block from Seal")
		}
		sealedBlock = ev.Block
	case <-time.After(5 * time.Second):
		close(stopCh)
		return nil, nil, errors.New("inline seal timed out")
	case <-w.exitCh:
		close(stopCh)
		return nil, nil, errors.New("worker stopped during inline seal")
	}

	hash := sealedBlock.Hash()

	// Fix up receipt block hashes (same as resultLoop)
	sealedReceipts := make([]*types.Receipt, len(receipts))
	var logs []*types.Log

	for i, r := range receipts {
		receipt := new(types.Receipt)
		sealedReceipts[i] = receipt
		*receipt = *r

		receipt.BlockHash = hash
		receipt.BlockNumber = sealedBlock.Number()
		receipt.TransactionIndex = uint(i)

		receipt.Logs = make([]*types.Log, len(r.Logs))
		for j, l := range r.Logs {
			logCopy := new(types.Log)
			receipt.Logs[j] = logCopy
			*logCopy = *l
			logCopy.BlockHash = hash
		}

		logs = append(logs, receipt.Logs...)
	}

	log.Info("Successfully sealed new block", "number", sealedBlock.Number(),
		"sealhash", w.engine.SealHash(sealedBlock.Header()), "hash", hash,
		"elapsed", "inline")

	// Cache the witness so the WIT protocol can serve it to stateless peers
	// immediately, without waiting for the async DB write.
	if len(witnessBytes) > 0 {
		w.chain.CacheWitness(hash, witnessBytes)
	}

	// Broadcast to peers BEFORE writing to DB — the block is fully valid and
	// sealed, so peers can start processing it immediately. The DB write is
	// not needed for broadcast.
	w.mux.Post(core.NewMinedBlockEvent{Block: sealedBlock, SealedAt: time.Now()})

	sealedBlocksCounter.Inc(1)
	if sealedBlock.Transactions().Len() == 0 {
		sealedEmptyBlocksCounter.Inc(1)
	}
	w.clearPending(sealedBlock.NumberU64())

	// Write to chain DB asynchronously — the pipeline can proceed with the
	// next iteration using sealedBlock.Hash() directly, without waiting for
	// the DB write to complete.
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		_, err := w.chain.WriteBlockAndSetHeadPipelined(sealedBlock, sealedReceipts, logs, statedb, false, witnessBytes)
		if err != nil {
			log.Error("Pipelined SRC: async DB write failed", "block", sealedBlock.Number(), "err", err)
		}
	}()

	return sealedBlock, writeDone, nil
}
