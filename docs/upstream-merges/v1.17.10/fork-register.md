# Fork register: go-ethereum v1.17.10

Every upstream fork or EIP touched in this sync stays dormant on Bor unless
the PoS team decides otherwise in its own change.

| EIP/fork | Upstream PR | Batch | Touches | Bor gate (value) | Decision | Verified dormant |
| -------- | ----------- | ----- | ------- | ---------------- | -------- | ---------------- |
| EIP-8037 / EIP-2780 (Amsterdam): two-dimensional gas pool availability and the 2780 intrinsic floor in block building | #35773 | 1 | `core/gaspool.go`, `miner/worker.go`, `miner/pipeline.go`, `internal/ethapi/simulate.go` | `AmsterdamBlock` nil in `BorMainnetChainConfig`, `AmoyChainConfig`, both `internal/cli/server/chains` presets and both packaged genesis files | adopt-later (code adopted dormant) | Yes. Every new branch is behind `IsAmsterdam(number)`; with it false, `Available(false)` returns the legacy remaining gas and `minTxGas` is `params.TxGas`. |
| EIP-7928 (block-level access lists): encoding pipeline | #35772 | 1 | `core/` | same | defer (declined, with #35738) | Not merged. |
| EIP-8189 (snap/2): BAL catch-up for the trie | #35749 | 1 | `eth/protocols/snap`, `eth/downloader` | same | defer (decision C, v1.17.6) | Not merged. |
| EIP-8037 (Amsterdam): total transaction gas cap `MaxTxTotalGas = 1<<32-1` | #35835 | 2 | `params/protocol_params.go`, `core/state_transition.go`, `core/txpool/validation.go` | `AmsterdamBlock` nil everywhere | adopt-later (code adopted dormant) | Yes. Both checks are behind `rules.IsAmsterdam`; Bor's `MaxTxGas` (`1<<25`) is unchanged. |
| EIP-2935 (Prague, **live on Bor**): failed history system call no longer panics | #35835 | 2 | `core/state_processor.go` | Prague active (mainnet 73,440,256) | already-ours (spec alignment) | Not dormant, and it changes nothing while the call succeeds, which is always on Bor in practice. Flagged for cross-client parity. |
| EIP-8037 (Amsterdam): `eth_call` / `debug_traceCall` / non-strict `eth_simulateV1` execution not capped at `MaxTxGas` | #35845 | 4 | `core/state_transition.go`, `internal/ethapi`, `eth/tracers` | `AmsterdamBlock` nil everywhere | adopt-later (code adopted dormant) | Yes. Every new branch is `rules.IsAmsterdam && !skipExecutionGasCap`; only RPC simulation sets the flag. |
| EIP-7708 (Amsterdam): `eth_simulateV1` stops synthesizing transfer logs | #35617 | 4 | `internal/ethapi/simulate.go` | same | adopt-later (dormant) | Yes, behind `IsAmsterdam(number)`. Interacts with Bor's fee-contract transfer log; see POS-3738. |
| Ethereum chainspecs (time-based forks in JSON) | #35585 | 4 | `params/config.go`, `params/chainspecs/` | n/a | never-enable | Not merged; Bor's Ethereum presets stay block-based without post-merge forks. |
