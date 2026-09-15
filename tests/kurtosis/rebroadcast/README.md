# Out-of-sync transaction rebroadcast observation

Each rebroadcast batch checks the handler's synced flag, current local total
difficulty, and current peers directly. A known head uses locally calculated TD.
An unverified higher head suppresses rebroadcast for at most one minute, including
during peer backoff. Announcements, retries, and unrelated local chain progress
cannot extend that window. It can renew only after the previous advertised head
and TD are verified locally and the local TD has caught up to that claim.

A previously synced node resumes when no peer has an outstanding claim within
that window, even after failed catch-up. This deliberately permits rebroadcast
after the window expires if the advertised head is still unverified, including
during a long catch-up. Peer removal takes effect without waiting for the syncer.
Initial-sync failures leave rebroadcast disabled. These cases have Go regression
tests.

## CI integration

The existing `e2e-tests` job in `.github/workflows/kurtosis-e2e.yml` runs smoke,
RPC, and rebroadcast checks in one `Run E2E tests` step, against the same enclave.
It reuses the Bor and Heimdall images, setup, network diagnostics, and cleanup.
The shared Bor template enables debug logs and a two-second rebroadcast interval
with a 30-minute eligibility window. The fixture uses all four validators,
the candidate RPC node, and the baseline RPC peer from the smoke-test topology.

The test funds a fresh account using `REBROADCAST_FUNDER_KEY`, waits for the
funding receipt and empty pools, then raises validator gas-tip thresholds and
submits a single transaction from that account. It checks that this transaction
remains the only transaction in the target pool throughout observation. No
spammer is used, and other pending traffic makes the fixture fail.

The test asserts three phases:

1. The synced target rebroadcasts the fixture transaction in at least three batches.
2. After isolating P2P traffic and removing all original target peers, validators
   advance at least 20 blocks. The test reconnects with delay and bandwidth
   limits. During catch-up, the target must stay connected, advance its head,
   report active sync, identify at least three stuck-tx batches, and emit none.
3. Removing impairment lets the target catch up and rebroadcast at least three
   more batches containing the same sole pending transaction.

Network rules cover P2P traffic from all configured validators and other peers
to the target. Heimdall and HTTP RPC traffic stay intact. Cleanup independently
attempts network restoration, reconnection of the original peers, and gas-price
restoration on every validator; any failure fails the test and is recorded.

`summary.json` records initialization errors as well as phase results and cleanup
errors. `samples.jsonl` contains phase evidence and `target.log` contains target
logs. CI uploads these as `kurtosis-e2e-diagnostics`, including on failure.
Counts represent handler batches, not per-peer gossip deliveries.

## Local execution

With Docker, Kurtosis, Git, Python 3, a local `heimdall-v2:local` image, and a
funded devnet account available:

```bash
export REBROADCAST_FUNDER_KEY=<funded-devnet-account-key>
tests/kurtosis/rebroadcast/run.sh
```

The runner builds Bor, prepares Kurtosis PoS v1.4.2, and starts a dedicated
one-validator/one-RPC enclave without a spammer. Its default enclave name has a
random suffix. Set `BOR_IMAGE` to reuse a built image or `ENCLAVE` to choose a
name. `KEEP_ENCLAVE=true` keeps the network running after restoring test changes.
Otherwise the runner stops its enclave and retains it for diagnostics.
Results default to `build/rebroadcast-e2e/<enclave>/`; override with `ARTIFACTS`.

To use an existing devnet, call `e2e.py` with `--enclave`, `--artifacts`, the
candidate `--service`, all `--producers`, and any non-validator Bor services in
`--other-peers`. Prepare its Bor template using `prepare.py` before launch.
The default impairment is 1.5 seconds and 64 kbit/s; `--delay`, `--rate`, and
`--timeout` can be adjusted for the host. Missing catch-up evidence fails.

Run assertion, initialization, and cleanup tests without Docker:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s tests/kurtosis/rebroadcast -p 'test_*.py' -v
```

## Manual comparison with an older release

This scenario runs a local Polygon PoS network with transaction load, one RPC
node built from the current checkout, and one RPC node on Bor v2.9.0 for a
before/after comparison. The older node does not activate the devnet's Austin
fork at block 128, which models a connected node halted at a hard fork.

## Start the network

Build the candidate image from the repository root:

```bash
docker build -t bor:local --file Dockerfile .
```

Clone the Polygon PoS Kurtosis package and launch the scenario:

```bash
git clone --branch v1.4.2 --depth 1 https://github.com/0xPolygon/kurtosis-pos.git /tmp/kurtosis-pos
kurtosis run --enclave rebroadcast \
  --args-file "$PWD/tests/kurtosis/rebroadcast/params.yml" \
  /tmp/kurtosis-pos
```

Wait until the current Bor nodes pass block 128. The v2.9.0 node should stop at
block 127 when it rejects the Austin fork boundary. The `tx_spammer` service
keeps transaction gossip active during the observation.

## Add delay and observe

Seeding requires `OBSERVATION_PRIVATE_KEY` from a dedicated devnet account that
is not used by `tx_spammer` or any other sender. Fund it in genesis or before
block 128 so its balance exists in the halted baseline's state. Funding it only
on the current chain after the fork will not fund it on the halted node. Run
observations sequentially when reusing this account.

The baseline RPC node uses Bor v2.9.0. Apply 1.5 seconds of delay for 90
seconds:

```bash
export OBSERVATION_PRIVATE_KEY=<dedicated-prefunded-devnet-account-key>
tests/kurtosis/rebroadcast/observe.sh
```

For an in-sync control, run the same observation against the candidate RPC
node. Its seeded transaction will normally be mined before it becomes eligible
for rebroadcast:

```bash
SERVICE=l2-el-4-bor-heimdall-v2-rpc tests/kurtosis/rebroadcast/observe.sh
```

The script submits one transaction directly to the selected node, then reports
both batches identified by the txpool and batches emitted by the P2P handler.
The hard-fork-halted baseline remains connected and repeatedly emits that
transaction. The candidate's suppression at the sync transition is covered by
the focused `eth` package tests; delay alone may be too small to make this local
network require a catch-up sync.

The delay is applied from a short-lived container sharing the target's network
namespace, so the Bor image does not need `tc` or additional Linux capabilities.
The cleanup trap removes the rule when the script exits. Set `SEED_TX=false` to
observe an existing txpool without submitting another transaction or requiring
an observation key.

Remove the devnet when finished:

```bash
kurtosis enclave rm --force rebroadcast
```
