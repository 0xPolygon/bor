# Upstream merge plan: go-ethereum v1.17.10

| Field | Value |
| ----- | ----- |
| `TARGET` | `v1.17.10` (`1df30b18d`, upstream tag dated 2026-10-23) |
| `SYNC_BASE` | **`v1.17.6`** (`3d84c6b2e`), a verified ancestor of `upstream-merge-v1.17.4` @ `d55c5cd34` |
| `PROGRESS_TIP` | `a57907700` = tag `v1.17.8` (v1.17.7: `051aeafb75`, `a44b1a45cd`, docs `40d9eba175`, PR #2533; v1.17.8: `a9dff4e635`, `24f7cb2db5`). Milestone v1.17.8 triage and docs pending. |
| Batch size | 20 first-parent commits, at most |
| Range | `v1.17.6..v1.17.10`, **100 first-parent commits** (216 files) |
| Milestones | 4: `v1.17.7` (`3d858f858`), `v1.17.8` (`a57907700`), `v1.17.9` (`9dffe6e97`), `v1.17.10` (`1df30b18d`) |
| Batches | 7 |
| Base branch | **`upstream-merge-v1.17.10`**, cut from `upstream-merge-v1.17.4` @ `d55c5cd34` (see deviation 1) |
| Working branches | `ppatil-upstream-v1.17.7` … `ppatil-upstream-v1.17.10`, stacked |
| rerere | off (`-c rerere.enabled=false`), `-c merge.conflictStyle=zdiff3` |
| Trial-merge estimate | **109 conflicted files** cumulatively (base tip against `v1.17.10`, `git merge-tree`) |

## Why these releases, and why a separate base

v1.17.7 to v1.17.10 are small, but they carry upstream's hardening and
correctness fixes for code v1.17.6 brought in (block-level access lists,
snap/2, the Amsterdam EIPs) and v1.17.9's database fixes for power
failure. The
operator's working assumption (2026-11-10) is that these 100 commits go to
`develop` together with v1.17.6. Until that is decided, they're worked on a
separate base branch and reviewed as separate PRs.

## Deviations from the skill defaults (deliberate, operator-directed)

1. **The base branch is cut from `upstream-merge-v1.17.4` (#2502's head),
   not from `origin/develop`.** #2502 is under review, so merging into its
   branch would grow it and reset its reviews. Every milestone PR here stacks
   on `upstream-merge-v1.17.10`. Where that base finally lands is open:
   - (a) merged into `upstream-merge-v1.17.4`, so #2502 carries v1.17.10; or
   - (b) its own PR to `develop` after #2502 merges, whose diff is then only
     these four releases.

   Either way, review fixes on #2502 are merged into this base and cascaded
   up the stack. The name keeps `upstream`, so bor ruleset 626146's
   `refs/heads/**upstream**` signed-commits exclusion covers it.
2. **`SYNC_BASE` is `v1.17.6`, which isn't in `develop` yet.** Ownership
   detection (§4.3) anchors on `v1.17.6`. For questions that reach further
   back, also check `v1.16.8`, the last tag in `develop`.
3. **Upstream cutoff is fixed at `v1.17.10`.** Commits after the tag on
   `upstream/master` are out of scope.

## Already in the base

- **#35861** (`02de17351`, "p2p/discover: require bonded peer for address
  prediction"): ported as `6848e4008` (#67). Batch 3 should merge cleanly or
  converge. Take upstream wherever the two differ only cosmetically.
- No other upstream PR number from this range appears in
  `v1.17.6..upstream-merge-v1.17.4`. Ports that don't cite the upstream
  number won't show up in that search, so check the provenance of any
  conflicting Bor hunk as usual (§4.4).

## Batch table

Conflict counts are from trial merges of the base tip against each
first-parent commit (`git merge-tree`; data in the run directory's
`percommit.tsv`). "New" is conflicted files not already conflicted at the
previous boundary, so these are estimates.

| # | Milestone | Boundary | Commits | ≈ new conflicts | Theme | Risk |
| - | --------- | -------- | ------- | --------------- | ----- | ---- |
| 1 ✅ `051aeafb75` | v1.17.7 | `5d8fd6b60` | 14 (1–14) | 32 | #35723 block-range announcement in the downloader, #35749 BAL catch-up for snap, #35772 BAL encoding pipeline, #35792 custom history pruning cutoffs, **#35773 miner excludes txs short on gas (EIP-8037 two-dimensional)**, #35821 prefetcher shutdown leak, #35837 snap restart before the history cutoff | Downloader commits land on Bor's `bor_downloader.go` / `bor_fetchers_concurrent.go` split (as in v1.17.6's decision C). #35773 edits `miner/worker.go` and `internal/ethapi/api.go`, both heavily Bor-modified |
| 2 ✅ `a44b1a45cd` | v1.17.7 | `3d858f858` | 7 (15–21) | 30 | **#35582 txpool empty-state fallback during snap sync** (19 new conflicts: txpools, `blockchain_reader.go`), **#35835 spec-test fixtures v21**, #35682 NodeOps bootnodes, #35844 sync restart after unclean shutdown | **#35835 changes `consensus.Engine.Finalize`** (drops the BAL arguments) and adds `MaxTxTotalGas` (Amsterdam-gated) to `params/protocol_params.go`. Bor's `Finalize` signature is already its own (it returns receipts). Keep it and drop only the BAL arguments. It also moves withdrawals into `PostExecution`. Every live `PostExecution` caller on Bor is the non-Bor path (`chain_makers.go`, `simulate.go`, `t8ntool`), so this should be inert; confirm it. #35682 edits `params/bootnodes.go`; Bor's bootnodes are its own, so keep them |
| 3 ✅ `a9dff4e635` | v1.17.8 | `8fe53204a` | 16 (22–37) | 13 | **#35857 getPooledTransactions lookup limit**, **#35859 invalid receipts**, **#35856 snap charges per request**, #35836 blobpool gapped txs, #35861 (already in base), #35880 stale DNS addresses, #35878 `eth_capabilities`, **#35865 BAL codeHash cache**, #35863 pion/stun, #35288 delete the BAL on rewind | **Serving-side hardening.** Develop rewrote snap serving and added its own p2p and RPC limits (#2489–#2491). For each fix, decide whether Bor's code already bounds it, or whether to add the fix to Bor's version. Never just take one side. #35288 touches rewind; check it against `8ea7bf721` (the `bals` freezer padding) |
| 4 ✅ `24f7cb2db5` | v1.17.8 | `a57907700` | 16 (38–53) | 6 | **#35585 chainspecs support** (`params/config.go`), #35845 no execution-gas cap on `eth_call`, **#35881 jumpdest analysis caching**, #35617 / #35668 `eth_simulateV1` Amsterdam fixes, **#35860 / #35883 cells**, **#35862 block shape check before hashing**, #35896 tracer nonce change, #35804 catalyst reorg, **#35904 receipt size checks** | **Hardfork surface:** #35585 edits `params/config.go`. Confirm it adds no fork values to Bor presets. #35881 is a VM-adjacent cache: check it's consensus-neutral and how it interacts with Bor's jumpdest cache |
| 5 | v1.17.9 | `365bf9706` | 19 (54–72) | 15 | #35890 raw BAL verification, **#35691 pebble config**, #35927 lint, #35930 snap pivot ancestry, **#35939 freezer metadata at fixed size**, **#35936 pathdb reactivation after state sync**, #35842 / #35841 simulate, **#35905 BAL recording off in state view**, #35385 Gloas (beacon), #35673 discovery metrics, #35914 pre-London fee fields, **#35949 pathdb histories in background** | **Storage.** #35691 changes the v2 level sizes and touches `pebble_v1.go`. Bor keeps its own tuning for existing v1 databases (#2497); check whether the v1 change applies to Bor's v1 path. #35939 changes `freezer_meta.go`. Rerun `TestChainFreezerBALDowngradeRoundTrip` and the real-binary downgrade test (new → v2.10.3 → new). #35949 moves pathdb history writes to the background: check the witness and stateless paths |
| 6 | v1.17.9 | `9dffe6e97` | 19 (73–91) | 6 | #35953 / #35933 RPC, #35907 transaction history flag, #35935 `-dlgo` Go 1.27.2, #35961 tracer, **#35968 pending block timestamp**, **#35974 Amsterdam on Hoodi** (reverted in batch 7), **#35978 pathdb stale-layer retries**, #35875 log index cutoff, #35761 / #35928 / **#35988 tx validation**, #35982 frozen pivot, **#35975 BPO after Amsterdam**, **#35638 PGO-inlined interpreter switch** | **Highest decision density. See "Interpreter dispatch" below.** #35638 edits `core/vm/jump_table.go`, `interpreter.go` and `instructions.go` (hardfork surfaces). #35975 and #35974 edit `params/config.go`. #35988 changes the effective-tip comparison below the base fee: check Bor's txpool and miner ordering. #35968 is in `miner/miner.go` |
| 7 | v1.17.10 | `1df30b18d` | 9 (92–100) | 7 | **#35989 snap/2 auto-enabled once Amsterdam is active**, #35959 DUP/SWAP folding (interpreter), #35985 `-dlgo` Go 1.27.3, #36001 Hoodi Amsterdam revert, #36003 tx field validation | #35989 keys "Amsterdam active" off `config.AmsterdamTime`. Bor's Amsterdam gate is block-based, so on Bor snap/2 stays off even when set explicitly. That is dormant and acceptable, but it's a needs-wiring row for enabling Amsterdam. #35959 regenerates the interpreter again |

No batch exceeds the §5 threshold (~40 files or ~10 class-3). Batch 2 is 7
commits but 30 new conflicts, mostly from #35582. Split it at `f8f9bc574` if
the class-3 count climbs.

## Interpreter dispatch (#35638, #35959): decision before batch 6

#35638 replaces the jump-table call through a function pointer with a
generated switch (`core/vm/interpreter_gen.go`). There are three tiers:
- 57 hot, fork-stable opcodes get their own case, with static gas and stack
  bounds emitted as constants;
- 3 opcodes are fork-invariant but have dynamic gas;
- everything that varies by fork (CALL, CREATE, SSTORE, SLOAD, LOG, copies)
  still goes through the per-fork table in the default case.

The generator derives each opcode's metadata from the per-fork instruction
tables through `core/vm/genspec.go`. It also writes `cmd/geth/default.pgo`.

What this means for Bor:

- **Bor's own tables must feed the generator.** Bor carries forks upstream
  doesn't have (and, per POS-3738, an ordering problem between its jump
  tables and Amsterdam's). If `genspec.go` only knows upstream's fork list, a
  Bor-specific change to a "hot" opcode would be compiled into a constant and
  silently ignored.
  - Before resolving: list every opcode whose gas, stack bounds or handler
    differ in any Bor fork table.
  - Confirm none of them is in the 57-opcode tier, or that the generator
    refuses to emit it ("generation stops rather than emitting something
    wrong").
  - Then extend `genspec.go` with Bor's forks and run
    `go generate ./core/vm/...` on the merged tree. Don't take upstream's
    generated file as-is.
- **PIP-88 twins.** `gasSLoadPIP88` / `makeGasSStoreFuncPIP88` sit in the
  fork-varying tier (SLOAD and SSTORE), so they should still go through the
  table. Run the twin scan anyway, and `TestPIP88PrecompileGasCosts`.
- **Upstream ships the old loop as a test oracle.** Run
  `interpreter_gen_test.go` and the EVM fuzzer against Bor's fork configs
  (mainnet, Amoy) as well as upstream's.
- **PGO.** `default.pgo` lives in `cmd/geth`. Bor ships `cmd/bor` (the
  `bor` binary built from `cmd/cli`), so it won't get the speedup without its
  own profile. This only affects performance: a needs-wiring row, not a
  blocker.
- **Ordering with POS-3738.** POS-3738 (Amsterdam fork ordering in
  `NewEVM` / `LookupInstructionSet`) edits the same tables. Do it first on
  develop and back-merge, or do it after this lands. Either way, don't
  resolve both in one batch.
- **Evidence.** Replay mainnet blocks through `TestV2BlockSTMAllBlocks` and
  the witness regeneration tests at the batch 6 and 7 boundaries. Compare
  block-processing time against `d55c5cd34` on the same blocks.

## Checks that must run, by batch

- **Serving-side security fixes (batches 3–4):** #35857, #35859, #35856,
  #35883, #35862, #35904, #35865, #35881. For each, the batch report records
  whether Bor's current code already bounds the case, and the test that
  shows it.
- **Witness-path files (operator-gated, surface don't resolve):** #35772
  (`core/blockchain.go`, BAL pipeline), #35905 (BAL recording in the state
  view), #35949 / #35936 / #35978 (pathdb). The #2180 divergences must
  survive:
  - `addObjectWitness`'s `else if s.prefetcher == nil` branch;
  - `NewWitness`'s `types.CopyHeader`.

  Run `TestV2WitnessRegeneration{AllBlocks,PipelinedSRCAllBlocks,PipelinedSRCChained}`
  non-short on every batch that touches `core/state` or `triedb/pathdb`.
- **Hardfork surfaces:**
  - `params/config.go` (#35585, #35974, #35975, #36001);
  - `params/protocol_params.go` (#35835);
  - `core/vm/jump_table.go`, `interpreter.go`, `instructions.go` (#35638,
    #35959).

  Every Bor preset, both `internal/cli/server/chains/*.go` and both
  `builder/files/genesis-*.json` must stay nil for Amsterdam and later.
  `TestV2ForkParity` and `TestReinforceMultiClientPreCompilesTest` must pass.
  Uburu stays at mainnet 96,787,900 / Amoy 51,978,200.
- **Live-path consensus checks:**
  - #35773: miner transaction selection with two-dimensional gas. Show
    equivalence while `StateGas == 0`, as for EIP-8037 in v1.17.4.
  - #35988: effective tip below the base fee. Check Bor's txpool ordering
    and the miner's.
  - #36003 / #35761: stricter transaction field validation. Must not reject
    Bor's state-sync transactions or anything already on mainnet: replay.
- **Freezer:** #35939 and #35288 next to `8ea7bf721`. Rerun the downgrade
  round trip both ways.
- **Dependencies and toolchain:** #35863 (pion/stun), #35935 / #35985
  (`-dlgo` Go 1.27.2 / 1.27.3). Bor builds with Go 1.26.10, and `go.mod`'s
  `go` directive is unchanged in this range (1.25.0 upstream, 1.26.10 in
  Bor). Keep Bor's toolchain unless Bor's CI moves.

## Milestone gate (after batches 2, 4, 6 and 7)

As in v1.17.6:
- upstream-PR triage (the wiring pass), including:
  - #35792's `--history.chain` and #35907's transaction-history flags in
    `internal/cli/server`;
  - #35989's snap/2 auto mode against Bor's block gate;
  - #35682's bootnodes (keep Bor's);
  - #35638's PGO profile;
- the chores batch;
- the full suite: `make test`, lint, the non-short witness tests,
  `tests/bor`, Kurtosis (standard and stateless-witness), diffguard,
  govulncheck, and a mainnet replay through `TestV2BlockSTMAllBlocks`.

v1.17.7 and v1.17.10 are small, so their PRs can fold into their neighbours'
(two PRs, v1.17.7+8 and v1.17.9+10) if review prefers. Decide when opening
the PRs; the stacked branches stay per milestone either way.
