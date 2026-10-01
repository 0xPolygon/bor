# Upstream merge plan: go-ethereum v1.17.5

| Field | Value |
| ----- | ----- |
| `TARGET` | `v1.17.5` (`9621c6ad1`, upstream tag dated 2026-07-27) |
| `SYNC_BASE` | **`v1.17.4`** — verified ancestor of `ppatil-upstream-v1.17.4` |
| `PROGRESS_TIP` | `9621c6ad1` = **`v1.17.5`, target reached** (batch 6 committed as `ae5cc2781`) |
| Batch size | 20 first-parent commits |
| Range | `v1.17.4..v1.17.5`, **110 first-parent commits** |
| Milestones | 1 (`v1.17.5`) |
| Batches | 6 |
| Base branch | **`upstream-merge-v1.17.4`** (reused — see deviation below) |
| Working branch | `ppatil-upstream-v1.17.5` |
| Cut point | `ppatil-upstream-v1.17.4` @ `f399939ba` |
| rerere | off (`-c rerere.enabled=false`), `-c merge.conflictStyle=zdiff3` |

## Deviations from the skill defaults (deliberate, operator-directed)

1. **The base branch is not cut from `origin/develop`.** §3.1 would create
   `upstream-merge-v1.17.5` from develop. The release model for this effort is
   that v1.17.4 and v1.17.5 ship as a *single* stable release: every milestone
   PR merges into `upstream-merge-v1.17.4`, and that base goes to `develop`
   once, at the end. So v1.17.5 stacks on the v1.17.4 tip and drains into the
   same base. The base branch keeps its v1.17.4 name for now; it is renamed to
   `upstream-merge-v1.17.5` when the base → develop PR is opened. Any rename
   must keep `upstream` in the name or it loses bor ruleset 626146's
   `refs/heads/**upstream**` signed-commits exclusion.
2. **`SYNC_BASE` is `v1.17.4`, which is not in `develop`.** §2's computation
   looks for the highest release tag that is an ancestor of `develop`; here the
   fork point that matters is the tip of the in-flight stack, not develop.
   `v1.17.4` is confirmed an ancestor of `ppatil-upstream-v1.17.4`. Ownership
   detection (§4.3) anchors on `v1.17.4` accordingly.
3. **Upstream cutoff is fixed at `v1.17.5`.** There are already ~28 further
   commits on `upstream/master`. They are out of scope for this release; a
   later sync picks them up.

## Cut-point guard — one unmet precondition

§4.1 requires `upstream-merge-<N-1>-done` to be an ancestor of the cut point.
**That tag does not exist.** Only `upstream-merge-v1.16.9-done` and
`upstream-merge-v1.17.2-done` were ever created; the v1.17.3 and v1.17.4
milestones were completed without tagging.

