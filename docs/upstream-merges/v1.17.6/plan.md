# Upstream merge plan: go-ethereum v1.17.6

| Field | Value |
| ----- | ----- |
| `TARGET` | `v1.17.6` (`3d84c6b2e`, upstream tag dated 2026-09-23) |
| `SYNC_BASE` | **`v1.17.5`**, a verified ancestor of `ppatil-upstream-v1.17.5` |
| `PROGRESS_TIP` | `3d84c6b2e` = tag `v1.17.6` (batch 7 committed as `b2cf70229`; batches 1–6: `9c049ad21`, `a7231d9b9`, `72ef11add`, `825f06e74`, `cb7d79d71`, `fd4e9b9f9`, `96dc79855`). All merge batches done; milestone chores pending. |
| Batch size | 20 first-parent commits |
| Range | `v1.17.5..v1.17.6`, **136 first-parent commits** (330 files, +26,286 / −15,019) |
| Milestones | 1 (`v1.17.6`); no intermediate release tag |
| Batches | 8 (7 at size 20, with batch 3 pre-split into 3a/3b; see below) |
| Base branch | **`upstream-merge-v1.17.4`** (reused; see deviation 1) |
| Working branch | `ppatil-upstream-v1.17.6` |
| Cut point | `ppatil-upstream-eip2780-8037` @ `c48b61d5c` (#2455, the #35318 port, stacked on #2354) |
| rerere | off (`-c rerere.enabled=false`), `-c merge.conflictStyle=zdiff3` |
| Trial-merge estimate | **173 conflicted files** cumulatively (stack tip against `v1.17.6`, `git merge-tree`) |

## Why v1.17.6 is in this effort

Operator decision (2026-09-28): the upstream sync merges into `develop` only
once the upstream code it carries has **scheduled the hard fork it builds
toward**. v1.17.6 schedules Amsterdam on Sepolia (#35734, `AmsterdamTime`
1791294816 = 2026-10-06). So v1.17.4, v1.17.5 and v1.17.6 go to `develop`
together. Which Amsterdam EIPs Bor enables, and when, is decided afterwards,
behind Bor's own block-based Amsterdam gate.

Two consequences shape this plan:

- **Lean towards adopting (dormant) over declining.** The point of carrying
  Amsterdam is to be able to switch it on later, and every decline has to be
  re-ported before it can be enabled. The adopt-vs-decline items below are
  each a decision to make explicitly, not a default to carry forward.
- **"Scheduled" here means Sepolia, not mainnet.** v1.17.6 itself changed
  Amsterdam gas parameters (#35454, #35497), so a later mainnet-scheduling
  release may change them again. If the rule is "scheduled on Ethereum
  mainnet", expect at least one more release in this effort before `develop`.

## Deviations from the skill defaults (deliberate, operator-directed)

1. **The base branch is not cut from `origin/develop`.** Same model as
   v1.17.5: every milestone PR merges into `upstream-merge-v1.17.4`, and that
   base goes to `develop` once, at the end. v1.17.6 stacks on the v1.17.5 tip.
   Any rename of the base must keep `upstream` in the name, or it loses bor
   ruleset 626146's `refs/heads/**upstream**` signed-commits exclusion.
2. **`SYNC_BASE` is `v1.17.5`, which is not in `develop`.** The fork point
   that matters is the tip of the in-flight stack. Ownership detection (§4.3)
   anchors on `v1.17.5`. For "did Bor ever diverge here?" questions that reach
   further back, also check against `v1.16.8`, the last tag in `develop`.
3. **Upstream cutoff is fixed at `v1.17.6`.** Commits after the tag on
   `upstream/master` are out of scope.

## Preconditions before batch 1

These gate the first merge. None of them is done by this plan-only pass.

1. **#35318 (EIP-2780 / EIP-8037 spec changes) must be in the cut point.** **Met:** ported in #2455 (`c48b61d5c`), and the working branch is cut from it; review changes to #2455 cascade into this branch like any other fix. It was
   deferred out of v1.17.5 with the recorded decision "take as its own stacked
   PR", owed on top of v1.17.5 (`docs/upstream-merges/v1.17.5/needs-wiring.md`).
   v1.17.6 builds directly on it: #35454 (2780/8038 parameters), #35457
   (regular gas renamed to execution gas), #35486 and #35497 (gas price
   parameters) all edit the post-#35318 shape. Merging batch 1 without it
   would mean resolving v1.17.6's changes against code Bor doesn't have.
   #35318 also drops the `isMadhugiri` arm of Bor's **live** EIP-7825 gas
   cap, so it needs its own review.
2. **Met 2026-09-28: stack 2332 unstacked.** #2354's GitHub stacked-PR state must be fixed before any v1.17.6 PR is
   opened. A PR stacked on #2354 joins native stack 2332, inherits the false
   "conflicts" flag and has its CI blocked. The recommendation stands:
   `gh stack unstack 2332` (with approval), or recreate #2354 outside the
   stack. Local batches can start before this; PRs can't.
3. **Cut-point guard.** `upstream-merge-v1.17.5-done` doesn't exist (only
   `-v1.16.9-done` and `-v1.17.2-done` do). v1.17.5's plan has no pending
   rows and `v1.17.5` is an ancestor of the cut point, so the substance is
   met. **Waived by the operator on 2026-09-28**, as for v1.17.3 and v1.17.4.

