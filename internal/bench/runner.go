package bench

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	borconsensus "github.com/ethereum/go-ethereum/consensus/bor"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/internal/cli/server"
	"github.com/ethereum/go-ethereum/log"
)

const (
	// preflightPeekCount is how many transactions to examine during preflight.
	preflightPeekCount = 100

	// miningStartTimeout is how long to wait for the first block after starting mining.
	miningStartTimeout = 30 * time.Second

	// completionPollInterval is how often to check if all txs are mined.
	completionPollInterval = 1 * time.Millisecond
)

// Run executes a benchmark run with the given options.
// It returns nil on success or an error describing the failure.
func Run(ctx context.Context, opts Options) error {
	// Validate options
	if err := opts.Validate(); err != nil {
		return err
	}

	// Initialize report
	report := NewReport(opts)

	// Helper to set failure and write report
	fail := func(err error) error {
		report.SetFailure(err)
		if writeErr := report.Write(opts.OutPath); writeErr != nil {
			log.Error("Failed to write report", "err", writeErr)
		}
		return err
	}

	// Hash input files for reproducibility
	if err := report.HashInputFiles(opts); err != nil {
		return fail(WrapError(ErrCodeConfigLoad, "hash input files", err))
	}

	// Get expected chain ID from genesis
	expectedChainID, err := GetGenesisChainID(opts.GenesisPath)
	if err != nil {
		return fail(WrapError(ErrCodeConfigLoad, "get genesis chain ID", err))
	}
	log.Info("Loaded genesis chain ID", "chainID", expectedChainID)

	// Preflight: validate dataset before starting server
	log.Info("Running preflight dataset validation", "txsPath", opts.TxsPath)
	preflight, err := PreflightDataset(opts.TxsPath, expectedChainID, preflightPeekCount)
	if err != nil {
		return fail(err)
	}

	// Load and prepare config
	cfg, cleanup, err := LoadConfig(opts)
	if err != nil {
		return fail(err)
	}
	defer cleanup()

	// Validate config
	if err := ValidateConfig(cfg); err != nil {
		return fail(WrapError(ErrCodeConfigLoad, "validate config", err))
	}

	etherbase := GetConfigEtherbase(cfg)
	if etherbase == "" {
		return fail(NewBenchError(ErrCodeSignerNotAuthorized,
			"no etherbase configured; set miner.etherbase in config or ensure genesis has funded accounts"))
	}
	log.Info("Using etherbase", "address", etherbase)

	// Start server
	log.Info("Starting Bor server", "datadir", cfg.DataDir)
	srv, err := server.NewServer(cfg)
	if err != nil {
		return fail(WrapError(ErrCodeServerStart, "start server", err))
	}
	defer srv.Stop()

	backend := srv.Backend()
	if backend == nil {
		return fail(NewBenchError(ErrCodeServerStart, "ethereum backend is nil"))
	}

	// Set up mining authorization
	// In benchmark mode we bypass wallet-based signing since we use DevFakeAuthor
	backend.SetAuthorized(true)
	eb := common.HexToAddress(etherbase)
	backend.SetEtherbase(eb)

	// Authorize the consensus engine with a dummy signer
	if borEngine, ok := backend.Engine().(*borconsensus.Bor); ok {
		borEngine.Authorize(eb, func(_ accounts.Account, _ string, _ []byte) ([]byte, error) {
			return make([]byte, types.ExtraSealLength), nil
		})
		log.Info("Authorized Bor consensus engine", "signer", eb.Hex())
	} else {
		log.Warn("Consensus engine is not Bor; mining may not work correctly")
	}

	// Validate nonces against chain state
	if len(preflight.SenderNonces) > 0 {
		getNonce := func(addr common.Address) uint64 {
			return backend.TxPool().PoolNonce(addr)
		}
		if err := ValidateNonceAgainstChain(preflight, getNonce); err != nil {
			return fail(err)
		}
	}

	// Record starting block
	startBlock := backend.BlockChain().CurrentBlock().Number.Uint64()

	// Start mined tracker before ingestion
	tracker := NewMinedTracker(backend)
	stopTracker := make(chan struct{})
	go tracker.Watch(stopTracker)
	defer close(stopTracker)

	// Phase 1: Read all transactions into memory (~30MB for 100k txs)
	// Do this BEFORE starting mining to overlap I/O + decode with mining startup.
	log.Info("Starting transaction ingestion", "txsPath", opts.TxsPath)
	var allTxs []*types.Transaction
	var totalGas uint64
	result, err := ReadTransactions(opts.TxsPath, func(line int, tx *types.Transaction) error {
		allTxs = append(allTxs, tx)
		return nil
	})
	if err != nil {
		return fail(err)
	}
	totalGas = result.TotalGas
	report.TotalTxsInput = result.TotalTxs
	log.Info("Read all transactions into memory",
		"totalTxs", result.TotalTxs,
		"totalGas", result.TotalGas)

	// Phase 2: Parallel sender recovery across all CPUs.
	// This pre-warms the per-transaction sender cache so that pool validation
	// (which calls types.Sender()) gets an instant cache hit instead of doing
	// expensive secp256k1 ecrecover.
	signer := types.LatestSignerForChainID(expectedChainID)
	log.Info("Starting parallel sender recovery", "txs", len(allTxs), "threads", runtime.NumCPU())
	recoverStart := time.Now()
	parallelRecoverSenders(signer, allTxs)
	log.Info("Sender recovery complete", "elapsed", time.Since(recoverStart))

	// Phase 3: Mark total transaction count for the mined tracker
	// (replaces per-hash MarkSeen — tracker now counts via GasUsed)
	tracker.MarkSeenCount(uint64(len(allTxs)))

	// Phase 4: Submit ALL transactions to pool BEFORE starting mining.
	// This ensures the very first commitWork has all txs available,
	// producing maximally-full blocks and minimizing inter-block gaps.
	// The last batch uses sync=true to wait for promotion to pending.
	const batchSize = 4096
	for i := 0; i < len(allTxs); i += batchSize {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return fail(ctx.Err())
		default:
		}

		end := i + batchSize
		if end > len(allTxs) {
			end = len(allTxs)
		}
		batch := allTxs[i:end]

		// Use sync=true to ensure each batch is fully promoted to pending
		// before we submit the next one. This guarantees all txs are in the
		// pending set before mining starts, producing maximally-full blocks.
		errs := backend.TxPool().Add(batch, true)
		for j, err := range errs {
			if err != nil {
				tx := batch[j]
				log.Error("TxPool rejected transaction",
					"index", i+j,
					"hash", tx.Hash(),
					"nonce", tx.Nonce(),
					"err", err)
				return fail(WrapError(ErrCodeTxPoolReject,
					fmt.Sprintf("index %d: txpool rejected tx %s", i+j, tx.Hash().Hex()), err))
			}
		}

		if i == 0 || (i+batchSize)%10000 < batchSize {
			log.Info("Ingested batch", "txsSoFar", end, "total", len(allTxs))
		}
	}
	log.Info("Transaction ingestion complete",
		"totalTxs", result.TotalTxs,
		"totalGas", result.TotalGas)

	// Wait for all transactions to be promoted to pending state before starting
	// mining. This ensures the first commitWork sees all txs in Pending(), producing
	// maximally-full blocks and minimizing the number of blocks (and inter-block gaps).
	for {
		pending, queued := backend.TxPool().Stats()
		if uint64(pending) >= uint64(result.TotalTxs) {
			log.Info("All transactions promoted to pending", "pending", pending, "queued", queued)
			break
		}
		runtime.Gosched()
	}

	// Reset the report timer to exclude setup overhead (tx reading, sender recovery,
	// pool ingestion, server startup). The benchmark measures mining throughput only.
	report.StartedAt = time.Now().UTC()

	// Optional CPU profiling (set BENCH_CPUPROF=/path/to/file to enable)
	if cpuProfPath := os.Getenv("BENCH_CPUPROF"); cpuProfPath != "" {
		f, err := os.Create(cpuProfPath)
		if err == nil {
			pprof.StartCPUProfile(f)
			defer func() {
				pprof.StopCPUProfile()
				f.Close()
				log.Info("CPU profile written", "path", cpuProfPath)
			}()
		}
	}

	// Start mining AFTER all txs are in the pool so block 1 has transactions.
	if err := backend.StartMining(); err != nil {
		return fail(WrapError(ErrCodeMiningStalled, "start mining", err))
	}
	log.Info("Mining started")

	// Wait for first block to ensure mining is working
	log.Info("Waiting for first block production...")
	getBlockNum := func() uint64 {
		return backend.BlockChain().CurrentBlock().Number.Uint64()
	}
	if err := waitForBlock(ctx, getBlockNum, startBlock, miningStartTimeout); err != nil {
		return fail(WrapError(ErrCodeMiningStalled, "waiting for first block", err))
	}
	log.Info("First block produced, mining is active")

	// Log txpool stats after ingestion (informational only)
	pending, queued := backend.TxPool().Stats()
	log.Info("TxPool stats after ingestion", "pending", pending, "queued", queued)

	// Note: We don't fail on queued-only state here because:
	// 1. Transactions may still be promoting from queued to pending
	// 2. Mining may already be processing transactions
	// 3. The completion check will catch actual failures
	if pending == 0 && queued == 0 && result.TotalTxs > 0 {
		log.Warn("TxPool is empty after ingestion - transactions may have been immediately mined or rejected")
	}

	// Scan blocks mined during ingestion to catch transactions that were
	// mined before they were marked as seen
	currentBlock := backend.BlockChain().CurrentBlock().Number.Uint64()
	if currentBlock > startBlock {
		log.Info("Scanning blocks mined during ingestion",
			"from", startBlock+1,
			"to", currentBlock)
		tracker.ScanBlockRange(startBlock+1, currentBlock)
		log.Info("After historical scan", "mined", tracker.MinedCount())
	}

	// Wait for all transactions to be mined
	log.Info("Waiting for all transactions to be mined",
		"target", result.TotalTxs,
		"currentlyMined", tracker.MinedCount())

	waitErr := waitForCompletion(ctx, tracker, result.TotalTxs)

	// Calculate final stats (even on timeout, capture what we got)
	endBlock := backend.BlockChain().CurrentBlock().Number.Uint64()
	report.TotalTxsMined = tracker.MinedCount()
	if endBlock >= startBlock {
		report.BlocksMined = endBlock - startBlock
	}

	if waitErr != nil {
		// Capture partial results before failing
		log.Warn("Mining did not complete",
			"mined", report.TotalTxsMined,
			"target", result.TotalTxs,
			"blocks", report.BlocksMined)
		return fail(WrapError(ErrCodeTimeout, "waiting for mining completion", waitErr))
	}

	// Finalize report
	report.SetSuccess(totalGas)
	if err := report.Write(opts.OutPath); err != nil {
		return fmt.Errorf("write report: %w", err)
	}

	// Print summary
	log.Info("Benchmark complete",
		"duration", report.FinishedAt.Sub(report.StartedAt),
		"txsMined", report.TotalTxsMined,
		"blocksMined", report.BlocksMined,
		"txPerSec", fmt.Sprintf("%.2f", report.TxPerSec),
		"gasPerSec", fmt.Sprintf("%.2f", report.GasPerSec))

	return nil
}

