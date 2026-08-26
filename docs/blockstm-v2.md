# BlockSTM V2: Parallel Transaction Execution

## Overview

BlockSTM V2 is the parallel transaction execution engine for Bor (Polygon
PoS). It speculatively executes transactions in a block concurrently,
validates each tx's reads against the multi-version store, and re-executes
any whose reads turned stale. On the 241-block witness benchmark V2/4w
delivers roughly 1.6x speedup over the serial path
(549 mgas/s vs. 342 mgas/s, AMD Ryzen 7 5800H, all-in-memory).

## Architecture

### Components

```
                    ┌─────────────┐
                    │ V2StateProc │  (core/parallel_state_processor.go)
                    │  .Process() │
                    └──────┬──────┘
                           │
                    ┌──────▼──────┐
                    │ExecuteV2    │  (core/blockstm/v2_executor.go)
                    │  BlockSTM() │
                    └──────┬──────┘
          ┌────────────────┼────────────────┐
          │                │                │
    ┌─────▼─────┐   ┌─────▼─────┐   ┌─────▼─────┐
    │  Workers   │   │ Validator │   │ Settlement│
    │  (N gortn) │   │ (1 gortn) │   │ (1 gortn) │
    └─────┬─────┘   └─────┬─────┘   └─────┬─────┘
          │                │                │
    ┌─────▼─────┐   ┌─────▼─────┐   ┌─────▼─────┐
    │ParallelSDB│   │ StoreReads│   │  finalDB  │
    │ per-tx PDB│   │ + BalReads│   │ settlement│
    └─────┬─────┘   └───────────┘   └───────────┘
          │
    ┌─────▼──────────────────────────────────────┐
    │  SafeBase    Thread-safe base reads        │
    │              (sync.Map caches + pool       │
    │              copies of the underlying      │
    │              StateDB; trieReader runs in   │
    │              concurrent-reads mode)        │
    │  MVStore     Multi-version per-key store   │
    │              (sharded, lock-free bloom     │
    │              filter for read misses)       │
    │  MVBalanceStr Commutative balance deltas   │
    │              (per-tx Add/Sub recorded;     │
    │              ReadDelta sums entries < N)   │
    └────────────────────────────────────────────┘
```

### Execution Flow

1. **Task building.** Block transactions become `V2Task`s. Same-sender
   chains get pre-computed nonces (`SenderNonces`) so nonce reads on a
   chain are deterministic and skipped during validation.

2. **Parallel execution.** N worker goroutines pull tasks from a
   buffered dispatcher channel (window size
   `numWorkers * blockstm.InFlightTaskMultiplier`). Each tx runs in its
   own `ParallelStateDB`, reading from `SafeBase` (block-start state) +
   `MVStore` (prior txs' deferred writes) + `MVBalanceStore` (prior txs'
   balance deltas). Reads are recorded in `StoreReads` / `BalReads` for
   validation.

3. **Sequential validation.** A single goroutine validates txs in
   tx-index order. For each tx, it re-reads every recorded key from
   MVStore. A successful match — by writer/incarnation, or by
   value-equal fallback — keeps the tx; otherwise the tx is dispatched
   for re-execution. The validator is the single source of truth for
   settle ordering — `assertSettleOrder` (under the `invariants` build
   tag) pins this.

4. **Pipelined settlement.** As txs are validated, a settlement
   goroutine drains `chSettle` in tx-index order and applies each tx's
   writes to `finalDB` (the real, single-threaded `*state.StateDB`)
   through the `*Direct` setter family, then asks `finalDB` for the
   IntermediateRoot.

5. **Re-execution under per-key pipelining.** A failed tx's old
   `StoreReads` entries are flagged `ESTIMATE` in the MVStore, then a
   goroutine re-runs the tx with the next incarnation. Readers that
   encounter an `ESTIMATE` entry under `Incarnation > 0` block on
   `WaitForFinal(writerIdx)` until the upstream writer is finalized,
   then re-read.

### Transaction Lifecycle

