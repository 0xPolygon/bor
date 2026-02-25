// Package bench provides a benchmark tool for measuring Bor transaction
// processing throughput. It runs a single-node chain in deterministic test mode,
// ingests transactions from an input dataset, and produces a JSON report with
// performance metrics.
//
// The benchmark tool is designed for reproducible performance testing:
//   - Runs without Heimdall (uses devfakeauthor mode)
//   - No P2P networking (single-node isolation)
//   - Cold-start only (fresh datadir per run)
//   - Deterministic tx ingestion order
//
// Usage:
//
//	bor-bench --config ./config.toml --genesis ./genesis.json --txs ./dataset.txt --out ./result.json
package bench
