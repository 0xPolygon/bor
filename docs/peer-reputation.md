# Peer reputation observation

This draft enables reputation measurement by default on Bor's native peer network.
Normal `bor server` startup activates scoring. Use `--peer-reputation=false` or
`peer-reputation = false` in the `[p2p]` configuration block to disable it. Existing
configs without this field inherit the enabled default; an explicit false remains
an opt-out. Enable the existing metrics exporter separately to collect dashboard
data. Scoring starts even when metrics export is disabled.

The implementation computes scores and reports hypothetical actions. It does
not throttle, disconnect, jail, change slots, grant contribution credit or
change static/trusted treatment. Existing validation and enforcement still run.
There is no additional signature check, block execution, handshake or protocol.

## Placement and evidence

| Component | Location | Implemented behavior |
| --- | --- | --- |
| Bounded scoring ledger | `p2p/peerpolicy` | Per-identity history, traffic profiles, recent hashes, scores and metrics. No chain access or enforcement callbacks. |
| Integration and lifecycle | `eth/peer_policy.go` | Initializes the observer before peer workers start and connects existing result producers. |
| ETH message observations | `eth/protocols/eth/observation.go` | Reuses decode outcomes and existing block sanity/body-commitment checks. Counts block announcements, transaction announcements, block broadcasts and block-data request messages. |
| Completed block-body downloads | `eth/protocols/eth/observation_serving.go` | Builds the actual object manifest during existing reads. Records body bytes only after a successful synchronous reply write. Missing objects and failed writes do not become completed downloads. |
| Transaction admission | `eth/fetcher/tx_observation.go` | Reuses txpool results. Invalid sender signatures and KZG failures are correctness evidence. Unsolicited transaction count/bytes are measured across different hashes. |
| WIT validation | `eth/handler_wit2_peer.go` | Observes existing signed-announcement and witness-body mismatch strike decisions. It does not repeat verification. |
| Downloader decisions | `eth/downloader/peer_observation.go` | Counts existing classified failures by reason without treating an aggregate sync failure as attributed invalid data. |
| Operator inspection | `eth/handler_eth.go` | Adds `protocols.eth.reputation` to peer information when observation is enabled. |

Already-known transactions, nonce/fee/balance-dependent rejections and pool
capacity or admission policy do not receive correctness penalties. Successful
admission does not imply immediate executability. Their unsolicited traffic
still counts towards the transaction observation allowance. Solicited pooled
transaction replies are excluded from that allowance.

Old blocks are not invalid merely because of age. Unsolicited block messages
count towards block traffic allowances regardless of whether the block is old
or already known. Generic handler errors are not automatically invalidity:
database errors, unavailable context and cancellation must not blame the peer.

## Score computation

Use six monotonic-clock buckets of ten seconds each. At any observation or
inspection, discard expired buckets from the score. Effective event retention
is 50–60 seconds depending on its position within a bucket.

Each reason is present at most once per bucket. The score is deliberately
conservative while asynchronous paths lack complete delivery provenance:

```text
bucket_risk = maximum weight of any reason present in that bucket
risk = min(100, sum(bucket_risk over the six live buckets))
```

| Evidence | Weight |
| --- | ---: |
| Observed invalid encoding, block sanity/body commitment, transaction signature/KZG, WIT announcement/body strike | 60 |
| Excess traffic or repeated announcements/completed body downloads | 20 |
| Downloader failure, fetcher drop or existing jail notification alone | 0 |

Two correctness windows reach 100. Five resource-excess windows reach 100.
Repeated calls in one window cannot increase its score. Multiple reasons remain
visible in counters even when only the strongest one affects the score.
This can undercount independent incidents. It avoids inventing precise
cross-module deduplication and cannot be used as an enforcement policy yet.

Risk below 40 reports `none`. Risk 40–99 reports `throttle`. Risk 100 reports
`jail`. All three are hypothetical labels. The implementation never executes
these actions. These weights and limits are a fixed experimental profile in
`p2p/peerpolicy/evidence.go`, not GossipSub constants or production defaults.

## Traffic observation profile

All limits below apply to one peer in one ten-second bucket. Either item or
byte excess raises the corresponding reason once for scoring that bucket.

| Family | Item allowance | Byte allowance |
| --- | ---: | ---: |
| Block hash announcements | 640 hashes | 1 MiB |
| Transaction hash announcements | 163,840 hashes | 16 MiB |
| Unsolicited transactions | 32,768 transactions | 64 MiB |
| Unsolicited full blocks | 160 messages | 160 MiB |
| Header/body/receipt requests | 640 messages | 16 MiB |
| Completed block-body replies | 10,240 bodies | 160 MiB |

Announcement history is keyed by peer identity, family and hash. Two copies
within the history window are tolerated. More than 32 excess copies within one
bucket produce repetition evidence. Cross-peer copies do not share history.