Each transaction passes through one of two paths:

```
  Pending → Executing → Validating ─┬─ pass → Finalized → Settled
                                    └─ fail → ReExecuting → Finalized → Settled
```

Workers run `Executing` for many txs in parallel; the single validator
goroutine walks `Validating` in tx-index order; the single settle
goroutine consumes `Finalized` txs in tx-index order.

#### At most two executions per tx

A transaction is executed at most **twice**: the initial speculative
run, plus one re-execution if validation fails. The re-executed result
is trusted by construction — there is no second `Validate()` call. Three
load-bearing facts make this sound:

1. **Validation runs in tx-index order.** `runValidationLoop` iterates
   `i = 0..n-1` calling `validateOne(i)` (`v2_executor.go`).

2. **Before validating tx i, all predecessors are finalized.**
   `validateOne(i)` calls `finishReexec(reexecDone, i-1)`, which
   blocks on `reexecDone[i-1]` until tx i-1's re-execution has flushed
   its writes to MVStore. So by the time tx i's reads are checked,
   every prior tx's writes are committed at their final incarnation.

3. **The re-exec result is trusted, not re-validated.**
   `finishReexec` marks `finalized[idx]=true` and pushes to settle
   without calling `Validate()` again.

Together: when tx i's re-execution runs, it sees a fully-stabilized
predecessor history, so one re-exec converges. The build-tag-gated
`assertReexecVisitedExactlyOnce` pins this at runtime — every
dispatched re-exec is consumed exactly once.

#### Worked example: a cascading dependency

Three transactions where each depends on the previous:

```
tx1: writes slot A
tx2: reads slot A, writes slot B (only on the correct branch)
tx3: reads slot B
```

Phase 1 — parallel speculative execution:

```
tx1: writes A=newA                                [exec 1]
tx2: races past tx1, reads A=baseA               [exec 1]
     wrong branch → no write to B
tx3: reads B (no writer found, records absence)  [exec 1]
```

Phase 2 — sequential validation:

```
validateOne(0): tx1's reads are clean       → finalizePass
validateOne(1): tx2's recorded read of A
                doesn't match the current
                MVStore entry (now tx1's)    → dispatchReexec(1)
                  tx2 reexec: reads A=newA,
                  writes B=newB              [exec 2]
validateOne(2): finishReexec(1) — blocks
                  until tx2's reexec lands
                tx3's recorded "no writer
                for B" no longer matches
                (tx2 now writes B)           → dispatchReexec(2)
                  tx3 reexec: reads B=newB,
                  runs correctly             [exec 2]
```

Total execution counts: tx1 = 1, tx2 = 2, tx3 = 2. No transaction
reaches a third execution despite the cascading dependency. The
property holds because each re-execution runs against a stable
view — tx3's re-exec sees the final tx2 state, not a transient one,
because `finishReexec(1)` completed before `validateOne(2)` proceeded
to re-validate tx3's reads.

This is also the scalability ceiling. Because each successor's
re-exec waits for the predecessor's re-exec via `finishReexec(i-1)`,
a chain of dependent re-executions serializes through this gate. The
worker pool can churn through *initial* executions of later txs in
parallel during these waits, but re-execs themselves serialize.

#### Concurrency timeline (same cascade)

```
Workers:    [tx1_exec]──┐ [tx2_exec]──┐ [tx3_exec]──┐ ...
                        │             │             │
Validator:              └─validate(0) ┴─validate(1)─┴─validate(2)
                                       ↓dispatch    ↓ wait(reexec1)
                                       reexec1───→  ↓dispatch
                                                    reexec2 ───→
Settle:                  settle(0)     ...          settle(1)  settle(2)
```

### Key data structures

**`*state.ParallelStateDB`** (`core/state/parallel_statedb.go`).
Per-tx EVM state, implements `vm.StateDB`. Reads from `SafeBase` /
`MVStore` / `MVBalanceStore`; writes to local maps + (at flush time) to
the multi-version stores. Tracks reads in `StoreReads` and `BalReads`
for validation.

