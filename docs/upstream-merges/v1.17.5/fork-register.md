# Fork / EIP register: go-ethereum v1.17.5 sync

Per-batch record of every upstream hardfork- or EIP-bearing PR (invariant 9).
Default for anything upstream introduces is **disabled**: the bor gate stays
nil so the merged code is dormant. Enabling any of these is a separate,
explicit PoS-team decision with its own PR and N-1/N/N+1 activation tests.

`decision` ∈ {`never-enable`, `defer`, `adopt-later`, `already-ours`}.

| EIP / fork | upstream PR | batch | touches | bor gate (value) | decision | verified-dormant |
| ---------- | ----------- | ----- | ------- | ---------------- | -------- | ---------------- |
| **EIP-8037** (gas reservoir spec change) | #35173 `aec597d7b` | 1 | `core/vm/{evm,contract,gascosts,gas_table,instructions,jump_table,eips}.go`, `core/state_transition.go`, `core/tracing/hooks.go` | `Bor.AmsterdamBlock` = nil on every preset; reached via `evm.chainRules.IsAmsterdam`, populated at `params/config.go:1943` from the block-based `ChainConfig.IsAmsterdam(num)` | defer | Yes. The reservoir moves inside `GasBudget` (`gas.Exit(err)`), and the Amsterdam account-creation state charge is removed. Both the removed and retained paths are inside `IsAmsterdam` branches. Bor adopted EIP-8037 in full at batch 32 of the v1.17.4 sync, so this applies with the grain. `NewGasBudget` keeps its 2-arg form, so bor-only `consensus/bor/statefull/processor.go` is unaffected. |
| **EIP-7954** (max code size 32768 → 65536) | #35217 `38678ec51` | 1 | `params/protocol_params.go` | `MaxCodeSizeAmsterdam` is only consulted through `CheckMaxCodeSize(&rules, …)`; bor's live cap is `MaxCodeSizePostAhmedabad` = 32768 via `Bor.IsAhmedabad(BlockNumber)` in `checkMaxCodeSize` | defer | Yes. Amsterdam nil on all presets, so the new constant is unreachable. **Enable-time delta changed by this batch:** activating Amsterdam now doubles the contract size limit relative to Ahmedabad (32768 → 65536) rather than leaving it unchanged. Product call, flagged for the enablement decision. |
| **EIP-8282** (builder execution requests) | #35175 `4387f433c` | 1 | `core/state_processor.go`, `params/protocol_params.go` (`BuilderDepositAddress` `0x0000884d…8282`, `BuilderExitAddress` `0x000014574A…8282`) | `config.IsAmsterdam(number)` — bor's block-based one-arg gate — wrapping both queue calls in `PostExecution` | defer | Yes, and **wired dormant rather than dropped**. Upstream's version is entangled with the declined BAL cluster (3-value `PostExecution`, `rules` + `*bal.ConstructionBlockAccessList` params); re-expressed in bor's shape as `ProcessBuilderDepositQueue`/`ProcessBuilderExitQueue` taking `(requests, evm, blockAccessIndex)`, request types 0x03/0x04, mirroring bor's EIP-7002/7251 wrappers. Address constants present, so the wiring is complete, not half-present. |
| **EIP-2780** (intrinsic gas cost change) | #35212 `07dd8e052` | 1 | `core/`, `core/vm/` | Amsterdam-gated upstream; bor's Amsterdam nil | defer | Yes. Code merged, gate nil. Upstream's `core/eip2780_test.go` removed as a coverage gap — it depends on `rules8037` from `core/eip8037_test.go`, which itself depends on the declined `balChainConfig`. |
| **EIP-8246** (remove SELFDESTRUCT self-burn) | #35219 `68671a453` | 1 | `core/vm/instructions.go` (`opSelfdestruct6780`), `core/state/statedb{,_hooked}.go`, `core/state_transition.go`, `core/types/log.go`, `core/vm/interface.go` | `evm.chainRules.IsAmsterdam` in `opSelfdestruct6780`; bor's Amsterdam nil | defer | Yes. Under Amsterdam the self-burn is removed and the balance preserved, so the EIP-7708 residual-burn pass and `types.EthBurnLog` are deleted. Pre-Amsterdam behaviour is unchanged — the burn is retained in the `!IsAmsterdam` branch. **Supersedes** the EIP-7708 burn-log corner case bor wired dormant at batch 28 of the v1.17.4 sync; bor's serial implementation and its V2 twin were both removed, keeping the executors in lockstep. |
| **EIP-7997** (deterministic deployment factory) | #35223 `c8953d10c` | 2 | `consensus/misc/eip7997.go` (new), `core/chain_makers.go`, `core/state_processor.go`, `core/genesis.go`, `params/protocol_params.go` (`DeterministicFactoryAddress` `0x4e59b448…4956C`, `DeterministicFactoryCode`) | `config.IsAmsterdam(number)` — converted from upstream's two-arg `IsAmsterdam(num, time)`, which bor does not have; bor's Amsterdam nil | defer | Yes. The factory is inserted by an irregular state transition on the **first** Amsterdam block, detected as `IsAmsterdam(header) && !IsAmsterdam(parent)`. Under bor's block gating that predicate is identical to upstream's timestamp form. With `AmsterdamBlock` nil on every preset the transition never fires. `DeveloperGenesisBlock` also preallocates the factory (auto-merged), which affects only `--dev` chains. Upstream's `core/eip7997_test.go` removed as a coverage gap. |
| **EIP-8038** (cold storage access cost) | #35216 `dd8dd1520` | 2 | `core/vm/operations_acl.go`, `core/vm/eips.go`, `core/vm/gas_table.go`, `core/vm/jump_table.go`, `core/state_transition.go`, `params/protocol_params.go` (`ColdStorageAccessAmsterdam` = 3000) | `jt[SLOAD].dynamicGas = gasSLoad8038` installed only by the Amsterdam EIP enabler in `core/vm/eips.go`; bor's Amsterdam nil | defer | Yes. `gasSLoad8038` is a sibling of `gasSLoadEIP2929`, charging `ColdStorageAccessAmsterdam` on a cold slot. It arrived as an add/add against bor's `gasSLoadPIP88` at the same file offset; **both kept**, and PIP-88's own SLOAD/SSTORE path is untouched. Declining was not available: `core/vm/eips.go` had already auto-merged the call site. |
| **EIP-7928 spec change** (BAL) | #35260 `ea242145c` | 2 | `core/bal_test.go` only, within bor's footprint | n/a — BAL construction cluster declined at batch 28 of the v1.17.4 sync | never-enable (as declined) | n/a. Re-decline; the only bor-visible path is an already-deleted test file. |
| **Bogota** (stub fork) | #34057 `0d1cf34ec` | 4 | `params/config.go`, `params/forks/forks.go`, `core/vm/{evm,contracts,jump_table,jump_table_export}.go` | `Bogota.Block` = **nil on all six surfaces** (both params presets, both `internal/cli/server/chains/*.go`, both `builder/files/genesis-*.json`); reached via `Rules.IsBogota` from the block-based `IsBogota(num)` | defer | **Instruction-set basis corrected in batch 5 by #35383 `47450f97f`: derived from Amsterdam, not Osaka** — Bogota follows Amsterdam, so it inherits EIP-7843/8024/8037/8038 rather than skipping back. Precompiles are unaffected and this was re-verified: bor's `activePrecompiledContracts` has no `IsAmsterdam` case, so Amsterdam falls through to Osaka's set, which is exactly what the explicit `IsBogota` case returns — the two share a set and the delta stays empty. Yes. Upstream's Bogota is a pure stub: `newBogotaInstructionSet()` simply aliases another fork's set with no `enable*` calls, and both precompile lookups return the Osaka tables, so the **precompile delta versus the previous fork is empty** — verified by reading the stub, not assumed. P256 is retained (`PrecompiledContractsOsaka` carries `0x0100: &p256Verify{eip7951: true}`), satisfying PIP-27. Bor's own forks (Chicago, LisovoPro, Lisovo, MadhugiriPro, Madhugiri) are matched **above** Bogota in all three switches, so a Polygon fork's set can never be displaced by it. Converted from upstream's timestamp gating throughout, including a `checkCompatible` entry that auto-merged as `BogotaTime` and would not have compiled. Both bor guards worked rather than silenced: `TestReinforceMultiClientPreCompilesTest` (5-step checklist) and `TestV2ForkParity` (classified `{inV1: false, inV2: false}`). |

