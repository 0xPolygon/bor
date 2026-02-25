package bench

import (
	"context"
	"fmt"
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
	completionPollInterval = 200 * time.Millisecond
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

	// Start mining
	if err := backend.StartMining(); err != nil {
		return fail(WrapError(ErrCodeMiningStalled, "start mining", err))
	}
	log.Info("Mining started")

	// Record starting block
	startBlock := backend.BlockChain().CurrentBlock().Number.Uint64()

	// Start mined tracker before ingestion
	tracker := NewMinedTracker(backend)
	stopTracker := make(chan struct{})
	go tracker.Watch(stopTracker)
	defer close(stopTracker)

	// Wait for first block to ensure mining is working
	log.Info("Waiting for first block production...")
	getBlockNum := func() uint64 {
		return backend.BlockChain().CurrentBlock().Number.Uint64()
	}
	if err := waitForBlock(ctx, getBlockNum, startBlock, miningStartTimeout); err != nil {
		return fail(WrapError(ErrCodeMiningStalled, "waiting for first block", err))
	}
	log.Info("First block produced, mining is active")

	// Ingest transactions
	log.Info("Starting transaction ingestion", "txsPath", opts.TxsPath)
	var totalGas uint64
	result, err := ReadTransactions(opts.TxsPath, func(line int, tx *types.Transaction) error {
		// Check for context cancellation periodically
		if line%1000 == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}

		// Mark as seen for tracking
		tracker.MarkSeen(tx.Hash())

		// Submit to txpool
		errs := backend.TxPool().Add([]*types.Transaction{tx}, false)
		if len(errs) != 1 {
			return fmt.Errorf("unexpected txpool add result length: %d", len(errs))
		}
		if errs[0] != nil {
			log.Error("TxPool rejected transaction",
				"line", line,
				"hash", tx.Hash(),
				"nonce", tx.Nonce(),
				"err", errs[0])
			return WrapError(ErrCodeTxPoolReject,
				fmt.Sprintf("line %d: txpool rejected tx %s", line, tx.Hash().Hex()), errs[0])
		}

		// Log progress
		if line <= 3 || line%1000 == 0 {
			log.Info("Ingested transaction", "line", line, "hash", tx.Hash().Hex()[:16])
		}

		return nil
	})
	if err != nil {
		return fail(err)
	}

	totalGas = result.TotalGas
	report.TotalTxsInput = result.TotalTxs
	log.Info("Transaction ingestion complete",
		"totalTxs", result.TotalTxs,
		"totalGas", result.TotalGas)

	// Check txpool health after ingestion
	pending, queued := backend.TxPool().Stats()
	if err := ValidateTxPoolHealth(TxPoolStats{Pending: pending, Queued: queued}, result.TotalTxs); err != nil {
		return fail(err)
	}

	// Wait for all transactions to be mined
	log.Info("Waiting for all transactions to be mined",
		"target", result.TotalTxs,
		"currentlyMined", tracker.MinedCount())

	if err := waitForCompletion(ctx, tracker, result.TotalTxs); err != nil {
		return fail(WrapError(ErrCodeTimeout, "waiting for mining completion", err))
	}

	// Calculate final stats
	endBlock := backend.BlockChain().CurrentBlock().Number.Uint64()
	report.TotalTxsMined = tracker.MinedCount()
	if endBlock >= startBlock {
		report.BlocksMined = endBlock - startBlock
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