**`*state.SafeBase`** (`core/state/safe_base.go`). Thread-safe wrapper
around an underlying `*state.StateDB` with sync.Map caches for nonce /
balance / code / storage / existence reads. Cache misses go through a
bounded pool of `db.Copy()` instances; the pool copies share the
underlying `reader`, so the V2 entry point calls
`base.EnableConcurrentReads()` to put the trieReader into its
concurrent-safe mode (sync.Map node-resolve cache instead of in-place
mutation). This is enforced inside `ExecuteV2BlockSTM` so any caller
gets it for free.

**`*blockstm.MVStore`** (`core/blockstm/mvstore.go`). Sharded
(`mvStoreShards = 64`) multi-version store. Each key maps to a sorted
slice of `versionedEntry{txIdx, incarnation, value, estimate}`. A
lock-free bloom filter gates reads — if a key was never written, the
shard mutex is never touched.

**`*blockstm.MVBalanceStore`** (`core/blockstm/mvbalance_store.go`).
Sharded commutative delta store: each (addr, txIdx) records cumulative
`Add` and `Sub`. Reads sum every prior tx's deltas for the address;
re-ordering doesn't affect correctness because the operations commute.

**`*blockstm.v2ExecCtx`** (`core/blockstm/v2_executor.go`). Executor
state — workers, validation, dispatcher, settlement, per-tx
`completionCh` / `execDone` channels for waitForTx / waitForFinal.

### Validation

Every state read records a `StoreReadDesc{Key, WriterIdx, WriterInc,
StoreVal}`. At validation time, the validator re-reads each key:

- **Version match.** Same writer + incarnation → valid (fast path).
- **Value-based fallback.** Different version, but `valuesEqual(curVal,
  rd.StoreVal)` and the current entry isn't `ESTIMATE` → valid. Handles
  idempotent writes (e.g., a reentrancy-guard SSTORE that flips back).
- **Base-read match.** Recorded as base (`WriterIdx == -1`) and current
  state has no entry → valid.
- **Mismatch (incl. ESTIMATE).** Anything else → invalid → re-execute.

Special cases:

- **Sender-chain nonces.** Skipped per-address: only nonce reads for
  addresses in `SenderNonces` are exempted.
- **Balance deltas.** Validated by re-reading `(sumAdd, sumSub)`. Fees
  are applied via the real StateDB during settlement, not via
  MVBalanceStore — coinbase reads go through the same delta path as any
  other address (no asymmetric exemption).
- **GetCommittedState cache.** Per-tx cache pins the SSTORE "original"
  value across multiple SSTOREs on the same slot (otherwise an upstream
  re-execution mid-tx breaks gas accounting and refund counters).
- **GetCodeHash.** Reads `CodePath` through `readStoreWait` and records
  the read; ESTIMATE/COMMITTED entries are filtered like every other
  getter.

### Settlement

Each validated tx's writes hit `finalDB` via the `*Direct` setter
family, in `SettleTo`'s call order:

- **Nonces.** `SetNonceDirect` — bypass journal, mark dirty.
- **Storage.** `SetStorageDirectWithOrigins` — bypasses journaling and
  pre-populates `originStorage` so `FinaliseFastWithPrefetch` doesn't
  go to disk for origin lookups.
- **Code.** `SetCode` (the journaled path; rare per-tx).
- **Balances.** Replayed from the per-tx `BalanceOps` slice in order,
  interleaved with transfer-log emission so receipts and balance
  changes happen in the same order the serial path produced them.
- **Account creation / self-destruct.** Replayed from `created` and
  `destructed` maps onto the real `StateDB`.
- **Fee data.** Burn (post-London) + tip applied to the real StateDB
  with a deferred fee-transfer log (deprecated but kept for receipt
  parity with the serial path).

If a tx panicked, settlement is skipped and `V2ExecutionResult.PanickedIdx`
is recorded; `V2StateProcessor.Process` returns an error and the
serial fallback in `BlockChain.ProcessBlock` takes over.

