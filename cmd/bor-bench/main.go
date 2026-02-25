// bor-bench is a benchmark tool for measuring Bor transaction processing throughput.
//
// Usage:
//
//	bor-bench --config ./config.toml --genesis ./genesis.json --txs ./dataset.txt --out ./result.json
//
// The tool:
//  1. Starts a local Bor node in benchmark mode (no peers, no Heimdall)
//  2. Ingests transactions from the input file
//  3. Mines until all transactions are included in blocks
//  4. Outputs a JSON report with performance metrics
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ethereum/go-ethereum/internal/bench"
	"github.com/ethereum/go-ethereum/log"
)

func main() {
	var opts bench.Options

	flag.StringVar(&opts.ConfigPath, "config", "", "Path to Bor TOML configuration file (required)")
	flag.StringVar(&opts.GenesisPath, "genesis", "", "Path to genesis JSON file (required)")
	flag.StringVar(&opts.TxsPath, "txs", "", "Path to transaction file, one hex tx per line (required)")
	flag.StringVar(&opts.OutPath, "out", "", "Path to output report JSON file (required)")
	flag.StringVar(&opts.DataDir, "datadir", "", "Override datadir for this run (optional, uses temp dir if not set)")
	flag.StringVar(&opts.RunID, "run-id", "", "Caller-provided run identifier (optional)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "bor-bench - Bor transaction processing benchmark tool\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  bor-bench --config <path> --genesis <path> --txs <path> --out <path>\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExample:\n")
		fmt.Fprintf(os.Stderr, "  bor-bench --config ./benchmark.toml --genesis ./genesis.json --txs ./txs.txt --out ./report.json\n")
	}

	flag.Parse()

	// Validate required flags
	if err := opts.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n\n", err)
		flag.Usage()
		os.Exit(1)
	}

	// Set up logging
	log.SetDefault(log.NewLogger(log.NewTerminalHandlerWithLevel(os.Stderr, log.LevelInfo, true)))

	// Set up context with signal handling
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Run benchmark
	if err := bench.Run(ctx, opts); err != nil {
		fmt.Fprintf(os.Stderr, "Benchmark failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stdout, "Benchmark complete. Report written to: %s\n", opts.OutPath)
}
