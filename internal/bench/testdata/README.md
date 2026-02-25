# Benchmark Test Data

This directory contains configuration files for running the `bor-bench` benchmark tool.

## Files

- `benchmark.toml` - Bor configuration optimized for benchmarking
- `genesis.json` - Genesis file with pre-funded accounts (chain ID: 80002)

## Pre-funded Accounts

The genesis includes two funded accounts (1000 ETH each):

| Address | Balance |
|---------|---------|
| `0x85dA99c8a7C2C95964c8EfD687E95E632Fc533D6` | 1000 ETH |
| `0x6aB3d36C46ecFb9B9c0bD51CB1c3da5A2C81cea6` | 1000 ETH |

## Generating Transaction Dataset

The transaction dataset is not included due to size. Generate it using [polycli](https://github.com/0xPolygon/polygon-cli):

```bash
# Install polycli (if not already installed)
go install github.com/maticnetwork/polygon-cli/cmd/polycli@latest

# Generate 100k signed transactions for EOA transfers
polycli loadtest \
        --verbosity 700 \
        --chain-id 80002 \
        --output-raw-tx-only \
        --requests 1000 \
        --rate-limit 10000 \
        --concurrency 100 \
        --nonce 0 \
        --priority-gas-price 100000000000 \
        --gas-price 100000000000 > raw-txs.txt

# Generate transactions going toward a gas waster contract
polycli loadtest \
        --verbosity 700 \
        --chain-id 80002 \
        --output-raw-tx-only \
        --requests 100 \
        --rate-limit 10000 \
        --concurrency 100 \
        --gas-limit 1000000 \
        --mode contract-call \
        --calldata 0x \
        --contract-address 0xdeadbeef00000000000000000000000000000000 \
        --nonce 0 \
        --priority-gas-price 100000000000 \
        --gas-price 100000000000 > raw-txs-waste.txt
```

## Running the Benchmark

```bash
# Build the benchmark tool
go build -o bor-bench ./cmd/bor-bench

# Run the benchmark
./bor-bench \
    --config ./internal/bench/testdata/benchmark.toml \
    --genesis ./internal/bench/testdata/genesis.json \
    --txs ./raw-txs.txt \
    --out ./benchmark-report.json

# Test the case where we're calling the gas waste contract
./bor-bench \
    --config ./internal/bench/testdata/benchmark.toml \
    --genesis ./internal/bench/testdata/genesis.json \
    --txs ./raw-txs-waste.txt \
    --out ./benchmark-report-waste.json
```

## Expected Results

With the default configuration and 100k transactions:

- **Duration**: ~25-30 seconds
- **Throughput**: ~3,500-4,000 tx/sec
- **Gas throughput**: ~75-85 Mgas/sec
- **Blocks**: ~25-30 blocks

Results will vary based on hardware and configuration.

## Configuration Notes

Key settings in `benchmark.toml`:

```toml
[txpool]
    accountslots = 100000  # Must be >= number of transactions
    globalslots = 100000   # Must be >= number of transactions
    accountqueue = 100000
    globalqueue = 100000
```

If transactions are being dropped, increase these values.

The benchmark automatically sets:
- `bor.withoutheimdall = true`
- `bor.devfakeauthor = true`
- `p2p.maxpeers = 0`
- All RPC interfaces disabled


## Future Work

In the future, the genesis block should include some smart contracts
that are useful for load testing.
