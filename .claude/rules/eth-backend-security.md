---
paths:
  - "eth/backend.go"
  - "eth/api*.go"
  - "eth/bor_*.go"
  - "eth/catalyst/**/*.go"
  - "eth/catalyst/*.go"
  - "eth/fetcher/**/*.go"
  - "eth/fetcher/*.go"
  - "eth/filters/**/*.go"
  - "eth/filters/*.go"
  - "eth/gasestimator/**/*.go"
  - "eth/gasestimator/*.go"
  - "eth/tracers/**/*.go"
  - "eth/tracers/*.go"
  - "eth/ethconfig/**/*.go"
  - "eth/ethconfig/*.go"
  - "ethdb/**/*.go"
  - "ethdb/*.go"
  - "ethstats/**/*.go"
  - "ethstats/*.go"
---
# Eth Backend & Infrastructure Security — eth/, ethdb/, ethstats/

The Ethereum backend wires together the blockchain, txpool, P2P, and RPC layers. The fetcher pulls blocks and transactions from peers. Filters serve log queries. The catalyst package implements the Engine API. The database layer persists all chain data. Bugs here enable DoS, data loss, information leaks, and consensus failures through the Engine API.

## Threat Model

| Threat | Attack Vector | Impact |
|--------|---------------|--------|
| Engine API abuse | Forged `ForkchoiceUpdated` or `NewPayload` via compromised beacon | Force invalid head, skip validation |
| Fetcher manipulation | Announce nonexistent blocks to waste resources | Memory exhaustion, stalled sync |
| Filter DoS | Unbounded log query spanning entire chain | Node OOM or CPU exhaustion |
| Gas estimation abuse | Repeated expensive `eth_estimateGas` with max gas | CPU exhaustion on RPC node |
| Tracer information leak | Custom tracer exposing internal state via RPC | Private mempool data leaked |
| Database corruption | Concurrent writes without proper locking | Unrecoverable state, requires resync |
| Ethstats credential leak | Stats server password in config or logs | Node identity exposed |

## Critical Rules

### Engine API (eth/catalyst/) — inherited from geth, low priority for Bor

Bor is a PoA chain using the Bor consensus engine, not Ethereum's beacon chain. The catalyst package exists because Bor is forked from go-ethereum but is **not used in Bor's production consensus flow**. Changes here are low priority unless Bor is being prepared for a merge-style transition.

1. **Validate payload status strictly** — `NewPayload` must fully validate the block before returning `VALID`.

2. **ForkchoiceUpdated must verify finalized hash** — the finalized block hash must exist in the local chain.

3. **Engine API auth is mandatory** — JWT authentication required if the Engine API is exposed.

### Fetcher (eth/fetcher/)

4. **Bound announcement queue per peer** — a peer announcing thousands of block hashes without delivering bodies is a DoS vector. Cap queued announcements.

5. **Validate fetched blocks before importing** — never pass unvalidated blocks from the fetcher directly to `blockchain.InsertChain`. Always verify header PoA/PoW first.

### Filters (eth/filters/)

6. **Bound log query range** — queries like `eth_getLogs` with `fromBlock: 0, toBlock: latest` must be rejected or paginated. Unbounded queries scan the entire chain.

7. **Rate-limit filter creation** — each `eth_newFilter` allocates resources. Without limits, a client can exhaust server memory.

### Database (ethdb/)

8. **Batch size limits** — database write batches must not grow unbounded. Flush at regular size intervals.

9. **Iterator cleanup** — every `ethdb.Iterator` must be `Release()`d. Missing release leaks file descriptors in LevelDB/Pebble.

## Patterns to Flag

| Pattern | Severity | Why |
|---------|----------|-----|
| Engine API endpoint without JWT validation | CRITICAL | Unauthenticated chain control |
| `NewPayload` returning `VALID` before full block validation | CRITICAL | Beacon finalizes invalid block |
| `ForkchoiceUpdated` accepting finalized hash not in local chain | CRITICAL | Safety violation |
| Filter query without block range limit | HIGH | Full chain scan → OOM |
| `eth_estimateGas` without timeout or gas cap | HIGH | CPU DoS via expensive estimation |
| Fetcher importing block without header validation | HIGH | Invalid block in chain |
| `ethdb.Iterator` not `Release()`d in all code paths (including error) | HIGH | File descriptor leak |
| Database `Put` outside of a `Batch` in hot path | MEDIUM | Poor write performance, potential corruption |
| Tracer returning raw state data through RPC | MEDIUM | Information leak |
| `ethstats` password logged or hardcoded | MEDIUM | Credential exposure |
| `eth_call` / `eth_estimateGas` without state copy | HIGH | Concurrent state access race |

## Gas Estimation Security

- **Always use a state copy** — `eth_estimateGas` and `eth_call` execute EVM on a state snapshot, never on the live processing state
- **Cap execution time** — use `context.WithTimeout` to bound estimation duration
- **Binary search bounds** — gas estimation binary search must have a hard upper bound (block gas limit)

## Tracing Security

- **Custom JS tracers run in a sandbox** — but can still consume unbounded memory. Enforce timeout.
- **Native tracers must not leak across requests** — tracer state must be reset between calls.
- **Debug namespace must be restricted** — `debug_traceTransaction` is expensive and exposes internal EVM state. Should not be public.

## Review Checklist

- [ ] Does the change affect Engine API? If yes, verify JWT auth and payload validation completeness.
- [ ] Does the change modify the fetcher? If yes, verify per-peer bounds and block validation before import.
- [ ] Does the change affect filter queries? If yes, verify block range limits and memory bounds.
- [ ] Does the change modify gas estimation? If yes, verify state isolation and timeout enforcement.
- [ ] Does the change add or modify a tracer? If yes, verify sandbox timeout and no state leaks.
- [ ] Does the change affect database read/write patterns? If yes, verify iterator cleanup, batch usage, and crash safety.
- [ ] Does the change expose a new API endpoint? If yes, verify authentication requirements and rate limiting.
- [ ] Are all database iterators released in defer blocks (not just happy path)?
