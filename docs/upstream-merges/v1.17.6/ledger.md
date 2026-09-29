# Upstream merge ledger: go-ethereum v1.17.6

One row per resolution. Class 1 is a Bor-owned surface, class 2 is upstream-owned, and class 3 is semantic, hardfork-surface or non-content. Upstream PR numbers refer to go-ethereum.

## Batch 1: `v1.17.5..a235d2819` (commits 1–20)

32 conflicted files: 24 content and 8 modify/delete. 21 hunks were resolved mechanically, where exactly one side differed from the merge base.

### Consensus-relevant and hardfork-surface resolutions (read first)

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `params/protocol_params.go` | 3 (hardfork surface) | #35458 | Took upstream: `EthBurnLogEvent` removed. | EIP-7708 no longer defines a burn log. Bor has no user of the constant. The clean-merged hunks are #35454's EIP-2780/8038 parameter updates (`TxValueCost2780` 4244 → 6000, `TransferLogCost2780` removed, access-list costs 3000 → 2900); each is read only under `rules.IsAmsterdam`. |
| `core/vm/contracts.go` | 3 (hardfork surface) | #35388 | Kept Bor's case list; each return becomes `&PrecompiledContracts…`. | `activePrecompiledContracts` now returns a pointer to the package-level set, which the precompile cache uses as the set's identity. The set returned per fork is unchanged, including Bor's Chicago, LisovoPro, Lisovo, MadhugiriPro and Madhugiri branches and `IsVerkle` (Bor's name for the fork, per #2343). |
| `core/state_processor.go` `PreExecution` | 3 | #35458 | Kept Bor's signature (no BAL return); dropped the EIP-7997 activation-block insertion (`misc.ApplyEIP7997`). | The EIP-7997 update removes the irregular state transition and `consensus/misc/eip7997.go`. Amsterdam-only, so dormant. |
| `core/chain_makers.go`, `cmd/evm/internal/t8ntool/execution.go` | 2 | #35458 | Dropped the same EIP-7997 insertion. | As above. `chain_makers` also takes upstream's move of the EIP-4788 call to after the generator callback (so an explicit `SetParentBeaconRoot` is honoured), using Bor's two-argument `ProcessBeaconBlockRoot`. |
| #35264 cluster: `core/state_processor_parallel.go`, `core/types/bal/bal_lookup.go`, `core/state/statedb_eip_7928.go` (+ test), `core/state/reader_eip_7928.go`, `TestParallelReservationOverflowRejected` | 3 | #35264 | **Declined.** New files removed, and `reader_eip_7928.go` restored to Bor's version. | Upstream's BAL-driven parallel executor is built on block-access-list types that v1.17.5 declined (`bal_lookup.go` needs `AccountAccess.StorageChanges` and the encoding fields `BlockAccessIndex`/`PostBalance`/`PostNonce`/`NewCode`). Adopting it dormant would first mean re-adopting those BAL spec updates. Recorded in needs-wiring with POS-3737. |
| `core/state_processor.go` `Process`, `core/blockchain.go` (6 hunks), `core/types.go` | 1 | #35264, #35388 | Kept Bor. | Bor has its own `Process` signature and `processBlock`, and never took upstream's `ExecuteConfig`/`setupExecutionState`. The parallel-execution dispatch is not added, because #35264 is declined. Hunk 6 of `blockchain.go` is Bor's witness-metrics reporting, which is on the witness path, so kept as is. |

### Precompile result cache (#35388) — decision B

Adopted, and not wired. `core/vm/precompile_cache.go`, `EVM.SetPrecompileCache` and the new `RunPrecompiledContract(..., cache)` argument come in. Bor's `runPrecompile` / `runEcrecoverWithCache` pass `evm.precompileCache` through. Nothing on Bor sets the cache, so it stays nil and behaviour is unchanged. Bor's ecrecover cache (`vm.Config.EcrecoverCache`) is unchanged.

Call sites kept on Bor's side because the cache isn't wired:

| File | Class | Decision |
| --- | --- | --- |
| `core/vm/evm.go` (4) | 1 | Kept Bor's `runPrecompile` wrapper; its two inner `RunPrecompiledContract` calls pass `evm.precompileCache`. |
| `core/blockchain_reader.go` | 1 | Kept Bor. Bor has no `CodeDB`/`JumpDestCache` accessors, and no `PrecompileCache` accessor is added. |
| `core/state_prefetcher.go`, `core/stateless.go`, `eth/state_accessor.go`, `miner/worker.go` (hunk 3) | 1 | Kept Bor's call shapes. `stateless.go` is on the witness path and is unchanged. |

### Declined protocol features (decision D, as in v1.17.4/v1.17.5)

| File | Class | Upstream | Decision |
| --- | --- | --- | --- |
| `eth/protocols/eth/protocol.go` (2), `handlers.go`, `peer.go` | 1 | #35428 | Kept Bor. The hunks are eth/72 Cells/GetCells and eth/71 BAL message types, which Bor doesn't carry. `peer.go` keeps Bor's eth/70 `ReplyReceiptsRLP70`. |
| `core/txpool/blobpool/cache.go` (+ test) | 3 (modify/delete) | #35439 | Kept Bor's deletion (eth/72 sparse blobpool, declined). |
| `eth/protocols/snap/syncv2.go` (+ test) | 3 (modify/delete) | #35316 | Kept Bor's deletion (snap/2, declined; decision C). |
| `eth/downloader/downloader.go` | 3 (modify/delete) | #35462 | Kept Bor's deletion. Bor uses `bor_downloader.go`, whose `reportSnapSyncProgress` never reads `pivotHeader`, so the race #35462 fixes has no counterpart on Bor. |

### Other resolutions

| File | Class | Upstream | Decision |
| --- | --- | --- | --- |
| `core/vm/gas_table.go` (15), `core/vm/operations_acl.go` (5), `core/vm/interpreter.go` (1 of 2) | 2 | #35457 | Took upstream, mechanically: Bor's side matched the merge base. |
| `core/vm/interpreter.go` (1) | 1 | #35264 | Kept Bor's `Config` fields (switch dispatch, shared caches, ecrecover cache) and added upstream's `DisableParallelExecution`. |
| `core/state/reader.go` | 1 | #35264 | Kept Bor's deletion of `stateReaderWithCache`; Bor has its own concurrent reader. |
| `eth/syncer/syncer.go` | 1 | #35442 | Kept Bor. Bor's version already guards the nil dereference #35442 fixes. |
| `miner/worker.go` (hunks 1–2) | 1 | #35427 | Kept Bor. Upstream's `errStateReadFailure` guard lands in `generateWork`, which Bor doesn't use. **Bor's own sealing path has no `state.Error()` check, so this is recorded in needs-wiring.** |
| `core/eip7928_test.go`, `core/vm/eip8038_test.go`, `miner/payload_building_test.go` | 3 (modify/delete) | #35264, #35457, #35458 | Kept Bor's deletion. `eip7928_test.go` belongs to POS-3737, `eip8038_test.go` is held back under POS-3738, and Bor never carried `payload_building_test.go`. |
| `core/blockchain_test.go` | 1 | #35388 | Kept Bor's `ProcessBlock` call. |
| `core/eip2780_test.go`, `core/vm/eip8037_test.go` | 1 | #35457 | Kept Bor's adaptations (200 authorizations; POS-3738 skips); comments follow the rename. |