## Correctness — known bug classes and how V2 prevents them

A pattern emerged from V2 development: **PDB methods that modify state
without proper journal entries**. Serial's `StateDB` journals every
mutation; PDB's local maps bypass journaling, so each mutation needs an
explicit `parallelJournalEntry` for revert correctness.

Bugs of this class that have been fixed in tree:

| Symptom | Root cause | Fix |
|---|---|---|
| Missing transfer logs after revert | `RevertToSnapshot` truncated `BalanceOps` and `logs` but not `Transfers` | Snapshot saves `len(Transfers)`; revert truncates |
| Stale nonce reads pass validation | `SenderNonces` skipped ALL nonce validation if non-empty | Per-address check `SenderNonces[addr]` |
| ERC-4337 stale deposit balance | Balance reads outside writeset were skipped | Validate every balance read |
| `refund counter below zero` panic | `GetCommittedState` re-read MVStore mid-tx | Per-tx `committedCache` |
| AA23 reverts on EIP-7702 delegated accounts | `SetCode` skipped `CreateAccount` | `SetCode` calls `CreateAccount` if not already created |
| Reverted CREATE leaves stale bytecode | `SetCode` not journaled | `jkCode` entry restores `localCode` and MVStore |
| MEV cold-SLOAD gas mismatch | `Prepare` journaled the warm-coinbase add | Direct `accessList.AddAddress`, no journal |
| Reentrancy lock stuck in reverted call | `SetTransientState` not journaled | `jkTransient` entry restores prev value |
| Stale `EXTCODEHASH` from re-executing writer | `GetCodeHash` used raw `MVStore.Read` (no ESTIMATE filter, no read tracking) | Use `readStoreWait` + `recordStoreRead`; filters and tracks like every other getter |
| Wrong block state root from a panicked tx | `FlushToMVStore` + `SettleTo` ran on a partially-written PDB | Both early-return on `Panicked`; `V2StateProcessor.Process` returns error |
| `validateBalanceRead` and `diagnoseBalanceRead` disagreed on coinbase | Diagnose path had a stale coinbase skip | Drop the skip |
| `MVBalanceStore.ZeroDelta` bumped version on absent entries | Version invalidated downstream caches needlessly | Gate the bump on entry existence |
| Trie node race in V2 worker pool | `SafeBase` pool copies share the reader; `base` wasn't put into concurrent-reads mode | `ExecuteV2BlockSTM` defensively calls `base.EnableConcurrentReads()` |

## Performance

### Test data (Git LFS)

The 241 mainnet blocks + their pre-block witnesses live under
`core/blockstm/testdata/` and are managed via Git LFS — total size
~1.6 GB across 484 files (`*.block`, `*.witness.gz`, `codes.tar.gz`,
`codes/*.bin`). Both the consistency test (`TestV2BlockSTMAllBlocks`)
and the benchmark (`BenchmarkV2AllBlocks`) need them materialized
on disk.

A fresh clone gets LFS pointer files, not the actual data. Materialize
once after cloning:

```
git lfs install   # one-time per machine — installs the LFS hooks
git lfs pull      # fetches the actual block + witness data
```

Verify the fixtures resolved properly:

```
file core/blockstm/testdata/0x4EC6D10.block
# expected: JSON text data
# if you see "ASCII text" with a "version https://git-lfs.github.com..."
# header instead, LFS hasn't pulled yet.
```

The benchmark/consistency harnesses are gated on `BOR_BLOCKSTM_TEST=1`
to avoid surprising contributors who haven't pulled the fixtures —
the tests skip cleanly without LFS data when the env var is unset.

### Witness benchmark (in-memory, 241 mainnet blocks)

| Variant | Throughput |
|---|---:|
| Serial | 342 mgas/s |
| V2 / 4 workers | 549 mgas/s |
| V2 / 8 workers | 547 mgas/s |
| V2 / 16 workers | 303 mgas/s (over-subscribed) |

