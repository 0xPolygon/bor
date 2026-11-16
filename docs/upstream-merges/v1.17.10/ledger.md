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

## Batch 3: `v1.17.7..8fe53204a` (v1.17.8, 16 commits)

16 conflicted paths. No hardfork-surface file changed.

| Path | Class | Upstream PR | Decision | Why |
| ---- | ----- | ----------- | -------- | --- |
| `eth/protocols/eth/{handler,handlers,handler_test}.go` | 1 | #35857 | **Kept Bor (already bounded).** | Bor already caps `GetPooledTransactions` lookups per request at `maxPooledTxsServe = 256`, tighter than upstream's new 4096, and tests it (`peer_replies_test.go`, `peer_limits_test.go`). Upstream's test targets its own constant and wasn't taken. |
| `eth/protocols/eth/{receipt,receipt_test}.go` | 1 | #35859 | **Kept Bor (not applicable).** | Upstream's fix is in `ReceiptList.Derivable`, from the delayed-decode refactor (#33835) Bor deferred in v1.17.4. Bor's `ReceiptList68/69.DecodeRLP` decodes every receipt as the message arrives and rejects the whole response if one fails, so an undecodable receipt never reaches root derivation. |
| `eth/protocols/snap/handler.go` (`snap/handlers.go` kept deleted) | 1 | #35856 | **Ported.** `loads += max(resolved, 1)` at both `GetNode` sites in Bor's `ServiceGetTrieNodesQuery`. | Bor's snap serving lives in `handler.go`, after develop's rewrite. It had the same accounting, where a node served from the cache counted 0 toward `maxTrieNodeLookups`. |
| `core/blockchain.go` (rewind) | 2 | #35288 | Merged both: Bor's `DeleteBorReceipt` / `DeleteBorTxLookupEntry` plus upstream's `DeleteAccessList`. | Bor stores access lists through `rawdb.WriteAccessList` for blocks that carry one (Amsterdam), so a rewind should remove them too. A no-op on today's chains. |
| `core/txpool/blobpool/blobpool.go` (+ test) | 2 | #35836 | **Adopted.** The promotion loop is placed after Bor's checked-base-fee block. | Bor has the same `gapped` reorder buffer. Without this, gapped blob transactions whose missing nonce is included on chain stay stuck until they expire. |
| `go.mod`, `go.sum` | 2 | #35863 | `pion/stun/v3` v3.1.5 → v3.1.7, `pion/dtls/v3` v3.1.5, `pion/transport/v4` v4.1.0 (upstream's). Bor's newer `golang.org/x/*` kept. `go mod tidy`. | Higher version per module. |
| `core/types/bal/bal_lookup.go` (kept deleted), `core/state/reader_eip_7928.go` | 3 | #35865 | Kept Bor. | Part of the #35264 parallel-execution cluster declined in v1.17.6. |
| `cmd/devp2p/internal/ethtest/{conn,suite,suite_test}.go` | 3 | #35826, #35869 | Kept Bor, including code that merged clean. | eth/71 and eth/72 test plumbing; Bor carries neither protocol (decision D). |
| `README.md` | 1 | #35711 | Kept Bor. | Bor's README is its own. |
| `p2p/discover/v4_udp.go` | — | #35861 | Unchanged. | Already in the base, identical (`6848e4008`). |
| `p2p/dial.go` (+ `dial_dns_test.go`), `internal/ethapi/capabilities.go` (+ test), `beacon/params/networks.go`, `build/ci.go`, `version/version.go` | 2 | #35880, #35878, #35876, #35854, #35848 | Taken (clean). | Upstream-owned. |

## Batch 4: `8fe53204a..a57907700` (v1.17.8, 16 commits; completes v1.17.8)

15 conflicted paths. Hardfork surfaces: `params/config.go` (conflicted,
resolved to Bor, unchanged) and `core/state_transition.go` (`preCheck`
flags; behaviour change only behind Amsterdam).

