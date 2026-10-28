@AGENTS.md

## Sidecar validation

- Keep socket handles platform-specific and cross-compile `p2p` for Windows when changing socket diagnostics.
- Once a channel hello can reach the receiver, caller cancellation must not revoke an acknowledged lane. Bound the acknowledgement with its own deadline and test cancellation on both sides of the ownership transfer.
- Open all negotiated sidecar lanes independently across ETH, SNAP, and WIT. Test a missed lane through the production attachment path, including progress in the other protocols before its acknowledgement times out.
- Connection setup must not consume the per-lane open timeout. Cover transient rejection and a full dial timeout with all ten lanes opening concurrently; bound retries and keep cancellation and shutdown prompt.
