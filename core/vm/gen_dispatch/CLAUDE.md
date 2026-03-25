# gen_dispatch

Code generator that produces `interpreter_dispatch.go` - the switch-based EVM fast path (`runSwitch`).

## How it works

`go generate ./core/vm/` runs `main.go`, which emits a single `runSwitch()` method on `*EVM`. It's a flat `switch op` loop over opcodes. Hot opcodes (arithmetic, comparison, bitwise, stack, control flow, PUSH0-PUSH4) have their bodies inlined directly in the switch cases. Everything else falls through to a `default` case that uses the standard `jumpTable` dispatch.

The generated code lives in `interpreter_dispatch.go` (do not edit by hand).

## Entry conditions

`runSwitch` is only entered when ALL of these hold (checked in `interpreter.go`):

- `EnableEVMSwitchDispatch` is `true` in vm.Config
- Chain rules are Shanghai+ (PUSH0 is the latest fork-gated inlined opcode)
- EIP-4762 (Verkle) is NOT active (Verkle has different gas accounting)
- No tracer is attached (`debug == false`)

If any condition fails, execution falls through to the standard `Run()` loop.

## Gas accumulation model

Instead of deducting gas on every opcode, cheap ops (ADD, MUL, etc.) accumulate gas in a local `gasAccum` variable. The accumulator is flushed (checked and deducted from `contract.Gas`) only at control-flow boundaries: STOP, JUMP, JUMPI, JUMPDEST, INVALID, and the `default` fallback. This reduces the number of branches in the hot loop.

Two modes in the generator:

- `modeAccumulate` - adds to `gasAccum`, no immediate check
- `modeFlush` - flushes `gasAccum` before executing the opcode body

## Opcode shapes

The generator uses shape templates to emit stack checks and pointer arithmetic:

- `shapeBinaryOp` - pop 2, push 1 (e.g. ADD, MUL, LT)
- `shapeUnaryOp` - pop 1, push 1 in-place (e.g. ISZERO, NOT)
- `shapeTernaryOp` - pop 3, push 1 (e.g. ADDMOD, MULMOD)
- `shapePushVal` - push 1, overflow check at 1024 (e.g. PC, MSIZE)
- `shapePop1` - pop 1 (POP)
- `shapeCustom` - body handles everything (JUMP, JUMPI, STOP, INVALID)

## Assumptions

- Stack overflow checks use `stack.top >= 1024` for all push-like ops. The stack is a fixed `[1024]uint256.Int` array.
- The `jumpTable` for the current fork is passed in and used by the `default` case. Fork-gating of opcodes is handled by the jumpTable returning `nil`/`undefined` - the switch dispatch does NOT duplicate fork checks.
- `interrupt` (block builder timeout) is checked once per loop iteration at the top.
- `evm.abort` is checked only inside JUMP and JUMPI, matching the standard interpreter's `opJump`/`opJumpi`.
- Truncated PUSH operands (code ends mid-push) are zero-padded, matching geth semantics.
- PUSH1 and PUSH2 have specialised inlines for performance. PUSH3-PUSH4 use a generic slice + shift approach. PUSH5-PUSH32 share a single case block.

## Gotchas

- **Stack overflow error messages must match the standard interpreter.** The `limit` field in `ErrStackOverflow` must use `int(params.StackLimit) - 1` (i.e. 1023) to match what the jumpTable's `maxStack` reports. Using `1024` will cause error message divergence - the differential tests catch this.
- **Gas must be flushed before any operation that can return or branch.** If you add a new `modeAccumulate` opcode that can `return` or `continue`, it will silently skip the gas deduction. Use `modeFlush` for any opcode that exits the loop or modifies `pc`.
- **The `default` case flushes `gasAccum` before delegating to the jumpTable.** This is critical - without it, accumulated gas from prior cheap ops would be lost when hitting a non-inlined opcode.
- **REVERT must preserve return data.** When the switch dispatch hits REVERT via the `default` fallback, the return data flows through `operation.execute` and is returned correctly. If REVERT were ever inlined, the `ret` slice must be captured before returning.
- **`errStopToken` handling differs between `runSwitch` and `Run`.** `runSwitch` returns `errStopToken` directly; the caller in `Run()` converts it to `nil`. If adding new inlined opcodes that stop execution, return `errStopToken` (not `nil`).

