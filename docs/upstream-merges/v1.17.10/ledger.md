# Decision ledger: go-ethereum v1.17.10

`SYNC_BASE` is `v1.17.6`. Classes: 1 = Bor-owned surface, 2 = upstream-owned,
3 = semantic, hardfork or non-content conflict (modify/delete, add/add).

## Batch 1: `v1.17.6..5d8fd6b60` (v1.17.7, 14 commits)

32 conflicted paths. No hardfork-surface file changed.

| Path | Class | Upstream PR | Decision | Why |
| ---- | ----- | ----------- | -------- | --- |
| `core/blockchain.go` (struct, `stopWithoutSaving`, new `trackPrefetch`) | 3 | #35821 | **Ported to Bor's prefetcher.** Added `prefetchLock` / `prefetchWg`. `startPrefetchGoroutine` registers through `trackPrefetch` and marks itself done; shutdown waits after `bc.wg.Wait()`. | Bor launches the same detached block prefetcher, so the fix applies: without it, `Stop` can return while a prefetcher still reads, and its reads refill the pathdb clean caches `Close` just released. Upstream's own hunk sits in `setupExecutionState`, which Bor doesn't have, so it was dropped. |
| `core/blockchain.go` (`initializeHistoryPruning`, `loadLastState`) | 3 | #35792, #35837 | Kept Bor. Removed the `loadLastState` half of #35837, which had merged cleanly. | Both build on the `HistoryPolicy` refactor (#34036) that Bor deferred in v1.17.4. Bor's prune points cover only Ethereum networks, so the cutoff is 0 on Bor's chains. Recorded in needs-wiring with #34036. |
| `core/blockchain.go` (stats, BAL attach), `core/state_processor.go`, `core/types.go`, `core/block_validator.go`; new `core/digest_pipeline.go` dropped | 3 | #35772 | **Declined (kept Bor).** | Extends the receipt pipeline (#35738) that Bor declined in v1.17.6, and the block-level access list attach that Bor doesn't build (POS-3737). |
| `core/state_processor_parallel.go`, `core/eip7928_test.go` | 3 (modify/delete) | #35772 | Kept Bor's deletion. | #35264 declined in v1.17.6; `eip7928_test.go` belongs to POS-3737. |
| `core/history/historymode.go` (+ `historymode_test.go`, kept deleted), `cmd/geth/chaincmd.go`, `cmd/workload/testsuite.go`, `cmd/utils/flags.go`, `eth/backend.go`, `eth/ethconfig/{config,gen_config,config_test}.go`, `core/blockchain_test.go` | 3 | #35792, #35837 | **Declined (kept Bor).** `config_test.go` was add/add: Bor's file is its own Heimdall-backed test, so it's kept. | Same cluster as above: new `postosaka` and custom prune points on top of `HistoryPolicy`, which Bor doesn't have. |
| `eth/downloader/{peer,bor_fetchers_concurrent,metrics,queue,queue_test}.go`; `bor_fetchers_concurrent_{bodies,receipts}.go`, `resultstore.go`, `skeleton_test.go` restored to Bor | 1/3 | #35723, #35780 | **Declined (restored to Bor).** | #35780 prioritises BAL downloads, which Bor declined (decision C, v1.17.6). #35723 skips peers whose announced block range excludes the queue head. Bor's fetch loop is restructured (`collectIdlePeers`), so taking it is a port, not a resolution. Bor already announces ranges every 32 blocks, which matches upstream's 64-block slack. Recorded in needs-wiring as a follow-up. |
| `eth/downloader/{downloader,downloader_test,fetchers_concurrent_bals}.go` | 3 (modify/delete) | #35723, #35749 | Kept Bor's deletion. | Bor uses `bor_downloader.go` (permanent modify/delete, v1.17.5 needs-wiring). |
| `eth/downloader/beaconsync.go` | 3 | #35749 | Kept Bor. | Upstream removes the stale-pivot move in favour of snap/2's frozen pivot. Bor has no snap/2. |
| `eth/protocols/snap/{bal_apply,bal_apply_test,syncv2,syncv2_test}.go` (+ new `syncv2_complete_test.go`, dropped) | 3 (modify/delete) | #35749 | Kept Bor's deletion. | snap/2 declined (decision C). |
| `core/gaspool.go` (+ new `gaspool_test.go`), `miner/worker.go`, `miner/pipeline.go`, `internal/ethapi/simulate.go` | 3 | #35773 | **Adopted.** `GasPool.Gas()` is replaced by `Available(amsterdam)`. Ported every Bor caller (9 in `miner`, 1 in tests). `commitTransactions` keeps Bor's preamble and gains `isAmsterdam` / `minTxGas` from Bor's block-based `IsAmsterdam(number)`. The txpool-prefetch budget pools pass `false` because they only ever call `CheckGasLegacy`. `shouldLateRefillSpeculativeBlock` takes the fork flag. | Before Amsterdam, `Available(false)` returns `remaining`, exactly what `Gas()` returned, so block building is unchanged on every Bor network. After Amsterdam it bounds by both EIP-8037 dimensions. |
| `core/gaspool_test.go` (new) | 1 | #35773 | Adopted with one case adapted: the "execution unbound" case charges 20M instead of 32M. | Bor's `params.MaxTxGas` is `1<<25` (#1842), not upstream's `1<<24`. Upstream's 28M execution remainder is below Bor's cap, so that case binds on execution. The code agrees with `CheckGasAmsterdam` either way; the test's final loop checks that for both sides of the cap. |
| `internal/ethapi/api.go` (`applyMessage`) | 1 | #35773 | Kept Bor (`CallDefaults(globalGasCap, …)`). | Deliberate Bor divergence (#1856, restore the RPC gas cap). Bor doesn't size calls from the gas pool there. |
| `build/checksums.txt`, `build/ci.go` | 2 | #35782 | Merged both: Bor's `ppa-builder-1.24` (#2484) and upstream's `ppa-builder-1.25`. | Orthogonal additions. |
| `cmd/devp2p/internal/ethtest/suite.go` | 3 | #35805 | Kept Bor. | The changed test is eth/72 (`TestBlobTxAvailabilityFailure`), which Bor removed (decision D). |
| `.github/dependabot.yml` (new) | 1 | #35807 | Not taken. | Would turn on Dependabot PRs for 0xPolygon/bor. Bor's CI configuration is its own. |
| `triedb/pathdb/database.go`, `core/filtermaps/indexer_test.go`, `version/version.go` | 2 | #35789, #35823, #35771 | Taken (clean). | Upstream-owned. |

