@AGENTS.md

## Rebroadcast scheduling

Queue-offer and acceptance accounting follows the rebroadcast ticker cadence. Test delayed callbacks and skipped ticks through the real pool loop; moving timestamps to callback wall time can silently double the retry interval.
