---
paths:
  - "p2p/**/*.go"
  - "eth/protocols/**/*.go"
  - "eth/handler*.go"
  - "eth/peer*.go"
  - "eth/sync*.go"
  - "eth/downloader/**/*.go"
---
# P2P & Network Security — p2p/, eth/protocols/, eth/downloader/

The P2P layer is the primary external attack surface. Every byte received from peers is untrusted. Bugs here enable eclipse attacks, node crashes, network partitioning, and chain stalls.

## Threat Model

| Threat | Attack Vector | Impact |
|--------|---------------|--------|
| Eclipse attack | Surround victim node with attacker-controlled peers | Censor transactions, feed false chain |
| DoS via message flooding | Send oversized or excessive messages | Node OOM or CPU exhaustion |
| Malformed message crash | Send RLP that triggers panic during decoding | Node crash, network partition |
| Peer table poisoning | Inject bogus node records into discovery | Isolate nodes from honest peers |
| Amplification attack | Small request triggers large response | Bandwidth exhaustion |
| Block withholding | Send headers but withhold bodies | Stall sync, waste resources |

## Critical Rules

1. **Bound every allocation from peer data** — every `make()`, `append()`, or buffer sized by peer input MUST have a maximum. No exceptions.

```go
// WRONG
txs := make([]*types.Transaction, msg.TxCount)

// RIGHT
if msg.TxCount > maxTransactionsPerMessage {
    return errTooManyTransactions
}
txs := make([]*types.Transaction, msg.TxCount)
```

2. **Validate RLP before processing** — decode into typed structs with size limits. Never decode into `interface{}` or raw `[]byte` without bounds.

3. **Rate-limit per peer** — track message rates per peer and disconnect on abuse. Never allow a single peer to consume unbounded resources.

4. **Timeout all peer interactions** — use `context.WithTimeout` for every request/response cycle. Hanging reads from a malicious peer must not block the node.

5. **Verify Proof-of-Work/Authority before processing blocks** — reject blocks that fail basic header validation before doing expensive state processing.

## Patterns to Flag

| Pattern | Severity | Trigger | Why |
|---------|----------|---------|-----|
| `make([]T, peerValue)` without upper bound check | CRITICAL | Peer | OOM from malicious peer — any node on the network can trigger |
| `rlp.Decode` into unbounded slice without `rlp:"maxcount"` | CRITICAL | Peer | Memory bomb from crafted message |
| Missing `context.WithTimeout` on peer request | HIGH→CRITICAL | Peer | Hung peer blocks goroutine forever — externally triggerable |
| Peer banned/disconnected without logging reason | MEDIUM | Self | Makes incident investigation difficult |
| `io.ReadAll` on peer connection | CRITICAL | Peer | Unbounded read → OOM from any peer |
| New protocol message type without rate limiting | HIGH→CRITICAL | Peer | Flood attack from any peer on the network |
| Discovery response used without signature verification | CRITICAL | Peer | Sybil attack on peer table |
| Hardcoded bootnode keys in non-config files | MEDIUM | Self | Difficult to rotate if compromised |

## Sync Security

- **Header validation before body fetch** — never request block bodies for headers that fail basic checks
- **Pivot point verification** — snap sync pivot must be from a sufficiently deep block, not tip
- **Peer scoring** — track delivery quality, ban peers that deliver invalid data
- **Concurrent fetch limits** — bound the number of in-flight requests per peer

## Review Checklist

- [ ] Are all allocations from peer messages bounded?
- [ ] Are all peer interactions wrapped with timeouts?
- [ ] Is new message handling rate-limited?
- [ ] Are malformed messages handled gracefully (error return, not panic)?
- [ ] Are peer disconnect reasons logged for debugging?
- [ ] Does the change affect peer scoring? If yes, verify it can't be gamed.
- [ ] Does the change add a new protocol message? If yes, verify bounds on all fields.