| Path | Class | Upstream PR | Decision | Why |
| ---- | ----- | ----------- | -------- | --- |
| `params/config.go`; new `params/chainspecs.go` and `params/chainspecs/*.json` dropped | 3 (hardfork surface) | #35585 | **Declined (kept Bor).** `params/config.go` is byte-identical to before. | Upstream replaces the hand-written Ethereum chain configs with JSON chainspecs that carry the time-based forks. Bor's Ethereum presets deliberately drop the post-merge time-based forks, because Bor's fork model is block-based. Bor networks don't use these presets. |
| `core/state_transition.go`, `core/state_prefetcher.go`, `accounts/abi/bind/backends/simulated.go`, `internal/ethapi/{api,transaction_args}.go`, `eth/tracers/api.go` | 3 | #35845 | **Adopted.** `Message.SkipNonceChecks` / `SkipTransactionChecks` become the `SkipNonceCheck()` / `SkipEOACheck()` / `SkipGasLimitCapCheck()` setters, plus `SkipExecutionGasCapCheck()`. Bor's three remaining users were ported. `preCheck` keeps Bor's Madhugiri arm under `skipGasLimitCapCheck`, with the EOA check now its own block, as upstream. | The new behaviour (`eth_call`, `debug_traceCall` and non-strict `eth_simulateV1` get the whole gas limit for execution) is only reached when `IsAmsterdam`. Before Amsterdam, every check runs exactly as before; RPC messages still skip the EOA and cap checks together. |
| `core/state_transition.go` (fee payment, witness access) | 1 | #35668 | **Kept Bor.** | Bor's fee payment is its own consensus code (burnt-contract redirect, `noFeeBurnAndTip`). Upstream's change only concerns `NoBaseFee` simulation. |
| `internal/ethapi/simulate.go` | 3 | #35617, #35668, #35845 | **Adopted** on Bor's `IsAmsterdam(number)`: no synthesized transfer logs after Amsterdam (EIP-7708 emits them); the uncapped execution reservation in non-strict mode; the slot number carried forward only when the parent has one. | All dormant: Amsterdam is nil and Bor headers carry no slot number. |
| `internal/ethapi/api_test.go` | 1 | #35617, #35845 | `TestSimulateV1TransferLogs` (#35617) adopted with three Bor changes: the generator marks blocks post-merge (`SetPoS`, as the neighbouring test does); Bor's `0x…1010` fee-contract transfer log is excluded from the count; the post-Amsterdam case is skipped under POS-3738, like `TestEstimateGasAmsterdam`. Upstream's `TestExecutionGasCapAmsterdam` was not taken (coverage gap, see needs-wiring). | Bor's chain can't start with Amsterdam from genesis yet (POS-3738). The test exposed that, before Amsterdam, `eth_simulateV1` with `traceTransfers` already reports a Bor value transfer twice: the synthesized log and Bor's fee-contract log. Pre-existing, recorded in needs-wiring. |
| `eth/tracers/api_test.go` | 1 | #35845 | Kept Bor. | Upstream's new test is an Amsterdam test on a time-based config (coverage gap, with the above). |
| `core/jumpdest.go` (kept deleted), `core/vm/jumpdests.go`, `core/vm/evm.go` (a cleanly merged wrapper removed) | 3 | #35881 | **Declined (kept Bor).** | Upstream puts a per-transaction layer in front of its bounded, evicting shared cache, so eviction can't force re-analysis within a transaction. Bor's shared cache is an unbounded per-block `syncMapJumpDests` that never evicts, so that can't happen. Same reason as #35681 in v1.17.6. |
| `eth/protocols/eth/{handlers,handler,handler_test}.go` (a cleanly merged receipt check removed), `eth/handler_eth.go` | 1 | #35862, #35904, #35860, #35883 | **Kept Bor (not applicable).** | #35862 and #35904 bound item counts in `rlp.RawList` bodies and receipts, from the delayed-decode refactor (#33835) Bor deferred. Bor's `BlockBody` and receipt lists are decoded into typed slices as the message arrives, so a body can't claim more items than its bytes. #35860 / #35883 are eth/72 cells, which Bor doesn't carry. |
| `eth/catalyst/{api,api_test}.go` | 1 | #35804 | Kept Bor. | `eth/catalyst` is inherited and unused in Bor's consensus, and Bor's file has an older shape. |
| `cmd/devp2p/internal/ethtest/suite.go` | 3 | #35860 | Kept Bor. | eth/72 test. |
| `core/tracing/journal.go` (+ test), `beacon/light/request/*`, `accounts/abi/abigen/source.go.tpl`, `node/node_auth_test.go`, `internal/ethapi/api.go` (`eth_createAccessList` fee defaults, #35397), `version/version.go` | 2 | #35896, #35872, #35888, #35889, #35397 | Taken (clean). | Upstream-owned; `version` is now 1.17.8 stable. |

## Milestone v1.17.8 triage (32 upstream PRs)

| PR | Title | Outcome | Class |
| -- | ----- | ------- | ----- |
| #35848 | version: start v1.17.8 cycle | taken | inert |
| #35826 | devp2p: advertise eth/71 | kept Bor | inert (Bor carries no eth/71) |
| #35857 | getPooledTransactions lookup limit | kept Bor (already capped at 256) | inert |
| #35859 | don't skip invalid receipts | kept Bor (eager receipt decoding) | inert |
| #35836 | blobpool: promote gapped txs on reset | adopted | inert |
| #35856 | snap: charge trie-node lookups per request | ported to Bor's `snap/handler.go` | inert |
| #35869 | devp2p: negotiated eth version in tests | kept Bor | inert |
| #35876 | Hoodi beacon genesis time | taken | inert |
| #35861 | discovery: bonded peer for address prediction | already in base | inert |
| #35854 | PPA for Ubuntu 26.04 | taken (`gofmt` alignment fixed in the milestone commit) | inert |
| #35880 | p2p: stale addresses after DNS resolution | taken | inert |
| #35878 | `eth_capabilities` receipts bounded by the tx index | taken | inert |
| #35865 | BAL lookup codeHash cache | kept Bor (#35264 cluster) | consensus-relevant (EIP-7928, deferred) |
| #35863 | pion/stun v3.1.7 | taken | inert |
| #35711 | README | kept Bor | inert |
| #35288 | delete the access list on rewind | adopted | inert (no-op before Amsterdam) |
| #35585 | chainspecs | declined | consensus-relevant (never-enable, fork register) |
| #35872 | beacon light: EvCanRequestAgain | taken | inert |
| #35845 | skip the execution gas cap on `eth_call` | adopted; Bor callers ported | consensus-relevant (dormant), needs-wiring (tests) |
| #35881 | JUMPDEST per-transaction cache | declined | needs-wiring (wontfix) |
| #35617 | `eth_simulateV1` transfer logs post-Amsterdam | adopted; test adapted | consensus-relevant (dormant), needs-wiring (pre-existing double log) |
| #35397 | `eth_createAccessList` fee defaults | taken | inert |
| #35668 | `eth_simulateV1` Amsterdam blocks | simulate part adopted; fee hunk kept Bor | consensus-relevant (dormant) |
| #35860 | cells response index major | kept Bor (eth/72) | inert |
| #35883 | getCells lookup limit | kept Bor (eth/72) | inert |
| #35862 | verify block shape before hashing | kept Bor (eager body decoding) | inert |
| #35896 | tracing: creator nonce change without a frame | taken | inert |
| #35888 | abigen: chained library nonces | taken | inert |
| #35889 | node: flaky JWT test | taken | inert |
| #35804 | catalyst: forward canonical update | kept Bor (catalyst unused) | inert |
| #35904 | receipt size checks | kept Bor (eager receipt decoding) | inert |
| (version) | go-ethereum v1.17.8 stable | taken | inert |

v1.17.8 adds no CLI flag, config field, RPC method or metric that the merged
code exposes. Every fork/EIP-bearing PR has a fork-register row, and none
sets a fork value on a Bor network.

## Batch 5: `a57907700..365bf9706` (v1.17.9, 19 commits)

25 conflicted paths. No hardfork surface is touched. The live-path changes are
the pebble L0 file size (#35691), the fixed-size freezer metadata (#35939),
background history writes in pathdb (#35949, #35936) and the tracer state view
(#35905).

| Path | Class | Upstream PR | Decision | Why |
| ---- | ----- | ----------- | -------- | --- |
| `ethdb/pebble/pebble.go`, `ethdb/pebble/pebble_v1.go` | 2 (clean) | #35691 | **Adopted on both paths.** The L0 target file size goes from 2 MB to 16 MB; the base level stays at 4 MB. | Bor's tuning (#2170, kept on the v1 path in `0166dcc9db`) is in the write path (`BytesPerSync`, WAL, listeners, gauges) and uses the same L0 thresholds as upstream, so the change applies unchanged. It reaches every existing node, since they all open through `pebble_v1.go`. Upstream measured faster flushes and fewer memtable stalls, but read amplification rises from about 6 to 16; check import time and read amplification on the dev node and Amoy before release. |
| `core/rawdb/freezer_meta.go`, `freezer_table.go` (+ tests) | 2 (clean) | #35939 | **Adopted.** One upstream test call adapted to Bor's `newBatch(offset)`. | Metadata files are now padded to 32 bytes and rewritten in place, so a power loss can't leave a truncated file. Older binaries decode the RLP prefix and ignore the padding; the round trip new → v2.10.3 → new was rerun with real binaries (batch report). Independent of Bor's `bals` fill (`8ea7bf721`), which is in `freezer.go`. |
| `triedb/pathdb/buffer.go` | 3 | #35949 | **Merged both.** Upstream's `syncHistory func() error` replaces the freezer list; Bor's `nodesCache *AddressBiasedCache` is kept. | Upstream writes histories and root→id lookups on a background writer; the buffer flush waits for it before persisting. Bor's address-biased node cache is orthogonal. |
| `triedb/pathdb/database.go` (`Disable`) | 3 | #35949 | **Merged both:** Bor's removal of stale address-cache snapshots, then upstream's wait for scheduled writes. | Both are needed when a state sync starts. |
| `triedb/pathdb/database.go` (`Enable`, two hunks) | 3 | #35936, #35949 | **Took upstream.** | Stops the history indexers and removes the layer journal before the histories are purged, and waits for scheduled writes; fixes reactivation after a state sync. Bor's text there was a comment only. |
| `triedb/pathdb/history_reader.go` | 3 | #35949 | **Merged both:** Bor's `mu sync.RWMutex` and upstream's `wait`. | Bor's lock protects its reader map; upstream's `wait` makes history reads wait for background writes. |
| `triedb/pathdb/database_test.go` | 1 | #35936 | Took upstream's `TestReactivationCrash` and its helpers; `TestAdoptSyncedState` stays out. | `AdoptSyncedState` (#34874) was declined in v1.17.4 with snap/2. |
| `triedb/pathdb/{async_writer,disklayer,reader,journal,history}.go` (+ tests) | 2 (clean) | #35949, #35936 | Taken. | Checked against Bor: there is one `Database` constructor (it creates the writer), every root→id reader in the tree waits for scheduled writes, and Bor's `Close` saves its address caches before the writer is closed. Witness generation reads trie nodes from layers, not histories, so it isn't affected; the witness regeneration tests were rerun non-short. |
| `core/state/statedb_tracer.go` (new), `core/vm/evm.go`, `core/state/statedb_hooked.go`, `core/tracing/hooks.go` | 2 (clean) | #35905 | **Adopted.** | `GetVMContext` now hands tracers a read-only view that suspends block-level access list recording (EIP-7928, Amsterdam). The wrapping is live for every tracer, but no Bor code type-asserts the tracer's `StateDB`, and the view only clears `stateAccessList`, so witness recording is unchanged. |
| `eth/tracers/api.go` | 1 | #35905 | Kept Bor. | Upstream only moved blank lines around `defer evm.Release()`; Bor's EVM has no `Release` (declined in v1.17.4). |
| `beacon/**` (`block_sync_test.go`, `beacon_block.go`, `exec_header.go`, `exec_payload.go` conflicted; `header.go` taken from upstream) | 1 | #35385, #35956 | **Took upstream.** | Gloas support for the beacon light client, which moves from `protolambda/zrnt` to `attestantio/go-eth2-client`. Bor doesn't run the beacon light client; its only divergence in these files was import grouping, which left an unused import in `header.go`. |
| `go.mod`, `go.sum`, `cmd/keeper/go.{mod,sum}` | 1 | #35385, #35927 | Union, higher version on each clash, then `go mod tidy`. | New: `attestantio/go-eth2-client` v0.30.1 (with `pk910/dynamic-ssz`, `pk910/hashtree-bindings`, `OffchainLabs/go-bitfield`, `goccy/go-yaml`) for `beacon/`; `klauspost/cpuid/v2` v2.3.0. `protolambda/ztyp` becomes indirect. |
| `cmd/geth/{main,config,chaincmd}.go`, `cmd/utils/flags.go` | 2 | #35673 | **Took upstream** (`utils.MetricsFlags`, `utils.SetMetricsConfig`), keeping `utils.BorFlags`. Dropped the `utils.SnapV2Flag` reference upstream moved into the node flags. | `SetMetricsConfig` is Bor's `applyMetricConfig` moved to `cmd/utils`, with the same checks. snap/2 was declined (decision C). |
| `p2p/discover/{v4_udp,v5_udp,metrics}.go` | 2 | #35673 | Took upstream. | Discovery listener metrics. |
| `triedb/pathdb/buffer_test.go` | 1 | #35949 | Bor's three carry-over tests (#2170) build a `Database` by hand; they now set `writer: newAsyncWriter()` too. | The disk layer now submits history writes to the background writer, which upstream's constructor creates. |
| `internal/ethapi/api_test.go` | 1 | #35842, #35841 | `TestSimulateV1NonceLimit` and `TestSimulateV1LogIndex` adopted; their generators mark blocks post-merge (`SetPoS`), as in batch 4. | Without it, block 0 fails Bor's difficulty check. The log-index test passes with Bor's fee-contract log unchanged. |
| `eth/protocols/snap/{bal_apply,syncv2}*.go` (modify/delete) | 3 | #35890, #35930 | Kept Bor's deletion. | Both fixes are in snap/2, which Bor doesn't carry. |
| `.github/workflows/go.yml` (modify/delete), `build/checksums.txt` | 1 | #35927, #35924 | Kept Bor. | Bor's CI and spec-test pin (`v5.1.0`) are its own. |
| `cmd/devp2p/internal/ethtest/suite.go` | 1 | #35902, #35901 | Kept Bor. | eth/72 tests. |
| `internal/ethapi/{transaction_args,simulate,logtracer}.go` (+ tests), `core/blockchain.go`, `version/version.go` | 2 (clean) | #35914, #35842, #35841, #35952 | Taken. | #35914 is live: `eth_call` and friends now reject EIP-1559 fee fields for blocks before London (mainnet 23,850,000). `eth_simulateV1` enforces the nonce limit and numbers logs like receipts. `version` is 1.17.9 unstable. |

## Batch 6: `365bf9706..9dffe6e97` (v1.17.9, 19 commits; completes v1.17.9)

13 conflicted paths. **#35638 is deferred** (operator decision, 2026-11-11;
see needs-wiring). Hardfork surfaces: `params/config.go` conflicted and is
byte-identical to before; `core/state_transition.go` gains #35928's
pre-Cancun check, which no real block can reach.

| Path | Class | Upstream PR | Decision | Why |
| ---- | ----- | ----------- | -------- | --- |
| `core/vm/{evm,interpreter,instructions,instructions_test,jump_table}.go`, `go.mod`, `oss-fuzz.sh` (Bor's side kept); `core/vm/gen/`, `core/vm/genspec.go`, `core/vm/interpreter_gen{,_test}.go`, `cmd/geth/default.pgo`, `tests/fuzzers/evm/` (dropped) | 3 (EVM) | #35638 | **Deferred (kept Bor).** No other commit in the batch touches these files, so they are byte-identical to before. | Performance only, and it would make the generated switch the always-on interpreter, generated from fork tables whose Amsterdam ordering is still open (POS-3738). Bor's `bor` binary builds with `-pgo=prod.pprof`, so upstream's `default.pgo` wouldn't apply. Port plan in needs-wiring. |
| `params/config.go` | 3 (hardfork surface) | #35975 | **Kept Bor.** | Upstream reorders the time-based BPO forks after Amsterdam in the fork-order check. Bor's check is block-based and lists no BPO forks. |
| `params/chainspecs/hoodi.json` (modify/delete), `core/forkid/forkid_test.go` | 1 | #35974 | Kept Bor (chainspecs stay out; Bor's forkid cases kept). | Schedules Amsterdam on Hoodi, an Ethereum testnet; chainspecs were declined in batch 4 (#35585). Upstream reverts it in batch 7 (#36001). |
| `core/types/transaction.go` (+ tests), `core/txpool/legacypool/legacypool_test.go` | 2 (clean) | #35988 | **Adopted (live).** | When the fee cap is below the base fee, `EffectiveGasTipIntCmp` now returns -1 instead of comparing the full tip cap. Bor's only live caller is the legacy pool's `Pending` filter (`MinTip`), which stops returning such transactions to the miner; they couldn't be included anyway. `EffectiveGasTipCmp` already fell back to big-int on error. `totalFees` and the gas price oracle only read included transactions, which always cover the base fee. |
| `core/state_transition.go`, `internal/ethapi/{api,transaction_args}.go` | 2/3 | #35928 | **Adopted.** In `api.go`, Bor's `Call` → `CallWithState` split is kept, and upstream's `checkBlobFeeCap` runs first in `Call`. | `preCheck` now rejects a blob fee cap before Cancun. Only blob transactions carry one, and they were already rejected before Cancun because they always have blob hashes, so no block is affected. The RPC check rejects `maxFeePerBlobGas` without blob hashes. |
| `miner/miner.go` | 1 | #35968 | **Kept Bor (not applicable).** | Upstream's `getPending` doesn't exist in Bor; Bor's pending block comes from `worker.pendingBlock()`. |
| `eth/downloader/downloader{,_test}.go` (modify/delete) | 1 | #35982 | Kept Bor's deletion. | Frozen-pivot validation for snap/2 trie generation; Bor syncs through `bor_downloader.go` and doesn't carry snap/2. |
| `eth/filters/filter_system_test.go` | 1 | #35933 | Merged both: Bor's pending-logs case stays `10`, upstream's earliest..earliest case is `11`. | Different cases with the same index. |
| `build/checksums.txt` | 1 | #35935 | Kept Bor (Go 1.26.10). | Bor keeps its toolchain until its CI moves. |
| `triedb/pathdb/reader.go` (+ tests) | 2 (clean) | #35978 | Taken. | Historical state reads retry when the disk layer turns stale mid-read. No Bor change in the file. |
| `core/txpool/legacypool/legacypool_test.go` | 1 | #35988 | Upstream's new pending-filter test calls Bor's `Pending(filter, nil)`, which takes an interrupt flag and returns one value. | It passes against Bor's pool, confirming the live change. |
| `params/config_test.go` | 1 | #35975 | Dropped `TestCheckConfigForkOrderBPO`. | It tests the time-based fork-order change Bor declined. |
| `eth/tracers/logger/logger_test.go` | 1 | #35961 | `TestHaltedStoreCapture` runs with Hampi active (`AllEthashProtocolChanges` with `HampiBlock` 0, as Bor's gas tests do). | Before Hampi, Bor's SSTORE gas function deliberately reads the slot and leaves write protection to `opSstore`, for stateless nodes' witnesses, so the halted step reaches the tracer without an error and the logger still records its storage. Bor's witness behaviour is unchanged; until Hampi activates, the struct logger shows storage on an SSTORE refused in a static call, as it did before. |
| `internal/ethapi/api_test.go` | 1 | #35928, #35953 | `TestCreateAccessListBlobFeeDefault` skipped: its contract uses `BLOBBASEFEE`, which Bor never enabled (EIP-7516 is commented out in Bor's Cancun table). `TestSimulateV1DifficultyOverride` marks blocks post-merge (`SetPoS`) for the merged config, as in batches 4 and 5. | Coverage gap only for the default blob fee in `eth_createAccessList`; the check itself is adopted. |
| `core/filtermaps/*`, `core/rawdb/accessors_indexes.go`, `core/types/{tx_setcode,gen_authorization}.go`, `eth/filters/filter_system.go`, `eth/tracers/logger/logger.go`, `internal/ethapi/{simulate,override/override}.go`, `beacon/light/request/server.go`, `cmd/utils/flags.go`, `version/version.go` (+ tests) | 2 (clean) | #35875, #35761, #35933, #35961, #35953, #35969, #35957, #35907, #35958, #35990 | Taken. | Upstream-owned. #35761 (yParity validation) only applies to authorizations decoded from JSON. #35907 is `cmd/geth` flag wiring; Bor configures through `internal/cli`. `version` is 1.17.9 stable. |

## Milestone v1.17.9 triage (36 upstream PRs, plus 2 version commits)

| PR | Title | Outcome | Class |
| -- | ----- | ------- | ----- |
| #35890 | snap: verify raw BAL before decode | kept Bor (snap/2 not carried) | inert |
| #35691 | pebble: 16 MB L0 files | adopted, both paths | **live (storage performance)**: measure import time and read amplification during the dev-node and Amoy soak |
| #35927 | golang lint update | kept Bor's CI | inert |
| #35924 | spec-test fixtures v21.0.1 | kept Bor's pin | inert |
| #35930 | snap: pivot ancestry check | kept Bor (snap/2 only) | inert |
| #35901, #35902 | devp2p cell tests | kept Bor (eth/72) | inert |
| #35939 | freezer metadata at a fixed size | adopted | **live (storage)**: real-binary round trip new → v2.10.3 → new passed |
| #35936 | pathdb reactivation after state sync | adopted | live (state sync only) |
| #35842, #35841 | `eth_simulateV1` nonce limit, log numbering | taken | live (RPC) |
| #35905 | tracer state view without BAL recording | adopted | live wrapper, dormant effect (EIP-7928) |
| #35385, #35956 | Gloas beacon light client | taken | inert (Bor doesn't run the beacon light client); adds `attestantio/go-eth2-client` |
| #35673 | discovery listener metrics | taken | live (metrics only) |
| #35914 | reject EIP-1559 fields in calls before London | taken | live (RPC) |
| #35952 | bad-block report formatting | taken | inert |
| #35949 | pathdb histories written in background | adopted, merged with Bor's address-biased cache | **live (state storage)**: witness regeneration and BlockSTM replays unchanged |
| #35953 | `eth_simulateV1` override validation | taken | live (RPC) |
| #35933 | earliest `toBlock` in log filters | taken | live (RPC) |
| #35907 | transaction history decoupled from archive mode | taken (`cmd/geth` flags only) | inert for `bor` (`internal/cli` config) |
| #35935 | `-dlgo` Go 1.27.2 | kept Bor (Go 1.26.10) | inert |
| #35957 | beacon light request timeout | taken | inert |
| #35961 | struct logger omits storage on halted steps | adopted | live (tracing); before Hampi, SSTORE in a static call still shows storage |
| #35958 | doc comment | taken | inert |
| #35968 | miner pending timestamp | kept Bor (no `getPending`) | inert |
| #35974 | Amsterdam on Hoodi | kept Bor (no chainspecs) | inert |
| #35978 | pathdb historical reads retry on stale layer | taken | live (historical state reads) |
| #35875 | log index starts past the history cutoff | taken | live (log index) |
| #35761 | yParity validation for JSON authorizations | taken | live (RPC decoding) |
| #35928 | blob fee cap without blob hashes | adopted | consensus path, unreachable for real blocks; live (RPC) |
| #35982 | snap/2 frozen pivot validation | kept Bor (deleted `downloader.go`) | inert |
| #35988 | effective tip below the base fee | adopted | **live (txpool → miner)**: no change to block contents |
| #35975 | BPO forks after Amsterdam | kept Bor (block-based fork order) | inert |
| #35969 | access list kept with `gasPrice` | taken | live (RPC) |
| #35638 | generated switch interpreter with PGO | **deferred** (needs-wiring) | would be live (EVM dispatch); performance only |

Bor wiring candidates: none new. #35638 is the one deferred item, recorded in
needs-wiring together with #35959 (v1.17.10).

## Batch 7: `9dffe6e97..1df30b18d` (v1.17.10, 9 commits; completes the sync)

22 conflicted paths, almost all from the two declined commits. Hardfork
surfaces: none changed. One live change in transaction decoding (#36003),
with a Bor fix.

| Path | Class | Upstream PR | Decision | Why |
| ---- | ----- | ----------- | -------- | --- |
| `core/types/transaction.go` | 3 (consensus: block and transaction decoding) | #36003 | **Adopted, with a Bor fix.** `validateDecoded` skips the chain ID size check when a transaction type has no chain ID. | Upstream rejects decoded transactions whose V, R, S or chain ID exceed 256 bits. Bor's `StateSyncTx.chainID()` returns nil, and calling `BitLen()` on it panics, so as merged, decoding any block body that carries state-sync transactions (every state-sync sprint since Madhugiri) would crash the node. Confirmed with a throwaway probe test, then fixed. State-sync signature values are zero, so they still pass the remaining checks. No valid transaction on mainnet or Amoy can exceed 256 bits in these fields (sender recovery and the chain ID check already reject them), so nothing on chain is newly rejected. |
| `core/types/transaction_signing.go` | 2 | #36003 | Kept Bor's signer types; upstream's `errInvalidChainID` helper is used by `EIP155Signer`, which merged cleanly. | Bor never adopted upstream's `modernSigner`; the conflict was only the error wording inside it. |
| `cmd/evm/testdata/17/exp.json` | 1 | #36004 | Taken. | The t8n expectation now reports the oversized values at decode time. |
| `core/vm/stack.go`; `core/vm/gen/*`, `core/vm/interpreter_gen{,_test}.go`, `cmd/geth/default.pgo` (modify/delete) | 3 (EVM) | #35959 | **Deferred (kept Bor).** | Follow-up to #35638, deferred in batch 6 (needs-wiring). |
| `cmd/utils/flags.go`, `eth/{backend,handler}.go`, `eth/ethconfig/{config,gen_config}.go`, `eth/downloader/statesync.go`, `eth/protocols/snap/{handler,protocol}.go`, `cmd/devp2p/internal/ethtest/suite_test.go`; `eth/downloader/downloader.go`, `eth/protocols/snap/syncer.go` (modify/delete) | 3 | #35989, #35998 | **Declined (kept Bor).** | Auto-enables snap/2 an hour after Amsterdam and decouples snap/2 serving from the syncer choice. Bor doesn't carry snap/2 (decision C, v1.17.6). Also keyed on `AmsterdamTime`, which Bor doesn't use. |
| `core/forkid/forkid_test.go`, `params/chainspecs/hoodi.json` (modify/delete) | 1 | #36001 | Kept Bor. | Reverts #35974, which wasn't taken. |
| `build/checksums.txt` | 1 | #35985 | Kept Bor (Go 1.26.10). | Bor's toolchain. |
| `version/version.go` | 2 | #35991, #36002 | Taken. | 1.17.10 stable. |

## Milestone v1.17.10 triage (7 upstream PRs, plus 2 version commits)

| PR | Title | Outcome | Class |
| -- | ----- | ------- | ----- |
| #35989 | auto-enable snap/2 with Amsterdam | declined (snap/2 not carried) | inert |
| #35959 | fold DUP/SWAP into shared cases | deferred with #35638 | would be live (EVM dispatch) |
| #35985 | `-dlgo` Go 1.27.3 | kept Bor | inert |
| #35998 | snap.v2 help text | declined | inert |
| #36001 | revert Amsterdam on Hoodi | kept Bor | inert |
| #36003 | stricter transaction field validation | adopted with the nil chain ID guard | **live (block and transaction decoding)** |
| #36004 | t8n test fixture | taken | inert |

Bor wiring candidates: none new.
