package cli

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state/pruner"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/internal/cli/flagset"
	"github.com/ethereum/go-ethereum/internal/cli/server"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
)

const replayBatchSize = 2500

// RebuildStateCommand re-executes all blocks to rebuild state from scratch.
type RebuildStateCommand struct {
	*Meta

	configFile      string
	bloomfilterSize uint64
}

// MarkDown implements cli.MarkDown interface
func (c *RebuildStateCommand) MarkDown() string {
	items := []string{
		"# Rebuild state",
		"The `bor rebuild-state` command prunes all state data and re-executes every block from genesis " +
			"to rebuild the state trie. This serves as both an integrity check (verifying that the rebuilt " +
			"state root matches the original) and a benchmarking tool for measuring execution performance " +
			"in an isolated environment.",
		"All configuration from config.toml is honored, including cache sizes, parallel EVM, and Heimdall settings.",
		"**Warning**: This command modifies the database in-place. Back up your datadir before running.",
		c.Flags().MarkDown(),
	}

	return strings.Join(items, "\n\n")
}

// Help implements the cli.Command interface
func (c *RebuildStateCommand) Help() string {
	return `Usage: bor rebuild-state --config <config.toml>

  This command prunes state and re-executes all blocks to rebuild it from scratch.
  It verifies that the rebuilt state root matches the original chain tip.` + c.Flags().Help()
}

// Synopsis implements the cli.Command interface
func (c *RebuildStateCommand) Synopsis() string {
	return "Rebuild state by re-executing all blocks"
}

// Flags returns the command flags
func (c *RebuildStateCommand) Flags() *flagset.Flagset {
	flags := c.NewFlagSet("rebuild-state")

	flags.StringFlag(&flagset.StringFlag{
		Name:    "config",
		Value:   &c.configFile,
		Usage:   "Path to the config file (same as used by bor server)",
		Default: "",
	})

	flags.Uint64Flag(&flagset.Uint64Flag{
		Name:    "bloomfilter.size",
		Value:   &c.bloomfilterSize,
		Usage:   "Size of the bloom filter in MB for state pruning",
		Default: 2048,
	})

	return flags
}

// chainTipInfo stores the chain tip metadata for integrity verification.
type chainTipInfo struct {
	blockNumber uint64
	blockHash   common.Hash
	stateRoot   common.Hash
}

// Run implements the cli.Command interface
func (c *RebuildStateCommand) Run(args []string) int {
	flags := c.Flags()

	if err := flags.Parse(args); err != nil {
		c.UI.Error(err.Error())
		return 1
	}

	if c.configFile == "" {
		c.UI.Error("--config is required")
		return 1
	}

	// Step 1: Record chain tip and validate.
	tip, err := c.recordChainTip()
	if err != nil {
		c.UI.Error(fmt.Sprintf("Failed to record chain tip: %v", err))
		return 1
	}

	log.Info("Chain tip recorded",
		"block", tip.blockNumber,
		"hash", tip.blockHash,
		"stateRoot", tip.stateRoot,
	)

	// Step 2: Prune state and reset head (same backend open to avoid path issues).
	if err := c.pruneAndReset(); err != nil {
		c.UI.Error(fmt.Sprintf("Failed to prune/reset state: %v", err))
		return 1
	}

	log.Info("State pruned and head reset to genesis")

	// Step 3: Replay blocks (fresh backend since chain was reset).
	rebuilt, err := c.replayBlocks(tip.blockNumber)
	if err != nil {
		c.UI.Error(fmt.Sprintf("Failed to replay blocks: %v", err))
		return 1
	}

	log.Info("Rebuild complete",
		"block", rebuilt.blockNumber,
		"hash", rebuilt.blockHash,
		"stateRoot", rebuilt.stateRoot,
	)

	if rebuilt.stateRoot == tip.stateRoot && rebuilt.blockHash == tip.blockHash && rebuilt.blockNumber == tip.blockNumber {
		c.UI.Info("PASS: rebuilt state matches original")
		return 0
	}

	c.UI.Error(fmt.Sprintf("FAIL: state root mismatch — expected %s, got %s", tip.stateRoot, rebuilt.stateRoot))

	return 1
}