Run with:
```
BOR_BLOCKSTM_TEST=1 go test -run='^$' -bench='BenchmarkV2AllBlocks' \
    -benchtime=1x -timeout=600s ./core/ -count=1
```

Single-shot benchmark variance on this hardware is ±10–25% per variant
across consecutive runs of identical code, so V2/4w and V2/8w should
be read as "essentially the same speed."

### Production gap (historical observation)

Earlier production measurements with a real pebble-backed database
showed V2 running slower than serial on full nodes — roughly 140 mgas/s
V2 vs. 200 mgas/s serial — driven by:

1. **Re-execution overhead.** Production reports 50–60 validation
   failures per block vs. 0–6 on the in-memory benchmark. With pebble's
   real read latency, more txs are still mid-execution when the
   validator catches up, so more reads turn stale.

2. **Prefetcher contention.** The state prefetcher runs a parallel full
   serial execution to warm pebble's block cache. V2 benefits from the
   cache but contends for CPU and disk bandwidth.

3. **`IntermediateRoot`.** The post-execution trie computation is
   identical to serial — it doesn't improve in V2.

These numbers are illustrative; rerun on the target deployment before
making perf decisions.

### Why the in-order validator isn't the bottleneck

`TestV2ChainWaitDiagnostic` reports the validator goroutine spending
~70% of Phase-1 time blocked on `<-execDone[i]` across the 241-block
corpus (median 70.9%, p95 90.9%). At face value this looks like a fat
slack budget waiting to be reclaimed by going parallel.