The guard's *substance* is satisfied — v1.17.4's plan has zero pending rows, all
six upstream release tags (v1.16.9 → v1.17.4) are ancestors of
`ppatil-upstream-v1.17.4`, the milestone verification suite passed, and the
milestone PR (#2346) is open. The tag is simply missing.

Recommended before batch 1: create `upstream-merge-v1.17.4-done` at `f399939ba`
(local, needs its own operator approval per invariant 4 — tagging is never
automatic). Batch 1 should not start until this is either created or explicitly
waived.

## Batch table

| # | Milestone | Boundary | Commits | Theme | Risk |
| - | --------- | -------- | ------- | ----- | ---- |
| 1 | v1.17.5 | `04bf04530` | 20 (1–20) | pebble v2; EIP-8037 spec change; EIP-7954 max-code-size bump; EIP-8282 builder execution requests; EIP-2780; EIP-8246; state-prefetch fix for absent accounts | **highest decision density in the range.** Five EIPs each need a fork-register row; #35256 is witness-motivated (surface, don't resolve). **#34009 is a real porting job, not a take-theirs** — see below |
| 2 | v1.17.5 | `f9417bb27` | 20 (21–40) | EIP-7928 spec change; bpo schedule named-hardfork removal; amsterdam override flag; EIP-8038; EIP-7997 | BAL re-declines begin; `consensus/misc` moves |
| 3 | v1.17.5 | `76e3dc6b5` | 20 (41–60) | BAL validation; glamsterdam spec fixes; `AmsterdamTime` from `ChainConfig.Timestamp`; chain reset head | #35293 returns a **timestamp-based** accessor — bor deleted the timestamp fields and gates on blocks |
| 4 | v1.17.5 | `6e6fcef0b` | 20 (61–80) | snap access-list sync fix; Amsterdam gas-budget cap; BAL in payload bodies v2; EIP-2780/8037 follow-ups; **`all: add bogota fork to config` (#34057)** | **Bogota fork lands here.** Full invariant-6 + invariant-9 treatment; the fork-gating convention must be decided before this batch |
| 5 | v1.17.5 | `81ab8b594` | 20 (81–100) | bogota in `payloadVersion`; **sparse blobpool (#34047, 47 files)**; bogota instructions from Amsterdam; blobpool legacy serialization; amsterdam fork test coverage | Sparse blobpool is the largest commit in the range and builds on the declined `blobpool/cache.go`; likely another decline but a real decision |
| 6 | v1.17.5 | `9621c6ad1` | 10 (101–110) | state-prefetcher coordination with block processing; `version: release v1.17.5` | `core/state_prefetcher.go` is bor's `PrefetchStream` (pipelined-SRC) — real porting, not resolution. Version bump to v1.17.5 lands here |

Status: **all six batches done — `300b300aa`, `e55b0f517`, `ca82e548b`,
`f2de60ed7`, `1fb978c83`, `ae5cc2781`. 110 of 110 commits merged; the upstream
`v1.17.5` tag (`9621c6ad1`) is an ancestor of the branch tip, so the sync target
is reached.** Every commit is GPG-signed (`G`).

What remains is not merge work:

1. **Milestone close-out** — the chores commit that finally tracks
   `docs/upstream-merges/v1.17.5/`, which has been deliberately untracked
   throughout so no batch commit carried doc churn.
2. **The ordered follow-ups** below, none of which block this release.
3. **Review of the eleven v1.17.4 PRs**, which is the long pole for the whole
   effort and is unchanged by this batch — see the release-model deviation
   above: v1.17.4 and v1.17.5 ship as one stable release, so nothing here
   reaches `develop` until that base branch does.

Batch 1 outcome: 30 conflicts (22 content, 8 modify/delete) plus eleven further
breaks that git reported as clean merges. #35190 adopted as a unit on operator
decision; #35191 likewise, after an initial partial decline proved
self-inconsistent. EIP-8282 wired dormant in bor's shape; #34978 declined
wholesale. Four upstream EIP test files removed as coverage gaps. Full detail in
`ledger.md`.

Batch 2 outcome: 24 conflicts (18 content, 6 modify/delete) plus five further
breaks git reported as clean merges. Two commits dominated and both were
adopted **as units** because declining would have stranded an already
auto-merged half: #35029 (blob schedule — named forks no longer carry their
own `BlobConfig`; behaviour-neutral for bor because its Osaka blob values were
byte-identical to Prague's) and #35213 (`--override.amsterdam` — the flag
itself had auto-merged into `cmd/utils/flags.go` and `cmd/geth/main.go`, so a
decline would have shipped a flag that parses and silently does nothing).
Three EIPs merged dormant: EIP-7997, EIP-8038, and a re-decline of the
EIP-7928 BAL change. Three more upstream EIP test files dropped — but they
are orphaned by batch 1's decline chain, not by anything of their own, and the
whole seven-file gap unblocks behind two one-line edits (see `needs-wiring.md`).
Full detail in `ledger.md`.

Batch 3 outcome: 22 conflicts (16 content, 6 modify/delete) plus three breaks
git reported as clean merges. The one that mattered was **not** in a conflict:
#35252 relocates the chain-rewind state check, and its replacement auto-merged
without bor's `!bc.cfg.Stateless` exemption — left alone, a stateless bor node
would have hit `log.Crit` on any rewind and exited. The guard was re-applied at
the new site. #35285 (EIP-7997 during block building) was adopted in bor's
block-gated shape, with one gap recorded: bor's miner does not call
`core.PreExecution` at all. #35286 (eth/71 BAL) re-declined. A live-path finding
in bor's PIP-88 SSTORE twin was surfaced and deferred to the follow-up list
above by operator decision. Full detail in `ledger.md`.

Batch 4 outcome: 32 conflicts (26 content, 6 modify/delete) — the largest of the
sync — resolving to the **smallest** diff so far (31 files, +275/−29), because
four upstream commits were declined whole. **Bogota landed** in bor's
block-based form, nil on all six surfaces, with an empty and verified precompile
delta; both bor guards (`TestReinforceMultiClientPreCompilesTest`,
`TestV2ForkParity`) were worked rather than silenced. Declined: **#35318** by
operator decision (structural rework that drops bor's live Madhugiri arm of the
EIP-7825 cap — now the top follow-up), **#34702** (built on a TxFetcher shape
bor lacks), the **Engine-API group** #35347/#35348/#35335, and **#35283**.
Three of those four had already stranded auto-merged halves when declined
per-file, and were reverted whole instead. Full detail in `ledger.md`.

Batch 5 outcome: 41 conflicts (32 content, 9 modify/delete) — the widest set of
the sync — resolving to its smallest result, 11 files. **#34047 sparse blobpool
declined**: it introduces eth/72, which bor cannot host while eth/70 and eth/71
remain deferred, and extends a `blobpool/cache.go` bor does not have. Two
commits fall with it because their only files are ones it creates. Six further
commits declined as consequences of earlier decisions, plus a **GOGC pair that
bor already implements independently in its own CLI**. Adopted: the snap-sync
pivot-commitment guard, in bor's shape. **#35383 corrects Bogota's instruction
set to derive from Amsterdam rather than Osaka**; the batch-4 precompile finding
was re-verified against that and still holds. Full detail in `ledger.md`.

Batch 6 outcome (final): 18 conflicts (16 content, 2 modify/delete) resolving to
10 files, +43/−24 — the smallest batch, and the only one to build on the first
attempt with **no stranded auto-merged halves**. Ships `version/version.go` at
`1.17.5` / `stable`. **#35404 state-prefetcher coordination declined**: it was
checked first against the batch-1 #35256 pattern (upstream converging on bor)
and is not that case — both interfaces are diverged in both directions, a scalar
`execIndex` has no sound meaning under BlockSTM's out-of-order execution, and
bor's stream prefetcher carries arrival-order rather than block indices; port
recorded in `needs-wiring.md`. Adopted: **#35403** parallel block validation in
bor's shape, with the `intermediateRootTimer` moved to the root check's new
site; **#35405 applied to bor's `bor_downloader.go` twin**, which had the same
unlocked `committed.Store` on a live snap-sync path; #35406's version-conditional
`encodedSize` half; #35408's bearer-token case fix; and the snappy bump — for
which upstream pinned an unreleased pseudo-version, flagged rather than
smoothed over. The twin scan resolved three twins **three different ways**, and
one of them (`maxReorgDepth`'s off-by-one) is recorded as a bor decision rather
than taken silently. Race detector run on both the new goroutine in
`ValidateState` and the changed downloader lock ordering. Full detail in
`ledger.md`.

## Decision deadlines

| Decision | Due before | Why it stalls the batch |
| -------- | ---------- | ----------------------- |
| ~~**pebble v2 route**~~ — **DECIDED (operator, before batch 1): #34009 lands through this merge.** `psp-pebblev2` is abandoned and will be deleted; its commit `98237973d` is disregarded entirely, so there is no duplicate-landing risk and no reconciliation to do. Batch 1 adopts upstream's pebble v2 as a normal adoption, including the `cockroachdb/pebble/v2 v2.1.4` go.mod swap. | ~~batch 1~~ resolved | — |
| ~~**Bogota fork-gating convention**~~ — **DECIDED (operator, before batch 4): treat Bogota exactly as Shanghai / Cancun / Prague / Osaka / Amsterdam are treated.** Block-based `BogotaBlock *big.Int`, `IsBogota` chaining `IsLondon`, `Rules.IsBogota`, gate **nil on all six surfaces** (both params presets, both runtime presets, both packaged genesis files), wired dormant, explicit precompile delta against `core/vm/contracts_continuity_test.go`. Full shape table in `fork-register.md`. | ~~batch 4~~ resolved | — |

## Follow-ups owed on top of this milestone

Work found during the sync that is deliberately **not** being done inside a
merge batch, and must land after it. Detail and wiring steps in
`needs-wiring.md`.

| # | Follow-up | Found in | Why it waits, and what it depends on |
| - | --------- | -------- | ------------------------------------ |
| 1 | Adopt EIP-2780 / EIP-8037 spec changes (#35318) | batch 4 | Structural rework of the intrinsic-gas and gas-budget flow, not a spec tweak, and it silently drops the Madhugiri arm of bor's live EIP-7825 gas cap. Dormant either way, so it waits for its own review against the four EIP spec diffs upstream cites. **Must precede #2** — see the ordering note below. |
| 2 | Restore `core/eip8037_test.go` + `core/vm/eip8037_test.go`, then the five orphaned EIP test files | batch 2 | **Blocked on #1.** Upstream rewrote these tests as part of #35318, so at the v1.17.5 tip they are written against the post-#35318 shape. Restoring them first would mean restoring tests for a shape bor does not have. Done after #1 they become that PR's verification rather than a separate chore, which is a reason to consider merging the two. |
| 3 | Align `makeGasSStoreFuncPIP88` with upstream's post-#35261 ordering | batch 3 | **Independent of #1 and #2** — live EVM gas path (PIP-88 is enabled via Chicago, active on both production networks), unrelated to Amsterdam. Outcome-neutral by construction, but it changes a transaction's state read set, so it needs its own review, boundary tests in the 2300–2940 window, and explicit witness sign-off. Can be scheduled at any time. |
| 4 | `go mod tidy` in `cmd/keeper` | batch 2 | **Independent.** Pre-existing; module does not build standalone. Wants its own small, reviewable commit rather than being buried in a merge. |

### Ordering note: #35318 must land before the EIP tests

Recorded because the naive order is the wrong one. #35318 does not merely change
the code the dropped tests cover — it **rewrites those tests**: +384 lines in
`core/eip8037_test.go`, +643 in `core/eip2780_test.go`, +117 in
`core/eip8038_test.go`. Upstream's current versions therefore target the
post-#35318 `IntrinsicGas` / `preCheck` / gas-budget shape.

Re-verified at the v1.17.5 tip rather than carried over from the batch-2 note:
the two blocking files are still gated on exactly one line each —
`core/eip8037_test.go:41` (`cfg8037 = balChainConfig()`) and
`core/vm/eip8037_test.go:47` (`cfg.AmsterdamTime = new(uint64)`) — but the file
is now 970 lines rather than the 614 assessed at batch 2.

### Which release these ride is a separate question

Everything in #1 and #2 sits behind a nil Amsterdam gate and changes no
behaviour today, so neither has to be in this stable release. The hard
constraint is that they land **before Amsterdam is ever enabled**, together with
the other Amsterdam-enable items accumulated in `needs-wiring.md`: bor's miner
not applying EIP-7997, and `LatestFork` / the startup banner / `checkCompatible`
omitting Amsterdam. #3 and #4 are unrelated to Amsterdam and need not wait for
any of it.

## Batch 1 — pebble v2 (#34009) is a port, not an adoption

Recorded before starting, because the cost is easy to underestimate from the
commit subject.

Upstream keeps the old implementation as a new `ethdb/pebble/pebble_v1.go`
(753 lines), adds `version.go` (146) for on-disk version detection, and
restructures `pebble.go` into the v2 implementation (68 lines changed). It also
swaps `cockroachdb/pebble` for `cockroachdb/pebble/v2 v2.1.4` in `go.mod`, and
touches `core/rawdb/database.go`, `node/database.go`, `cmd/geth/dbcmd.go` and
`cmd/keeper/go.sum`.

Bor's `pebble.go` is heavily diverged — 163 metric/tuning references. The
Bor-authored work in that file is:

- `393dd212f` — `ethdb, triedb: tune pebble write path and add safe pathdb state carry-over (#2170)`
- `fdd8b3e95` — custom metrics with amplification + space logic
- `00798f0fa`, `608fb1f85` — additional pebbledb read/write metrics

That includes a bespoke amplification suite (`readAmpGauge`,
`levelWriteAmpGauge`, `totalWriteAmpGauge`, `calcWriteAmpGauge`,
`calcSpaceAmpGauge`, `walWriteAmpGauge`) with no upstream equivalent.

**The decision the batch must make explicitly:** where Bor's instrumentation and
write-path tuning live once upstream splits v1 and v2 — the v2 path only, both
paths, or v1 only. Existing databases stay v1 until migrated, so "v2 only"
silently drops observability from every node that has not migrated. Taking
upstream's `pebble.go` wholesale drops all of it. This is a class-1 resolution
with a real behavioural consequence for operators and must be documented in
full, not resolved as churn.

## Carried context from the v1.17.4 sync

- **Witness rule is in force.** Never alter bor's witness generation /
  propagation / import autonomously. #35256 and #35404 both touch prefetch and
  state-reader paths adjacent to it; surface, do not resolve.
- **Recurring declines** re-touched by this range — snap/2, BAL construction,
  `blobpool/cache.go`, `eth/catalyst/witness.go`, EraE, miner stress harness,
  `core/bintrie_witness_test.go`, and the `eth/downloader` rename. All map to
  existing `needs-wiring.md` rows; each is a re-decline, not a new decision.
- **`consensus/` is untouched by this range** — no `consensus.Engine`,
  `Finalize`, or `FinalizeAndAssemble` change. This is the structural reason
  v1.17.5 is materially lighter than v1.17.4 despite a similar commit count.
- **diffguard's `Quality metrics` job is broken for stacked PRs** —
  `--base origin/${{ github.base_ref }}` resolves to the stack bottom, so it
  analyses the whole sync and the runner kills it at 45 min (exit 143). The
  v1.17.5 PRs inherit this unless the workflow is fixed.