// initBackend creates an eth.Ethereum backend using the same config pipeline as bor server.
func (c *RebuildStateCommand) initBackend() (*eth.Ethereum, *node.Node, error) {
	config, err := server.ReadConfigFile(c.configFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config: %w", err)
	}

	if c.dataDir != "" {
		config.DataDir = c.dataDir
	}

	if err := config.LoadChain(); err != nil {
		return nil, nil, fmt.Errorf("failed to load chain: %w", err)
	}

	nodeCfg, err := config.BuildNode()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build node config: %w", err)
	}

	stack, err := node.New(nodeCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create node: %w", err)
	}

	accountManager := accounts.NewManager(&accounts.Config{})
	keydir := stack.KeyStoreDir()
	accountManager.AddBackend(keystore.NewKeyStore(keydir, keystore.StandardScryptN, keystore.StandardScryptP))

	ethCfg, err := config.BuildEth(stack, accountManager)
	if err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("failed to build eth config: %w", err)
	}

	backend, err := eth.New(stack, ethCfg)
	if err != nil {
		stack.Close()
		return nil, nil, fmt.Errorf("failed to create eth backend: %w", err)
	}

	return backend, stack, nil
}

// closeBackend cleanly shuts down the backend and node.
func closeBackend(backend *eth.Ethereum, stack *node.Node) {
	backend.BlockChain().Stop()
	stack.Close()
}

// recordChainTip opens the chain, validates it has blocks, reads the tip info, and closes it.
func (c *RebuildStateCommand) recordChainTip() (*chainTipInfo, error) {
	backend, stack, err := c.initBackend()
	if err != nil {
		return nil, err
	}

	chaindataPath := stack.ResolvePath(chaindataPath)
	log.Info("Resolved chaindata path", "path", chaindataPath)

	head := backend.BlockChain().CurrentBlock()

	if head.Number.Uint64() == 0 {
		closeBackend(backend, stack)
		return nil, fmt.Errorf("datadir has no blocks to rebuild (only genesis found) — verify your --config and --datadir point to a synced chain")
	}

	tip := &chainTipInfo{
		blockNumber: head.Number.Uint64(),
		blockHash:   head.Hash(),
		stateRoot:   head.Root,
	}

	closeBackend(backend, stack)

	return tip, nil
}

// pruneAndReset opens a single backend, prunes all state, resets head pointers, then closes.
// Using a single backend avoids path resolution mismatches between steps.
func (c *RebuildStateCommand) pruneAndReset() error {
	backend, stack, err := c.initBackend()
	if err != nil {
		return err
	}
	defer closeBackend(backend, stack)

	chaindb := backend.ChainDb()

	// Prune state.
	scheme := rawdb.ReadStateScheme(chaindb)

	switch scheme {
	case rawdb.HashScheme:
		if err := c.pruneHashState(chaindb, stack); err != nil {
			return err
		}
	case rawdb.PathScheme:
		if err := c.prunePathState(chaindb, stack); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown or empty state scheme: %q", scheme)
	}

	log.Info("State pruning complete")

	// Reset head pointers to genesis.
	genesisHash := rawdb.ReadCanonicalHash(chaindb, 0)
	if genesisHash == (common.Hash{}) {
		return fmt.Errorf("genesis block not found in database")
	}

	batch := chaindb.NewBatch()
	rawdb.WriteHeadBlockHash(batch, genesisHash)
	rawdb.WriteHeadHeaderHash(batch, genesisHash)
	rawdb.WriteHeadFastBlockHash(batch, genesisHash)
	rawdb.WriteFinalizedBlockHash(batch, common.Hash{})

	if err := batch.Write(); err != nil {
		return fmt.Errorf("failed to reset head pointers: %w", err)
	}

	rawdb.DeleteSnapshotRoot(chaindb)

	log.Info("Head pointers reset to genesis", "genesisHash", genesisHash)

	return nil
}