## Batch 2: `5d8fd6b60..3d858f858` (v1.17.7, 7 commits)

36 conflicted paths. Hardfork surfaces changed: `params/protocol_params.go`
(new constant) and `core/state_transition.go` (`preCheck`, Amsterdam-gated).

| Path | Class | Upstream PR | Decision | Why |
| ---- | ----- | ----------- | -------- | --- |
| `core/state_processor.go` (`ProcessParentBlockHash`) | 3 (live path) | #35835 | **Taken (merged clean).** A failed EIP-2935 history system call is now ignored instead of panicking. | Spec behaviour (the call is unchecked). The call runs on Bor from Prague (mainnet 73,440,256). It's made from the system address with a fixed budget, and a call to an address without code succeeds, so on Bor it doesn't fail in practice. Before this, any failure would have stopped every node; now nodes continue. Flag for cross-client parity. |
| `core/state_transition.go` (`preCheck`), `core/txpool/validation.go`, `params/protocol_params.go`, `core/eip8037_test.go` | 3 (hardfork surface) | #35835 | **Adopted dormant.** `MaxTxTotalGas = 1<<32-1` added next to Bor's `MaxTxGas = 1<<25` (unchanged). Both `preCheck` and txpool validation reject a gas limit above it only when `IsAmsterdam`. Bor's pre-Amsterdam line (`IsOsaka \|\| isMadhugiri`) is kept. | EIP-8037's total cap. Bor's `AmsterdamBlock` is nil on every network, so it's inert. Before this, Bor applied no transaction gas cap at all after Amsterdam. |
| `consensus/consensus.go`, `consensus/{beacon,clique,ethash}/consensus.go` | 1 | #35835 | Kept Bor's `Finalize(…, receipts) ([]*types.Receipt, error)`. | Bor's engine interface is its own and never took the BAL arguments upstream now drops. Bor's beacon engine keeps applying withdrawals in `Finalize`. |
| `core/state_processor.go` (`Process`, `PostExecution`; new `ProcessWithdrawals` removed), `core/chain_makers.go`, `internal/ethapi/simulate.go` (a second `PostExecution` call that merged clean, removed), `cmd/evm/internal/t8ntool/execution.go`, `miner/worker.go` | 1/3 | #35835 | **Declined (kept Bor).** | Upstream moves withdrawals from `Finalize` into `PostExecution`. Bor's `PostExecution` runs only when `Bor == nil` and returns only requests; taking the move together with Bor's `Finalize` would credit withdrawals twice on non-Bor test chains. Bor pins its own spec fixtures (`spec-tests v5.1.0`), which don't need the new ordering. |
| `build/checksums.txt` | 1 | #35835 | Kept Bor's fixtures pin (`v5.1.0`). | Bor's own spec-test version. |
| `tests/block_test.go`, `tests/state_test.go` | 2 | #35835 | Taken (clean). | Two BAL test skips and the `-short` sampling fix; upstream-owned harness. |
| `core/state_processor_parallel.go`, `eth/api_debug_replay.go`, `core/eip8246_test.go` | 3 (modify/delete) | #35835, #35582 | Kept Bor's deletions. | #35264, #35439 and POS-3737 declined earlier. |
| `core/blockchain_reader.go`, `core/txpool/{txpool,legacypool/legacypool,blobpool/blobpool,blobpool/interface}.go`, `eth/{api_backend,api_debug,state_accessor}.go`, and 11 test files | 1 | #35582 | **Kept Bor.** | Upstream changes `StateAt` / `HistoricState` from a header to `(root, number, time)` and restores an empty-state fallback in the txpools. Bor never took the header form: its `StateAt(root)` carries the pipelined-import FlatDiff overlay, and Bor's three txpools already fall back to `StateAt(types.EmptyRootHash)` via `PostExecState`. So for Bor the PR is signature churn only; the test changes are mock signatures. |
| `core/blockchain.go`, `core/blockchain_test.go` | 3 | #35844 | **Declined (kept Bor).** A cleanly merged `historyPruningRequired` helper was removed. | Extends #35837's history-cutoff restart path, declined in batch 1. |
| `params/bootnodes.go` | 2 | #35682 | Took upstream's `MainnetBootnodes` (EF NodeOps ENRs plus the 4 legacy enodes). | Ethereum mainnet's list, used only by `cmd/utils` / `cmd/devp2p`. Bor networks use `BorMainnetBootnodes` / `AmoyBootnodes` from the chain presets, which are unchanged. Drops 4 enodes upstream removed earlier. |
| `cmd/devp2p/internal/ethtest/suite.go` | 3 | #35818 | Kept Bor. | Changes `makeBlobTxs` for the eth/72 tests Bor removed (decision D). |
| `triedb/pathdb/history_reader_test.go`, `version/version.go` | 2 | #35839, #35847 | Taken. | Upstream-owned; version is now 1.17.7 stable. |

