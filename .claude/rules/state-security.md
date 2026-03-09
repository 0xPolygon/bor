---
paths:
  - "core/state/**/*.go"
  - "core/blockstm/**/*.go"
  - "core/state_processor.go"
  - "core/state_transition.go"
  - "core/block_validator.go"
  - "core/stateless/**/*.go"
  - "trie/**/*.go"
  - "triedb/**/*.go"
---
# State & Parallel Execution Security — core/state/, core/blockstm/, trie/

State management handles account balances, contract storage, and the Merkle Patricia Trie. BlockSTM adds parallel execution. Bugs here can corrupt balances, break state proofs, or cause consensus divergence between nodes.

## Threat Model

| Threat | Attack Vector | Impact |
|--------|---------------|--------|
| Balance corruption | Race condition in parallel state access | Fund loss or creation |
| State root mismatch | Different execution order produces different root | Consensus split |
| Trie corruption | Incorrect node encoding or pruning | Unrecoverable state loss |
| Witness forgery | Invalid stateless witness accepted | Fake state transition verified |
| Snapshot inconsistency | Stale snapshot used for block building | Invalid block produced |
| Journal corruption | Incomplete revert on failed transaction | State leak between transactions |

## BlockSTM-Specific Rules

BlockSTM executes transactions in parallel and detects conflicts. This is one of the most subtle security surfaces in Bor.

1. **Multi-version data store must be linearizable** — every read must return the value from the latest committed write that precedes it in block order. A stale read means wrong execution.

2. **Conflict detection must be complete** — if transaction B reads a location written by transaction A (where A < B in block order), B must be re-executed after A commits. Missing a dependency produces wrong state.

3. **Abort/re-execution must fully reset state** — when a transaction is re-executed, all its previous writes must be invalidated. Partial cleanup causes phantom state.

4. **Sequential fallback must produce identical results** — serial and parallel execution are not mutually exclusive; BlockSTM can run both paths during the same block (parallel first, serial re-execution on conflict). The state root must be byte-identical regardless of which path produced the final result. Any divergence between parallel and sequential output is a consensus-splitting bug.

## Patterns to Flag

| Pattern | Severity | Why |
|---------|----------|-----|
| `StateDB` accessed from multiple goroutines without BlockSTM coordination | CRITICAL | Race condition → balance corruption |
| `stateObject` copied by value (not pointer) | CRITICAL | Mutations lost, state divergence |
| Trie node deleted during iteration | HIGH | Iterator corruption, missing state |
| Missing `Finalise()` call between transactions in a block | CRITICAL | Dirty state leaks between txs |
| Journal revert that doesn't restore all modified fields | CRITICAL | Partial rollback → state corruption |
| State snapshot used after new blocks committed | HIGH | Stale reads |
| `common.Hash` used as map key with struct containing pointer | MEDIUM | Non-deterministic map iteration order |
| Parallel test passing but sequential equivalent failing (or vice versa) | CRITICAL | BlockSTM correctness bug |
| Trie commit without proper reference counting | HIGH | Premature node garbage collection |

## State Transition Security

- **Gas accounting must match between parallel and sequential execution** — verify by running both paths on the same block
- **Receipt generation must be deterministic** — logs, gas used, and status must be identical regardless of execution order
- **State prefetcher must not affect execution outcome** — prefetch is optimization only; reads must not come from prefetch cache without validation

## Review Checklist

- [ ] Does the change affect StateDB read/write paths? If yes, verify thread safety under BlockSTM.
- [ ] Does the change modify trie encoding/hashing? If yes, run full consensus test suite — a single bit difference in state root is a chain-splitting bug.
- [ ] Does the change affect journal/snapshot logic? If yes, verify complete rollback on failed transactions.
- [ ] Does the change produce identical state roots in parallel vs sequential execution?
- [ ] Are all state modifications accounted for in the journal for revert support?
- [ ] Does the change affect state pruning? If yes, verify no live state is pruned.
