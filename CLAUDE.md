@AGENTS.md

## Rebroadcast scheduling

Queue-offer and acceptance accounting follows the rebroadcast ticker cadence. Test delayed callbacks and skipped ticks through the real pool loop; moving timestamps to callback wall time can silently double the retry interval.

## Sidecar validation

- Keep socket handles platform-specific and cross-compile `p2p` for Windows when changing socket diagnostics.
- Once a channel hello can reach the receiver, caller cancellation must not revoke an acknowledged lane. Bound the acknowledgement with its own deadline and test cancellation on both sides of the ownership transfer.
