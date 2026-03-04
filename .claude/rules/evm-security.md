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

4. **Memory expansion cost must be enforced** — memory gas cost follows a quadratic formula. Ensure expansion checks happen before the memory access, not after.

5. **EIP activation must be gated by block number / timestamp** — new opcodes and gas schedule changes must check fork activation. Applying an EIP before its activation block splits the chain.

## Patterns to Flag

| Pattern | Severity | Why |
|---------|----------|-----|
| Gas cost not matching Yellow Paper or EIP spec | CRITICAL | Consensus split |
| `uint64` overflow in gas calculation without check | CRITICAL | Free computation |
| Precompile returning different result than go-ethereum reference | CRITICAL | Consensus split |
| New opcode without fork activation gate | CRITICAL | Premature activation → chain fork |
| `interpreter.evm.StateDB` modified without gas charge | CRITICAL | Free state mutation |
| Stack bounds check missing for new opcode | HIGH | Stack underflow/overflow |
| Memory access before expansion gas charge | HIGH | Free memory usage |
| `SELFDESTRUCT`/`CREATE`/`CREATE2` without proper gas handling | CRITICAL | Spec violation |
| Panic in opcode handler | CRITICAL | Node crash on specific contract input |

## BlockSTM Interaction

When the EVM runs under BlockSTM parallel execution (`core/blockstm/`), additional constraints apply:
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