### Outside any conflict

- **The #35457 rename reached three Bor-only files that never conflicted:** `core/vm/interpreter_dispatch.go` (Bor's switch interpreter), the PIP-88 twins in `core/vm/operations_acl.go`, and `runEcrecoverWithCache` in `core/vm/evm.go`. The renames are `RegularGas`/`UsedRegularGas`/`ChargeRegular`/`ChargeRegularOnly`/`DrainRegular` to their `Execution` forms, applied only in files the compiler flagged. `interpreter_dispatch.go` was verified rename-only: reversing the rename reproduces the pre-merge file byte for byte. The same rename was applied to `core/vm/contracts_test.go`, `dispatch_test.go` and `evm_precompile_cache_test.go`.

### Found by the test tier

- **#35459 (preimage recording in parallel execution) declined with the #35264 cluster.** Bor's `TestPDBMethodParity` guard flagged its new `StateDB.AddPreimages`, which has no BlockSTM V2 counterpart. Its only caller is upstream's parallel executor, which is declined, so the method is removed rather than exempted.
- **`core/eip8037_test.go`, updated upstream in this batch:**
  - `TestValidationFloorCostCap` uses 600,000 bytes of calldata instead of 300,000, because Bor's `MaxTxGas` is 2^25 against upstream's 2^24.
  - `TestCreate2TransientEmptyDestNoRefill`, `TestCreate2StorageOnlyDestCharged` and `TestBlockBaseFeeUsesMax` are skipped with the POS-3738 reason. All three pass with `Bor = nil`.

## Batch 2: `a235d2819..7924511bf` (commits 21–40)

36 conflicted files: 25 content and 11 modify/delete. The mechanical pass resolved 1 hunk; every other hunk had changes on both sides.

### Consensus-relevant and hardfork-surface resolutions (read first)

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `params/protocol_params.go` | 3 (hardfork surface) | #35486, #35497 | Took upstream (clean merge). | `RegularPerAuthBaseCost` is renamed `ExecutionPerAuthBaseCost` (value unchanged, 7816). EIP-8038 repricing: `AccountWriteAmsterdam` 8000 → 9000, `CallValueTransferAmsterdam` 10300 → 11300, `ColdStorageAccessAmsterdam` 3000 → 2100, `StorageClearRefundAmsterdam` 12480 → 11616, `CreateAccessAmsterdam` 11000 → 12000, `TxAccessListStorageKeyGasAmsterdam` 2900 → 2000. Every reader is Amsterdam-only: the `*8038` / `*8037And8038` gas functions installed by `enable8037And8038` in the Amsterdam jump table, `intrinsicBaseGasEIP2780`, and the `rules.IsAmsterdam` branches of `IntrinsicGas` and the EIP-7702 authorization path. No PIP-88 twin reads them. Dormant on Bor. |
| `core/vm/contracts.go` | 3 (hardfork surface) | #35473 | Took upstream's `Cacheable`/`NormalizeInput` hooks and cache-scope keying; kept Bor's `blake2F{pip88 bool}`. | The cache key is now the (normalised) input instead of its hash, scoped by (active precompile set, address). Gas is charged before the lookup, so PIP-88 gas variants are unaffected, and the cache stays unwired (nil) on Bor, as in batch 1. No precompile set, `RequiredGas` or `Run` changed. |
| `core/block_validator.go` (2) | 3 | #35490 | **Kept Bor** (the concurrent `validateResult` + `IntermediateRoot` from #35403). | Upstream reverted #35403 because `validateResult` read the block access list (`res.Bal.ToEncodingObj()`) while `IntermediateRoot → Finalise` wrote the same BAL maps (go-ethereum#35478). Bor's `validateResult` reads only receipts and requests, which `IntermediateRoot` never touches, and Bor has no BAL validation, so the race has no counterpart. Keeps Bor's `intermediateRootTimer`. **If BAL validation is ever adopted (POS-3737), it must not run concurrently with `IntermediateRoot`.** Recorded in needs-wiring. |

### State size tracker removal (#35520) — adopted

Upstream retired the state size tracker (superseded by a live tracer). Adopted:

