@AGENTS.md

## Sidecar validation

- Keep socket handles platform-specific and cross-compile `p2p` for Windows when changing socket diagnostics.
- Once a channel hello can reach the receiver, caller cancellation must not revoke an acknowledged lane. Bound the acknowledgement with its own deadline and test cancellation on both sides of the ownership transfer.
- Open all negotiated sidecar lanes independently across ETH, SNAP, and WIT. Test a missed lane through the production attachment path, including progress in the other protocols before its acknowledgement times out.
- Connection setup must not consume the per-lane open timeout. Cover transient rejection and a full dial timeout with all ten lanes opening concurrently; bound retries and keep cancellation and shutdown prompt.
- Prefer the RLPx dialer for connection setup, with a bounded acceptor fallback for missing or unreachable endpoints. Agree on the connection before returning lanes, and never replace a committed connection during crossed dials. Test both node-ID orderings and traffic on all ten returned lanes; stream opening stays tied to node ID.
- Keep sidecar policy, buffer allowances and serving budgets scoped to the local node; multiple node instances may have different settings and must not share memory or lookup slots.
- Keep pending handshakes separate from authenticated connection capacity. Match connection and window budgets to RLPx admission, including trusted peers and live peer-limit changes, in both directions.

## Rebroadcast scheduling

Queue-offer and acceptance accounting follows the rebroadcast ticker cadence. Test delayed callbacks and skipped ticks through the real pool loop; moving timestamps to callback wall time can silently double the retry interval.

## Optional Heimdall transports

- A gRPC-only endpoint can omit its REST URL, but must still provide real sync status for span synchronization and failover health checks. Validate nonempty REST URLs and preserve status errors and cancellation.

## Pull request validation

- Test against the latest target-branch merge, including affected test packages. Upstream API changes can break the merge result while the feature branch alone still passes.