// waitForBlock waits for a new block to be produced after startBlock.
func waitForBlock(ctx context.Context, getBlockNum func() uint64, startBlock uint64, timeout time.Duration) error {
	deadline := time.After(timeout)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("timeout after %v waiting for block > %d", timeout, startBlock)
		case <-ticker.C:
			current := getBlockNum()
			if current > startBlock {
				return nil
			}
		}
	}
}

// parallelRecoverSenders recovers the sender address for each transaction
// in parallel across all available CPUs. This pre-warms the per-transaction
// sender cache (tx.from) so subsequent types.Sender() calls are instant.
func parallelRecoverSenders(signer types.Signer, txs []*types.Transaction) {
	numWorkers := runtime.NumCPU()
	if numWorkers > len(txs) {
		numWorkers = len(txs)
	}

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	// Interleaved distribution: worker i handles txs[i], txs[i+n], txs[i+2n], ...
	// This ensures early transactions are recovered first (they'll be needed first).
	for i := 0; i < numWorkers; i++ {
		go func(start int) {
			defer wg.Done()
			for j := start; j < len(txs); j += numWorkers {
				types.Sender(signer, txs[j])
			}
		}(i)
	}
	wg.Wait()
}

// waitForCompletion waits until all transactions are mined.
func waitForCompletion(ctx context.Context, tracker *MinedTracker, target uint64) error {
	ticker := time.NewTicker(completionPollInterval)
	defer ticker.Stop()

	lastLog := time.Now()
	for {
		mined := tracker.MinedCount()
		if mined >= target {
			return nil
		}

		// Log progress periodically
		if time.Since(lastLog) > 5*time.Second {
			log.Info("Mining progress", "mined", mined, "target", target,
				"remaining", target-mined)
			lastLog = time.Now()
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Continue polling
		}
	}
}