// pruneHashState deletes hash-based state trie nodes using the bloom-filter pruner.
func (c *RebuildStateCommand) pruneHashState(chaindb ethdb.Database, stack *node.Node) error {
	resolvedPath := stack.ResolvePath("")

	p, err := pruner.NewPruner(chaindb, pruner.Config{
		Datadir:   resolvedPath,
		BloomSize: c.bloomfilterSize,
	})
	if err != nil {
		return fmt.Errorf("failed to create pruner: %w", err)
	}

	if err := p.Prune(common.Hash{}); err != nil {
		return fmt.Errorf("pruning failed: %w", err)
	}

	return nil
}

// prunePathState deletes all path-based state data by range-deleting known
// key prefixes and resetting the state history freezers.
func (c *RebuildStateCommand) prunePathState(chaindb ethdb.Database, stack *node.Node) error {
	log.Info("Pruning path-based state data")

	// 1. Delete all path-based trie nodes (prefixes A, O).
	log.Info("Deleting account trie nodes")
	rawdb.DeleteHistoryByRange(chaindb, rawdb.TrieNodeAccountPrefix)

	log.Info("Deleting storage trie nodes")
	rawdb.DeleteHistoryByRange(chaindb, rawdb.TrieNodeStoragePrefix)

	// 2. Delete all snapshot flat state (prefixes a, o).
	log.Info("Deleting snapshot account data")
	rawdb.DeleteHistoryByRange(chaindb, rawdb.SnapshotAccountPrefix)

	log.Info("Deleting snapshot storage data")
	rawdb.DeleteHistoryByRange(chaindb, rawdb.SnapshotStoragePrefix)

	// 3. Delete state ID mappings (prefix L).
	log.Info("Deleting state ID mappings")
	rawdb.DeleteHistoryByRange(chaindb, rawdb.StateIDPrefix)

	// 4. Reset persistent state ID to 0.
	rawdb.WritePersistentStateID(chaindb, 0)

	// 5. Delete snapshot and trie journal metadata.
	batch := chaindb.NewBatch()
	rawdb.DeleteSnapshotRoot(batch)
	rawdb.DeleteSnapshotGenerator(batch)
	rawdb.DeleteSnapshotJournal(batch)
	rawdb.DeleteSnapshotRecoveryNumber(batch)
	rawdb.DeleteTrieJournal(batch)

	if err := batch.Write(); err != nil {
		return fmt.Errorf("failed to delete snapshot metadata: %w", err)
	}

	// 6. Delete on-disk trie journal file.
	triedbDir := stack.ResolvePath("triedb")
	ancientDir := stack.ResolveAncient(chaindataPath, "")

	for _, name := range []string{"merkle.journal", "merkle.journal.tmp"} {
		p := filepath.Join(triedbDir, name)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove journal file %s: %w", p, err)
		} else if err == nil {
			log.Info("Removed trie journal file", "path", p)
		}
	}

	// 7. Delete state history index data.
	batch.Reset()
	rawdb.DeleteStateHistoryIndexMetadata(batch)
	rawdb.DeleteTrienodeHistoryIndexMetadata(batch)

	if err := batch.Write(); err != nil {
		return fmt.Errorf("failed to delete state history index metadata: %w", err)
	}

	rawdb.DeleteStateHistoryIndexes(chaindb)
	rawdb.DeleteTrienodeHistoryIndexes(chaindb)
	rawdb.DeleteStateHistoryIndex(chaindb)

	// 8. Reset state and trienode freezers.
	stateFreezer, err := rawdb.NewStateFreezer(ancientDir, false, false)
	if err != nil {
		log.Warn("No state freezer to reset", "err", err)
	} else {
		if err := stateFreezer.Reset(); err != nil {
			stateFreezer.Close()
			return fmt.Errorf("failed to reset state freezer: %w", err)
		}
		stateFreezer.Close()
		log.Info("Reset state freezer")
	}

	trienodeFreezer, err := rawdb.NewTrienodeFreezer(ancientDir, false, false)
	if err != nil {
		log.Warn("No trienode freezer to reset", "err", err)
	} else {
		if err := trienodeFreezer.Reset(); err != nil {
			trienodeFreezer.Close()
			return fmt.Errorf("failed to reset trienode freezer: %w", err)
		}
		trienodeFreezer.Close()
		log.Info("Reset trienode freezer")
	}

	// 9. Compact database to reclaim space.
	log.Info("Compacting database")

	if err := chaindb.Compact(nil, nil); err != nil {
		log.Warn("Database compaction failed", "err", err)
	}

	return nil
}