## Batch table

Conflict counts are from trial merges of the stack tip against each boundary
(`git merge-tree`), so they're estimates. The column is new conflicted files
relative to the previous boundary.

| # | Boundary | Commits | ≈ new conflicts | Theme | Risk |
| - | -------- | ------- | --------------- | ----- | ---- |
| 1 ✅ `9c049ad21` | `a235d2819` | 20 (1–20) | 38 (32 actual) | **#35264 parallel block execution with BAL**, **#35388 precompile result cache**, #35454 2780/8038 params, #35458 EIP-7997 update, #35457 regular→execution gas rename | **Highest decision density.** Two adopt-vs-decline decisions (A, B below) plus two hardfork-surface files. Needs precondition 1 |
| 2 | `7924511bf` | 20 (21–40) | 26 | **#35386 BAL downloading**, snap v2 catch-up pivot, #35471 ancient-BAL canonical check, #35486 and #35497 gas params, #35490 block-validation revert, #35473 cache keyed on input, #35520 state sizer deprecated | BAL download lands in `eth/downloader/downloader.go`, which Bor replaced with `bor_downloader.go` (C). #35497 is a hardfork-surface gas-parameter change |
| 3a | `6017c756e` | 6 (41–46) | ≈15 | **#35498 unset the block-level access list in `Finalise`**, #35509 blob pool init, #35524 blob-size validation, #35526 cache budget, #35544 `debug_executionWitness` error, kzg cells | **Witness-adjacent, operator-gated.** #35498 touches `core/stateless.go`, `core/block_validator.go` and `statedb.go` `Finalise` again; carry #2180's `currentBlockDestructs` into every `Finalise` path |
| 3b | `e991e6aaf` | 14 (47–60) | ≈41 | **#35514 "update for glam8"** (≈31 of the new conflicts, mostly `internal/ethapi` golden testdata), snap parallel state response, #35553 `eth_config`, #35551 freezer head truncation, #35070 `GetLogs` range | Mostly mechanical testdata. #35514's new code-existence check in `processRequestsSystemCall` is **inert on Bor**: every live caller gates on `Bor == nil` (see below) |
| 4 | `bbb9119ca` | 20 (61–80) | 24 | **#35537, #35593 (already ported, E)**, **#35581 EIP-7610 removal**, #35575 parallel-exec gas limit, #35367 blocked-tx size cap, #35591 Holesky removal, #35572 blob fetcher | Take upstream on the pre-ported pair and drop Bor's copy (upstream-shadowing, converges). #35591 edits `params/config.go` and `core/forkid` |
| 5 | `7538039f0` | 20 (81–100) | 14 | **#35588 fork validation in state transition**, **#35633 repay debt after child frame** (EIP-8037 gas), #35595 post-execution commit, eth/72 tests, snap storage-range abort | #35588 must not reject Bor's own transaction types (state-sync) before a fork. #35633 is in `core/vm/gascosts.go`; inert while `StateGas == 0` |
| 6 | `83e67ae63` | 20 (101–120) | 10 | **#35646 gas tracer and gas measurement rework**, #35681 jumpdest cache budget, ethclient/ethapi fixes, metrics | **#35646 touches `core/vm/operations_acl.go`**, where the PIP-88 twins live. Run the twin scan |
| 7 | `3d84c6b2e` | 16 (121–136) | 5 | **#35734 Amsterdam scheduled on Sepolia**, #35680 downloader scheduling rework, snap v2 optimisations, #35719 witness fork gate (catalyst), #35738 receipts hashed alongside execution, #35753 flag to disable the precompile cache, #35754 otel 1.46, version bump | **Hardfork surface.** #35734 sets `AmsterdamTime` on Sepolia; Bor has no timestamp Amsterdam field, so convert or decline, and confirm every Bor preset stays nil. #35753 needs CLI wiring into `internal/cli/server` |