## Updating this generator

1. Edit `main.go` - modify the `opcodes` or `pushOps` slices, or the emitter methods
2. Run `go generate ./core/vm/`
3. Run the differential tests:

   ```bash
   go test ./core/vm/ -run "TestDispatch|TestPreShanghai|TestStackOverflow|TestDefaultFallback|TestInterrupt|TestAbort"
   ```

4. The tests run every case through BOTH `runSwitch` and the standard interpreter and assert identical: return data, gas remaining, error messages, and logs

## Testing and validation

After any change to the generator, the generated dispatch code, or the standard interpreter, run through these steps:

### 1. Regenerate and build

```bash
go generate ./core/vm/
go build ./core/vm/
```

### 2. Differential tests

Every test runs the same bytecode through both `runSwitch` and the standard `Run()` loop, then asserts identical results: return data, gas remaining, error messages, and emitted logs.

**Any change to the generator or inlined opcode bodies MUST have a corresponding test in `dispatch_test.go`.** Use `runDiff()` - it runs both paths and fails if they diverge on anything. Never test `runSwitch` in isolation.

```bash
go test -v -count=1 ./core/vm/ -run "TestDispatch|TestPreShanghai|TestStackOverflow|TestDefaultFallback|TestInterrupt|TestAbort"
make lint
```

### 3. Benchmark (optional, for performance changes)

Runs the snailtracer contract through both paths side by side:

```bash
go test -run='^$' -bench=BenchmarkSnailtracer -benchmem -count=10 ./core/vm/
```

### 4. Execution spec tests (optional, for major changes)

Run the ethereum execution spec tests against a devnet with `evm-switch-dispatch = true` in the node config. See the main `CLAUDE.md` for the full test command. These catch any divergence from the Ethereum spec that unit tests might miss.

## SOP: When the EVM changes

### New opcode added by a hard fork

1. The opcode will already work via the `default` fallback case (jumpTable dispatch) - no change is required for correctness
2. If the opcode is hot (called frequently) and has simple constant gas, consider inlining it:
   - Add an entry to the `opcodes` slice in `main.go` with the right `shape`, `gas`, `mode`, and `body`
   - The inline body must produce identical results to the `opXXX` function in `instructions.go`
3. Run `go generate ./core/vm/`
4. Add a test case to `dispatch_test.go` covering the new opcode - use `runDiff()` so both paths are compared
5. Run the full differential test suite
6. If the new opcode is fork-gated (only available after a specific block), you do NOT need to add a fork check in the switch dispatch - the `jumpTable` in the `default` case already handles this by returning `nil`/`undefined` for inactive opcodes

### Gas cost changed for an existing opcode

1. If the opcode is inlined: update the `gas` field in its `opDef` entry in `main.go`
2. If the gas change is fork-dependent (different cost before/after a fork): do NOT inline it. Move it to the `default` fallback by removing its entry from `opcodes`. The jumpTable already handles fork-varying gas via `constantGas`/`dynamicGas`
3. Run `go generate ./core/vm/` and the differential tests

### Existing opcode behaviour changed

1. If the opcode is inlined: update the `body` string in its `opDef` entry to match the updated `opXXX` function in `instructions.go`
2. Compare the new `opXXX` implementation line-by-line with the inline body - they must be semantically identical
3. Watch out for: new error conditions, changed stack effects, new state reads/writes
4. Run `go generate ./core/vm/` and the differential tests
5. Add test cases in `dispatch_test.go` that exercise the changed behaviour

### New precompile added

No changes needed here. Precompiles are called via CALL/STATICCALL/DELEGATECALL, which all go through the `default` fallback path.

### Upstream geth merge

When merging upstream go-ethereum changes that touch the EVM:

1. Check `core/vm/instructions.go` for any changes to `opXXX` functions that have inlined equivalents in the switch dispatch
2. Check `core/vm/jump_table.go` for gas cost changes or new opcodes
3. Check `core/vm/interpreter.go` for changes to the main `Run()` loop - the switch dispatch mirrors its structure (interrupt check, abort check, gas deduction)
4. For each changed opcode, update the corresponding `opDef` in `main.go` if it's inlined
5. Run `go generate ./core/vm/` and the full differential test suite
6. If the merge adds a new fork, check if the entry condition in `interpreter.go` needs updating (currently gates on Shanghai+)