## Bogota — gating convention DECIDED (operator, before batch 4)

**Decision: treat Bogota exactly as Shanghai, Cancun, Prague, Osaka and
Amsterdam are treated in bor.** Arriving in batch 4 (#34057 `0d1cf34ec`), with
follow-ups #35355 and #35383 in batch 5.

That is a concrete, already-established pattern in `params/config.go`, not a new
one, so the batch has no room to improvise:

| Surface | Required shape |
| ------- | -------------- |
| `ChainConfig` field | `BogotaBlock *big.Int` with `json:"bogotaBlock,omitempty"` and the standard `// Bogota switch Block (nil = no fork, 0 = already on bogota)` comment. **Block-based; upstream's `BogotaTime` is converted, never carried.** |
| Activation helper | `func (c *ChainConfig) IsBogota(num *big.Int) bool { return c.IsLondon(num) && isBlockForked(c.BogotaBlock, num) }` — the post-London form used by `IsOsaka` and `IsAmsterdam`, not the bare form used by `IsShanghai`/`IsCancun`/`IsPrague` |
| `params.Rules` | `IsBogota bool`, populated in `Rules()` from `c.IsBogota(num)` |
| `ChainConfig.Block(fork)` | `case fork == forks.Bogota: return c.BogotaBlock` |
| Gate value | **nil on every surface** — both `params/config.go` presets, both `internal/cli/server/chains/{mainnet,amoy}.go` runtime presets, and both `builder/files/genesis-{mainnet-v1,amoy}.json`. Absent from all of them is the correct state; Bogota is not scheduled on any Polygon network. |
| Startup banner | Follows automatically from the block-based branch; no timestamp line. |
| Precompiles | If the fork carries a VM instruction set, state the intended precompile delta against the previous fork explicitly and update or explicitly preserve `core/vm/contracts_continuity_test.go`. Silently copying the previous map is a review finding. |

The failure class this prevents is the Chicago one: a fork present in the
packaged genesis JSON but missing from the Go runtime preset, so a normal
`bor server --config ...` startup loads a nil gate and never activates while the
genesis file looks correct. Nil everywhere is consistent and safe; nil in some
places and set in others is the bug.

Enabling Bogota is a separate, explicit PoS-team decision with its own PR and
N-1/N/N+1 activation tests, per invariant 9.

## Standing note

Bor's `params.Rules.IsAmsterdam` is derived from the **block-based**
`ChainConfig.IsAmsterdam(num *big.Int)` at `params/config.go:1943`, not from a
timestamp. Any upstream code arriving with `IsAmsterdam(number, time)` or
`AmsterdamTime` must be converted, and any new upstream test relying on
`AmsterdamTime` cannot compile against bor — see the coverage-gap rows in
`needs-wiring.md`.
