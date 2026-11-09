@AGENTS.md

## Sidecar validation

- Keep socket handles platform-specific and cross-compile `p2p` for Windows when changing socket diagnostics.
- Once a channel hello can reach the receiver, caller cancellation must not revoke an acknowledged lane. Bound the acknowledgement with its own deadline and test cancellation on both sides of the ownership transfer.
- Open all negotiated sidecar lanes independently across ETH, SNAP, and WIT. Test a missed lane through the production attachment path, including progress in the other protocols before its acknowledgement times out.
- Connection setup must not consume the per-lane open timeout. Cover transient rejection and a full dial timeout with all ten lanes opening concurrently; bound retries and keep cancellation and shutdown prompt.
- Keep connection initiation tied to RLPx direction and stream opening tied to node ID. Cover both node-ID orderings and one-sided endpoint knowledge.
- Keep sidecar-dependent policy and serving budgets scoped to the local node; multiple node instances may have different settings and must not share lookup slots.

- Keep pending handshakes separate from authenticated connection capacity; the active limit must cover both incoming and outgoing sidecar connections.

## Rebroadcast scheduling

Queue-offer and acceptance accounting follows the rebroadcast ticker cadence. Test delayed callbacks and skipped ticks through the real pool loop; moving timestamps to callback wall time can silently double the retry interval.

## Optional Heimdall transports

- A gRPC-only endpoint can omit its REST URL. Validate nonempty REST URLs and return an error for REST-only operations when none is configured.