It isn't. During every one of those waits, the worker pool is busy
executing the next batch of transactions in parallel. The validator
is *riding behind* worker progress, not blocking it — it can't catch
up faster because the data it needs (each tx's flushed writes) hasn't
landed in MVStore yet. Removing the in-order walk doesn't speed up
the workers, and on this corpus the worker tail dominates Phase-1
wall-clock for almost every block.

We confirmed this empirically by prototyping three alternative
validator pipelines and measuring them against the corpus:

| Strategy | Soundness | Mean Phase-1 |
|---|---|---:|
| Sequential walk, 1 reexec per tx (current) | ✓ sound | 122.6 ms |
| Per-tx validators, blanket chain-wait `[0..i-1]` before final validate | ✓ sound | 119.1 ms (-3%) |
| Per-tx validators, chain-wait only on actual read-set deps | ✗ **unsound** | 117.6 ms (n/a) |
| Optimistic validate→reexec loop + final chain-wait + revalidate | ✓ sound | 120.6 ms (-2%) |

Per-tx validators with blanket chain-wait shave ~3% by hiding some
ride-along time across goroutines, but the chain-wait at the
soundness boundary still serializes through reexec dependencies.

The deps-only "cascade-aware" variant looked tempting (a reader
should only wait for the txs whose writes it actually reads) and is
the smallest change in code volume — but it's unsound: a reexec of
tx *j* (where *j* ∉ deps(*i*)) can introduce a NEW writer for a key
*i* read from a closer prior writer *j′*, and the strict deps-only
chain-wait misses *j* entirely. State root diverges on 100% of the
corpus blocks once any block contains such a reexec.

Optimistic multi-reexec patches the hole by adding a final chain-wait
+ revalidate, regaining soundness — but the final chain-wait *is* the
soundness boundary and still gates finalize, so wall-clock ends up
roughly equal to blanket-chain-wait, with extra allocations from the
reexec churn.

**Headline:** the in-order validation walk is *not* the binding
constraint on this corpus. Wall-clock improvements need to attack one
of:

1. **Worker tail** — better intra-tx parallelism, predictive
   scheduling that starts the slowest tx first, or splitting hot
   contracts.
2. **Reexec count** — better dependency prediction (`toPrev` /
   conflict-addr chaining) reduces vfail rate at the source. Each
   percentage point of vfail rate is roughly 1ms per block.
3. **The soundness boundary itself** — full per-key reader
   notification (V1-style cascade tracking) would let the deps-only
   chain-wait become sound, recovering the ~5% measured under the
   unsound prototype. Significant engineering investment.

## Drift detection (automated)

Several tests fail the build / `go test` when an upstream go-ethereum
merge introduces a divergence between V1 (`*state.StateDB`) and V2
(`*state.ParallelStateDB`). Each catches a different drift class:

| Test | What it pins |
|------|---|
| `core/vm/statedb_impl_test.go` | Compile-time conformance: PDB satisfies `vm.StateDB` |
| `TestPDBMethodParity` | Every exported `*StateDB` method exists on `*ParallelStateDB` or is in `pdbExemptMethods` with a category |
| `TestV2DependencyCompileCheck` | Every `*StateDB` method V2 settle calls remains present (build fails otherwise) |
| `TestDirectSetterParity_*` | `SetXDirect` produces byte-identical state root to journaled `SetX + Finalise` |
| `TestV2JournalEntryCoverage` | Every journal-entry type in `journal.go` has a `parallelJournalEntry` mapping or documented implicit handling |
| `TestV2TracingHookParity` | Every `tracing.Hooks` field is classified as fired-in-V2 or skipped-with-rationale |
| `TestV2ForkParity` | Every `params.ChainConfig.IsX` fork rule is classified per V1/V2 path |
| `TestPDB_AllGetters_*` | Every PDB getter records its read with the right `WriterIdx` (Committed / ESTIMATE / NoEntry / AtTxZero) |
| `FuzzV2ExecutorVsSerial` | Random tx batches run through `ExecuteV2BlockSTM` produce the same state root as a serial `ApplyMessage` loop |
| `TestV2BlockSTMAllBlocks` (gated) | 241 real Polygon mainnet blocks — V1 and V2 produce identical state roots end-to-end |

Build-tag invariants (`-tags invariants`) add runtime assertions:
- `assertSettleOrder` — V2 validation loop's induction holds.
- `assertReexecVisitedExactlyOnce` — drain loop doesn't lose a tx.
- `assertSettleNotPanicked` — panicked PDBs never reach settle.

## What could still be improved

### Performance

1. **Reduce re-executions.** The dominant cost in production (50–60
   vfails per block on full nodes vs 0–6 in the in-memory benchmark).
   Options: conflict prediction (use prior blocks' vfail addresses),
   wait-before-execute on predicted-conflict txs, batch re-execution.
   Note: on the in-memory corpus the validator chain itself is *not*
   the binding constraint — the worker tail is — so refactoring the
   validation pipeline alone won't help (see "Why the in-order
   validator isn't the bottleneck" above for the prototypes that
   established this).

2. **Worker tail.** On the in-memory corpus, Phase-1 wall-clock is
   pinned at the slowest worker's completion time. Splitting hot
   contracts, intra-tx parallelism for heavy txs, or predictive
   scheduling that starts the slowest tx first are the levers. The
   alternative-validator prototypes above showed ≤3% gap between
   strategies precisely because none of them touch this term.

3. **Prefetcher integration.** Today the prefetcher runs a full serial
   execution concurrently. Letting V2 wait for partial prefetcher
   completion could trade a small latency hit for less CPU contention.

4. **Block-level pipelining.** Overlap block N's `IntermediateRoot` /
   commit with block N+1's execution.

5. **Per-key reader notification.** Would make a deps-only chain-wait
   sound (today the unsound prototype showed ~5% on cascade-heavy
   blocks). Means tracking `map[Key][]int` (key → reader tx indices)
   updated on every read, plus invalidation logic when a reexec adds
   a new writer for a key already-read. ~1k LOC and per-read overhead.

### Architecture

1. **Verkle-mode access events.** `ParallelStateDB.AccessEvents()`
   returns nil. Needs a fork gate before Verkle activates on Polygon.

2. **V1 BlockSTM removal.** `ParallelStateProcessor` (V1 MVHashMap-based)
   and the entire `CompletionTracker` module are still in tree but
   unreachable in production — `bc.opcodeLevel` is never set true and
   `mvh.CT` is never assigned. Removing them would eliminate ~1500
   lines of dead code, but should wait until V2 has fully soaked.

### Tooling

1. **Mutation testing in CI.** [diffguard](https://github.com/0xPolygon/diffguard)
   already runs on V2 critical paths and reports Tier-1 logic
   kill-rates ≥ 99%. Worth wiring into nightly CI with a Tier-1 ≥ 90%
   gate.

2. **Race-detected fuzz in CI.** The fuzz under `-race` caught the
   shared-trie-reader race that the non-race fuzz missed. Worth
   running `go test -race -fuzz=FuzzV2ExecutorVsSerial -fuzztime=…`
   on a nightly schedule.

3. **Production witness collection on validation failure.** If a real
   block fails V2's `validator.ValidateState`, save the witness +
   block + chain config so the failure can be reproduced locally.

## File map

| File | Purpose |
|------|---------|
| `core/blockstm/v2_executor.go` | V2 BlockSTM executor: worker pool, dispatcher, validation, settlement |
| `core/blockstm/mvstore.go` | Sharded multi-version per-key store with bloom filter |
| `core/blockstm/mvbalance_store.go` | Commutative balance delta store |
| `core/blockstm/mvhashmap.go` | V1 MVHashMap (legacy) and shared key/bloom helpers |
| `core/blockstm/completion_tracker.go` | V1 opcode-level suspension primitive (legacy, unreachable in production) |
| `core/blockstm/invariants_{on,off}.go` | Build-tag-gated runtime assertions for executor invariants |
| `core/state/parallel_statedb.go` | `*ParallelStateDB`: per-tx `vm.StateDB` implementation |
| `core/state/parallel_statedb_validate.go` | Read-set validation against MVStore |
| `core/state/parallel_statedb_settle.go` | `SettleTo`: apply per-tx writes to finalDB |
| `core/state/parallel_statedb_journal.go` | Tagged-union journal entries + revert handlers |
| `core/state/safe_base.go` | Thread-safe base-state reads, sync.Map caches, pool copies |
| `core/state/invariants_{on,off}.go` | Build-tag-gated PDB-side runtime assertions |
| `core/parallel_state_processor.go` | `V2StateProcessor`, `ExecuteV2BlockSTM`, `v2Env`, settle-fn closure |
| `core/blockchain.go` | Production integration: reader setup, prefetcher, ProcessBlock |
| `core/evm.go` | `Transfer` + `RecordTransfer` for V2 deferred logs |

### Tests of note

| File | Purpose |
|------|---------|
| `core/state/v2_method_parity_test.go` | Reflect-based `*StateDB` ↔ `*ParallelStateDB` method parity + V2 dependency compile-check |
| `core/state/v2_direct_setter_parity_test.go` | `SetXDirect` ↔ journaled-`SetX` state-root parity |
| `core/state/v2_journal_entry_coverage_test.go` | AST-based journal-entry coverage (every revert kind has a parallel mapping) |
| `core/state/parallel_statedb_getter_table_test.go` | Symmetric "every PDB getter is tracked" table (Committed / ESTIMATE / NoEntry / AtTxZero) |
| `core/parallel_state_processor_hooks_parity_test.go` | `tracing.Hooks` field-by-field fire/skip classification |
| `core/parallel_state_processor_fork_parity_test.go` | `params.ChainConfig.IsX` V1/V2 reference parity |
| `core/state/v2_differential_test.go` | PDB-only diff against serial StateDB on hand-written scenarios |
| `core/state/v2_fuzz_test.go` | Fuzz on the same diff |
| `core/state/v2_executor_differential_test.go` | Synthetic-env executor diff |
| `core/v2_serial_parity_fuzz_test.go` | Real-tx executor diff: `ExecuteV2BlockSTM` vs. `ApplyMessage` loop |
| `core/v2_blockstm_test.go` | Targeted balance-validation integration tests |
| `core/mainnet_witness_benchmark_test.go` | `BenchmarkV2AllBlocks` (perf) and `TestV2BlockSTMAllBlocks` (consistency) on 241 mainnet blocks |
