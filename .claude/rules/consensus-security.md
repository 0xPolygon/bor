---
paths:
  - "consensus/**/*.go"
  - "miner/**/*.go"
---
# Consensus Security — consensus/bor/, miner/

The Bor consensus engine determines who produces blocks and validates the chain. Bugs here can cause chain splits, halted block production, or validator set manipulation. Every change is CRITICAL by default.

## Threat Model

| Threat | Attack Vector | Impact |
|--------|---------------|--------|
| Validator impersonation | Forged or replayed signatures in block headers | Unauthorized block production |
| Sprint manipulation | Incorrect sprint boundary calculation | Wrong producer selected, chain fork |
| Snapshot poisoning | Malicious validator set in snapshot | Attacker gains block production rights |
| Heimdall desync | Stale or fabricated span/checkpoint data | Network-level incident: Heimdall being in sync is a prerequisite for Bor to function. Desync causes wrong validator set, halted block production, missed checkpoints, and can trigger cascading failures across the entire network — not just individual nodes |
| Reorg attack | Manipulated difficulty or block timing | Chain reorganization, double-spend |
| State sync injection | Malicious state sync events from Heimdall | Corrupted L2 state |

## Critical Invariants — Verify on Every Change

1. **Signer recovery must use the canonical signing method** — `ecrecover` on the sealed header hash with the signature from `extraData`. Any deviation allows forged blocks.

2. **Sprint boundaries must be deterministic** — `IsSprintStart()` and sprint length must match across all nodes. Sprint length comes from `BorConfig.Sprint` map (fork-gated), never hardcoded. Off-by-one errors cause consensus splits.

3. **Validator set must come from Heimdall via verified spans** — never trust validator data from peers or local cache without verifying the span source. Span cache invalidation is security-critical; stale spans mean wrong producer.

4. **Difficulty calculation must be deterministic** — `inturn` vs `outturn` difficulty must produce identical values on all nodes for the same block number and signer.

5. **Block time enforcement must be strict, with PIP-66 awareness** — blocks with future timestamps or timestamps violating minimum period must be rejected. **PIP-66 (early block announcement)** complicates this: under Rio fork, blocks can be announced before `header.Time` is reached (the primary producer starts building early). The verification path (`verifyHeader`) uses relaxed timing checks post-Rio (`now >= parent.Time` instead of `now >= header.Time`). Be careful not to confuse PIP-66's intentional early-announcement logic with actual timestamp violations — Claude should check whether code is in the producer path (Prepare/Seal, where early timing is expected) or the verifier path (VerifyHeader, where strictness matters).

6. **Succession number determines producer priority** — succession 0 is the primary producer, higher numbers are backups with increasing delay. `GetSignerSuccessionNumber` must agree across all nodes for the same block and validator set. Errors in succession calculation cause block production conflicts.

7. **Milestone and checkpoint finality must be respected** — once a milestone is locked via Heimdall, the chain must not reorg past it. Ignoring milestone locks enables double-spend even without 51% attack.

8. **BorConfig changes are consensus-breaking** — `Sprint`, `Period`, `ProducerDelay`, `RioBlock`, and other `BorConfig` fields are fork-gated. Changing them without proper fork activation splits the chain.

## Patterns to Flag

| Pattern | Severity | Trigger | Why |
|---------|----------|---------|-----|
| `ecrecover` without verifying signer is in current validator set | CRITICAL | Peer/Validator | Anyone can produce a valid signature and broadcast blocks |
| Snapshot loaded from DB without integrity check | CRITICAL | Self | Corrupted DB → wrong validator set (local corruption only) |
| Heimdall data used without checking span boundaries | CRITICAL | Validator | Stale validator set → wrong producer. Malicious validator could exploit timing. |
| `time.Now()` in verification paths (`VerifyHeader`, `VerifySeal`) | HIGH | Peer | Non-determinism → consensus split. Peer-sent blocks trigger verification. Note: `time.Now()` in `Prepare` (producer-only) is acceptable. |
| Panic in `VerifyHeader`, `VerifySeal`, `Prepare`, `Finalize` | CRITICAL | Peer/Validator | Crash → chain halt. A crafted block from any peer or validator triggers `VerifyHeader` on all receiving nodes. |
| Mutex held across Heimdall RPC calls | HIGH | Self | Deadlock if Heimdall is slow/down (local operational issue) |
| Sprint length hardcoded instead of from chain config | HIGH | Self | Fork boundary bugs (code error, not externally triggered) |
| State sync events processed without merkle proof verification | CRITICAL | Validator | Arbitrary state injection — malicious validator crafts events affecting all nodes |

## Review Checklist for Consensus Changes

- [ ] Does the change affect block production order? If yes, verify all nodes compute the same order.
- [ ] Does the change modify header validation? If yes, ensure backward compatibility with existing chain.
- [ ] Does the change touch snapshot logic? If yes, verify snapshot persistence and recovery paths.
- [ ] Does the change affect Heimdall communication? If yes, verify timeout handling and fallback behavior.
- [ ] Does the change modify difficulty calculation? If yes, verify fork choice is unaffected.
- [ ] Is there a hard fork boundary involved? If yes, verify activation logic and chain config gating.
- [ ] Are all error paths handled without panics? Consensus code must never crash the node.
