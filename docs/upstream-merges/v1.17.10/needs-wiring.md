# Needs wiring: go-ethereum v1.17.10

Upstream changes this sync deliberately didn't adopt, so they can be picked
up later.

| Item | Source | Status | Why deferred | To adopt | Pointers |
| ---- | ------ | ------ | ------------ | -------- | -------- |
| Downloader skips peers outside their announced block range (#35723) | batch 1 (`5d8fd6b60`, `572bd3696`) | adopt-later (recommended) | Bor's fetch loop is restructured around `collectIdlePeers`, so this is a port, not a merge resolution. | Add `next()` to Bor's body and receipt queues (`queue.NextBody` / `NextReceipt`), `serves` / `servedRange` on `peerConnection`, `BlockRange()` on the downloader `Peer` interface, the skip in `collectIdlePeers`, and the `peers/outofrange` gauge. Bor peers already announce ranges every 32 blocks (`eth/handler.go` `shouldSend`), matching upstream's 64-block slack. Decide how witness queues treat ranges. | `eth/downloader/bor_fetchers_concurrent.go`, upstream `fetchers_concurrent.go` |
| Recent and custom history pruning cutoffs (#35792) and snap restart before the cutoff (#35837) | batch 1 (`398dc536e`, `5d8fd6b60`) | deferred | Built on `HistoryPolicy` (#34036), deferred in v1.17.4. Bor's prune points only list Ethereum networks. | Adopt #34036 first, then add Bor networks' prune points if Bor wants history pruning. | `core/history/historymode.go`, `core/blockchain.go` `initializeHistoryPruning` / `loadLastState` |
| BAL encoding pipeline (#35772) | batch 1 (`920c07774`) | deferred | Extends the receipt pipeline (#35738) declined in v1.17.6. | Adopt with #35738 and POS-3737. | `core/digest_pipeline.go` |
| Post-Amsterdam semantics of Bor's gas-pool reads in block building | batch 1 (#35773) | review before enabling Amsterdam | Bor's miner reads the pool in places upstream doesn't (freed-gas reports to the prefetcher, the resubmit ratio, the speculative refill check, the pending-block guard). They now use `Available(isAmsterdam)`. That is exact before Amsterdam; afterwards it's the larger-of-two-dimensions bound, which may not be what each site wants. | Check each site against EIP-8037 when Amsterdam is scheduled. | `miner/worker.go`, `miner/pipeline.go` |
| Dependabot configuration (#35807) | batch 1 (`1a916a8db`) | wontfix | Bor's CI configuration is its own. | Separate decision by Bor maintainers. | `.github/dependabot.yml` upstream |
