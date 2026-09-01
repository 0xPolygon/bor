# Decision ledger: go-ethereum v1.17.5 sync

Append-only record of every conflict resolved, one entry per path, plus the
per-batch fork/EIP and twin scans. `SYNC_BASE` is `v1.17.4`; ownership
detection anchors there, never on the previous batch boundary.

## Batch 1/6 (`04bf04530`, plan row 1) — MILESTONE-OPENING

Range `v1.17.4..04bf04530`, 20 first-parent commits. 30 conflicted paths (22
content, 8 modify/delete) and **eleven further breaks that git reported as
clean merges**. That ratio is the batch's main lesson: the conflicts were the
cheap part, and every one of the eleven was found by the compiler, the vet
pass or the tests rather than by `git`.

### Consensus-critical and hardfork-surface resolutions

#### `params/protocol_params.go` — EIP-7954 max code size (hardfork surface)

`MaxCodeSizeAmsterdam` moves 32768 → **65536** (#35217). Took upstream's value,
kept bor's `(EIP-7954)` annotation.

Verified dormant. Bor's live cap is `MaxCodeSizePostAhmedabad = 32768`, applied
through `checkMaxCodeSize`, which consults `Bor.IsAhmedabad(BlockNumber)`
first; the Amsterdam constant is only reachable via `CheckMaxCodeSize(&rules,
…)` once Amsterdam activates, and Amsterdam is nil on every bor preset. No
behaviour change today.

Recorded for the enablement decision: turning Amsterdam on now doubles the
contract size limit relative to Ahmedabad rather than leaving it unchanged.
That is a product call, not a merge one. See `fork-register.md`.

#### `core/state_processor.go` — EIP-8282 wired dormant, adapted to bor's shape

EIP-8282 (#35175) adds a builder deposit/exit queue to `PostExecution`. It
arrives entangled with the **BAL construction cluster bor declined at batch 28
of the v1.17.4 sync**: upstream's `PostExecution` returns
`(requests, blockAccessList, err)` and its helpers take `rules` and
`*bal.ConstructionBlockAccessList`; bor's return two values and take neither.

Neither side-pick was correct. Keeping ours silently drops a new EIP; taking
theirs re-adopts the declined BAL cluster through the back door. Wired the EIP
in bor's shape instead:

```go
func ProcessBuilderDepositQueue(requests *[][]byte, evm *vm.EVM, blockAccessIndex uint32) error {
	return processRequestsSystemCall(requests, evm, 0x03, params.BuilderDepositAddress, blockAccessIndex)
}
```

— mirroring bor's existing EIP-7002/7251 wrappers exactly, called behind
`config.IsAmsterdam(number)`, bor's **block-based** one-argument gate. Request
types 0x03/0x04; the address constants had already auto-merged into
`params/protocol_params.go`, so the wiring is complete rather than half-present.

The other four hunks kept bor's side: declined BAL in `PreExecution`, the
two-value signature, and block-based `IsPrague(number)`.

#### `core/state/statedb.go` — convergence, not a witness change

#35256 moves the prefetch call ahead of the `acct == nil` short circuit so
absent accounts still have their trie path walked — "the trie path proves its
non-existence for witnesses".

**Bor already did this.** At HEAD the prefetch is at line 1290 and the nil check
at 1296. Upstream converged on bor's ordering. Took bor's side, which means the
witness rule was satisfied rather than triggered: nothing about witness
generation changes.

Resolved by replacing the whole `getStateObject` function with bor's HEAD body
rather than `checkout --ours`, because EIP-8246 separately modified this file
and `--ours` would have discarded that.

Noted, not fixed: bor's `getStateObject` increments `s.AccountLoaded` twice
(once after the destruct check, once after `setStateObject`). Pre-existing, and
a merge is the wrong place to change it.

#### EIP-8246 (#35219) — a relocation that orphaned a bor-only twin

EIP-8246 **removes the SELFDESTRUCT self-burn** under Amsterdam: the balance is
preserved and the account kept as balance-only, so there is no burn left to
log. Upstream therefore deleted `StateDB.LogsForBurnAccounts`, its hooked
forwarder, the `vm.StateDB` interface entry, the `state_transition.go` call
site and `types.EthBurnLog`. All five removals landed cleanly in bor.

Bor also had **`ParallelStateDB.LogsForBurnAccounts`**, a twin upstream cannot
touch because it has no such file. It survived, orphaned, still calling the
deleted `types.EthBurnLog` — so the tree did not build. Deleted it.

This keeps V1/V2 in lockstep: the single call site went through the
`vm.StateDB` interface and served both executors, so both lose the end-of-tx
pass together, and the replacement logic in `opSelfdestruct6780` is reached
identically by both. Upstream's replacement is gated on `IsAmsterdam`, and bor
populates `Rules.IsAmsterdam` at `params/config.go:1943` from the block-based
`ChainConfig.IsAmsterdam(num)`, so it stays dormant.

### #35190 — adopted as a unit, on operator decision

A snap-sync correctness fix spanning eight files. Without it a block present
only by hash (side chain, or an orphan from an unclean shutdown) can be used as
the sync link-up point and its canonical mapping is never repaired.

The fix has two halves that only work together: **detection** (require the
canonical mapping) and **repair** (write unconditionally). Adopting only
detection produces a re-request loop — the downloader keeps deciding "not
linked" while `writeLive` keeps skipping the write because `HasBlock` is true.
Partial adoption is worse than either extreme. Escalated to the operator, who
chose full adoption.

All four landing sites:

- `eth/downloader/bor_downloader.go` — ported the `GetCanonicalHash(uint64)
  declaration into bor's `BlockChain` interface. The declaration arrived in a
  modify/delete conflict because bor renamed `downloader.go`; a reflex
  keep-deleted would have broken the build, since the auto-merged
  `beaconsync.go` had already gained four call sites.
  `core/blockchain_reader.go:380` already implements it.
- `eth/downloader/beaconsync.go` — merged both intents: bor's package-local
  `FullSync`/`SnapSync` constants (the declined syncModer refactor #33157) with
  upstream's canonical-hash condition.
- `eth/downloader/skeleton.go` — bor has **no `linked` method**; it inlined the
  check at two sites. Ported the canonical-hash requirement into both.
- `core/blockchain.go` — dropped the `skipPresenceCheck` machinery (six sites)
  while keeping bor's `(int, error)` signature, `headers` collection for the
  bor reorg check, `HasHeader` guard and bor-receipt handling. The `HasHeader`
  guard is bor's own — the merge base has no such check — so it stays.

Also repaired here: the auto-merge had replaced two of bor's `return 0, err`
inside `writeLive` with upstream's single-value `return err`, which the
`(int, error)` signature does not admit.

### #35191 — v0 blob sidecar drop, adopted as a unit (reversal recorded)

Initially declined in `core/txpool/validation.go`, keeping bor's block-based
Osaka gate on sidecar version. That was wrong, and the tests found it: the
commit's **production** half had auto-merged into `blobpool.go` (cell proofs,
`txBlobOverhead` via `kzg4844.CellProofsPerBlob`), so the tree contradicted
itself — validation demanded v0 pre-Osaka while the pool stored v1 only.

Adopted as a unit: `validateBlobTx` requires `BlobSidecarVersion1`
unconditionally and regains upstream's simplified signature. Impact on bor is
nil in practice — `eth/backend.go` declares `blobTxPool` but never assigns it,
and the blob pool is not in bor's subpool list — but the tree is now
self-consistent.

`blobpool_test.go` was resolved as a real three-way merge (via `merge-file`
against the base), not a side-pick: bor has five commits of customisation in
that file, so both "ours" and "theirs" lose something. Each conflict took bor's
`_ = os.MkdirAll` errcheck idiom and exported `GetBlobs(ctx, …)` API together
with upstream's `newSlotterEIP7594` and v1-only expectations.

### pebble v2 (#34009) — a port, not an adoption

Upstream keeps the old implementation as `ethdb/pebble/pebble_v1.go` (753
lines), adds `version.go` for on-disk format detection, makes `pebble.go` the v2
implementation, and swaps `cockroachdb/pebble` for `cockroachdb/pebble/v2
v2.1.4`.

Bor's `pebble.go` carries 163 metric/tuning references — `#2170`'s write-path
tuning and pathdb carry-over, plus a bespoke amplification suite
(`readAmpGauge`, `levelWriteAmpGauge`, `totalWriteAmpGauge`,
`calcWriteAmpGauge`, `calcSpaceAmpGauge`, `walWriteAmpGauge`) with no upstream
equivalent. All of it was preserved and ported onto v2's renamed API, verified
against the module source rather than guessed:

| bor used (v1) | pebble v2 |
| ------------- | --------- |
| `NumFiles` | `TablesCount` |
| `Size` | `TablesSize` |
| `BytesRead` | `TableBytesRead` |
| `BytesCompacted` | `TableBytesCompacted` |
| `BytesFlushed` | `TableBytesFlushed` |
| `BytesIn` | `TableBytesIn` |
| `Score`, `WriteAmp()` | unchanged |

`Compact` gained `context.Background()` (a v2 requirement). Four of the renames
were inside conflicts; **four more were not** — `level.Size`, `BytesIn`,
`BytesFlushed`, `BytesCompacted` at lines 1052–1083 sat in auto-merged bor code
that kept v1 names while the import moved to v2. Only the compiler found them.

**Decision, and its operator-visible consequence.** Bor's instrumentation lives
in `pebble.go`, i.e. the v2 path only. It was deliberately *not* duplicated into
`pebble_v1.go`, which would fork the tuning across two implementations for a
transitional path. A node whose database is still at a pre-v2 format version
therefore opens through upstream's v1 implementation and reports only upstream's
basic metrics until the database reaches a v2-capable format. `go.mod` keeps
both pebble v1 and v2 — `pebble_v1.go` needs v1, so that is correct rather than
residue.

### Remaining resolutions

| Path | Class | Decision |
| ---- | ----- | -------- |
| `core/vm/evm.go` | 3 | EIP-8037 spec change (#35173): reservoir moves inside `GasBudget`, `gas.Exit(err, reservoir)` → `gas.Exit(err)`, Amsterdam account-creation state charge dropped. Took upstream; call sites auto-merged consistently and `NewGasBudget` keeps its 2-arg form, so bor-only `consensus/bor/statefull/processor.go` still matches. |
| `core/vm/instructions.go` | 2 | #35173 added two `evm.GetRules().IsAmsterdam` sites; bor never adopted that accessor. Converted to `evm.chainRules.IsAmsterdam`, the idiom already used three times in the same file. Semantically identical — bor's `Rules.IsAmsterdam` is block-derived. |
| `core/txpool/validation.go` | 3 | The auto-merged `core.IntrinsicGas(…, from, tx.To(), value, …)` **requires** `from`, so bor's `if _, err := types.Sender(…)` form could not compile. Resolved as `from, err := types.Sender(signer, tx)` with bor's `&& !opts.AllowUnprotectedTxs` preserved. Kept bor's block-based EIP-7623 gate with upstream's new `FloorDataGas` signature. **Residual risk:** on the `err != nil && AllowUnprotectedTxs` path `from` is the zero address and now feeds intrinsic-gas calculation. Pre-existing bor behaviour; upstream's change widened its blast radius. |
| `core/rawdb/eradb/{eradb,eradb_test}.go` | 3 | #34978 (ere files in the era store) declined wholesale — it imports `internal/era/execdb`, a package bor deleted entirely, so there is no landing site. Per-hunk "keep ours" left the file inconsistent with its own auto-merged parts; restoring bor's version wholesale is the coherent form. |
| `core/rawdb/database.go` | 2 | Upstream refactored `hasPreexistingDb` into `fileExists`/`anyFileMatches`; the new switch had already auto-merged, so the conflict was bor's superseded inline glob. Took upstream. |
| `eth/downloader/api.go` | 1 | Kept bor's `api.mux.Subscribe(StartEvent{})`. Upstream's `SubscribeSyncEvents` preamble had auto-merged and collided with it; removed. |
| `eth/downloader/skeleton_test.go`, `eth/catalyst/api_test.go` | 1 | Kept bor's side (`earlierBackoff`; `t.Skip("bor: not relevant")` on `TestGetBlobsV1`). |
| `cmd/devp2p/discv4cmd.go` | 2 | Took upstream. Bor's side was pre-restructure code; upstream's multi-address support had auto-merged above, declaring `socket, err` and accumulating `extPort`, so bor's side would redeclare and use out-of-scope variables. |
| `cmd/evm/internal/t8ntool/transaction.go` | 2 | Added only `holiman/uint256` (used twice); bor already imports `urfave/cli/v2` higher in the block, so upstream's re-add would duplicate it. |
| `go.mod`, `go.sum`, `cmd/keeper/go.sum` | 1 | Union by module path, preferring bor's pins (e.g. gnark-crypto v0.19.2 over v0.18.1), plus upstream's `cockroachdb/pebble/v2 v2.1.4`. `go mod tidy` clean afterwards. |
| 8 modify/delete paths | 3 | Keep-deleted, each traced to its upstream commit and confirmed a genuine bor deletion: `core/bal_test.go`, `core/bintrie_witness_test.go`, `core/txpool/blobpool/cache{,_test}.go`, `eth/downloader/downloader{,_test}.go`, `internal/era/execdb/{iterator,reader}.go`. |

### Bor-only twin scan

Three twins needed hand-fixing, **none of which conflicted**:

- `core/state/parallel_statedb.go` — `LogsForBurnAccounts`, orphaned by EIP-8246
  (above). Deleted.
- `eth/tracers/parity.go` — still on the old `core.IntrinsicGas` signature.
  Updated to `(…, in.msg.From, in.msg.To, in.msg.Value, rules, CostPerStateByte)`.
- `core/txpool/blobpool/blobpool_test.go` — seven `newSlotter(` call sites after
  upstream renamed it `newSlotterEIP7594`.

`core/vm/operations_acl.go`'s PIP-88 twins (`makeGasSStoreFuncPIP88`,
`gasSLoadPIP88`) and the `pip88 bool` precompiles in `core/vm/contracts.go` were
checked and are unaffected: this batch changes neither `makeGasSStoreFunc`,
`gasSLoadEIP2929`, nor any precompile `RequiredGas` formula.

### Coverage gaps — four upstream test files removed

Every new EIP test in this batch is written against either timestamp-based
`AmsterdamTime` (which bor deleted; it gates by block) or `balChainConfig` from
the declined BAL cluster, so none compiles against bor:

| File | Blocked on |
| ---- | ---------- |
| `core/eip8037_test.go` | `balChainConfig` (declined `core/bal_test.go`) |
| `core/vm/eip8037_test.go` | `cfg.AmsterdamTime` |
| `core/eip2780_test.go` | `rules8037`, defined in `core/eip8037_test.go` |
| `core/eip8246_test.go` | `config.AmsterdamTime` |

Same precedent as `core/bintrie_witness_test.go` in the v1.17.4 sync. Bor takes
these EIPs' code without upstream's tests; rows in `needs-wiring.md`.

### Verification (invariant 8, per-batch tier)

- `go build ./...` — exit 0.
- `go vet ./...` — only the two pre-existing lock-copy findings
  (`core/parallel_state_processor.go:341`, `trie/secure_trie.go:88`), matching
  the v1.17.4 baseline exactly.
- `gofmt` clean on every touched file; `go mod tidy` clean in the main module
  and `cmd/keeper`.
- Package tests, all ok: `ethdb/pebble`, `params`, `core/rawdb`,
  `core/rawdb/eradb`, `core/txpool` (+ `blobpool`, `legacypool`, `locals`),
  `eth/downloader`, `core/state`, `core/vm`, `eth/tracers` (all subpackages),
  `cmd/devp2p`.
- One failure, pre-existing and signature-matched to the v1.17.4 baseline:
  `cmd/devp2p/internal/ethtest` panics on
  `params.(*BorConfig).CalculatePeriod` ← `miner.(*worker).newWorkLoop`, a nil
  `BorConfig`. Invisible to CI because `TESTALL` filters `cmd/`. Not introduced
  here; still owed as its own bug report.

### Method note for the remaining batches

Scripted region-replacement broke `core/blockchain.go` twice before it was done
with targeted edits. The cause is structural and will recur: the shared `}`
*after* a conflict can close different constructs on each side — there it closed
`if !skipPresenceCheck {` for bor and `if bc.insertStopped() {` for upstream — so
any resolution must consume it. Script the mechanical files; hand-edit anything
where brace ownership differs across sides.

## Batch 2/6 (`f9417bb27`, plan row 2)

Range `04bf04530..f9417bb27`, 20 first-parent commits. 24 conflicted paths (18
content, 6 modify/delete) plus **five further breaks git reported as clean
merges** — the same ratio lesson as batch 1, at a smaller absolute scale.

The batch is dominated by two commits: #35029 (blob-schedule shape) and #35213
(amsterdam override flag). Both are cases where declining the conflicted files
would have left an *auto-merged half* stranded, so both were adopted as units.

### Consensus-critical and hardfork-surface resolutions

#### #35029 — named forks no longer carry their own blob schedule

Upstream's rule change: from Prague onward the blob schedule is updated only at
BPO forks, so named forks (Osaka, Amsterdam, UBT) must not declare a
`BlobConfig`. Upstream drops `DefaultOsakaBlobConfig`, removes `Osaka`,
`Amsterdam` and `UBT` from `BlobScheduleConfig`, drops the osaka/amsterdam rows
from `CheckConfigForkOrder`, and rewrites `ChainConfig.BlobConfig(fork)` from a
per-fork switch into a downward walk with a nil guard.

**Adopted, in bor's shape.** This touches `params/config.go`, a hardfork
surface, so the reasoning is recorded in full:

- **Bor has no BPO forks at all.** Its `BlobScheduleConfig` is
  `{Cancun, Prague, Osaka, Verkle}` — `Verkle` is bor-only and untouched by
  #35029, so it is retained. Adoption here means dropping `Osaka` only.
- **The change is provably behaviour-neutral for bor.** Bor's
  `DefaultOsakaBlobConfig` was byte-identical to `DefaultPragueBlobConfig`
  (Target 6, Max 9, UpdateFraction 5007716), so an Osaka fall-through to Prague
  returns the same values. Verified against every bor config that sets a blob
  schedule.
- **Neither production network is affected either way.** `AmoyChainConfig` and
  `BorMainnetChainConfig` set no `BlobScheduleConfig` at all (and no
  `OsakaBlock`), so the live path never reaches these entries.
- **The live path is `consensus/misc/eip4844.latestBlobConfig`, not
  `ChainConfig.BlobConfig`.** The latter has **zero callers** in bor — its
  upstream callers live in surfaces bor diverged from. Both were updated
  consistently regardless.
- **Adoption brings a nil guard bor actually wants.** Upstream's rewritten
  `BlobConfig` opens with `if c.BlobScheduleConfig == nil { return nil }`; bor's
  switch had none, and bor's production configs are exactly the nil case. The
  function is uncalled today, so this is latent-bug removal, not a fix.

Banner lines were kept as bor's: bor prints forks by block (`#%-8v`) and its
Osaka line already carried no `blob:` suffix, so it matches upstream's
post-change shape without edit.

#### #35213 — amsterdam override flag, adopted as a unit

Upstream adds `--override.amsterdam`, threading a new `OverrideAmsterdam`
through `ChainOverrides` → `ethconfig.Config` → `eth.New` → `cfg.AmsterdamTime`.

Five files conflicted, but **`cmd/utils/flags.go` and `cmd/geth/main.go`
auto-merged** — the flag was already registered and already wired into the geth
command's flag set. Declining the conflicted five would have shipped a
`--override.amsterdam` that parses, is accepted, and silently does nothing.
That is the batch-1 partial-adoption hazard in its quietest form: no build
break, no test failure, just a flag that lies.

Adopted, converted to bor's block-based gate throughout: `OverrideAmsterdam` is
`*big.Int` (matching bor's existing `OverrideOsaka`/`OverrideVerkle`), read via
`ctx.Int64(...)` → `new(big.Int).SetInt64(v)`, and applied as
`cfg.AmsterdamBlock`, not `cfg.AmsterdamTime`. `eth/ethconfig/gen_config.go` is
gencodec-generated and heavily diverged in bor; it was resolved to bor's side
and the single new field inserted in the four places the generator would put it.

Noted, not changed: bor's override-flag usage strings all say "fork timestamp"
while bor applies them as block numbers — a pre-existing inconsistency shared by
`--override.osaka` and `--override.verkle`. The new flag inherits it rather than
diverging from its siblings.

### Fork/EIP scan

Three EIPs arrived; all merge dormant behind bor's nil Amsterdam gate. Full
rows in `fork-register.md`.

- **EIP-7997** (#35223, deterministic deployment factory) — auto-merged into
  `core/chain_makers.go` and `core/state_processor.go` using upstream's
  two-arg `IsAmsterdam(num, time)`, which bor does not have. Rewired to bor's
  one-arg block gate. The transition predicate ("Amsterdam active here, not at
  parent") is identical under block gating.
- **EIP-8038** (#35216, cold storage access cost) — `gasSLoad8038` collided
  with bor's `gasSLoadPIP88` as an add/add at the same offset; both kept. Its
  other halves (`params/protocol_params.go`, `core/vm/eips.go:620` which calls
  `gasSLoad8038`) had auto-merged, so declining would have broken the build.
- **EIP-7928 spec change** (#35260) — re-decline of the BAL construction
  cluster; touched only `core/bal_test.go`, already deleted.

### Remaining resolutions

| Path | Class | Decision |
| ---- | ----- | -------- |
| `node/jwt_handler.go` | 1 | #35022, case-insensitive `bearer` scheme. Took upstream verbatim. **Upstream's own commit is defective**: the check became case-insensitive but `strings.TrimPrefix(auth, "Bearer ")` did not, so a lowercase `bearer` now passes the check and is not stripped. Kept verbatim so upstream's eventual fix merges cleanly; not bor's to diverge on. |
| `triedb/pathdb/nodes.go` | 1 | #35232 prealloc. Merged bor's `clean *AddressBiasedCache` signature with upstream's `make(..., len(s.storageNodes)+1)`. |
| `core/rawdb/freezer_table.go` | 1 | #35258, `truncateHead` on an empty table. Bor's side equalled the base; took upstream. `resetTo` confirmed present in bor. |
| `eth/handler_eth.go` | 3 | #35237 preallocs the `seen` map in `handleTransactions`. **No landing site**: bor has neither `handleTransactions` nor `packet.Items()`, because it declined #33835 (delayed p2p decoding) and #33511 (drop eth/68). Kept ours. Not a new backlog row — a consequence of those two, recorded there. |
| `cmd/devp2p/internal/ethtest/suite.go` | 2 | #35138 fixes a `t.Fatalf` that was missing its format verbs, so a mismatched request id printed nothing useful. Bor declined upstream's eth/70 ethtest branching, so the conflict was kept as ours; two of bor's three sites had auto-merged the fix and the third was applied by hand. The `v5test` half auto-merged. |
| `consensus/misc/eip4844/eip4844.go` | 2 | #35029's live half. Dropped the Osaka case from `latestBlobConfig`; bor's one-arg `IsPrague(london)`/`IsCancun(london)` retained. Bor already had the nil-`BlobScheduleConfig` guard here. |
| `tests/init.go`, `core/genesis_test.go`, `consensus/misc/eip4844/eip4844_test.go`, `core/eth_transfer_logs_test.go` | 1 | #35029 fixture updates. Bor carries none of upstream's BPO/Amsterdam test configs, so those regions were kept empty; stray `Osaka:` blob entries removed to match the struct. `eth_transfer_logs_test.go` kept bor's block-based Amsterdam activation — bor never had upstream's `blobConfig.Amsterdam = blobConfig.Osaka` hack, which #35029 deletes. |
| 6 modify/delete paths | 3 | Keep-deleted, each confirmed a genuine prior bor deletion and each a `_test.go` or `package main` file, so none can strand a declaration non-test code depends on: `core/bal_test.go`, `core/bintrie_witness_test.go`, `core/eip8246_test.go`, `core/txpool/blobpool/cache_test.go`, `core/vm/eip8037_test.go`, `miner/stress/main.go`. |

### Bor-only twin scan

No twin needed hand-fixing this batch. `core/vm/operations_acl.go`'s PIP-88
twins were re-checked because EIP-8038 lands beside them: #35216 adds a new
`gasSLoad8038` and does not touch `gasSLoadPIP88`, `makeGasSStoreFuncPIP88`, or
any shared helper, so the PIP-88 path is unchanged.

The witness rule was not triggered: no commit in this range touches
`core/stateless/`, `eth/protocols/wit/`, or witness handling in the miner,
blockchain or downloader.

### Coverage gaps — three more upstream test files removed, and a cheap unblock

`core/eip7997_test.go`, `core/eip8038_test.go` and `core/vm/eip8038_test.go`
were added by this batch and none compiles against bor:

| File | Blocked on | Which resolves to |
| ---- | ---------- | ----------------- |
| `core/eip7997_test.go` | `mkState` | `core/eip8037_test.go` (removed in batch 1) |
| `core/eip8038_test.go` | `mkState`, `rules8037` | `core/eip8037_test.go` (removed in batch 1) |
| `core/vm/eip8038_test.go` | `amsterdam8037EVM` | `core/vm/eip8037_test.go` (removed in batch 1) |

Note what this is and is not. **None of the three depends on `AmsterdamTime` or
`balChainConfig` directly.** They are orphaned purely by batch 1's decline
chain, exactly as `core/eip2780_test.go` was.

Reading the two blocking files makes the cost concrete, and it is much smaller
than the seven dropped files suggest. Each is blocked on **one line**:

- `core/eip8037_test.go:40` — `cfg8037 = balChainConfig()`, where upstream's
  helper is just `cfg := *params.MergedTestChainConfig; cfg.AmsterdamTime = new(uint64); return &cfg`.
  Bor has `params.MergedTestChainConfig`, so the bor form is the same three
  lines with `cfg.AmsterdamBlock = new(big.Int)`.
- `core/vm/eip8037_test.go:47` — `cfg.AmsterdamTime = new(uint64)`, the same
  one-line conversion.

Restoring those two files (1359 lines of upstream test coverage) would unblock
all seven dropped EIP test files at once. It is deliberately **not** done here:
re-adding 1359 lines of upstream tests to a merge batch, then debugging any that
disagree with bor's diverged EVM, is precisely the scope creep the batch model
exists to prevent. Recorded as a single recommended follow-up in
`needs-wiring.md` rather than seven separate rows.

### Verification (invariant 8, per-batch tier)

- `go build ./...` exit 0.
- `go vet ./...` reports exactly the two known baseline findings —
  `core/parallel_state_processor.go:341` and `trie/secure_trie.go:88` lock
  copies. Nothing new.
- `gofmt` clean on every touched file. `go mod tidy` clean in the main module.
- Package tests, all ok: `params`, `consensus/misc/...`, `core`, `core/vm/...`,
  `core/rawdb/...`, `core/txpool/...`, `eth/...`, `eth/ethconfig`, `node/...`,
  `triedb/pathdb/...`, `tests/...`, `cmd/devp2p`.
- Two failing packages, both **re-run at the batch-1 tip (`300b300aa`) and
  reproduced there with identical assertion text**, so neither is batch 2's:
  - `cmd/devp2p/internal/ethtest` — the known nil-`BorConfig` panic in
    `params.(*BorConfig).CalculatePeriod` ← `miner.(*worker).newWorkLoop`.
  - `cmd/geth` — `TestAttachWelcome` (endpoint never opens, 3 subtests),
    `TestCustomGenesis`, `TestConsoleWelcome`, `TestCustomBackend`, `TestExport`.
    Both packages are invisible to CI, which filters `cmd/` out of `TESTALL`.

Correction to batch 1's verification note, which claimed `go mod tidy` clean in
`cmd/keeper`: it is not. `go mod tidy` there adds 25 `/go.mod` hash lines, and
without them `cmd/keeper` does not build standalone — **at the batch-1 tip as
well as here**, verified by building both. No commit in this range touches
`cmd/keeper`, so the tidy churn was reverted rather than folded into a merge
commit. Tracked in `needs-wiring.md`.

## Batch 3/6 (`76e3dc6b5`, plan row 3)

Range `f9417bb27..76e3dc6b5`, 20 first-parent commits. 22 conflicted paths (16
content, 6 modify/delete) plus **three further breaks git reported as clean
merges**, one of which would have disabled bor's stateless nodes.

### Consensus-critical and hardfork-surface resolutions

#### #35252 — chain reset head, and a stateless-node regression git called clean

Upstream decouples chain rewinding from state recovery in the path scheme:
`rewindPathHead` stops materializing state and only locates the head, and
`setHeadBeyondRoot` gains a single-shot recovery block after the rewind loop so
a deep rollback performs one fsync instead of one per block. It also exports
`stateRecoverable` as `StateRecoverable`.

Two conflicts, but the resolution that mattered was outside them. **Bor's
`setHeadBeyondRoot` carried a stateless-node exemption upstream does not have** —
`if !bc.cfg.Stateless && !bc.HasState(newHeadBlock.Root)`. Upstream deletes that
block from `updateFn` and adds its replacement further down the function, and
**the replacement auto-merged cleanly without the exemption**. Left alone, a
stateless bor node — which holds no state by design — would reach
`log.Crit("Chain is stateless at a non-genesis block")` on any rewind and exit
the process.

Bor's guard was re-applied at the new site rather than the old one, so stateless
nodes skip the recovery switch exactly as before and every other node gets
upstream's single-shot rollback. This is a preservation of existing bor
behaviour against an upstream restructure, not a change to it; the standing
witness/stateless rule is satisfied by keeping the semantics identical.

Bor's other divergence here disappeared with the code that carried it:
`rewindPathHead`'s recovery block used `log.Crit` where upstream reset to
genesis, and upstream deletes the block entirely.

#### #35293 — `AmsterdamTime` accessor, in bor's block-based form

Upstream adds `case fork == forks.Amsterdam: return c.AmsterdamTime` to
`ChainConfig.Timestamp`. Bor renamed that accessor to `Block(fork) *big.Int` and
converted every case to block fields, so the faithful port is
`return c.AmsterdamBlock`. Consistent with the standing note that bor's
Amsterdam is block-gated. The accessor currently has no callers in bor.

#### #35285 — EIP-7997 applied during block building

Upstream moves the EIP-7997 irregular state transition out of `Process` and into
`PreExecution`, so it also runs for block building, simulation and tracing, and
changes `PreExecution`'s `parent` parameter from `common.Hash` to
`*types.Header`. The bug is real and matters at activation: a builder that does
not insert the factory produces a block whose state root disagrees with every
validator that does.

Adopted in bor's shape — `isEIP7997Transition` deleted, the transition folded
into `PreExecution` behind bor's one-arg `IsAmsterdam(number)`,
`ProcessParentBlockHash` fed `parent.Hash()`, and all call sites updated.
`Process` already had the auto-merged `parent` lookup and nil check.

**One gap recorded rather than closed:** bor's miner does not call
`core.PreExecution` at all. `(*worker).prepareWork` inlines EIP-4788 and
EIP-2935 as separate steps and has no EIP-7997, so upstream's fix has no landing
site there. The conflicted region in `miner/worker.go` is empty on bor's side —
adding the transition would be authoring new consensus-path code, not resolving
a conflict — so it is left for the enablement work, alongside the v1.17.4 sync's
note that `consensus/bor` does not assert the EIP-7928/7843 header fields. Row
in `needs-wiring.md`.

### Fork/EIP scan

No new fork or EIP arrives in this batch. EIP-7997 (batch 2's row) gains its
block-building path; EIP-8038 (batch 2's row) gains an affordability guard via
#35261, which lands dormant. Both rows in `fork-register.md` are unchanged in
decision — still `defer`, still nil-gated.

### Remaining resolutions

| Path | Class | Decision |
| ---- | ----- | -------- |
| `core/types/bal/bal_encoding.go` | 2 | #35281 switches the account-ordering check to `isStrictlySortedFunc` so duplicate addresses are rejected, as EIP-7928 requires. **The helper it calls exists at the merge base but bor deleted it**, so taking upstream's one-line change alone would not have compiled. Adopted properly: bor's `Validate(rules params.Rules)` signature kept, strict check taken, and the eight-line helper restored with a comment explaining why a non-strict check is insufficient. |
| `accounts/manager.go` | 1 | #35133 rewrites `drop` around a map and `slices.DeleteFunc`; the old `sort.Search` returned the index of the next greater wallet when the target was absent, dropping the wrong entry. Bor's only divergence was a blank line, so upstream taken wholesale. `accounts/manager_test.go` auto-added. |
| `core/txpool/locals/journal.go` | 1 | #35300 nils the writer before checking `Close`'s error. The fix auto-merged; the conflict was bor's now-duplicate `journal.writer = nil` below it. Removed. |
| `rpc/types.go` | 1 | #35271 rejects a block parameter carrying neither number nor hash. Taken. |
| `cmd/geth/main.go`, `cmd/utils/flags.go` | 2 | #34975 replaces `ctx.IsSet` with `ctx.Bool` so `--flag=false` is respected rather than treated as presence. Sepolia's case auto-merged one line above bor's, so bor's Mumbai/Amoy/BorMainnet cases were converted too rather than leaving one switch mixing both semantics; these cases only log. `NoDiscoverFlag` taken as upstream. |
| `eth/tracers/api.go` | 2 | #35285's parent-header change taken. Upstream's accompanying `evm.Release()` was **not** taken: bor's `vm.EVM` has no `Release` method, so the line would not compile. The other four `PreExecution` call sites in this file auto-merged. |
| `internal/ethapi/simulate.go` | 1 | #35285 parent-header change; `parent` was already a `processBlock` parameter. |
| `miner/worker.go` | 3 | Kept ours — see the #35285 note above. |
| `eth/protocols/eth/{protocol,peer,handlers,handler_test}.go` | 3 | #35286 fixes the eth/71 `BlockAccessListsMsg` empty marker. **Bor carries no eth/71 BAL surface at all** — `RawBlockAccessList`, `BlockAccessListPacket`, `ReplyBlockAccessLists`, `RequestBALs`, `handleGetBlockAccessLists`, `serviceGetBlockAccessListsQuery` and `handleBlockAccessLists` are all absent. Re-decline of the existing eth/70+ and BAL deferrals. |
| 6 modify/delete paths | 3 | Keep-deleted. `eth/downloader/{downloader,syncmode}.go` were checked against the standing rule and are **comment-only** in this range, so there is no declaration or behaviour to port into `bor_downloader.go`. `eth/protocols/snap/syncv2{,_test}.go` (bor carries no snap/2), `miner/payload_building_test.go`, `core/vm/eip8038_test.go` (removed as a coverage gap in batch 2). |

### Bor-only twin scan

**A live-path finding, deferred by operator decision.** #35261 adds an
affordability check to upstream's Amsterdam SSTORE gas function, because a cold
access cost above the 2300 reentrancy sentry means the sentry alone no longer
establishes that the access can be paid for. Bor's `makeGasSStoreFuncPIP88`
meets the same precondition — `ColdSstoreCostPIP88` is 2940 — and still reads
committed state before resolving the cost. Unlike upstream's function, which is
dormant, bor's is reached through `newChicagoInstructionSet()` and Chicago is
active on both production networks.

Not changed here: it is a live EVM gas path, and the fix alters a transaction's
state read set and therefore witness composition, so it needs its own review and
tests. Recorded as a follow-up owed on top of this milestone in
`needs-wiring.md` and `plan.md`.

No other twin needed attention. The witness rule was not otherwise triggered:
nothing in this range touches `core/stateless/` or `eth/protocols/wit/`.

### Coverage gaps

| File | Blocked on |
| ---- | ---------- |
| `TestBlockAccessListsUnavailableDecode` (`eth/protocols/eth/handler_test.go`) | Auto-added by #35286 outside the conflict; references `BlockAccessListPacket` and `makeTestBAL`, both absent from bor. Removed — the third batch running in which declining a commit's conflicted half left its auto-merged half stranded. |

### Verification (invariant 8, per-batch tier)

- `go build ./...` exit 0 — after fixing one clean-merge break in
  `cmd/evm/internal/t8ntool/execution.go`, where #35285's t8n half arrived using
  upstream's two-arg `IsAmsterdam`.
- `go vet ./...` reports exactly the two known baseline findings
  (`core/parallel_state_processor.go:341`, `trie/secure_trie.go:88`).
- `gofmt` clean on every touched file; `go mod tidy` clean in the main module.
- Package tests, all ok: `accounts`, `params`, `rpc`, `core`, `core/types/bal`,
  `core/txpool/...`, `core/vm/...`, `eth`, `eth/protocols/...`,
  `eth/tracers/...`, `internal/ethapi/...`, `miner`, `triedb/...`,
  `cmd/utils`, `cmd/devp2p`.
- Three failing packages, each re-run at the batch-2 tip (`e55b0f517`) and
  reproduced there with an identical failure set, so none belongs to this batch:
  `cmd/evm` (`TestT8n`, `TestEVMTracing`, `TestEvmRun`, `TestEvmRunRegEx` —
  baselined explicitly because this batch edits `t8ntool/execution.go`),
  `cmd/geth` (same five as batch 2), and `cmd/devp2p/internal/ethtest` (the
  known nil-`BorConfig` panic). CI sees none of them; `TESTALL` filters `cmd/`.

## Batch 4/6 (`6e6fcef0b`, plan row 4) — BOGOTA

Range `76e3dc6b5..6e6fcef0b`, 20 first-parent commits. 32 conflicted paths (26
content, 6 modify/delete) — the largest of the sync — resolving to a **small**
diff (31 files, +275/−29), because four upstream commits were declined whole.

### Bogota (#34057) — the fork this batch exists for

Operator decision, taken before the batch: **treat Bogota exactly as Shanghai,
Cancun, Prague, Osaka and Amsterdam are treated in bor.** Upstream's Bogota is a
pure stub — config plumbing and an instruction-set alias, no behaviour.

Converted from upstream's timestamp form to bor's block form throughout:

| Surface | Landed as |
| ------- | --------- |
| `ChainConfig` | `BogotaBlock *big.Int` (`json:"bogotaBlock,omitempty"`) |
| Activation | `IsBogota(num *big.Int)`, chaining `IsLondon` — the post-London form used by `IsOsaka`/`IsAmsterdam` |
| `params.Rules` | `IsBogota`, populated from `c.IsBogota(num)` |
| `ChainConfig.Block(fork)` | `case fork == forks.Bogota` (bor's rename of upstream's `Timestamp(fork)`) |
| Fork ordering | `CheckConfigForkOrder` gains `bogotaBlock`, optional |
| Compatibility | `checkCompatible` gains a **block** entry — upstream's `isForkTimestampIncompatible(c.BogotaTime, …)` auto-merged and would not have compiled |
| Banner | block form, `#%-8v` |
| VM | `newBogotaInstructionSet()`, `bogotaInstructionSet`, and cases in `evm.go`, `contracts.go` (`ActivePrecompiles`) and `jump_table_export.go` |
| Gate value | **nil on all six surfaces** — both `params/config.go` presets, both `internal/cli/server/chains/*.go` runtime presets, both `builder/files/genesis-*.json` |

**Precompile delta versus the previous fork: empty, and verified rather than
assumed.** `newBogotaInstructionSet()` is `newOsakaInstructionSet()` with no
`enable*` calls, and both `activePrecompiledContracts` and `ActivePrecompiles`
return the Osaka tables. P256 is therefore retained (`PrecompiledContractsOsaka`
carries `0x0100: &p256Verify{eip7951: true}`), satisfying PIP-27. Bor's own
forks — Chicago, LisovoPro, Lisovo, MadhugiriPro, Madhugiri — are matched
**above** Bogota in all three switches, so on any Polygon network the bor fork's
set wins and Bogota can never displace it. Bogota sits immediately above
Amsterdam, preserving upstream's relative order.

Two bor guards fired and were worked, not silenced:

- **`TestReinforceMultiClientPreCompilesTest`** rejects any new `params.Rules`
  field until its 5-step checklist is done. Steps 1–2 are the precompile
  findings above; step 3 (Erigon parity) is vacuous while the gate is nil but
  must be revisited at enablement; step 4 needs no new multiclient e2e because
  no precompile is introduced. `IsBogota` was then added to the expected list.
- **`TestV2ForkParity`** requires every fork rule to be classified against the
  serial and parallel state processors. Bogota is `{inV1: false, inV2: false}`:
  it selects an instruction set in `core/vm` and carries no state-processor
  branch.

One deliberate deviation from upstream: upstream sets `BogotaTime: 0` on its
`AllDevChainProtocolChanges` preset. Bor's dev chain stops at Prague and never
enables Osaka, and Bogota's instruction set derives from Osaka, so enabling it
there would activate an Osaka-derived fork on a non-Osaka chain. Left nil.

### Four commits declined whole — and why partial declines were not available

The recurring lesson of this sync reached its sharpest form here. In three of
these four, an initial per-file decline left the commit's **auto-merged** half
stranded, and the toolchain caught it:

| Commit | Why declined | What the partial decline stranded |
| ------ | ------------ | --------------------------------- |
| **#35318** — EIP-2780/EIP-8037 spec changes | Operator decision: take it as its own stacked PR. It is a structural rework, not a spec tweak — `IntrinsicGas` drops `costPerStateByte` and returns `uint64` instead of `vm.GasCosts`, `preCheck` gains a `rules params.Rules` parameter, and the gas-budget reservation moves out of `buyGas`. **It also intersects live bor behaviour:** bor's EIP-7825 cap reads `!isAmsterdam && (isOsaka \|\| isMadhugiri)`, and upstream's replacement is `!rules.IsAmsterdam && rules.IsOsaka` — silently dropping `isMadhugiri`, which is live on mainnet. Entirely Amsterdam-gated, so declining is behaviour-neutral today. | — (declined before resolution) |
| **#34702** — protect high-value peers from random dropping | Builds on a `TxFetcher` shape bor does not have: upstream's `NewTxFetcher(chain, validateMeta, …)` versus bor's `NewTxFetcher(hasTx, …)`. Adopting would first require adopting the previously-declined `chain`/`validateMeta` refactor. | A whole new `eth/txtracker` package auto-added, and references to it bled into auto-merged parts of `eth/dropper.go` (7 sites) and `eth/handler.go`. Reverted the full footprint and removed the package. |
| **#35347 / #35348** — BAL in payload bodies v2; beacon/engine RLP | Both extend `ExecutableData.BlockAccessList`, a field bor removed with the declined BAL construction cluster. | `beacon/engine/types.go` kept an auto-merged `bal` reference plus now-unused `bytes` and `rlp` imports. |
| **#35335** — configurable engine-API max reorg depth | Not declined on its own merits — it shares `eth/catalyst/api.go` with the two above. Its accessor and enforcement had auto-merged, so keeping only the config plumbing would have shipped an `--engine.maxreorgdepth` flag wired to nothing: the "flag that lies" failure from batch 2, in a package bor does not use in production anyway. Declined with the group so the Engine API surface has one coherent story. | — |
| **#35283** — eels tests@v20.0.0 fixes | Test-harness update against a fixture release bor does not consume; bor's `tests/init.go` carries none of upstream's BPO config table. | `tests/gen_stenv.go` gained a `SlotNumber` accessor for a field bor's `stEnv` does not have. |

### Remaining resolutions

| Path | Class | Decision |
| ---- | ----- | -------- |
| `go.mod`, `go.sum`, `cmd/keeper/go.{mod,sum}` | 1 | #35336 bumps c-kzg-4844 v2.1.6 → **v2.1.8**. The main `go.mod` auto-merged to v2.1.8, so the keeper module was aligned to match. Bor's newer pins for the other shared deps (`secp256k1` v4.4.0, `dot` v1.9.2, `gods` v1.18.1) were kept, and the union's superseded upstream entries trimmed, so the keeper diff is exactly the ckzg swap. `go mod tidy` clean afterwards. |
| `params/protocol_params.go` | 2 | #35341 refreshes the EIP-8282 builder deposit/exit addresses and their bytecode for glamsterdam devnet-7. The two code blobs were copied verbatim from the upstream blob rather than retyped — a single wrong nibble would be a silent consensus bug. Dormant behind Amsterdam. |
| `core/vm/runtime/runtime.go` | 2 | #35301 caps the regular-gas budget at `MaxTxGas` under Amsterdam, spilling the remainder into the state reservoir, at all three `NewGasBudget` call sites. Independent of the declined #35318. |
| `core/state_transition.go` | 2 | #35342 charges the calldata floor when it exceeds regular gas (`max(gasUsedBeforeRefund-txStateGas, floorDataGas)`). Verified independent of #35318 — it is written against the pre-#35318 shape, which is bor's current shape — so it was re-applied after that file was reverted. |
| `eth/protocols/snap/handler.go` | 1 | #35289 discards the message **before** the size check, so an oversized message is still drained instead of being left in the stream. Taken. |
| `miner/worker.go` | 3 | #35295 caps a configured `MaxBlobsPerBlock` to the protocol limit. **No landing site:** bor's miner has neither the `maxBlobsPerBlock` method nor a `MaxBlobsPerBlock` config field, and reads `eip4844.MaxBlobsPerBlock` directly. Kept ours. |
| 6 modify/delete paths | 3 | Keep-deleted: `core/bintrie_witness_test.go`, `core/eip2780_test.go`, `core/eip8037_test.go`, `core/eip8038_test.go` (coverage gaps from batches 1–2), `eth/catalyst/witness.go` (bor deleted it; #34057's change there is only a `checkFork` argument), `eth/protocols/snap/syncv2.go` (bor carries no snap/2). |

### Bor-only twin scan

No twin needed changing. The PIP-88 SSTORE ordering item raised in batch 3
remains open and deferred to the post-milestone follow-up list; nothing in this
range touches it. The witness rule was not triggered — no commit here touches
`core/stateless/` or `eth/protocols/wit/`.

### Verification (invariant 8, per-batch tier)

- `go build ./...` exit 0. Two clean-merge breaks were fixed first: `bogotaTime`
  in `checkCompatible`, and a `go mod tidy` that had to be re-run after the
  #34702 revert restored `eth/handler.go`'s `siphash` dependency.
- `go vet ./...` reports exactly the two known baseline findings.
- `gofmt` clean; `go mod tidy` clean in the main module.
- Package tests, all ok: `params`, `core` (full), `core/vm/...`, `core/rawdb/...`,
  `eth/protocols/...`, `eth/tracers/...`, `miner`, `tests/...`, `p2p/dnsdisc`,
  `cmd/devp2p`, `accounts/abi/...`, `internal/testlog`.
- One failing package, signature-matched to the standing baseline:
  `cmd/devp2p/internal/ethtest`'s nil-`BorConfig` panic. Invisible to CI.

## Batch 5/6 (`81ab8b594`, plan row 5)

Range `6e6fcef0b..81ab8b594`, 20 first-parent commits. 41 conflicted paths (32
content, 9 modify/delete) resolving to 11 files, +181/−104 — the widest
conflict set of the sync and its smallest result, because one 47-file commit and
five dependents were declined together.

### #34047 sparse blobpool — declined, with its dependency chain

EIP-8070 Sparse Blobpool, and the single largest commit in the range. It
introduces **protocol version eth/72**, relaying blob *cells* rather than whole
blobs, adds `BlobBuffer`/`BlobFetcher`, a custody bitmap supplied by the
consensus layer through `forkchoiceUpdatedV4`, and `engine_getBlobsV4`.

Declined on two independent grounds, either of which is sufficient:

1. **Bor is on `[ETH69, ETH68]`.** It declined eth/70 (#33153) and eth/71, both
   with recorded rationale centred on `ExcludeStateSyncReceipt`. eth/72 cannot
   be adopted before those; this is the fourth deferral on the eth-protocol
   surface and the divergence is compounding.
2. **`core/txpool/blobpool/cache.go` does not exist in bor** — it arrived as a
   modify/delete here, having been declined earlier. #34047 extends it by 231
   lines.

The full 47-file footprint was reverted: 38 files back to bor HEAD, 9 removed
outright (`buffer.go`, `cache.go`, `conversion.go`, `custody_bitmap.go`,
`blob_fetcher.go` and their tests). Two further commits fall with it because
**their only files are ones #34047 creates** — #35387 (serialize legacy data
conversions, touching only `conversion{,_test}.go`) and #35393 (blob queueing
metric, touching only `blob_fetcher.go`).

### Bogota follow-up #35383 — corrects the batch-4 record

`newBogotaInstructionSet()` is rebased from `newOsakaInstructionSet()` to
`newAmsterdamInstructionSet()`. It auto-merged cleanly, and it is the right
correction: Bogota follows Amsterdam, so it should inherit Amsterdam's opcodes
(EIP-7843 SLOTNUM, EIP-8024, EIP-8037/8038) rather than skipping back to Osaka's.

**The batch-4 precompile finding is unaffected, and was re-verified rather than
assumed.** Bor's `activePrecompiledContracts` has no `IsAmsterdam` case at all,
so Amsterdam falls through to `IsOsaka` and returns `PrecompiledContractsOsaka`
— exactly what the explicit `IsBogota` case returns. Bogota and Amsterdam
therefore share a precompile set and the delta between them remains empty; only
the opcode basis moved. The `params.Rules` field list is unchanged, so
`TestReinforceMultiClientPreCompilesTest` does not re-fire.

### Other declines

| Commit | Why | What a partial decline would have stranded |
| ------ | --- | ------------------------------------------ |
| **#35372** — pass `TargetGasLimit` via engine API | Engine-API group, declined in batch 4; bor does not use it in production. | `beacon/engine/pa_codec.go` auto-merged the `p.TargetGasLimit` accessor while the field stayed declined in `types.go`. Caught by the compiler; full 7-file footprint reverted. |
| **#35369** — export chain with block-level access list | Adds a BAL to the on-disk export format (`exportBlock`, `makeExportBlock`), from the declined BAL cluster. | `cmd/utils/cmd.go` kept an unused `bal` import and two undefined symbols; `cmd/utils/export_test.go` referenced `makeExportBlock`. Both restored to bor HEAD. |
| **#35373** — preserve zero engine reorg depth | Builds directly on #35335's `EngineMaxReorgDepth`, declined in batch 4. | — |
| **#35378** — reuse the chain's jumpdest cache when building payloads | **No landing site:** bor's `BlockChain` has no `jumpDestCache` field for the new `JumpDestCache()` accessor to return, and the miner half is written against upstream's `generateParams`, which bor replaced with `getWorkReq`. | — |
| **#35396** — fix tracer panic | Edits `initRuntimeGasBudget`, a function introduced by the declined #35318. Falls with that deferral. | — |
| **#35364** — improve amsterdam fork test coverage | Its entire footprint is the EIP test files bor keeps deleted. It also adds a new one, `core/eip7708_test.go`, which references `senderKey` from `core/eip8037_test.go` — removed as a coverage gap, joining the same orphan family. | `core/eip7708_test.go` auto-added and failed to compile. |
| **#35363 / #34851** — gogc flag and GOGC tuning | **Bor-only twins already present.** Bor implements both independently: a `cache.gogc` flag with `GoGC` config field and `sanitizeGoGC` in its own CLI (`internal/cli/server/`), and the cache-derived heuristic `gogc := max(20, min(100, 100/(cache/1024)))` at `cmd/utils/flags.go:1797` — which is precisely what #34851 adds upstream. Adopting would give bor two competing GOGC knobs across two CLIs. | — |

### Adopted

| Path | Class | Decision |
| ---- | ----- | -------- |
| `eth/downloader/beaconsync.go` | 2 | #35402 stops advancing the snap-sync pivot once it has been committed. Adopted **in bor's shape**: bor's condition gains `!d.committed.Load()` but not upstream's `d.snapSyncer.FrozenPivot()`, which lives in the `downloader.go` bor renamed away. Bor already owns the field and uses the identical guard at `bor_downloader.go:2024`, so this aligns two paths that had drifted. The `syncModer.disableSnap()` half of the same commit is a re-decline — bor declined syncModer (#33157) and has no `eth/downloader/syncmode.go`. |
| `core/vm/jump_table.go` | 1 | #35383, above. |
| `core/tracing/hooks.go` | 1 | Upstream tightened the EIP-8037 gas-hook comments. Verified **comment-only**: filtering the diff for non-comment, non-blank lines yields nothing. |
| `triedb/pathdb/database.go` | 1 | #35400 reports nothing recoverable during state sync. |
| `core/rawdb/freezer_meta.go`, `cmd/devp2p/internal/ethtest/chain.go`, `.github/CODEOWNERS`, `.mailmap`, `eth/catalyst/simulated_beacon.go` | 1 | Small auto-merged fixes taken as-is. |
| 9 modify/delete paths | 3 | Keep-deleted: five `core/eip*_test.go` + `core/vm/eip8038_test.go` (coverage gaps), `core/txpool/blobpool/cache{,_test}.go`, and `eth/downloader/{downloader,syncmode}.go` — the latter two checked against the standing rule and confirmed to carry only `syncModer` changes bor cannot host. |

### Bor-only twin scan

Two twins found, both already present in bor and therefore requiring no change:
the GOGC pair above. The witness rule was not triggered.

### Verification (invariant 8, per-batch tier)

- `go build ./...` exit 0, after three revert rounds driven by the compiler.
- `go vet ./...` reports exactly the two known baseline findings.
- `gofmt` clean; `go mod tidy` clean in the main module.
- Package tests, all ok: `core` (full), `core/vm/...`, `core/tracing/...`,
  `core/rawdb/...`, `eth`, `eth/downloader/...`, `eth/catalyst/...`, `miner`,
  `triedb/pathdb/...`, `cmd/utils`, `cmd/devp2p`.
- One failing package, signature-matched to the standing baseline:
  `cmd/devp2p/internal/ethtest`'s nil-`BorConfig` panic.

## Batch 6/6 (`9621c6ad1`, plan row 6) — final batch, committed as `ae5cc2781`

Range `81ab8b594..9621c6ad1`, 10 first-parent commits. 18 conflicted paths (16
content, 2 modify/delete) resolving to 10 files, +43/−24. The smallest batch of
the sync, and the only one where the build was clean on the first attempt — no
revert rounds, and **no stranded auto-merged halves**: every file in the final
diff traces to a commit that was adopted deliberately.

The batch ships `version/version.go` at **`1.17.5` / `Meta = "stable"`**
(#35421), which is what makes this branch a v1.17.5 sync rather than an
unstable snapshot. That field auto-merged from the `"unstable"` value taken in
batch 1 and was deliberately left alone until this commit.

### #35404 state-prefetcher coordination — declined, port recorded

The one commit in this batch that needed real analysis. Upstream adds an
`execIndex *atomic.Int64` to `Prefetcher.Prefetch` and `Processor.Process`: the
serial processor publishes the index of the transaction it is currently
executing, prefetch workers skip anything the main pass has already reached, and
`prefetchOrder` promotes transactions above 1M gas to the front of the queue.

The prior batch's #35256 turned out to be upstream converging on bor's ordering,
so that was checked first here. **It is not the same case.** The motivation
converges; the mechanism does not, and bor cannot host it as written:

1. **Both interfaces are diverged in both directions, not merely by this
   parameter.** Bor's are
   `Process(block, statedb, cfg, author *common.Address, interruptCtx context.Context)`
   and
   `Prefetch(block, statedb, cfg, intermediateRootPrefetch bool, interrupt *atomic.Bool) *PrefetchResult`
   — no leading `ctx`, no `jumpDestCache` (declined with #35378), plus `author`,
   `interruptCtx` and a `*PrefetchResult` return upstream does not have. This is
   a design port, not a parameter addition.
2. **A scalar `execIndex` has no sound meaning on bor's production path.** Bor
   executes blocks with BlockSTM (`core/parallel_state_processor.go`), which runs
   transactions out of order and speculatively; there is no monotonic index below
   which work is known finished. Publishing one from the serial processor alone
   would hand the prefetcher a signal that is silently wrong on the path that
   actually matters.
3. **Bor's prefetcher is stream-based, and its workers do not carry block
   indices.** `PrefetchStream` pulls from a channel and assigns `txIdx` from an
   atomic counter — arrival order, not block position — so `execIndex.Load() >=
   int64(i)` cannot even be expressed. Its other caller feeds a transaction
   stream with no block to order at all, which also makes `prefetchOrder`'s
   whole-block sort inapplicable there.
4. **It is prefetch-adjacent and edits `core/blockchain.go`'s block-processing
   entry point.** Per the standing rule this is surfaced rather than resolved
   autonomously, even though the prefetcher itself works on a throwaway
   `statedb.Copy()` and does not feed witness composition.

All 7 files kept at bor HEAD; the batch's own `blockPrefetchTxsSkippedMeter`
went with it, since nothing else references it. Recorded in `needs-wiring.md`
with the port shape. The benefit is prefetch efficiency, not correctness.

### Adopted

| Path | Class | Decision |
| ---- | ----- | -------- |
| `core/block_validator.go` | 2 | **#35403 adopted in bor's shape.** Upstream parallelizes `ValidateState`: the derivable-field checks move into a new `validateResult` running on its own goroutine while `IntermediateRoot` is computed on the caller's, and the two errors are joined with the result error taking precedence. The restructure auto-merged; the conflict was only its tail, where upstream deletes the state-root check that bor's side had wrapped in `intermediateRootTimer`. Resolved by moving bor's instrumentation to the check's new site, so the metric still brackets exactly the `IntermediateRoot` call. `validateResult` drops upstream's `block` parameter, which only the declined Amsterdam BAL branch used — it returns with the BAL cluster. Error ordering is preserved: the receipt-root/requests-hash error still wins over a root mismatch, as before. |
| `eth/downloader/bor_downloader.go` | 2 | **#35405 applied to bor's twin.** Upstream's target `eth/downloader/downloader.go` does not exist here (renamed away), so the modify/delete was kept deleted — but bor's `commitPivotBlock` carries the identical defect at `bor_downloader.go:2210`: `d.committed.Store(true)` outside `pivotLock`, while every other pivot transition in the same file serializes through it. Bor's snap sync is live, and batch 5 adopted #35402's `!d.committed.Load()` guard, which is precisely the read side this ordering protects — so the twin was fixed rather than left drifting one release behind its own read path. The sole caller releases `pivotLock` before the call, so there is no reentrancy. `go test -race ./eth/downloader/...` clean. |
| `core/types/tx_blob.go` | 1 | **#35406's applicable half.** `encodedSize()` now adds the version byte for non-v0 sidecars. This is kept even though the blobpool half is declined, because it is not a stranded half: the addition is gated on `sc.Version != BlobSidecarVersion0`, and bor decodes both v0 and v1 sidecars, encoding v1 as `[tx, version, blobs, …]`. Bor's size was therefore under-counting v1 sidecars by the version byte; the fix makes `Size()` match what is actually encoded, and is inert for v0. |
| `node/jwt_handler.go`, `node/rpcstack_test.go` | 1 | #35408 — the bearer-prefix check was already case-insensitive, but the token was then stripped with a case-*sensitive* `TrimPrefix`, so a lowercase `bearer ` left the prefix in the token and the request failed. Sliced by length instead. The test moves that case from the reject list to the accept list. |
| `version/version.go` | 1 | #35421 — `1.17.5`, `Meta = "stable"`. |
| `go.mod`, `go.sum`, `cmd/keeper/go.{mod,sum}` | 1 | #35422 snappy bump, taken so this branch's dependency set is genuinely v1.17.5's. Worth flagging: upstream pinned an **unreleased pseudo-version** (`v1.0.1-0.20260716114414-9ae09f520e93`), not a tagged release. Kept bor's own neighbouring entries (`golang/mock`, `gofrs/flock v0.13.0` and the keeper module's extra requirements). `go mod tidy` clean afterwards in the main module. Trivially revertible if the team would rather stay on tagged `v1.0.0`. |

### Declines

| Commit | Why | Stranded half? |
| ------ | --- | -------------- |
| **#35399** — clear partial map when dropping last waitlist peer | Its only file is `eth/fetcher/blob_fetcher.go`, created by the declined #34047. Arrived as modify/delete; kept deleted. | None — single file. |
| **#35391** — allow reorg depth equal to `maxReorgDepth` | Engine-API group, declined since batch 4. Bor does not use the Engine API in production. **But see the twin note below** — this one is not purely inert. | None — single file. |
| **#35406** — blobpool half | Written entirely against the cell-sidecar shape bor does not have: `BlobTxForPool.CellSidecar`, `sidecar.Version`, and `txSizeWithoutBlob`'s eth/72 form. Bor's `blobTxForPool` still holds plain `Blobs` (v0), having declined #35191 and #34047. | No — the `tx_blob.go` half was independently applicable and was kept deliberately, not by omission. |
| **#35407** — skip memory-limit sanitize when total memory is unknown | **No landing site.** The guard it adds (`total > 0 &&`) is inside the `MemoryLimitFlag` block, and bor replaced this entire region of `setGoRuntimeSettings` with its own cache-derived GOGC heuristic — bor has no `MemoryLimitFlag` handling here at all. Same bor-only twin that caused #35363/#34851 to be declined in batch 5. | None — single file. |

### Bor-only twin scan

Three twins, and unusually they did **not** all resolve the same way — which is
the point of running the scan per batch rather than assuming the prior verdict:

- `commitPivotBlock` (`bor_downloader.go`) — same defect as upstream's, live
  path, **fixed** (above).
- `setGoRuntimeSettings` GOGC/memory-limit (`cmd/utils/flags.go`) — bor's
  replacement is deliberate and #35407 has nowhere to land, **declined**.
- `maxReorgDepth` (`eth/catalyst/api.go`) — bor has a package-level
  `maxReorgDepth = 32` const predating upstream's configurable field, and its
  check `depth >= maxReorgDepth` carries **the same off-by-one #35391 fixes**:
  a reorg of exactly 32 is rejected although the const is documented as "the
  maximum reorg depth accepted". Left at HEAD and recorded in
  `needs-wiring.md`. Changing it alters accept/reject behaviour at exactly
  depth 32 on a bor-authored constant — a bor decision, not something to
  smuggle in on an upstream merge, and the surrounding Engine API is unused in
  production so nothing is urgent.

The witness rule was not triggered: #35404 touches `core/blockchain.go` but was
declined whole, and no adopted change goes near witness generation,
propagation or import.

### Verification (invariant 8, per-batch tier)

- `go build ./...` exit 0 — first attempt, no revert rounds.
- `go vet ./...` reports exactly the two known baseline findings
  (`core/parallel_state_processor.go:341`, `trie/secure_trie.go:88`).
- `gofmt` clean; `go mod tidy` clean in the main module (it also validates the
  new snappy pseudo-version resolves).
- Package tests, all ok: `core` (full), `eth`, `core/types/...`, `node/...`,
  `eth/downloader/...`, `eth/catalyst/...`, `core/txpool/...`, `cmd/utils/...`,
  `version/...`.
- **Race detector**, run because this batch introduces a goroutine into
  consensus-critical `ValidateState` and changes lock ordering in the
  downloader: `go test -race ./core -run 'Validate|Insert|Chain|State'` and
  `go test -race ./eth/downloader/...` both clean.
- The three standing known-failing packages re-run and **signature-matched** to
  the baseline: `cmd/devp2p/internal/ethtest` (nil-`BorConfig` panic via
  `params.(*BorConfig).CalculatePeriod`), `cmd/geth` (`TestAttachWelcome`,
  `TestConsoleWelcome`, `TestExport`, `TestCustomBackend`, `TestCustomGenesis`),
  `cmd/evm` (`TestT8n`, `TestEvmRun`, `TestEVMTracing`, `TestEvmRunRegEx`). All
  invisible to CI, which excludes `cmd/`.