Batch 3 is pre-split under §5: at 56 new conflicted files it exceeds the ~40
threshold, and #35514 alone accounts for 31. Batch 1, at 38, is close; split
it at #35264 if the class-3 count climbs past ~10.

## Adopt-vs-decline decisions

Each needs an explicit decision, recorded in the ledger and fork register.
The lean, per the operator's direction, is **adopt dormant** unless adopting
breaks a live Bor path.

**A. Parallel block execution with BAL (#35264, plus #35441, #35443, #35459,
#35461, #35465, #35575, #35693), batch 1 onward.**
Upstream's own parallel executor, in a new `core/state_processor_parallel.go`
(no name clash with Bor's `core/parallel_state_processor.go`). It's selected
per block by `BlockChain.useBALExecution`, only for Amsterdam blocks that
carry an access list, so it's dormant on Bor.
- *Lean:* adopt dormant alongside BlockSTM. Bor keeps BlockSTM as its path.
  Upstream's `blockchain.go` dispatch (+140 lines) has to be merged with Bor's
  BlockSTM selection so that neither path is taken by accident.
- *Open question for the team:* in the long run, does Bor build BALs inside
  BlockSTM (POS-3737), switch to upstream's executor post-Amsterdam, or run
  both? Not needed to merge; needed before enabling Amsterdam.

**B. Precompile result cache (#35388, #35473, #35526, #35578, #35753),
batches 1–7.**
A general, address-keyed result cache wired through `RunPrecompiledContract`
and `EVM.SetPrecompileCache`. Unlike A, this isn't fork-gated. It runs on
every block once it's set, so it's live-path code, even if correct caching
is consensus-neutral. It overlaps Bor's own ecrecover cache
(`EcrecoverCache`, `runEcrecoverWithCache` in `core/vm/evm.go`).
- *Options:* (i) adopt upstream's cache and retire Bor's ecrecover cache;
  (ii) adopt it but leave it disabled on Bor (#35753's flag) and keep Bor's;
  (iii) decline.
- *Lean:* (ii) for this merge, since it keeps behaviour identical, then
  benchmark (i) as its own change. Touches `core/stateless.go` and
  `core/vm/contracts.go` (hardfork-surface file), so it's documented in full.
- #35753's flag needs wiring into Bor's `internal/cli/server` config (a
  milestone triage item).

**C. BAL downloading and snap/2 (#35386, #35316, #35463, #35477, #35533,
#35299, #35722, #35705, #35680, #35462, #35515), batches 2–7.**
Snap/2 was declined four times. The reason is the landing site: Bor replaced
`eth/downloader/downloader.go` with `bor_downloader.go` (≈2,570 lines against
upstream's ≈1,250), so these commits arrive as modify/delete conflicts.
- *Lean:* this is the decision the Amsterdam direction makes hardest to
  defer again. BAL downloading is how a syncing node gets the access lists
  that Amsterdam blocks carry. Adopting means porting Bor's downloader onto
  upstream's snap/2 state machine, which is its own branch and PR, not a batch
  resolution.
- *Proposal:* decline in the batches (recorded in needs-wiring, as before),
  and open a dedicated snap/2 + BAL-download porting effort as a precondition
  of enabling Amsterdam. That keeps the batches mergeable without pretending
  the port is small.

**D. eth/71 (EIP-8159) and eth/72 (sparse blobpool).**
In this range they're mostly tests (#35389, #35368, #35647) and eth/72
blob-serving tweaks (#35543, #35589, #35524, #35572). The protocol code was
declined in v1.17.4/v1.17.5.
- *Lean:* keep declined here; tests for undeclared protocols go to
  needs-wiring as coverage gaps. Revisit with the protocol backlog, which is
  now four deferrals deep.

**E. Already in the stack, so expect upstream-shadowing conflicts:**
- #35537 (`c57bbe417`) and #35593 (`596096afe`), batch 4: ported by hand in
  #2341 (`a2a83b9dd`). Take upstream and drop Bor's copy where it converges.
  Keep Bor's additions that upstream doesn't have: the eth/70 advertisement
  (`ProtocolVersions = [ETH69, ETH68]`), the 64-byte packet-envelope reserve,
  and `bor_fetchers_concurrent.go`'s round-trip scaling.
- #35581 EIP-7610 removal (`1aea939a5`), batch 4: #2342 argued the EIP-7610
  rework is a no-op for Bor's chain IDs. Verify that removing it changes no Bor
  path before taking it.

## Checks that must run, by batch

- **Witness-path files (operator-gated, surface don't resolve):** #35388
  (`core/stateless.go`), #35498 (`core/stateless.go`, `block_validator.go`,
  `statedb.go`), #35514 (`eth/catalyst/witness.go`, upstream's engine-API
  witness, which Bor doesn't use), #35719, #35544. Both #2180 divergences
  that must survive every merge: `addObjectWitness`'s
  `else if s.prefetcher == nil` branch and `NewWitness`'s `types.CopyHeader`.
  Run `TestV2WitnessRegeneration{AllBlocks,PipelinedSRCAllBlocks,PipelinedSRCChained}`
  non-short on every batch that touches `core/state` (baseline: Chained 222/222).
- **Twin paths:** v1.17.5 split `Finalise` into `Finalise` and
  `finaliseAmsterdam`. #35498 changes it again, so carry `currentBlockDestructs`
  into every path. The PIP-88 twins in `core/vm/operations_acl.go` (#35646,
  batch 6) and `core/vm/contracts.go` (#35388, batch 1) need the lockstep diff.
- **Hardfork surfaces:** `params/config.go` (#35454, #35458, #35486, #35497,
  #35423, #35591, #35734), `params/protocol_params.go`,
  `core/vm/contracts.go` / `jump_table.go` (#35388, #35457, #35473),
  `core/forkid` (#35591, #35734). Every Bor preset, both
  `internal/cli/server/chains/*.go` and both `builder/files/genesis-*.json`
  must stay nil for Amsterdam and Bogota, and the fork meta-guards
  (`TestV2ForkParity`, `TestReinforceMultiClientPreCompilesTest`) must pass.
- **Already inert on Bor, confirmed here:** #35514's `GetCodeSize` check in
  `processRequestsSystemCall`. Every live caller is gated on `Bor == nil`:
  `core/state_processor.go:136` (serial), `miner/worker.go` (block building)
  and `internal/ethapi/simulate.go` (`eth_simulateV1`). `PostExecution`'s
  ungated form is reached only from `cmd/evm`, `chain_makers.go` and the
  simulator's non-Bor branch.
- **Live-path gas:** #35633 and #35646 edit `core/vm/gascosts.go` and the
  interpreter. Show equivalence at `StateGas == 0`, the same argument used for
  EIP-8037 in v1.17.4, and replay mainnet blocks through `TestV2BlockSTMAllBlocks`.
- **Dependencies:** #35754 moves otel to 1.46 (develop is at 1.45); #35568
  cockroachdb/swiss; #35573 and #35656 move the Go floor and `-dlgo` to 1.27.1.
  Take the higher version per module, then `go mod tidy`. Check Bor's CI Go
  version before taking the Go floor.

## Milestone gate (after batch 7)

Same as v1.17.5: upstream-PR triage (wiring pass, including #35753's CLI
flag), chores batch, then the full suite. That's `make test` (baseline 144
packages / 0 failures), lint, the non-short witness tests, `tests/bor`, the
kurtosis devnets (standard and stateless-witness), diffguard, govulncheck, and
a mainnet replay through `TestV2BlockSTMAllBlocks`.