| File | Class | Decision |
| --- | --- | --- |
| `core/state/state_sizer.go` (+ test) | 3 (modify/delete) | Took upstream's deletion. Bor's only edits were an import regroup and a code-dedup comment. |
| `core/blockchain.go` (6) | 1 | Kept Bor; removed the `StateSizeTracking` config field, `stateSizer` field, start/stop, `StateSizer()` accessor and the `Notify` calls. `CommitWithUpdate` now discards the unused update (`root, _, err`). **One of the removed `Notify` calls is on the pipelined-SRC commit path (`runSRCCompute`)**: a nil-guarded metrics call, removed with no behaviour change. |
| `cmd/utils/flags.go` (3), `cmd/geth/main.go`, `eth/ethconfig/{config,gen_config}.go`, `eth/backend.go`, `eth/api_debug.go`, `internal/web3ext/web3ext.go`, `core/state/statedb.go` | 1/2 | Dropped the flag wiring, config field, `debug_stateSize` API and console method; snap/2 (`SnapV2`) stays absent (declined, decision C). `--state.size-tracking` stays registered as a hidden deprecated flag (upstream's `flags_legacy.go`). `eth/api_debug.go` keeps Bor's `ExecutionWitness(rpc.BlockNumber)` signature (witness path). `eth/backend.go` keeps Bor's `trieJournalDirectory`. |
| `internal/cli/server/config.go` | 1 (outside any conflict) | Kept the `state.size-tracking` HCL/TOML key so existing Bor config files still parse; it is now ignored. Removed its default and its wiring. `docs/cli/default_config.toml` is unchanged. |

### Declined

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `eth/downloader/{queue,queue_test,peer,resultstore,skeleton_test,metrics,bor_fetchers_concurrent}.go`, `fetchers_concurrent_bals.go` (new), `downloader.go` (+ test) | 1/3 | #35386, #35493 | **Declined.** Restored every file to Bor's version, removed the new file, kept `downloader.go` deleted. | BAL downloading needs eth/71 `GetBlockAccessLists`, which Bor doesn't carry (decision D). Git's rename detection had carried a #35386 edit (`errInvalidBAL`) into Bor's `bor_fetchers_concurrent.go`; that was reverted too. |
| `eth/downloader/downloader.go` | 3 (modify/delete) | #35515 | Not ported. | Bor's `bor_downloader.go` has the same ordering (`close(beaconPing)` before `spawnSync` registers on `cancelWg`), but only on the beacon-sync path, which Bor doesn't drive. Recorded in needs-wiring (low). |
| `eth/api_debug_replay.go` (new), `debug_replayBadBlock` console method | 3 | #35423 | **Declined** (this part only). | The replay rebuilds the block through BAL construction (`ConstructionBlockAccessList.Merge`, BAL-returning `PostExecution`, a 6-argument `Finalize`), none of which Bor has. The rest of #35423 is adopted (below). |
| `eth/protocols/snap/{bal_apply,syncv2,syncv2_test}.go` | 3 (modify/delete) | #35463, #35477 | Kept Bor's deletion. | snap/2 declined (decision C). |
| `core/state_processor_parallel.go` | 3 (modify/delete) | #35465 | Kept Bor's deletion. | #35264 was declined in batch 1. |
| `eth/catalyst/api_testing.go` (+ test) | 3 (modify/delete) | #35501 | Kept Bor's deletion. | Bor never carried `testing_buildBlockV1`. |
| `core/vm/eip8038_test.go` | 3 (modify/delete) | #35497 | Kept Bor's deletion. | Held back under POS-3738. |
| `params/config.go` | 1 | #35423 | Kept Bor; `ChainConfig.GoString` not added. | It only forwards to `ChainConfig.String`, which Bor doesn't have (Bor uses `Description()`). |

### Other resolutions

| File | Class | Upstream | Decision |
| --- | --- | --- | --- |
| `core/rawdb/accessors_chain.go` (4) | 2 | #35423, #35471 | Took upstream (BAL kept in the freezer when attached; bad blocks stored with an execution detail; canonical-hash check before reading an ancient BAL), keeping Bor's bor-receipt and total-difficulty freezer appends. Dormant: Bor never attaches a BAL, and only upstream's `generateWork` records bad-block details. |
| `cmd/geth/dbcmd.go` | 2 | #35376, #35423 | Took upstream's `defer it.Release()` and the new `db import-badblocks` command, adapted to Bor's 4-argument `MakeChainDatabase`. `debug_getBadBlocks` gains upstream's optional file argument. |
| `miner/worker.go` (2), `miner/payload_building.go` | 1 | #35423 | Kept Bor. Upstream records reverted transactions in `generateWork`, which Bor doesn't use; the new struct fields merge but stay empty. |
| `eth/syncer/syncer.go` | 1 | #35433 | Kept Bor. Bor removed the synthesized finalized/safe markers entirely. |
| `eth/catalyst/simulated_beacon.go` | 1 (outside any conflict) | #35392 | Converted `IsAmsterdam(num, time)` to Bor's block-based `IsAmsterdam(num)`. |
| `build/ci.go` (2), `internal/build/util.go` | 2 | #35510 | Took upstream's per-distro retry refactor; carried Bor's `common.VerifyPath` check into the new `buildDebianPackage`. |
| `core/vm/eip8037_test.go` | 1 | #35497 | Kept Bor's POS-3738 skip and took the test update. |

## Batch 3a: `7924511bf..6017c756e` (commits 41–46)

30 conflicted files: 24 content and 6 modify/delete. No hunk resolved mechanically. #35498 accounts for most of them.

### #35498: `StateDB` finalise/commit API takes `params.Rules` (read first; witness, pipelined-SRC and BlockSTM paths)

Upstream changed `Finalise`, `IntermediateRoot`, `Commit`, `CommitWithUpdate` (and internal `commit`, `commitAndFlush`, `handleDestruction`) from `(deleteEmptyObjects, noStorageWiping bool)` to `params.Rules`, and `vm.StateDB.Finalise` with them. It also renamed `clearJournalAndRefund` to `clearInternal`, which now also drops the per-transaction block access list, and changed `Finalise`'s Amsterdam dispatch from `s.stateAccessList != nil` to `rules.IsAmsterdam`.

**Adopted, and every Bor call site translated so it computes exactly what it did before.** Keeping Bor's bool API instead would have left every upstream caller (tracers, simulate, cmd/evm, tests, consensus engines) conflicting in every later batch.

The translation rules:

- A fork-derived bool (`IsEIP158(num)`, `IsCancun(num)`) becomes `config.Rules(num, …)`. Bor's `Rules()` sets `IsEIP158`, `IsCancun` and `IsAmsterdam` from those same one-argument block checks and ignores the timestamp.
- A literal `true` passed as `deleteEmptyObjects` becomes the EVM's rules (`evm.GetRules()`). Its `IsEIP158` is true on every Bor network (`EIP158Block` = 0 in `params`, `internal/cli/server/chains/{mainnet,amoy}.go` and both packaged genesis files).
- A literal `false` becomes `params.Rules{}`, as upstream does for genesis.

| Site | Before | After | Path |
| --- | --- | --- | --- |
| `core/blockchain.go` `writeBlockWithState` | `CommitWithUpdate(n, IsEIP158(num), IsCancun(num))` | `CommitWithUpdate(Rules(num, …), n)` | import |
| `core/blockchain.go` `runSRCCompute` | `CommitWithUpdate(n, deleteEmptyObjects, IsCancun(num))` | `CommitWithUpdate(Rules(num, …), n)` | **pipelined SRC** |
| `core/state/statedb.go` `CommitSnapshot(bool)` | `Finalise(deleteEmptyObjects)` | `Finalise(Rules{IsEIP158: b, IsAmsterdam: s.stateAccessList != nil})`; signature unchanged | **pipelined SRC / witness**. `IsAmsterdam` reproduces the old dispatch exactly. |
| `core/state/statedb.go` `FinaliseFast` | `clearJournalAndRefund()` | `clearInternal()` | **BlockSTM V2 settlement**. The extra access-list clear is a no-op before Amsterdam. |
| `core/stateless.go` | `IntermediateRoot(IsEIP158(num))` | `IntermediateRoot(Rules(num, …))` | **witness**. Bor's 5-value return is kept. |
| `consensus/bor/bor.go` (2) | `tempState.IntermediateRoot(false)` | `IntermediateRoot(params.Rules{})` | **witness propagation** (span / state-sync reads) |
| `consensus/bor/bor.go` `FinalizeAndAssemble` | `IntermediateRoot(IsEIP158(num))` | `IntermediateRoot(Rules(num, …))` | block production |
| `consensus/bor/statefull/processor.go` (2) | `Finalise(true)` | `Finalise(vmenv.GetRules())` | **state-sync execution** |
| `miner/pipeline.go` | `IntermediateRoot(IsEIP158(num))` | `IntermediateRoot(Rules(num, …))` | **pipelined mining** |
| `core/parallel_state_processor.go` (4) | `Finalise(IsEIP158(num))`, `Finalise(true)`, `IntermediateRoot(IsEIP158 / isEIP158)` | `evm.GetRules()` / `Rules(num, …)` | **BlockSTM V1 and V2** |
| `core/state/parallel_statedb.go` `ParallelStateDB.Finalise` | `(bool)` | `(params.Rules)`; still returns nil | **V2 twin** (`TestPDBMethodParity`) |
| `core/state_processor.go` (5), `core/state_prefetcher.go`, `core/block_validator.go`, `core/genesis.go` (2), `core/chain_makers.go`, `eth/state_accessor.go`, `eth/tracers/api.go`, `internal/ethapi/{api,simulate,override/override}.go`, `consensus/{clique,ethash,beacon}` | bools | as above | — |
| 34 Bor test files, 174 call sites | bools | `params.Rules{IsEIP158: a, IsCancun: b}` (a literal `false` field is dropped) | tests; converted mechanically with a balanced-parenthesis rewriter |

`vm.EVM.GetRules()` is added. It is upstream's one-line accessor over `evm.chainRules`, which upstream introduced in #34957 (BAL construction, declined in v1.17.5).

#2180's `currentBlockDestructs` survives on every deletion arm: `Finalise`, both deletion cases of `finaliseAmsterdam`, and `FinaliseFast`'s `finaliseDelete`. `addObjectWitness` and `NewWitness` are untouched.

| File | Class | Decision |
| --- | --- | --- |
| `core/state/statedb.go` (4) | 3 | Took the `Rules` signatures. Kept Bor's removal of `commitAndFlush`'s `deriveCodeFields` argument. |
| `core/block_validator.go`, `core/blockchain.go`, `core/genesis.go`, `core/stateless.go`, `core/state_processor.go` (6) | 1 | Kept Bor's side (concurrent validation, commit timers, 5-value `ExecuteStateless`, no `AssembleBlock`, no BAL merge, no system-call `Prepare`) and converted only the arguments. |
| `core/chain_makers.go` (3) | 2 | Took upstream: `makeHeader` no longer computes a throwaway `Root` (the engine's finalize sets it). Kept Bor's `IsVerkle`. |

### Other resolutions

| File | Class | Upstream | Decision |
| --- | --- | --- | --- |
| `eth/fetcher/tx_fetcher.go` (+ test), `eth/handler_eth.go` (2), `tests/fuzzers/txfetcher/txfetcher_fuzzer.go` | 1 | #35524 | **Declined**; restored to Bor. The version-dependent size check exists because eth/72 announces blob transactions without the blob payload. Bor has neither eth/72 nor a blob pool, and Bor's `Notify` returns only an error. |
| `eth/backend.go` | 1 | #35509 | Kept Bor, and dropped the clean-merged `blobpool.NewCache` construction. Bor's txpool has no blob subpool, and the blob cache was declined in batch 1 (#35439). |
| `common/lru/blob_lru.go` (2) | 2 | #35526 | Took upstream (key bytes count against the budget). Only the unwired precompile cache uses it. |
| `eth/api_debug_test.go` | 1 (outside any conflict) | #35544 | The nil-block guard merged cleanly into Bor's `ExecutionWitness`. Its test is adapted to Bor's `rpc.BlockNumber` signature, using a height past the head. |
| `cmd/evm/internal/t8ntool/execution.go` (3), `cmd/evm/main.go`, `tests/state_test_util.go` (2), `eth/tracers/api_test.go`, `core/vm/gas_table_test.go`, `core/state/{statedb_fuzz,trie_prefetcher}_test.go`, `core/txpool/blobpool/blobpool_test.go` (7) | 2 | #35498 | Took upstream's `Rules` form, keeping Bor's one-argument fork checks, `IsVerkle` and the `_, _ =` discards. |
| `core/eip7928_test.go`, `core/state/statedb_eip_7928_test.go`, `core/state_processor_parallel.go`, `core/txpool/blobpool/cache_test.go`, `core/vm/eip8038_test.go`, `eth/api_debug_replay.go` | 3 (modify/delete) | #35498 | Kept Bor's deletions (POS-3737, #35264, #35439, POS-3738, #35423 replay). |
| `crypto/kzg4844` | — | #35528 | Clean merge; `BlobsFromDataCells` is unused on Bor. |

### Found by the test tier

- **`TestFinalizeAndAssembleReturnsCommitTime` panicked** at `consensus/bor/bor.go`. The first translation used upstream's `isMerge = header.Difficulty.Sign() == 0`, which dereferences `Difficulty`; that test's headers leave it nil, and the old code never read it. `isMerge` only sets `Rules.IsMerge`, which no finalise/commit path reads, so the Bor-owned sites (`consensus/bor/bor.go`, `core/{blockchain,block_validator,stateless}.go`, `miner/pipeline.go`) pass `false`. Upstream-owned sites keep upstream's form.
- **`TestV2ForkParity`**: `IsEIP158` moves from `{inV1: true, inV2: true}` to `{false, false}`. Neither processor names the fork any more, because both pass `params.Rules` to `Finalise`/`IntermediateRoot`, so V1/V2 parity holds.

## Batch 3b: `6017c756e..e991e6aaf` (commits 47–60)

44 conflicted files: 40 content and 4 modify/delete. 23 of them are `internal/ethapi` JSON fixtures.

### Read first

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `core/state_processor.go` `processRequestsSystemCall` | 3 | #35514 (glam8) | Added upstream's `GetCodeSize(addr) == 0` guard to Bor's signature (no rules/BAL parameters). | A request-producing system call now fails the block if the contract has no code. **Inert on Bor chains:** Prague is active on Bor mainnet (73,440,256) and Amoy (22,765,056), but every caller of the request system calls (`StateProcessor.Process`, `chain_makers`, `ethapi/simulate`) is gated on `Bor == nil`, and `t8ntool` is test tooling. Bor has no system contracts deployed, so an ungated caller would reject every Bor block. Gating these calls is now load-bearing. |
| `go.mod`, `go.sum` | 2 | #35568 | Took `cockroachdb/swiss` → `v0.0.0-20260820225851` (fixes the build on Go 1.27) and kept Bor's `go-logr/logr v1.4.4`; `go mod tidy` changed only those lines. | — |
| `cmd/keeper/go.sum` | 2 | #35568 | Union of both sides; `cmd/keeper/go.mod` unchanged. | `cmd/keeper` doesn't build on Bor at HEAD either (missing otel sums), and no workflow or Makefile target builds it. Not tidied, to avoid unrelated churn. |

### glam8 test fallout (#35514)

Upstream's test backends now deploy `core.SystemContractAllocs()` at genesis, because the new guard invalidates blocks on merged configs without them. That changes the genesis state root, so every derived hash changes:
- Regenerated the 29 `internal/ethapi/testdata` fixtures with `WRITE_TEST_FILES=1`, starting from Bor's versions. **The only keys that changed are `hash`, `blockHash`, `parentHash`, `stateRoot`, and the state-sync `transactionHash`** (Bor derives it from the block hash). No gas, log, status or receipt payload changed.
- Updated the inline expectations in `api_test.go` (block hash, state-sync tx hash) and `TestSupplyOmittedFields`'s genesis hash (its genesis now uses `SystemContractAllocs()`; `TestSupplyRewards` keeps its hash).
- `core/blockchain_test.go`: kept Bor's `TestChainConfig` and Bor's test block, and added upstream's `withSystemContracts` helper.
- `ethclient/ethclient_test.go`: kept Bor's `AllDevChainProtocolChanges1` and took upstream's `genesisAlloc()`.

### Declined

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `eth/protocols/snap/{sync,sync_test,metrics}.go`, new `sync_runner.go` / `sync_profile.go` | 1 | #35533 | **Declined**; restored to Bor. | It parallelizes snap/1 response processing, written against upstream's post-snap/2 split (unexported `syncer`, `NewV1Syncer`). Bor kept the pre-split exported `Syncer` (snap/2 declined, decision C). A performance refactor onto a diverged file needs its own change. Recorded in needs-wiring. |
| `core/txpool/blobpool/{blobpool,blobpool_test,metrics}.go` | 1 | #35543, #35552 | **Declined**; restored to Bor. | #35543 memoizes RLP for peers older than eth/72; Bor's `GetRLP` has no version argument (eth/72 declined) and Bor's txpool has no blob subpool. #35552 is a log-level change in the same unused pool. |
| `beacon/engine/types.go` (2), `eth/catalyst/api.go` (3), `eth/catalyst/witness.go`, new `api_forks_test.go` / `witness_fork_test.go` | 1/3 | #35514 | Kept Bor; new test files dropped. | Bor removed the BAL engine payload handling, `ForkchoiceUpdatedV4`, `GetPayloadV6`, `NewPayloadV5` and upstream's engine-API witness endpoint. glam8's fork-selection and optional-`TargetGasLimit` changes target those. |
| `internal/ethapi/api.go` (2), `api_test.go` (3), `testdata/eth_config-bpo-skip.json` | 1 | #35553 | Kept Bor's block-based `eth_config`. | Upstream skips unconfigured optional forks when picking "next" by timestamp. Bor's `eth_config` is block-based and its `LatestFork` ignores its argument, so the upstream loop has no direct equivalent; Bor dropped the eth_config table test. |
| `core/eip8246_test.go` | 3 (modify/delete) | #35514 | Kept Bor's deletion (POS-3738). | — |
| `eth/protocols/snap/handler_test.go`, `tests/testdata` | 3 (modify/delete) | #35514 | Kept Bor's deletions. | — |

### Other resolutions

| File | Class | Upstream | Decision |
| --- | --- | --- | --- |
| `rpc/json.go` (5), `rpc/http_test.go`, new `rpc/jsonscan.go` (+ test) | 2 | #35518 | Took upstream's faster JSON decoding; Bor's side was whitespace only. The new benchmark's `rpc.NewServer()` is adapted to Bor's 3-argument `NewServer("", 0, 0)`. |
| `core/txpool/locals/journal.go` | 2 | #35564 | Took upstream (check the close error before replacing the journal). |
| `p2p/discover/v5_udp_test.go` | 2 | #35542 | Took upstream (`SetFallbackUDP`). |
| `core/rawdb/freezer_test.go` | 1 (outside any conflict) | #35551 | New tests adapted: Bor's `NewFreezer` takes an extra offset argument, and `TestTruncateHeadBelowGroupTail` also writes Bor's `diffs` and bor-receipt tables. |
| `crypto/kzg4844`, `eth/filters`, `cmd/devp2p`, `cmd/evm/testdata` | — | #35529, #35070, #34770, #35560 | Clean merges. |

### Found by the test tier

- `core` `TestFlushPendingImportSRCRollsBack` failed once in the full suite ("pruned ancestor"). **Flaky before this batch:** 2/40 failures at batch 3a (`72ef11add`) and 1/40 here, so not introduced by this merge. Pipelined-SRC test; also flaky on `develop` (1/60 at `b5a6adff4`). Tracked in **POS-3740**.
- `cmd/devp2p/internal/ethtest` panics with a nil `BorConfig` in `CalculatePeriod`, from `miner.newWorkLoop` on a non-Bor config. Identical at batch 3a; this package wasn't in earlier batches' tiers.

## Batch 4: `e991e6aaf..bbb9119ca` (commits 61–80)

47 conflicted files: 36 content and 11 modify/delete.

### Consensus-relevant and hardfork-surface resolutions (read first)

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `core/vm/evm.go`, `core/vm/eip7610.go` (+ test), `cmd/geth/snapshot.go` | 3 (hardfork surface) | #35581 | **Adopted: EIP-7610 removed.** | EIP-7610 was withdrawn upstream. **Byte-identical on Bor.** The removed check, `isEIP7610RejectedAccount`, only rejects addresses listed under the chain ID in `eip7610Accounts`, whose only entry is Ethereum mainnet. It returns false for 137 and 80002, so Bor's `create` never took that branch (v1.17.4 fork register, #34718). The `snapshot list-eip7610-eligible-accounts` diagnostic goes with it. |
| `params/config.go` (3), `core/genesis.go`, `core/genesis_test.go`, `core/forkid/forkid_test.go`, `cmd/geth/main.go`, `cmd/devp2p/nodesetcmd.go`, `beacon/params` | 3 (hardfork surface) | #35591 | **Adopted: Holesky removed.** Kept every Bor entry (Bor mainnet, Amoy, Mumbai, and the Goerli/Rinkeby entries Bor still carries). | Ethereum testnet; no Bor preset, fork gate or genesis references it. `BorMainnetChainConfig`, `AmoyChainConfig`, the chain presets and the genesis JSON are untouched. |
| `rpc/json.go` | 2 | #35576 | Took upstream; Bor's side was whitespace. | **Operator-visible RPC change:** a JSON-RPC call passing `null` for a required, non-pointer argument now returns `invalid argument N` instead of being decoded as a zero value. |

### eth/70 receipts: already ported (#2341)

| File | Class | Upstream | Decision |
| --- | --- | --- | --- |
| `eth/protocols/eth/{dispatcher,handlers,peer}.go` | 1 | #35537, #35593 | Kept Bor. #2341 already ported the partial-receipt deadlock fix (`dispatchResend`/`reqResend`) and empty-partial-response rejection, in Bor's `requestTracker` form with the ETH69/68-only advertisement, the 64-byte reserve and `bor_fetchers` round-trip scaling. The clean merge had re-added upstream's own `dispatchResend`, `reqResend` handler and `TestBufferReceiptsNoProgress` next to Bor's copies; restored those files to Bor. Upstream's only addition is a `size` field for its tracker, which Bor's doesn't use. |
| `eth/protocols/eth/broadcast.go` | 1 | #35589 | Kept Bor, and dropped the clean-merged `announceable`. It filters sparse blob announcements to pre-eth/72 peers; Bor doesn't announce blob transactions (the PIP-15 path is kept). |
| `eth/protocols/eth/handler.go` | 2 | #35591 | Comment only (Holesky → Sepolia network ID example). |

### Declined

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `core/txpool/blobpool/*`, `eth/fetcher/blob_fetcher.go` (+ test), `core/txpool/blobpool/buffer.go` (+ test) | 1/3 | #35367, #35429, #35572 | Restored to Bor; Bor's deletions kept. | Written against upstream's sparse blob pool (`BlobTxForPool`, custody, conversion queue), which Bor never took; Bor's txpool has no blob subpool. |
| `core/blockchain.go` (2), new `core/blockchain_livetracer_test.go` | 1 | #35512 | Kept Bor; test dropped. | `ExecuteConfig.EnableTracer` lives on upstream's `ExecuteConfig`/`setupExecutionState`/`ProcessBlock`, which Bor never took. |
| `beacon/engine/types.go` (3), `eth/catalyst/api.go` | 1 | #35580 | Restored to Bor. | Hive fix for the engine API's BAL payload handling (`executableDataToBlock`/`attachAccessList`, `ErrMalformedPayload`); Bor's `ExecutableData` has no `BlockAccessList`. |
| `cmd/devp2p/internal/ethtest/{eth71,accesslists}.go` (new), `suite.go`, `conn.go`, `snap2.go` | 1/3 | #35389 | Dropped; Bor kept. | eth/71 (EIP-8159) tests; Bor carries no eth/71. |
| `Dockerfile`, `build/checksums.txt`, `.gitea/*`, `.github/workflows/go.yml` | 1/3 | #35573 | Kept Bor's toolchain (`go 1.26.8`, `golang:1.26.8-alpine`) and Bor's workflow deletions. | Upstream moves builds to Go 1.27 and drops 1.24. Bor's release toolchain is a Bor release decision, not a merge-batch one. |
| `README.md` | 1 | #35591 | Kept Bor. | Bor's README never carried upstream's Holesky section. |

### Other resolutions

| File | Class | Upstream | Decision |
| --- | --- | --- | --- |
| `go.mod`, `go.sum`, `cmd/keeper/go.{mod,sum}` | 2 | #35573 | Kept Bor's newer pins and took upstream's higher ones (`x/tools` 0.49.0, `x/mod` 0.39.0); `go mod tidy` clean. `cmd/keeper/go.sum` is a union (that module doesn't build on Bor; see batch 3b). |
| `tests/{block,state}_test.go` | 1 | #35581 | Kept Bor's skip lists. Bor doesn't carry `tests/testdata`. |
| `tests/block_test_util.go` | 2 | #35574 | Took upstream (nil-safe `baseFeePerGas` comparison). |
| `eth/tracers/api_test.go` (4) | 1/2 | #35583 | Took the optional `*rpc.BlockNumberOrHash` block argument, kept Bor's `t.Context()` and Bor's parity tests, and added `TestTraceCallDefaultsToLatest` with Bor's 3-argument `rpc.NewServer`. `debug_traceCall` now defaults to `latest` when the block is omitted (operator-visible, additive). |
| `internal/ethapi/api_test.go` | 2 | #35592 | Kept Bor's preconf test and added upstream's `TestEstimateGasAmsterdam` (EIP-2780 estimates), with `AmsterdamTime` converted to Bor's `AmsterdamBlock = 0`. **Skipped under POS-3738**: the backend chain fails to start with `missing head header`, the same failure as the held-back `core/eip8246_test.go` (comment added on POS-3738). |
| `common/lru`, `core/vm/precompile_cache.go` | — | #33719, #35578 | Clean merges; the precompile cache stays unwired. |
| `eth/gasestimator`, `eth/api_debug.go`, `core/txpool` | — | #35592, #35554 | Clean merges: plain-transfer estimates return used gas; `debug_storageRangeAt` returns iterator errors. |

### Found by the test tier

- `internal/ethapi` `TestEstimateGasAmsterdam`: skipped under POS-3738 (see the row above).
- Otherwise the same pre-existing set as batch 3b: `cmd/geth` 5, `cmd/evm` 4 (identical subtests), and the `cmd/devp2p/internal/ethtest` nil-`BorConfig` panic.

## Batch 5: `bbb9119ca..7538039f0` (commits 81–100)

22 conflicted files (20 content, 2 modify/delete), 15 of them under `cmd/devp2p/internal/ethtest`.

### Consensus-relevant and hardfork-surface resolutions (read first)

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `core/state_transition.go` | 3 (fork gating) | #35588 | **Adopted (clean merge).** `preCheck` now rejects a message carrying an access list before Berlin, blob hashes before Cancun, or set-code authorizations before Prague, with `ErrTxTypeNotSupported`. | **No effect on Bor's state-sync:** `statefull.ApplyMessage` and `ApplyBorMessage` call `vm.EVM.Call` directly and never go through `preCheck`, and `StateSyncTx.accessList()` is nil. **Canonical history is unaffected:** the block's signer already rejects each typed tx before its fork (Berlin 14750000, Cancun 54876000, Prague 73440256 on mainnet; Amoy's are all set too), and Bor never admits blob txs. **Operator-visible:** `eth_call`/`eth_estimateGas` against a block before the relevant fork now fail when the call carries an access list, blob hashes or authorizations. |
| `core/vm/gascosts.go` (+ `eip8037_test.go`) | 2 | #35633 | Adopted (clean merge). `GasBudget.Absorb` pays down outstanding spilled debt from the state reservoir after merging a child frame. | EIP-8037 state-gas accounting; `StateGas` is zero unless `IsAmsterdam` (nil on every Bor preset), so it's inert. It's consistent with #2455's EIP-8037 port. |
| `core/state/stateupdate.go`, `core/types/state_account.go` | 2 | #35595 (encoder half) | Adopted (clean merge). Slim-account and slot encoding now write RLP by hand into a shared buffer. | Same bytes as `rlp.EncodeToBytes`; the witness and full test tiers vouch for it. |

### Declined

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `core/blockchain.go` (3) | 1 (witness path) | #35595 (block-write half) | Kept Bor. | Upstream moves `WriteBlock`/`WriteReceipts`/`WritePreimages` into a goroutine inside `writeBlockWithState`, overlapping them with the state commit. Bor's `writeBlockWithState` also writes the witness, the Bor state-sync receipts and the TD in the same batch, and witness code needs the operator's sign-off. The `BlockWrite` stat it feeds lives in `core/blockchain_stats.go`, which Bor doesn't carry (kept deleted). See needs-wiring. |
| `beacon/engine/types.go` (2), `eth/catalyst/api.go` | 1 | #35664 | Restored to Bor. | Hive fix for the BAL payload decode (`attachAccessList`, `NewPayloadV5`), which Bor doesn't carry. The clean merge had also re-added an `attachAccessList` call; removed with the restore. |
| `cmd/devp2p/internal/ethtest/*` (15 files, incl. `testdata/*`, `mkchain.sh`, `forkenv.json`) | 1/3 | #35368, #35647, #35648 | Restored to Bor. | Osaka testdata and eth/72 tests; `readAnyFrom` (#35648) exists only for the eth/72 cell tests. Same policy as batch 4's #35389. |
| `build/checksums.txt` (3) | 1 | #35656, #35601 | Kept Bor. | Go 1.27.1 and spec-tests v20.0.2 pins; Bor keeps its own toolchain and fixture pins. |
| `README.md` | 1 | #35624 | Kept Bor. | Ethereum-mainnet storage sizing; Polygon's requirements differ. |

### Other resolutions

| File | Class | Upstream | Decision |
| --- | --- | --- | --- |
| `eth/protocols/snap/handler.go` (`handlers.go` deleted in Bor) | 2 | #35299 | Ported to Bor's pre-split `handler.go`: `abort = true` when the storage iterator reaches the limit hash, so capped storage-range replies carry the endpoint proofs. |
| `ethclient/ethclient.go` | 2 | #35605 | Took upstream: `TransactionCount` returns `ethereum.NotFound` for a `null` count. Bor's side was whitespace. |
| `tests/{block,state}_test.go` | 1 | #35601 | Kept Bor's skip lists (Bor pins its own fixtures). |
| `core/filtermaps`, `core/rawdb`, `eth/tracers`, `rpc/json.go`, `triedb/pathdb/metrics.go`, `eth/fetcher/tx_fetcher.go` | — | #35619, #35603, #35645, #35629, #35360, #35587, #35616, #35620 | Clean merges: filtermaps reorg panic fix, `SafeDeleteRange` iterator error, freezer close on era failure, tracer test flake and log-limit fixes, JSON key unescaping, the pathdb metric prefix, and a comment. |

### Found by the test tier

- `core/vm` `TestAbsorbFailedChildKeepsDebt` (new with #35633): fails with `used state gas = 0, want 97920`. **Skipped under POS-3738**, the same class as the file's seven existing skips: Chicago's instruction set shadows Amsterdam's, so the SSTORE state-gas charge never fires on a config with Bor forks. Its sibling `TestAbsorbReturnsStateGas` passes only vacuously for the same reason (nothing is charged, so nothing needs returning); add both to POS-3738's un-skip/re-check list.
- Otherwise 143/144 packages passed; the pre-existing `cmd/*` set is unchanged.

## Cascade 2026-09-29: #2455 test-config fix (`855630f34`, merged as `ff4b7aaa8`)

#2455 now clears `Bor` in the EIP-8037 test configs (`core/eip8037_test.go`, `core/vm/eip8037_test.go`), so Amsterdam's instruction set is selected instead of Chicago's. This supersedes the POS-3738 skips added here in batch 1 (`TestCreate2TransientEmptyDestNoRefill`, `TestCreate2StorageOnlyDestCharged`, `TestBlockBaseFeeUsesMax`) and batch 5 (`TestAbsorbFailedChildKeepsDebt`). All are un-skipped and pass, and `TestAbsorbReturnsStateGas` now exercises a real charge. The fork-ordering blocker on real Bor configs stays in POS-3738. #2455 also adds a block-based `Amsterdam` entry to `tests.Forks`.

## Batch 6: `7538039f0..83e67ae63` (commits 101–120)

20 conflicted files (16 content, 4 modify/delete) and 8 new files.

### Consensus-relevant and hardfork-surface resolutions (read first)

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `core/vm/operations_acl.go` (clean), `core/vm/{gascosts,contract,instructions}.go` (clean), `core/vm/evm.go`, `core/vm/interpreter.go`, `core/state_transition.go` (clean), `core/tracing/hooks.go` | 3 (EVM gas surface) | #35646 | **Adopted.** `evm.go`: took upstream's `traceFrameExit`. `interpreter.go`: took upstream's `GasBudget`-typed `gasCopy` and kept Bor's `isShanghai`. `hooks.go`: took the new reasons 23–25 (`GasChangeRefundRevertedState`, `GasChangeTxGasForwarded`, `GasChangeStateGasRepaid`); Bor still doesn't carry `OnStateUpdate`. | **Tracing-only; gas results are unchanged.** The new `UsedExecutionGas -= coldCost` in `makeCallVariantGasCallEIP2929` (live on Bor since Berlin) and `Absorb`'s tracer argument don't feed `GasBudget.Used()` or any receipt, block or refund computation: `UsedExecutionGas` is read only by `GasBudget`'s own bookkeeping and `String()`. **PIP-88 twin lockstep:** `gasSLoadPIP88` and `makeGasSStoreFuncPIP88` are unchanged, their upstream originals (`gasSLoadEIP2929`, `makeGasSStoreFunc`) are unchanged in this batch, and every other shared function's Bor-vs-upstream divergence is identical before and after the merge. **Switch interpreter twin:** `interpreter_dispatch.go` runs only when `!debug` (no tracer), and `interpreter.go`'s charging is unchanged apart from the `cost` → `execCost` rename, so it needs no change. |
| `crypto/bn256/cloudflare/twist.go` (clean) | 3 (precompile library) | #35686 | Adopted. `twistPoint.Neg` now copies `t`. | **Can't change a precompile result on Bor:** on amd64/arm64 the BN254 precompiles use `gnark` (`crypto/bn256/bn256_fast.go`), and in `cloudflare`'s Miller loop the negated point feeds `lineFunctionAdd`, which reads only its `x` and `y`. |

### Operator-visible RPC changes (clean merges)

| Upstream | Change |
| --- | --- |
| #35627 | `eth_getHeaderByNumber("pending")` returns `null`. A `safe`/`finalized` tag that can't be resolved returns `null` instead of an error; Bor's `finalized` comes from milestones. |
| #35695 | `eth_call`/`eth_estimateGas` with `blobVersionedHashes` or an `authorizationList` (now including an empty one) and no `to` fail with `ErrBlobTxCreate` / `ErrSetCodeTxCreate`. |
| #35672 | The `difficulty` block override is ignored on zero-difficulty headers. Bor headers always carry a non-zero difficulty, so there's no change on Bor. |
| #35688 | `debug_getModifiedAccounts*` returns iterator errors. |

### Declined

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `eth/downloader/*` (`bor_fetchers_concurrent{,_bodies,_receipts}.go`, `metrics.go`, `queue.go`; `downloader.go` and `fetchers_concurrent_bals.go` kept deleted) | 1 | #35678 (downloader half), #35689 | Restored to Bor. | Download and progress metrics only. Bor's fetch loop is restructured (`collectIdlePeers`, back-off, witness and receipt queue kinds, Bor's `DeliverBodies` signature), so the hunks don't map onto it. See needs-wiring. |
| `eth/protocols/snap/{sync,metrics}.go` (`syncv2.go` kept deleted) | 1 | #35689 | Restored to Bor. | Snap progress metrics written against upstream's post-snap/2 split. |
| `core/jumpdest.go` (kept deleted) | 1 | #35681 | Declined. | Bor's jumpdest cache is its own unbounded `syncMapJumpDests` (`core/vm/jumpdests.go`), not upstream's size-bounded sharded LRU, so there's no budget to charge the entry overhead against. |
| `README.md` (2) | 1 | #35683, #35694 | Kept Bor. | Upstream README wording. |

### Other resolutions

| File | Class | Upstream | Decision |
| --- | --- | --- | --- |
| `core/blockchain.go` (2) | 2 | #35678 (core half) | Kept Bor's TD and bor-receipt ancient write and Bor's relocated hash-to-number writes; added the `chain/ancient/{write,sync,bytes}` metrics around them. |
| `p2p/server.go`, `cmd/devp2p/{main,discv4cmd,discv5cmd}.go`, new `cmd/devp2p/discoverycmd.go` | 2 | #35254 | Took upstream's combined discovery listener; Bor's `server.go` side was whitespace. `runRPCServer` uses Bor's 3-argument `rpc.NewServer("", 0, 0)`; imports fixed. |
| `internal/ethapi/testdata/eth_getHeaderByNumber-tag-pending.json` | 2 | #35627 | Took upstream (`null`). |
| `accounts/abi/bind/v2/lib_test.go` | 2 | #35701 | The new `TestWatchEventsIgnoreMismatch` adapted to Bor's single-return `testSetup` (`backend.Close()`). |
| `build/ci.go`, `build/rpm/*`, `build/completions/*`, `build/deb/ethereum/deb.install`, `.gitea/workflows/release-copr.yml` | 1 | #35557 | Clean merge taken as-is: upstream's rpm/Copr packaging. Bor releases don't use `build/ci.go` packaging or `.gitea`; keeping it verbatim keeps future merges small. |
| `accounts/abi/*`, `ethclient`, `eth/api_debug.go`, `metrics`, `p2p/pipes`, `rpc/client_test.go`, `cmd/evm` testdata, `core/gas_tracing_test.go` | — | #35332, #35701, #35610, #35688, #35642, #35644, #35634, #35636, #35646 | Clean merges. |

### Found by the test tier

- **Bor's state-sync tracing wrapper dropped every frame for V2-only tracers (fixed).** #35646 makes `muxTracer` expose only the V2 depth hooks (`OnEnterV2`/`OnExitV2`/`OnOpcodeV2`/`OnFaultV2`). `eth/tracers/state_sync_tracing_hooks.go` wrapped and gated on the V1 fields only, so `debug_traceBlock*` with `muxTracer` on a block carrying a state-sync tx failed with `incorrect number of top-level calls` (`TestTraceBlockByNumber_StateSync_AllRegisteredTracers/muxTracer`). The wrapper now exposes the V2 hooks and forwards with the inner `Emit*` helpers, which prefer V2 and fall back to V1. It keeps a per-frame entry-gas stack (as the mux does) so V1 inner tracers still get the right `gasUsed`. Bor's wrapper unit tests now drive it through `Emit*`, as the EVM does. One limit: inside the mux, the synthetic state-sync root reports `gasUsed` 0, because a V2 exit carries leftover gas, not gas used, and the root has no budget. Tracers used directly (not via mux) still get `receipt.GasUsed` on the root.
- **`TestV2TracingHookParity`:** Bor's guard flagged the four new V2 depth hooks. They're classified `firedInV2: true`, since BlockSTM V2 uses the same `core/vm` `Emit*` calls (`captureBegin`/`captureEnd`, `interpreter.go`) as V1.
- **`Example_haltedBeforeFrame`** (new with #35646): uses 8238 access-list keys instead of 4094, because Bor's `MaxTxGas` is 2^25 against upstream's 2^24. Its expected output changes only in the numbers (33555432 / 181828); the event sequence is identical to upstream's.

## Batch 7: `83e67ae63..3d84c6b2e` (commits 121–136, tag `v1.17.6`)

53 conflicted files (38 content, 15 modify/delete) and 2 new files. Most of the batch is declined because it builds on surfaces Bor didn't take (BAL parallel processor, eth/72 and blob buffer, snap v2, upstream's downloader and receipt paths).

### Consensus-relevant and hardfork-surface resolutions (read first)

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `params/config.go`, `core/forkid/forkid_test.go` | 3 (hardfork surface) | #35734 | **Declined (kept Bor).** | Upstream schedules Amsterdam on Sepolia (`AmsterdamTime` 1791294816). Bor's `SepoliaChainConfig` carries none of upstream's time-based forks, and Bor's forkid test has Sepolia's post-merge rows commented out. **`BorMainnetChainConfig`, `AmoyChainConfig`, the chain presets and the genesis JSON are untouched; `AmsterdamBlock` stays nil on every Bor preset.** |
| `core/block_validator.go`, `core/types.go`, `core/types/hashing.go` (+ test), `core/state_processor.go`, `core/state_processor_parallel.go`, `core/eip8037_test.go`, `core/eip7928_test.go`; new `core/receipt_pipeline.go` dropped | 3 (receipt root, bloom) | #35738 | **Declined (kept Bor).** | Upstream hashes the receipt trie and bloom in a background pipeline fed from inside the tx loop, and the validator reads its digest. On Bor the receipt set isn't only the tx loop's: `Finalize` appends the post-Madhugiri state-sync receipt after the loop (`receiptsCountBeforeFinalize`), and the BlockSTM processors assemble their own results. A pipeline that misses a receipt produces a receipt-root mismatch on import. It's a performance change on a consensus path. See needs-wiring. |
| `core/state_processor.go`, `core/chain_makers.go`, `miner/worker.go`, `eth/tracers/api.go`, `cmd/evm/internal/t8ntool/execution.go`, selfdestruct tracer test | 2 | #35693 | **Declined (kept Bor).** | Adds a `ctx` parameter to `ApplyTransaction`/`ApplyTransactionWithEVM` so upstream's BAL parallel processor records otel spans. Bor declined that processor, and the signature change would ripple through Bor's BlockSTM, miner and consensus call sites. |

### Declined

| File | Class | Upstream | Decision | Why |
| --- | --- | --- | --- | --- |
| `cmd/devp2p/internal/ethtest/*` (incl. `accesslists.go` kept deleted, testdata) | 1/3 | #35687, #35637, #35722 (test half) | Restored to Bor. | Osaka/Amsterdam BAL devp2p tests; same policy as batches 4 and 5. |
| `eth/downloader/*`, `p2p/msgrate/msgrate.go` (`downloader.go`, `fetchers_concurrent_bals.go` kept deleted) | 1 | #35680 | Restored to Bor. | Body/receipt scheduling rework against upstream's fetchers and queue. Bor's fetch loop is restructured, and the `msgrate.NewTrackers` signature change would break Bor's downloader and snap syncer callers. See needs-wiring. |
| `eth/protocols/snap/{sync_profile,sync_runner,syncv2*}.go` (kept deleted), `progress_test.go` | 1 | #35705 | Declined. | Snap v2 optimizations; Bor has no snap v2. |
| `core/txpool/blobpool/{blobpool,buffer,buffer_test}.go`, `core/txpool/errors.go`, `eth/fetcher/{tx_fetcher,metrics}.go` | 1 | #35766 | Restored to Bor. | Bounds upstream's blob buffer; `ErrOutOfCapacity` is only returned by that buffer. Bor's tx fetcher predates the #35572 delivery-metrics and buffer rework (declined in batch 4). |
| `core/blockchain.go`, `eth/backend.go`, `eth/ethconfig/{config,gen_config}.go`, `cmd/utils/flags.go`, `cmd/geth/{main,chaincmd}.go` | 1 | #35753 | Declined. | A `--cache.noprecompile` flag for the precompile result cache, which Bor adopted but never wired (`BlockChain` has no `precompileCache`). Goes with the #35388 needs-wiring row. |
| `eth/catalyst/witness.go` (kept deleted), `eth/api_debug_replay.go` (kept deleted) | 1 | #35719, #35693, #35738 | Declined. | Files Bor doesn't carry. |

### Other resolutions

| File | Class | Upstream | Decision |
| --- | --- | --- | --- |
| `eth/protocols/snap/sync.go` (+ `sync_test.go`) | 2 | #35722 | **Ported** into Bor's pre-split `Syncer.OnAccounts`: an account range starting at the zero origin with no proof is verified as a full range (`VerifyRangeProof` with a nil first key and proof). Upstream's two tests were ported (Bor's `testPeer` has no `trieLock`, and its handler `cap` is `uint64`). `TestSyncFullAccountRangeNoProof` fails without the fix and passes with it. |
| `go.mod`, `go.sum` | 2 | #35754 | Kept Bor's pins and took upstream's higher ones: otel 1.46.0, testify 1.12.1, protobuf 1.36.12, grpc-gateway 2.30.0, genproto 20260819; Bor's grpc 1.83.2 is already higher. `go mod tidy` raised the indirects otel 1.46 requires (otelgrpc/otelhttp contrib 0.70.0, cpuid 2.2.7, httpsnoop 1.1.0, oapi-codegen runtime 1.6.0, mapstructure 2.5.0, yaml 3.0.5). |
| `cmd/keeper/go.{mod,sum}` | 1 | #35754 | Max of both sides and a `go.sum` union, as in batch 3b (the module doesn't build on Bor and isn't in CI). |
| `internal/ethapi/api.go` (+ `api_test.go`) | 2 | #35698 | Took upstream. **Operator-visible:** `eth_createAccessList` no longer rejects up front a call with more authorizations than `gas / CallNewAccountGas`. The new `TestCreateAccessListAuthorizationGas` uses `AmsterdamBlock = 0`; its Amsterdam subtest is skipped under POS-3738 (`missing head header`, same as `TestEstimateGasAmsterdam`), and the pre-Amsterdam subtest passes. |
| `ethdb/memorydb/memorydb.go` (+ `dbtest`), `core/state/statedb.go` | — | #35756, #35759 | Clean merges: a batch delete of the empty key is no longer a range delete; comment fix. |
| `version/version.go` | — | #35770 | Took upstream (`1.17.6-stable`), as at earlier milestones; Bor's own version is separate. |
