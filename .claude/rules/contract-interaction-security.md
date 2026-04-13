---
paths:
  - "accounts/abi/**/*.go"
  - "accounts/abi/*.go"
  - "consensus/bor/contract/**/*.go"
  - "consensus/bor/statefull/**/*.go"
  - "consensus/bor/abi/**/*.go"
  - "consensus/bor/heimdall/span/**/*.go"
---
# Contract Interaction Security — accounts/abi/, consensus/bor/contract/, consensus/bor/statefull/

Bor uses system contract calls for validator set management (`commitSpan`, `getBorValidators`), state sync (`commitState`, `lastStateId`), and EIP-4788/7002 system calls. ABI encoding/decoding is the serialization boundary between Go and EVM. Bugs here can corrupt the validator set, inject arbitrary L1→L2 state, or silently drop system transactions.

## Threat Model

| Threat | Attack Vector | Impact |
|--------|---------------|--------|
| ABI encoding mismatch | Pack/Unpack with wrong types or argument order | Wrong calldata → silent contract misbehavior |
| Return value misparse | Unpack into wrong Go type or ignore empty return | Stale validator set, wrong lastStateId |
| System call failure swallowed | EVM revert not propagated to consensus | State sync skipped, span not committed |
| Gas limit abuse | SystemTxGas too high or too low for system calls | Block gas exhaustion or call OOG |
| Caller impersonation | System call with wrong `From` address | Contract rejects or wrong authorization |
| State sync injection | Malformed `recordBytes` passed to `commitState` | Arbitrary state written to L2 |
| ABI spec drift | Contract upgraded but Go ABI string not updated | Encoding mismatch → silent failures |

## Critical Invariants

1. **System calls must use the canonical system address** — `0xffffFFFfFFffffffffffffffFfFFFfffFFFfFFfE`. The system contracts check `msg.sender == SYSTEM_ADDRESS`. Using any other address causes the call to revert or, worse, succeed with wrong authorization.

2. **ABI Pack/Unpack errors must never be ignored** — a packing error means the calldata is malformed. Sending malformed calldata to a system contract can have unpredictable results including silent no-ops.

3. **System call return values must be validated** — `commitSpan` returns no value (check `err` only), but `getBorValidators` and `lastStateId` return critical data. An empty return (`len(ret) == 0`) must be treated as an error, not decoded.

4. **State must be finalised after system calls** — `statefull.ApplyMessage` calls `Finalise(true)` on error to clean up partial state. On success, the caller (`Bor.Finalize`/`FinalizeAndAssemble`) is responsible for finalising after all system calls complete. Missing `Finalise` causes state changes to be lost. Note: `ApplyBorMessage` (used for `commitSpan`) does NOT call `Finalise` on success — the caller must handle it.

5. **ABI JSON strings must match deployed contract bytecode** — if the contract is upgraded (e.g., via governance), the embedded ABI must be updated in lockstep. A mismatch causes encoding errors that may not surface until runtime.

## Patterns to Flag

| Pattern | Severity | Why |
|---------|----------|-----|
| `abi.Pack()` error ignored or logged without returning | CRITICAL | Malformed calldata sent to system contract |
| `abi.UnpackIntoInterface()` on empty `[]byte` | CRITICAL | Panic or zero-value returned silently |
| System call `err != nil` logged but execution continues | CRITICAL | State sync or span commit silently skipped |
| `msg.From` not `systemAddress` for system contract call | CRITICAL | Contract rejects call or wrong auth |
| `vmenv.Call()` return `err` ignored after system call | CRITICAL | Failed state transition appears successful |
| Gas limit for system call changed without benchmarking | HIGH | OOG on large validator sets or state syncs |
| `big.NewInt(0).SetInt64(t)` with negative or overflow `t` | HIGH | Wrong timestamp passed to commitState |
| New ABI method added without corresponding Go binding | MEDIUM | Dead code or missing functionality |
| `common.HexToAddress(gc.StateReceiverContract)` called repeatedly | LOW | Should be cached; address parsing is not free |

## System Contract Call Flow

```
Bor.Finalize() / Bor.FinalizeAndAssemble()
  └→ GenesisContractsClient.CommitState()
       └→ abi.Pack("commitState", time, recordBytes)
       └→ statefull.ApplyMessage(msg, state, header, ...)
            └→ evm.Call(systemAddress, stateReceiverContract, data, gas, value)
            └→ state.Finalise(true)

Bor.checkAndCommitSpan()
  └→ spanner.CommitSpan(...)
       └→ abi.Pack("commitSpan", newSpan, startBlock, endBlock, validatorBytes, producerBytes)
       └→ statefull.ApplyBorMessage(evm, msg)
```

## Review Checklist

- [ ] Does the change modify ABI Pack/Unpack calls? If yes, verify argument types and order match the Solidity contract.
- [ ] Does the change affect system call gas limits? If yes, benchmark with maximum validator set size and state sync payload.
- [ ] Does the change alter the `From` address of system messages? If yes, verify the system contracts accept the new sender.
- [ ] Are all EVM call errors propagated (not just logged)?
- [ ] Is `Finalise(true)` called after every system call that modifies state?
- [ ] Does the change affect state sync event processing? If yes, verify event ordering and idempotency (replaying the same event must be safe).
- [ ] Are return values from system contract calls validated for expected length before unpacking?
