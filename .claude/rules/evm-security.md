---
paths:
  - "core/vm/**/*.go"
  - "core/vm/*.go"
---
# EVM Execution Security — core/vm/

The EVM executes arbitrary smart contract code. Bugs in opcode implementation, gas accounting, or precompiles can break consensus, enable fund theft via contract exploits, or crash all nodes on the network.

## Threat Model

| Threat | Attack Vector | Impact |
|--------|---------------|--------|
| Gas undercount | Opcode charges less gas than its actual cost | DoS via artificially cheap computation |
| State corruption | Incorrect SSTORE/SLOAD semantics | Contract storage violated, fund loss |
| Precompile bug | Incorrect output from ecrecover, bn256, etc. | Signature bypass, proof forgery |
| Stack manipulation | Incorrect stack depth tracking | EVM state corruption |
| Consensus split | Different EVM result on different nodes | Chain fork |
| Reentrancy at protocol level | Incorrect call depth or gas forwarding | Unexpected state changes |

## Critical Rules

1. **Gas accounting must be exact** — every opcode must charge the precise gas defined in the Yellow Paper / EIP specs. Undercharging enables DoS; overcharging breaks contracts.

2. **All arithmetic on EVM values must use `uint256`** — never convert to native Go `int`/`uint64` for computation that feeds back into EVM state. Overflow semantics must match the EVM spec (wrapping 256-bit).

3. **Precompile outputs must match reference implementations** — test against Ethereum consensus test vectors. A single-bit difference in bn256 pairing or ecrecover causes a consensus split.

4. **Precompile lifecycle must follow the contracts.go registration protocol** — every precompile change (activation, deactivation, gas cost change) must:
   - Be gated behind a hard fork in `params.Rules`
   - Have a corresponding entry in `ActivePrecompiles` for the fork
   - Be initialized via the `init()` function in `core/vm/contracts.go`
   - Be mirrored in other Polygon clients (Erigon) to avoid cross-client consensus splits
   
   This has been a repeated source of production incidents — precompile changes without proper fork management or without corresponding Erigon changes cause network-level consensus failures.

5. **Memory expansion cost must be enforced** — memory gas cost follows a quadratic formula. Ensure expansion checks happen before the memory access, not after.

6. **EIP activation must be gated by block number / timestamp** — new opcodes and gas schedule changes must check fork activation. Applying an EIP before its activation block splits the chain.

## Patterns to Flag

| Pattern | Severity | Trigger | Why |
|---------|----------|---------|-----|
| Gas cost not matching Yellow Paper or EIP spec | CRITICAL | RPC User/Proposer | Consensus split — any tx sender can deploy contract exploiting underpriced opcode |
| `uint64` overflow in gas calculation without check | CRITICAL | RPC User/Proposer | Free computation — any user can craft a tx triggering overflow |
| Precompile returning different result than go-ethereum reference | CRITICAL | RPC User/Proposer | Consensus split — any contract call can hit a precompile |
| Precompile added/removed/changed without fork gate in `params.Rules` | CRITICAL | Self | Premature activation → chain fork. Has caused production incidents. Code-level error. |
| Precompile missing from `ActivePrecompiles` for the target fork | CRITICAL | Self | Precompile not accessible despite code being present |
| Precompile not initialized via `init()` in `core/vm/contracts.go` | CRITICAL | Self | Nil map entry → panic or silent miss |
| Precompile change without corresponding Erigon client update | CRITICAL | Self | Cross-client consensus split on Polygon network |
| New opcode without fork activation gate | CRITICAL | Self | Premature activation → chain fork |
| `interpreter.evm.StateDB` modified without gas charge | CRITICAL | RPC User/Proposer | Free state mutation — any contract call can exploit |
| Stack bounds check missing for new opcode | HIGH | RPC User/Proposer | Stack underflow/overflow — malicious contract can trigger |
| Memory access before expansion gas charge | HIGH | RPC User/Proposer | Free memory usage — crafted contract input |
| `SELFDESTRUCT`/`CREATE`/`CREATE2` without proper gas handling | CRITICAL | RPC User/Proposer | Spec violation — any contract deployment can exploit |
| Panic in opcode handler | CRITICAL | RPC User/Proposer | Node crash on specific contract input — **any user can craft a tx that crashes every node on the network** |

## BlockSTM Interaction

When the EVM runs under BlockSTM parallel execution (`core/blockstm/`), additional constraints apply:
- **Serial and parallel execution can be active simultaneously** — they are not mutually exclusive. BlockSTM runs transactions in parallel but falls back to serial re-execution on conflicts. Both paths must produce identical results. Never assume "if parallel is on, serial is off" or vice versa.
- **State reads/writes are tracked by MVHashMap** — the EVM must not bypass `StateDB` for state access, or BlockSTM will miss dependencies.
- **Deterministic gas usage is mandatory** — if parallel execution and sequential execution produce different gas for the same tx, the state root will diverge.
- **Interrupt propagation must work across call depths** — the `evm.interrupt` flag (set by block builder timeout) must be checked at all call depths, not just top-level.

See `state-security.md` for the full BlockSTM threat model.

## EIP Implementation Checklist

When implementing or modifying an EIP:
- [ ] Gas costs match the EIP specification exactly
- [ ] Fork activation check is present and correct (block number or timestamp)
- [ ] Ethereum consensus test vectors pass (`tests/` directory)
- [ ] Stack input/output counts match the EIP
- [ ] Memory expansion costs are charged before memory access
- [ ] Behavior matches go-ethereum reference for the same EIP
- [ ] Edge cases documented: zero inputs, max values, empty calldata

## Precompile Change Checklist

When adding, removing, or modifying a precompile:
- [ ] Change is gated behind a hard fork in `params.Rules` (e.g., `rules.IsNapoli`)
- [ ] Precompile registered via `init()` in `core/vm/contracts.go`
- [ ] `ActivePrecompiles` updated for the target fork in `core/vm/contracts.go`
- [ ] Gas costs match the EIP specification
- [ ] Output matches go-ethereum reference implementation
- [ ] Corresponding change tracked/implemented in Erigon client
- [ ] Test vectors cover activation boundary (block N-1 without, block N with)