Body-download history also permits the initial completed transfer and one
retry. More than 32 MiB of subsequently repeated body bytes in a bucket produces
repetition evidence. The manifest is constructed from served objects, not the
requested hash list. A local successful write is not proof of remote receipt.

When a delivery has an explicit invalidity reason, that delivery's traffic is
still measured but does not also consume its resource-scoring allowance.
Announcement volume and repetition select one reason per observation.

## Bounds and performance

There are at most 1,024 peer records and 128 recent object entries per record.
Only the first 128 hashes per announcement/reply are tracked for repetition;
all items and bytes count towards volume. Hash churn can evict repetition
history and identity churn can evict scores. Neither cache is a security cap.
Eviction is measured. Scores survive same-ID reconnects while their record
remains resident. They do not persist across process restarts.

The observer has no goroutines, disk writes, network calls or validation passes.
A mutex protects bounded bookkeeping. Decode and database work happen outside
that lock. Scores do not gate gossip. The synchronous observer still costs CPU
and allocations, so benchmark and sentry measurements are required; this is
not a claim of zero overhead. Inbound bytes are decoded ETH frame sizes. Body
reply bytes are encoded body sizes before transport compression and exclude the
reply envelope. They are not a measurement of physical network bytes.

## Existing enforcement disposition

No existing jail mechanism is migrated in this observation-only change.

| Mechanism | This draft | Current expiry/reset ownership | Before score enforcement |
| --- | --- | --- | --- |
| WIT2 signed announcement/body strikes | Kept; shadow evidence at existing strike entry points | Existing WIT tracker and P2P jail remain authoritative | Migrate strike decisions to one owner while retaining immediate protocol rejection. |
| Downloader soft strikes/backoff | Kept; classified counters and zero-weight sync-failure evidence | Downloader retains its 30-second soft backoff and escalation rules | Separate scheduling backoff from misconduct; do not convert timeout alone to invalidity. |
| Downloader mismatch/ghost-state strikes and jailed map | Kept; zero-weight observation | Existing downloader windows and 5/30-minute backoffs remain authoritative | Resolve attribution and reconcile expiry/admission rules with the policy owner. |
| Block/transaction fetcher `dropPeer` callbacks | Kept; zero-weight drop observation | Existing disconnect paths | Preserve mandatory rejection; carry typed delivery evidence before assigning weight. |
| Handler/P2P jail | Kept; zero-weight jail observation | Existing P2P timer and admission checks | One authoritative misconduct jail decision and expiry; no second timer. |

A correctness observation followed by a legacy disconnect/jail does not get
additional risk from the action. Observation does not extend either existing
jail. Downloader and P2P ledgers are still distinct; this draft does not claim
to have unified them.

## Dashboard and rollout

Import `peer-reputation-dashboard.json` into Grafana or copy its panels into the
Bor P2P health dashboard. Select the sentry instance before interpreting data.
The repository contains a dashboard template, not a change to a live dashboard.

Metrics have fixed names, with no peer IDs, IP addresses or hashes as labels:

- `eth/peerpolicy/events/<reason>`: reason observations.
- `eth/peerpolicy/windows/<reason>`: first observation of a reason per bucket.
- `eth/peerpolicy/downloader/<reason>`: classified sync-failure observations.
- `eth/peerpolicy/items/<family>` and `bytes/<family>`: input/output volumes.
- `eth/peerpolicy/would_throttle` and `would_jail`: upward action transitions.
- `eth/peerpolicy/evictions`: bounded peer-ledger evictions.
- `eth/peerpolicy/risk`: event-sampled risk distribution, not current peer counts.

The Prometheus exporter changes `/` to `_`. Reason suffixes in metric names
also use underscores. Peer snapshots retain readable hyphenated reason names.
The `other` byte family includes unclassified frames; do not sum it with the
decoded transaction byte family to estimate wire throughput.

Start on selected mainnet sentries. Correlate reason windows and hypothetical
actions with syncing, reorgs, transaction rebroadcasts and operator traffic.
Compare propagation latency, CPU, memory, serving latency and ledger evictions
against observation disabled. Investigate false positives before tuning the
profile or implementing enforcement.

## Explicit follow-up work

This draft implements scoring and observation, not the entire serving-policy
proposal. It does not implement resource reservations, BP-reserved headroom,
slot allocation, contribution priority or an enforcement mode. SNAP/WIT serving
accounting, receipt/header reply manifests and complete deferred-import supplier
provenance are also not covered. Those require their own integration tests.

Reputation alone cannot stop repeated valid downloads or identity rotation.
Before enabling score-driven penalties, implement shared hard resource bounds,
reserve serving capacity for explicitly designated BP peers and finish the
enforcement migration above. Static/trusted peers are observed with the same
experimental profile; their existing exemptions are unchanged.
