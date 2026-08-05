# Upstream merge plan: go-ethereum v1.17.4

| Field           | Value                                                        |
| --------------- | ------------------------------------------------------------ |
| Target          | `v1.17.4` (`36a7dc72e96b3f42846be925cfeb2fad18489917`)        |
| SYNC_BASE       | `v1.16.8` (`abeb78c647e354ed922726a1d719ac7bc64a07e2`) — highest upstream release tag that is an ancestor of `develop`. Persisted; never recompute on resume. |
| Batch size      | 20 first-parent commits                                       |
| Upstream remote | `upstream` (https://github.com/ethereum/go-ethereum.git)      |
| Base branch     | `upstream-merge-v1.17.4` (cut from `origin/develop`). Slash-free on purpose: bor ruleset 626146 excludes `refs/heads/**upstream**` from the signed-commit requirement, and `**` does not cross `/` — slashed names stay rule-covered and reject unsigned upstream commits. Working branches follow the same constraint (`<username>-upstream-<milestone>`). |
| Planned         | 2026-07-06 (plan-only; no merges performed)                   |
| Totals          | 607 first-parent commits, 6 milestones, 33 batches            |
| Merge status    | **Complete — all 33 batches merged (2026-08-05).** Branch tip `f397229ee`; `git merge-base --is-ancestor v1.17.4 HEAD` passes and all six milestone tags (`v1.16.9`, `v1.17.0`, `v1.17.1`, `v1.17.2`, `v1.17.3`, `v1.17.4`) are ancestors of the branch. Remaining before the milestone PR: the verification suite (full tests, lint, two kurtosis devnets, diffguard with mutation, govulncheck). |

Notes:

- `v1.16.9` is not integrated into `develop` (it lives on upstream `release/1.16`), so it is the first milestone. Expect already-applied no-op conflicts when crossing between release branches (its two crypto fixes reappear on the v1.17.0 line as `c974722dc` / `9b78f45e3`).
- Already-integrated guard: none of the six milestone tags is an ancestor of `origin/develop` (checked 2026-07-06); all six stand.
- Boundary SHAs are upstream first-parent commits (trees upstream CI passed).

## Batches

Status legend: `pending` / `in-progress` / `merged` / `skipped`.

| #  | Milestone | Batch | Boundary    | Commits | Theme                                                                                          | Status  |
| -- | --------- | ----- | ----------- | ------- | ---------------------------------------------------------------------------------------------- | ------- |
| 1  | v1.16.9   | 1/1   | `95665d570` | 3       | crypto security backports (ECIES invalid-curve, secp256k1 coordinate check) + release          | merged (`a06dbd7d2`) |
| 2  | v1.17.0   | 1/12  | `f23d506b7` | 20      | bugfix sweep: rawdb/state/pathdb fixes, RPC framing, blobpool, filters                          | merged (`2a85bf611`) |
| 3  | v1.17.0   | 2/12  | `cf93077fa` | 20      | verkle/UBT groundwork, rawdb+rlp hardening, fusaka beacon update                                | merged (`ad0f54e9c`) |
| 4  | v1.17.0   | 3/12  | `a122dbe45` | 20      | **EIP-8024 introduced** (`core/vm`), miner `--miner.maxblobs`, gnark-crypto bump                | merged (`7278fa559`) |
| 5  | v1.17.0   | 4/12  | `228933a66` | 20      | catalyst getPayload fork checks, pebble config, slow-block stats, downloader syncmode           | merged (`8b4a6095d`) |
| 6  | v1.17.0   | 5/12  | `5dfcffcf3` | 20      | state/code-read metrics, tx fetcher validation, blobpool legacy sidecar removal                 | merged (`4e17d3dc0`) |
| 7  | v1.17.0   | 6/12  | `710008450` | 20      | snap-sync locking, getBlobsV3, pathdb history indexing, go-verkle removal                       | merged (`85a1750c5`) |
| 8  | v1.17.0   | 7/12  | `94710f79a` | 20      | state update hook, trienode history compression, blobpool gaps; KZG peer-drop fix               | merged (`09cddf9b7`) |
| 9  | v1.17.0   | 8/12  | `8fad02ac6` | 20      | OpenTelemetry RPC tracing, **read-only gas handlers (`core/vm`)**, trienode history enable      | merged (`c1537496c`) |
| 10 | v1.17.0   | 9/12  | `845009f68` | 20      | eth_getProofs for history, **EIP-8024 immediate-byte update**, alloc reductions                 | merged (`0f76e0c30`) |
| 11 | v1.17.0   | 10/12 | `c12959dc8` | 20      | perf/alloc sweep, keccak vendoring, Ledger Gen5, rlp RawList                                    | merged (`f4f2c07fd`) |
| 12 | v1.17.0   | 11/12 | `c50e5edfa` | 20      | EraE format, rlp iterators, HTTP/2 JSON-RPC, telemetry CLI wiring                               | merged (`2c0608fc6`) |
| 13 | v1.17.0   | 12/12 | `0cf3d3ba4` | 7       | delayed p2p decoding, header-verification hardening, **v1.17.0 release**                        | merged (`70994671a`) |
| 14 | v1.17.1   | 1/2   | `9ecb6c4ae` | 20      | **syscall value-transfer disable (`core/vm`)**, BAL type changes, **Amsterdam precompile touch** | merged (`189030866`) |
| 15 | v1.17.1   | 2/2   | `16783c167` | 20      | **eth/68 protocol drop**, **EIP-7843 SLOTNUM**, **8024 enabled in Amsterdam**, Go 1.26; release  | merged (`b6175113d`) |
| 16 | v1.17.2   | 1/4   | `00540f946` | 20      | **EIP-7778 block gas accounting**, miner prefetcher, **amsterdam jump table**, default cache 4096 | merged (`09c784851`) |
| 17 | v1.17.2   | 2/4   | `77e7e5ad1` | 20      | codedb refactor, **EIP-7954 max contract size**, trienode history alongside data                | merged (`0a83ed542`) |
| 18 | v1.17.2   | 3/4   | `e23b0cbc2` | 20      | stateless codedb fix, **call-variant gas measurement rework (`core/vm`)**, bintrie parallel hash | merged (`1abb57b8f`) |
| 19 | v1.17.2   | 4/4   | `be4dc0c4b` | 17      | **EIP-7708**, simulateV1/getProofs limits, **v1.17.2 release**                                  | merged (`682b4c380`) |
| 20 | v1.17.3   | 1/7   | `04e40995d` | 20      | **eth/70 partial receipts**, BAL storage layer + snap/2 BAL serving                             | merged `d9a3a0bc9` (snap/2 deferred; **eth/70 adopted separately**, see below) |
| 21 | v1.17.3   | 2/7   | `c453b99a5` | 20      | **gas becomes vector <regularGas, stateGas> (`core`)**, bintrie fixes                           | merged (`582a825ae`) |
| 22 | v1.17.3   | 3/7   | `5af5510b1` | 20      | EIP-7610 rework, CachingDB split (merkle/binary), freezer fsync fix                             | merged (`e15a2b63b`) — **CachingDB split (#34700) deferred**, adopted separately in `b0ea85ca0`, see below |
| 23 | v1.17.3   | 4/7   | `33c1bd59f` | 20      | **gas budget (`core/vm`)**, **EIP-7976 calldata floor**, **FinalizeAndAssemble removed (consensus iface)** | merged (`7b7b9f352`) — 37 conflicts, **#34726 FinalizeAndAssemble removal DECLINED** (Bor's finalization produces state-sync receipts) |
| 24 | v1.17.3   | 5/7   | `41b856d47` | 20      | BAL spec update, **stack arena (`core/vm`)**, skip tx gas cap post-Amsterdam                    | merged (`9c16c4244`) — 29 conflicts, **#33960 stack arena: API adopted, Bor's GEVM storage layout kept** (operator decision; `EVM.Release()` + 17 call sites declined) |
| 25 | v1.17.3   | 6/7   | `1abbae239` | 20      | **EIP-7981 access-list cost**, eth_call block overrides, TypeMux removal                        | merged (`90cbef62b`) — 17 conflicts, **#32585 TypeMux→Feed DECLINED** (entangled with the deferred #33157 SyncMode relocation + Bor's forked downloader) |
| 26 | v1.17.3   | 7/7   | `117e067f0` | 16      | misc fixes, **core.Message moves to uint256**, **v1.17.3 release**                              | merged (`96945e338`) — 7 conflicts; caught and fixed a real regression (Bor's signed `effectiveTip` wrapped as uint256 on the NoBaseFee path). `version/version.go` deliberately not bumped — belongs in the milestone chores commit |
| 27 | v1.17.4   | 1/7   | `1149f76dc` | 20      | pre/post-execution wrap (`core`+miner), GasChangeHook v2, block-level accessList                | merged (`859b9fabb`) — 27 conflicts / 46 hunks; #34803 + #34812 both adopted (declining had become the expensive option). Caught a consensus-relevant auto-merge: upstream's `var` block replaced Bor's and built the EVM block context with a **nil author**, which would have compiled and silently changed `context.Coinbase`. The flagged assembly risk is **not** here — #34812 leaves `Finalize`/`FinalizeAndAssemble` untouched; that lands in row 28 |
| 28 | v1.17.4   | 2/7   | `ca1a027fa` | 20      | **block accessList construction (consensus/miner)**, eth71 BAL response, engine_hasBlobs        | merged (`5df1648b0`) — 26 conflicts. **Two operator-approved declines**, the flagged ones: the whole **BAL construction cluster** (#34957 + eth/71 + EELS JSON + validation + t8n — it changes `Engine.Finalize`, where Bor returns receipts, for a feature that is dormant and cannot run under V2) and **#35009's witness field reordering** (reachable via Bor's legacy `FromExtWitness` decode fallback). Adopted: #33969's rpc/blob JSON encoding rework (+`fjl/jsonw`), SetHead error propagation, slot number, engine_hasBlobs. See the four new `needs-wiring.md` rows |
| 29 | v1.17.4   | 3/7   | `4017efe34` | 20      | **jumpdest bitmap global cache (`core/vm`)**, RPC hardening, deprecated CLI flag removal        | merged (`047e24c29`) — 25 conflicts. **#34850 global jumpdest cache declined** (operator-approved): bor already has a per-block shared cache, and upstream's cross-block LRU would be a bor-side architecture change needing benchmarks, not a merge resolution — recorded as a perf follow-up. First batch of the milestone with **zero** diff on both witness and hardfork surfaces. Upstream #35023 independently arrived at bor's `BaseFee` head+1 gate, vindicating batch 27's keep-ours; bor was also already ahead on the pion/stun v3 bump |
| 30 | v1.17.4   | 4/7   | `80d9ba5d9` | 20      | blobpool status queue, BAL freezer management, snapv2 skeleton                                  | merged (`51aa42761`) — 38 conflicts, the largest of the milestone, but only **nine** upstream commits. **#34977 "BAL freezer" adopted in full** (operator-approved): not a BAL feature but a generic freezer refactor (`prunable bool` → `tailGroup string`, `Tail(group)` / `TruncateTail(group, n)`) whose interface change had already auto-merged across 13 of its 24 files — forced, the same shape as batch 28's #33969. Bor's `diffs` and `bor-receipts` tables mapped into `ChainFreezerBlockDataGroup` (preserving the previous single-shared-tail semantics); bor's `offset` audited through upstream's wholesale `repair()`/`validate()` rewrite; the upgrade path proven by `TestChainFreezerBALAlignment` on bor's six-table set. **Two deferral extensions**: #34896's EraE spec churn (batch-11 deferral) and #35098's 3.1k-line snapv2 skeleton (snap/2, now deferred a third time). Both standing scans empty for the second batch running |
| 31 | v1.17.4   | 5/7   | `e444c267a` | 20      | **clef removed**, repo-wide typo sweep (`all:`), blob sidecar fixes                             | merged (`338ffb831`) — 34 conflicts over **ten** commits, **19 of them the clef removal**; 119 files, +534/−10186, the only overwhelmingly-deletion batch of the sync. **#35097 clef accepted** after verifying it is not operator-facing: bor's `Makefile`/`build/ci.go`/goreleaser/docker carry **zero** clef references (bor never built it), nothing outside `cmd/clef`+`signer/core`+`signer/rules` imports the removed packages (the two apparent consumers match on `signer/core/apitypes`, which upstream keeps), and `--signer` is untouched since `accounts/external` is an RPC client to any signer. **The real find was a bor-only interface broken by a clean auto-merge**: #35100's pointer-ised `GetBalance` left `consensus/bor/api.Caller` declaring the value form, breaking `eth/ethconfig` at four sites (no call sites, so signature-only + mock regen). #33540's snapshot shutdown refactor adopted wholesale (bor's divergence was blank-line-only); #35099 combined with bor's block-based `IsOsaka(head.Number)`; #35132 is the **first tracing commit bor could take** (needs only `otel/propagation`, and bor does carry OpenTelemetry — it lacks the `internal/telemetry` wrapper). Both standing scans clean for the third batch running |
| 32 | v1.17.4   | 6/7   | `8c540cb08` | 20      | **snap/2 sync logic**, **EIP-8037 state-creation gas**, cgroup memlimit                         | merged (`8ce37d5ba`) — 39 conflicts over **ten** commits; 54 files, +1916/−657 taken of 101 files / +8152/−1456 offered. Two halves pulling opposite ways. **EIP-8037 (#33601) adopted in full — the fourth forced adoption and the largest**: 25 of its 36 files auto-merged, including the whole gas substrate (`core/gaspool.go`, `core/vm/gascosts.go`, `interpreter.go`, `jump_table.go`, `state_processor.go`, `tests/*`). Hardfork surface moves but **no new fork, no activation height, no precompile change** — `params/config.go`, both presets, both genesis JSONs and `forkid` untouched, `ActivePrecompiles` unchanged, continuity test correctly unedited; gated on Amsterdam (nil everywhere, gate already block-based). Gas equivalence *proved*: at `StateGas == 0`, `ExitHalt(0)`/`ExitRevert()`/`ExitSuccess()` reproduce bor's old `UseGas(RegularGas)` / preserve / identity exactly. Bor's Ahmedabad 32 KB cap preserved through upstream's check-relocation via a new `evm.checkMaxCodeSize`; the `opCreate`/`opCreate2` `readOnly` deletion is **not** a regression (#33601 moved the guard into the four CREATE gas functions, which run first). **snap/2 cluster (#34626 + 6 follow-ups) declined a fourth time** (operator-approved) — decisive new obstacle is bor's `downloader.go` → `bor_downloader.go` rename (2570 vs 1252 lines), leaving all four downloader commits with no landing site; 9 files removed, 26 reverted, `SnapV2` field + `--snap.v2` flag stripped. **#35124 declined as one unit** (blob cache + `txorder` move — the move only exists to feed a cache bor can't use, since `blobTxPool` is never assigned). #34947 adopted, #35170 ported to bor's slice-shaped packet, #34995/#35163/#35172 declined as orphans. **#35156 was a second forced adoption**: 21+2 call sites auto-merged, so bor's GEVM `top`/`data` stack gained all seven in-place methods. Six bor-only twins fixed, five build-surfaced. Both standing scans clean for the **fourth** batch running |
| 33 | v1.17.4   | 7/7   | `36a7dc72e` | 4       | reflect.Pointer sweep (`all:`), **v1.17.4 release**                                             | merged (`f397229ee`) — **final batch; `git merge-base --is-ancestor v1.17.4 HEAD` confirms geth v1.17.4 is fully merged.** Smallest of the sync: 3 conflicts over two commits, 14 files, +20/−20. Both standing scans clean for the **fifth** batch running and trivially so — zero witness-mentioning hunks anywhere in the range and **not one hardfork-surface file touched**. `version/version.go` **kept bor's `Patch = 0`/`unstable`**, following the batch-25 release-*tag* precedent rather than batch-12's release-*cycle* one; re-verified against all seven v1.17.x milestone tips. This retires the stale "bump it in the M6 chores commit" plan item — b25 is explicit the bump is *not taken at all*, not deferred. #35176's two conflicts were blank-line-only (resolver-verified); one bor-only twin completed by hand (`internal/cli/server/service.go:270`, a dir upstream lacks — compiles either way since `const Ptr = Pointer`, but an `all:` sweep must not leave a file behind). **#35002 was a genuine no-op, verified not assumed**: bor already carried the fixed `minInterval - diff` from local commit `5410680b1` — the **fifth** upstream convergence onto bor. No regressions |

## Out-of-band adoptions (not merge batches)

Features deferred at a batch and picked up afterwards as their own branch and
stacked PR, because they are hand-written ports rather than merges. Each is cut
from the tip of the batch that deferred it, so the tree still matches the state
upstream's commit expected.

| Feature | Deferred at | Branch | Cut from |
| ------- | ----------- | ------ | -------- |
| `core/vm` catch-up | batches 4–19 | `ppatil-corevm-catchup` | `ppatil-upstream-v1.17.2` tip |
| EIP-7975 / eth/70 partial receipts (#33153) | batch 20 | `ppatil-upstream-eth70` | `d9a3a0bc9` (batch 20 tip) |
| `CachingDB` split into `MPTDatabase` + `UBTDatabase` (#34700) | batch 22 | `ppatil-upstream-mptubt` — `b0ea85ca0` | `e15a2b63b` (batch 22 tip) |

Milestone v1.17.3 is therefore split across four PRs, with two adoption PRs
stacked between the merge PRs: batch 20 alone (#2340), then eth/70 (#2341), then
batches 21–22 (#2342), then the `CachingDB` split (#2343), then batches 23–26 plus
the milestone chores on `ppatil-upstream-v1.17.3-part3`.

The #34700 adoption was **scheduled before batch 23**, not open-ended: its sequel
#34763 applies the same MPT/UBT split to `core/state/reader.go` inside batch 23,
so batch 23 must not be attempted against a non-split tree. Adopted partially —
the type split, not the runtime fork-boundary plumbing; see the `needs-wiring.md`
row for what was declined and why.

## Bor-specific risk flags (advance warning, not a substitute for per-batch review)

- **Witness logic is off-limits for the whole sync (operator-directed).** Bor's
  witness generation, propagation and import are a deliberate divergence, so no
  batch may alter them as a side effect of an upstream merge. Treat as no-go:
  `core/stateless/`, `eth/protocols/wit/`, the `s.witness` collection blocks in
  `core/state/statedb.go`, and witness handling in `core/blockchain.go`,
  `miner/worker.go` and the downloader. A *file* overlapping is not the feature
  overlapping — `statedb.go` in particular holds witness blocks next to
  unrelated state code, so resolve around them. If an upstream change genuinely
  requires changing witness behaviour, stop and surface it; record it in
  `needs-wiring.md` rather than resolving it autonomously. State the check in
  each batch report, the same way fork gates are stated.
- **Consensus interface churn:** batch 23 removes `FinalizeAndAssemble` from the
  consensus interface and batches 27–28 rework block assembly around block
  access lists — `consensus/bor` implements this interface, so expect class-3
  conflicts and real porting work there, not mechanical merges.
- **EVM/hardfork surfaces** (invariant: full individual walkthrough, never
  grouped): EIP-8024 (batches 4, 10, 15), EIP-7843 SLOTNUM (15), EIP-7778 (16),
  EIP-7954 (17), EIP-7708 (19), EIP-7976 (23), EIP-7981 (25), EIP-8037 (32),
  Amsterdam jump-table/precompile wiring (14–16), call-gas rework (18), gas
  vector (21), gas budget (23), jumpdest cache (29). Every one of these touches
  `core/vm` / `params` and lands behind upstream's Amsterdam fork — Bor must
  decide fork-gating against its own hardfork schedule; precompile continuity
  tests (`core/vm/contracts_continuity_test.go`) must be preserved.
- **P2P protocol changes:** eth/68 dropped (15), eth/70 partial receipts (20),
  eth/71 BAL (28), snap/2 (20, 30, 32) — check against Bor's protocol
  registration and peer management.
- **Miner:** prefetcher enable (16), payload rebuild timing (15), slot number
  (27–30) — interacts with Bor's sprint-based mining loop.
- **Wide-but-shallow sweeps:** typo sweep (31) and reflect.Pointer sweep (33)
  touch `all:` — expect many trivial conflicts against Bor-modified files.
- **v1.16.9 duplicates:** its two crypto fixes reappear in batches 10 and 13 —
  expect no-op/already-applied conflicts there.

## Resume anchors

- `PROGRESS_TIP`: `70994671a` (v1.17.0 batch 12/12 `0cf3d3ba4` merged). **All 12 v1.17.0 batches merged.** Next = v1.17.0 **milestone chores** (durable-docs commit + `gen_config.go` regen + `pdbExemptMethods` fix + full integration/kurtosis/diffguard/govulncheck + PR triage), then the v1.17.0 stacked draft PR, then milestone v1.17.1 (batches 14–15, boundary `9ecb6c4ae`).
- Working branch for milestone 1 (`v1.16.9`): `ppatil-upstream-v1.16.9` — cut
  from `upstream-merge-v1.17.4` on the milestone's first batch.
- Full first-parent logs per milestone captured in the planning run directory:
  `runs/pos-merge-upstream/2026-07-06T09-56-56Z-claude-v1.17.4-plan/log-<tag>.txt`.

### Milestone v1.17.3 — complete

Batches 20–26 plus two hand-written adoption commits are merged on four stacked
branches. The milestone deliberately spans four PRs rather than one:

| PR | Head | Contents |
| -- | ---- | -------- |
| #2340 | `ppatil-upstream-v1.17.3` | batch 20 |
| #2341 | `ppatil-upstream-eth70` | eth/70 adoption |
| #2342 | `ppatil-upstream-v1.17.3-part2` | batches 21–22 |
| #2343 | `ppatil-upstream-mptubt` | #34700 `CachingDB` split adoption |
| #2345 | `ppatil-upstream-v1.17.3-part3` | batches 23–26 |

Next: milestone v1.17.4 (rows 27–33). The risk flags below still apply — batches
27–28 rework block assembly around block access lists, which is where the
`FinalizeAndAssemble` decline from batch 23 will resurface.
