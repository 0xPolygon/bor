---
paths:
  - "rpc/**/*.go"
  - "node/**/*.go"
  - "internal/ethapi/**/*.go"
  - "graphql/**/*.go"
  - "ethclient/**/*.go"
---
# RPC & API Security — rpc/, node/, internal/ethapi/, graphql/

The RPC layer exposes the node to external callers. It is a direct attack surface for DoS, information leaks, and unauthorized access. Public nodes are actively targeted.

## Threat Model

| Threat | Attack Vector | Impact |
|--------|---------------|--------|
| DoS via expensive calls | `eth_call` with max gas, `debug_traceTransaction` on large blocks | CPU/memory exhaustion |
| Information leak | Verbose errors, debug APIs exposed publicly | Internal state revealed |
| Auth bypass | Missing JWT check on Engine API, CORS misconfiguration | Unauthorized node control |
| Resource exhaustion | Unbounded subscriptions, large batch requests | Connection/memory exhaustion |
| SSRF | User-supplied URLs in RPC params forwarded by node | Internal network scanning |

## Critical Rules

1. **Validate all RPC parameters** — check types, ranges, and sizes before processing. An `eth_getLogs` with a 10M-block range must be rejected.

2. **Enforce resource limits** — gas caps on `eth_call`/`eth_estimateGas`, block range limits on log queries, batch size limits on JSON-RPC batches. RPC methods that return batch data (e.g., `eth_getBlockReceipts`, `eth_getLogs`, methods returning arrays of transactions or blocks) must enforce hardcoded maximum return sizes or configurable limits via node config. Without explicit caps, a single call can force the node to serialize and transmit gigabytes of data.

3. **Separate public and private APIs** — `debug_*`, `admin_*`, `personal_*` must never be exposed on public HTTP/WS. The `bor_*` namespace (`bor_getSnapshot`, `bor_getSigners`, `bor_getAuthor`) exposes validator-sensitive data and should be reviewed for public exposure. Verify API namespace configuration.

4. **JWT authentication on Engine API** — the consensus client (Heimdall) connection MUST require valid JWT. Verify in `node/jwt_handler.go`.

5. **Safe error messages** — RPC errors must not include stack traces, internal paths, or sensitive state. Wrap internal errors before returning.

## Patterns to Flag

| Pattern | Severity | Trigger | Why |
|---------|----------|---------|-----|
| Debug/Admin API callable without auth | CRITICAL | RPC User | Full node control for any network-reachable attacker |
| `eth_call` without gas cap enforcement | HIGH→CRITICAL | RPC User | CPU DoS via infinite-loop contract — any public RPC user |
| Log query without block range limit | HIGH→CRITICAL | RPC User | Memory DoS fetching millions of logs — any public RPC user |
| Raw internal error returned to RPC caller | MEDIUM | RPC User | Information leak (paths, state) |
| WebSocket subscription without connection limit | HIGH→CRITICAL | RPC User | Connection exhaustion from any user |
| Batch request without item count limit | HIGH→CRITICAL | RPC User | Memory/CPU DoS from any user |
| RPC method returning unbounded tx/block arrays without max size cap | HIGH→CRITICAL | RPC User | Single call serializes GBs of data — any public user can trigger |
| CORS set to `*` in production config | MEDIUM | RPC User | Any website can call the node |
| JWT secret loaded from command-line arg (visible in /proc) | MEDIUM | Self | Secret leak (local misconfiguration) |

## Review Checklist

- [ ] Are all new RPC methods behind appropriate API namespaces?
- [ ] Are parameter bounds validated (block ranges, gas limits, array sizes)?
- [ ] Are expensive operations metered or rate-limited?
- [ ] Do error responses avoid leaking internal details?
- [ ] Is authentication required where expected (Engine API, admin endpoints)?
- [ ] Are WebSocket subscriptions bounded per connection?
- [ ] Does the change affect CORS or origin checking?
- [ ] Does the RPC method return arrays of txs, blocks, or receipts? If yes, verify a hardcoded max or configurable limit caps the response size.