// replayBlocks re-executes all blocks from 1 to targetBlock using InsertChain.
// Returns the chain tip info after replay for integrity verification.
func (c *RebuildStateCommand) replayBlocks(targetBlock uint64) (*chainTipInfo, error) {
	backend, stack, err := c.initBackend()
	if err != nil {
		return nil, err
	}
	defer closeBackend(backend, stack)

	bc := backend.BlockChain()

	// Setup interrupt handling using the two-channel pattern from cmd/utils/cmd.go
	interrupt := make(chan os.Signal, 1)
	stop := make(chan struct{})

	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)

	defer signal.Stop(interrupt)
	defer close(interrupt)

	go func() {
		if _, ok := <-interrupt; ok {
			log.Info("Replay interrupted, stopping at next batch")
		}

		close(stop)
	}()

	checkInterrupt := func() bool {
		select {
		case <-stop:
			return true
		default:
			return false
		}
	}

	log.Info("Starting block replay", "from", 1, "to", targetBlock)

	start := time.Now()
	lastLog := time.Now()

	var totalTxs uint64

	blocks := make(types.Blocks, 0, replayBatchSize)

	for batchStart := uint64(1); batchStart <= targetBlock; batchStart += replayBatchSize {
		if checkInterrupt() {
			log.Warn("Replay interrupted by user", "lastBlock", batchStart-1)
			return nil, fmt.Errorf("interrupted at block %d", batchStart-1)
		}

		batchEnd := batchStart + replayBatchSize - 1
		if batchEnd > targetBlock {
			batchEnd = targetBlock
		}

		blocks = blocks[:0]

		var batchTxs uint64

		for i := batchStart; i <= batchEnd; i++ {
			block := bc.GetBlockByNumber(i)
			if block == nil {
				return nil, fmt.Errorf("missing block %d", i)
			}

			blocks = append(blocks, block)
			batchTxs += uint64(len(block.Transactions()))
		}

		if _, err := bc.InsertChain(blocks, false); err != nil {
			return nil, fmt.Errorf("failed to insert blocks %d-%d: %w", batchStart, batchEnd, err)
		}

		totalTxs += batchTxs

		now := time.Now()
		if now.Sub(lastLog) >= 10*time.Second || batchEnd == targetBlock {
			elapsed := now.Sub(start)
			blocksPerSec := float64(batchEnd) / elapsed.Seconds()
			txPerSec := float64(totalTxs) / elapsed.Seconds()
			pct := float64(batchEnd) / float64(targetBlock) * 100

			log.Info("Replay progress",
				"block", batchEnd,
				"of", targetBlock,
				"pct", fmt.Sprintf("%.1f%%", pct),
				"blocks/s", fmt.Sprintf("%.1f", blocksPerSec),
				"tx/s", fmt.Sprintf("%.1f", txPerSec),
				"elapsed", elapsed.Round(time.Second),
			)

			lastLog = now
		}
	}

	elapsed := time.Since(start)

	log.Info("Block replay complete",
		"blocks", targetBlock,
		"transactions", totalTxs,
		"elapsed", elapsed.Round(time.Second),
		"blocks/s", fmt.Sprintf("%.1f", float64(targetBlock)/elapsed.Seconds()),
		"tx/s", fmt.Sprintf("%.1f", float64(totalTxs)/elapsed.Seconds()),
	)

	head := bc.CurrentBlock()

	return &chainTipInfo{
		blockNumber: head.Number.Uint64(),
		blockHash:   head.Hash(),
		stateRoot:   head.Root,
	}, nil
}
