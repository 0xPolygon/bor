---
paths:
  - "core/blockchain.go"
  - "core/blockchain_insert.go"
  - "core/blockchain_reader.go"
  - "core/bor_blockchain_reader.go"
  - "core/forkchoice.go"
  - "core/headerchain.go"
  - "core/genesis.go"
  - "core/genesis_alloc.go"
  - "core/chain_makers.go"
  - "core/evm.go"
  - "core/bor_events.go"
  - "core/bor_fee_log.go"
  - "core/types/**/*.go"
  - "core/types/*.go"
  - "core/rawdb/**/*.go"
  - "core/rawdb/*.go"
  - "rlp/**/*.go"
  - "rlp/*.go"
  - "params/**/*.go"
  - "params/*.go"
---
# Blockchain & Chain Management Security — core/, core/types/, core/rawdb/, rlp/, params/

The blockchain layer manages chain insertion, reorgs, fork choice, genesis initialization, block/transaction types, RLP encoding, database persistence, and fork activation parameters. Bugs here cause chain splits, data loss, invalid reorgs, or permanent chain corruption.

## Threat Model

| Threat | Attack Vector | Impact |
|--------|---------------|--------|
| Invalid reorg | Manipulated total difficulty or fork choice logic | Double-spend, transaction reversal |
| Chain corruption | Interrupted write during insertChain or SetHead | Unrecoverable node, requires resync |
| Genesis mismatch | Different genesis config on different nodes | Permanent chain split from block 0 |
| Fork activation bug | Wrong block number or timestamp for hard fork | Premature or missed activation → chain split |
| RLP decoding panic | Malformed block/tx data from peer or DB | Node crash on specific block |
| Freezer corruption | Ancient data written out of order or truncated | Missing historical blocks, sync failure |
| Type confusion | Wrong transaction type decoded from RLP | Invalid signature verification, tx replay |

## Critical Invariants

1. **Fork choice must be deterministic** — `ReorgNeeded()` must return the same result on all nodes given the same inputs. Any tie-breaking that depends on arrival order or local state is a consensus bug.

2. **Chain insertion must be atomic** — if `insertChain` fails partway through, no partial state may be committed. The chain must remain at the pre-insertion head.

3. **SetHead must not lose finalized data** — `SetHead`/`SetHeadWithTimestamp` must respect finalized block markers. Rolling back past finalized blocks requires explicit override.

4. **Genesis state root must match genesis block** — the alloc accounts, their balances, code, and storage must hash to exactly the genesis state root. A single wei difference means nodes disagree from block 0.

5. **RLP encoding must be round-trip stable and consistent across transaction types** — `Encode(Decode(data)) == data` for all valid inputs. Any asymmetry causes consensus splits when blocks are relayed. Additionally, encoding must be consistent across all transaction types (legacy, EIP-2930, EIP-1559, EIP-4844 blob). Inconsistent encoding between tx types causes peers running different clients (e.g., Erigon) to reject blocks or disconnect — a real production issue that has caused network-level incidents. When adding or modifying tx type encoding, verify that both Bor and Erigon produce identical wire format.

6. **Fork activation parameters must be immutable after genesis** — changing `params.ChainConfig` fork blocks/timestamps after chain start creates a stealth hard fork.

## Patterns to Flag

| Pattern | Severity | Why |
|---------|----------|-----|
| `insertChain` without proper rollback on error | CRITICAL | Partial state committed → corruption |
| `SetHead` past finalized block without explicit flag | CRITICAL | Safety violation, potential double-spend |
| `ReorgNeeded` result depending on local timestamp or random | CRITICAL | Non-deterministic fork choice → split |
| RLP `Decode` into `interface{}` without type assertion | HIGH | Silent type confusion |
| `rawdb.WriteHeader` / `WriteBody` without matching `WriteTd` | HIGH | Inconsistent chain data |
| Freezer write without fsync or ordering guarantee | HIGH | Data loss on crash |
| `ChainConfig` fork field compared with `==` instead of `.Cmp()` | HIGH | Nil-safe comparison needed for `*big.Int` |
| New transaction type without RLP encode/decode + hash tests | CRITICAL | Consensus split on tx inclusion |
| RLP encoding inconsistency between tx types (e.g., legacy vs typed envelope) | CRITICAL | Cross-client peer drops, network partition between Bor and Erigon nodes |
| `genesis.Alloc` modified without updating expected state root | CRITICAL | Genesis mismatch across nodes |
| Panic in `blockchain.go` reorg path | CRITICAL | Node crash during chain reorganization |
| `rawdb.ReadHeader` result used without nil check | HIGH | Nil dereference on missing data |

## Database Integrity Rules

- **Never delete data that may be needed for reorgs** — keep at least `TriesInMemory` (128) recent states
- **Freezer (ancient) data is append-only** — once frozen, data must not be modified
- **Batch writes must be atomic** — use `ethdb.Batch` and commit once, not individual puts
- **Key encoding must be deterministic** — block number keys use big-endian 8-byte encoding

## RLP Security

- **Decode with size limits** — use struct tags `rlp:"maxcount=X"` for slices
- **Test round-trip** — every new type that implements `rlp.Encoder`/`rlp.Decoder` needs `Encode(Decode(x)) == x` tests
- **Never decode untrusted RLP into unbounded types** — `[]interface{}` or `[][]byte` without limits

## Review Checklist

- [ ] Does the change affect chain insertion or reorg logic? If yes, verify atomic rollback and event emission.
- [ ] Does the change modify fork choice? If yes, verify determinism — same inputs must produce same output on all nodes.
- [ ] Does the change add or modify a fork activation? If yes, verify block/timestamp gating and backward compatibility.
- [ ] Does the change affect RLP encoding of blocks, transactions, or receipts? If yes, verify round-trip stability and add consensus test vectors.
- [ ] Does the change modify rawdb read/write patterns? If yes, verify batch atomicity and nil-safety on reads.
- [ ] Does the change affect genesis initialization? If yes, verify state root matches expected value for all networks (mainnet, amoy).
- [ ] Does the change touch freezer/ancient data? If yes, verify append-only semantics and crash recovery.
- [ ] Does the change add a new transaction type? If yes, verify RLP codec, signature scheme, hash computation, and signer support.
- [ ] Does the change affect RLP encoding of any tx type? If yes, verify encoding is consistent across all tx types and matches Erigon's encoding for the same type. Cross-client encoding mismatches cause peer disconnections at the network level.
