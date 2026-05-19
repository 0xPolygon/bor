---
paths:
  - "params/config.go"
  - "params/protocol_params.go"
  - "core/forkid/forkid.go"
  - "internal/cli/server/chains/*.go"
  - "builder/files/genesis-*.json"
  - "core/vm/**/*.go"
  - "core/vm/*.go"
  - "consensus/bor/bor.go"
  - "miner/worker.go"
---
# Hardfork Rollout Review

Hardfork changes are consensus-critical. A hardfork is not safely wired until
every network config surface, runtime activation path, compatibility check, and
boundary test agrees on the same fork name and activation height.

The failure mode to prevent: a fork block is added to the packaged genesis JSON
files but not to the normal server runtime presets. A node started through
`bor server --config ...` with `chain = "mainnet"` or `chain = "amoy"` then
loads the Go preset with `Bor.<Fork>Block == nil`, so `Bor.Is<Fork>(num)` never
activates even though the genesis file looks correct.

## Trigger Conditions

Apply this rule whenever a change:

- Adds, renames, or modifies a hardfork block, fork method, or fork-specific
  `params.Rules` field.
- Changes EVM opcode gas, SLOAD/SSTORE behavior, precompile gas, precompile
  activation, transaction validation, receipt/block encoding, or fork ID.
- Changes Bor consensus or miner behavior gated by a fork, including header
  extra fields, producer timing, early block announcement, or prefetch logic.
- Touches mainnet or Amoy chain presets, packaged genesis JSON, startup banner
  output, or config compatibility checks.
- Introduces release-only fork wiring that must remain equivalent across
  public Bor, private Bor, devnets, and alternate clients.

## Required Wiring

For every new or changed fork, verify all of these surfaces before approving:

- `params.BorMainnetChainConfig` and `params.AmoyChainConfig` in
  `params/config.go` include the correct `Bor.<Fork>Block` values.
- Runtime server presets in `internal/cli/server/chains/mainnet.go` and
  `internal/cli/server/chains/amoy.go` include the same fork values as
  `params/config.go`.
- Packaged genesis files under `builder/files/`, especially
  `genesis-mainnet-v1.json` and `genesis-amoy.json`, include the same fork
  values when those files are part of the rollout.
- `BorConfig` has the new field, JSON tag, nil-safe `Is<Fork>(num)` helper,
  copy/equality handling, config validation, and config compatibility logic.
- `ChainConfig.Rules` exposes the fork state where EVM, tx, block, or
  precompile code needs it.
- Fork ID calculation includes the fork only when configured and at the correct
  height, so peer compatibility matches the rollout plan.
- Startup banner or config logging prints the new fork. For mainnet and Amoy,
  a normal server startup path should show the expected `#<block>` value.

## Execution Semantics

- Fork activation boundaries must be tested at `N-1`, `N`, and `N+1` for each
  network-specific activation height.
- EVM gas schedule changes must be gated only by the fork rules, not by local
  node configuration, wall clock time, sync mode, or mining mode.
- Precompile additions, removals, and gas changes must update
  `core/vm/contracts.go` registration, `ActivePrecompiles`, initialization,
  gas tables, and tests for both pre- and post-fork behavior.
- Any change that affects block validity, transaction validity, receipt roots,
  state roots, logs bloom, RLP encoding, or fork ID must have cross-client
  parity tracked for Polygon Erigon before the rollout is considered complete.
- Release branches and private security branches must carry the same fork
  constants and activation logic as the public branch they are meant to ship.

## Review Checks

- [ ] Does `git diff` show a new fork name or block value? If yes, list every
      config surface that must contain it and compare the values directly.
- [ ] Can a normal `chain = "mainnet"` or `chain = "amoy"` startup load a nil
      fork block while the packaged genesis JSON has a value? If yes, this is a
      blocking consensus bug.
- [ ] Are mainnet and Amoy values intentionally different, and are both present
      everywhere they are needed?
- [ ] Are devnet/local genesis or config presets deliberately included or
      deliberately excluded? The PR should make that choice obvious.
- [ ] Do tests cover the exact activation boundary and the unchanged
      pre-activation behavior?
- [ ] Does the startup banner, config dump, or equivalent smoke test prove that
      the runtime-loaded chain config contains the new fork value?
- [ ] If the fork changes EVM behavior or protocol encoding, is there a
      matching Erigon compatibility note, issue, or implementation?