## Milestone v1.17.7 triage (21 upstream PRs)

Classes: inert (no Bor-facing surface), needs-wiring (a Bor surface left to
connect, recorded in `needs-wiring.md`) and consensus-relevant (in
`fork-register.md`).

| PR | Title | Outcome | Class |
| -- | ----- | ------- | ----- |
| #35771 | version: start v1.17.7 cycle | taken | inert |
| #35723 | downloader: block range announcement | declined | needs-wiring (recommended follow-up port) |
| #35782 | build: fix PPA builder | taken, merged with Bor's 1.24 builder | inert |
| #35749 | snap: BAL catch-up for the trie | declined (decision C) | consensus-relevant (EIP-8189, deferred) |
| #35772 | core: BAL encoding pipeline | declined | consensus-relevant (EIP-7928, deferred) |
| #35805 | devp2p: blob availability test | kept Bor (eth/72 removed) | inert |
| #35780 | downloader: prioritize head access lists | declined (decision C) | needs-wiring (with BAL downloading) |
| #35792 | history pruning cutoffs | declined | needs-wiring (with #34036) |
| #35789 | pathdb: release clean caches when disabling | taken | inert |
| #35773 | gas pool availability (EIP-8037) | adopted; Bor callers ported | consensus-relevant (dormant), needs-wiring (post-Amsterdam review of Bor's pool reads) |
| #35821 | wait for prefetchers on shutdown | ported to Bor's prefetcher | inert (shutdown only) |
| #35823 | filtermaps test hang | taken | inert |
| #35807 | dependabot grouping | not taken | inert (Bor CI is its own) |
| #35837 | snap restart before the history cutoff | declined | needs-wiring (with #34036) |
| #35582 | txpool empty-state fallback | kept Bor (already has the fallback) | inert |
| #35835 | spec-test fixtures v21 | EIP-2935 unchecked call taken; EIP-8037 total cap adopted dormant; `Finalize` / withdrawals move declined | consensus-relevant (both rows in the fork register) |
| #35682 | NodeOps bootnodes | taken (Ethereum `MainnetBootnodes` only) | inert: Bor networks use their presets' bootnodes |
| #35839 | pathdb flaky test | taken | inert |
| #35818 | devp2p blob tx helper | kept Bor | inert |
| #35844 | sync restart after unclean shutdown | declined | needs-wiring (with #34036) |
| #35847 | version: release 1.17.7 | taken | inert |

No PR in v1.17.7 adds a CLI flag, config field, RPC method or metric that
the merged code exposes. The ones that would have (#35792's
`--history.chain` values, #35723's `peers/outofrange` gauge) were declined.
Every fork/EIP-bearing PR has a fork-register row, and none sets a fork
value on a Bor network.
