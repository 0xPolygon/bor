---
paths:
  - "core/txpool/**/*.go"
---
# Transaction Pool Security — core/txpool/

The transaction pool receives transactions from both RPC and peers. It is the primary ingestion point for untrusted data and a key DoS target. Bugs here can stall block production, exhaust memory, or enable transaction censorship.

## Threat Model

| Threat | Attack Vector | Impact |
|--------|---------------|--------|
| Memory exhaustion | Flood pool with maximum-size transactions | Node OOM |
| CPU exhaustion | Submit transactions requiring expensive validation | Node stall |
| Pool eviction gaming | Manipulate gas prices to evict legitimate transactions | Transaction censorship |
| Nonce gap attack | Submit transactions with gaps to occupy pool slots | Legitimate txs rejected |
| Blob pool abuse | Submit many blob transactions (EIP-4844) consuming disk | Storage exhaustion |
| Replacement gaming | Repeated replace-by-fee with minimal bump | CPU waste on re-validation |

## Critical Rules

1. **Enforce pool size limits** — total transaction count AND total byte size must be bounded. Both legacy and blob subpools need independent limits.

2. **Validate before storing** — signature recovery, nonce checks, balance checks, and gas price validation must all happen before a transaction enters the pool.

3. **Rate-limit per sender** — limit the number of pending + queued transactions per account to prevent a single account from consuming the entire pool.

4. **Rate-limit per peer** — track transaction submission rate per peer and throttle or disconnect abusive peers.

5. **Replacement fee bump must be enforced** — replacement transactions must pay a minimum percentage increase in gas price to prevent replacement spam.

## Patterns to Flag

| Pattern | Severity | Why |
|---------|----------|-----|
| Transaction added to pool before signature verification | CRITICAL | Fake txs fill pool |
| Pool size check after insertion (not before) | HIGH | Momentary OOM possible |
| Missing per-account transaction limit | HIGH | Single account fills pool |
| Blob data stored in memory instead of disk/temp | HIGH | Blob txs are 128KB+ each |
| Missing replacement fee bump check | MEDIUM | Replacement spam |
| Transaction validation that allocates proportional to tx size without limit | HIGH | Memory bomb via large calldata |
| Pool metrics not updated on eviction | LOW | Incorrect monitoring |
| Panic in transaction validation path | HIGH | Peer-triggerable crash |

## Review Checklist

- [ ] Are pool size limits enforced before insertion?
- [ ] Is transaction validation complete before pool admission (sig, nonce, balance, gas)?
- [ ] Are per-account and per-peer rate limits in place?
- [ ] Does the change affect eviction order? If yes, verify it can't be gamed to censor transactions.
- [ ] Are blob transactions handled with disk-backed storage?
- [ ] Is the replacement fee bump threshold enforced?
