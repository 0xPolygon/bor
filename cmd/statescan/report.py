#!/usr/bin/env python3
"""report.py — state-GROWTH report from statescan parquet dumps.

Reads the three parquet files written by cmd/statescan (records, accounts,
slots) and produces a markdown report plus optional CSV tables, streaming the
big tables with pyarrow so the full slots table is never held in memory.

The report is organised around one distinction: a slot write to an existing
slot is CHURN (I/O cost now), a write to a previously-empty slot or a new
account is GROWTH (disk cost forever). Every ranking is done on growth, and
churn gets its own section.

Sections
  TL;DR                  templated sentences built from the thresholds below
  Growth ledger          new accounts / fresh slots / estimated bytes, per half
  Growth attribution     top-N addresses by fresh slots, with mapping-key share,
                         retouch/clear rates, hours active, sparkline
  Watchlist              addresses whose fresh writes jumped >=10x (half-over-half
                         or vs --baseline-csv), split into sustained vs spike
  Factories              contracts ranked by in-window nonce delta (= CREATEs),
                         ERC-1967 proxies deployed, contracts that started storage
  Burst episodes         clustered anomalous blocks with owner attribution
  Hourly                 slot writes / fresh / created per hour + sparklines
  I/O profile            per-block percentiles, SSTORE-saturated blocks,
                         busiest blocks with owner, churn leaders, system accounts
  Concentration          top-k share of writes and of fresh writes
  Active senders         EOAs by nonce delta (= txs sent)

Semantics reminder: every value in the dump is PRE-state (reverse diff).
"created" means the account did not exist before that block; "fresh write"
means the slot was zero/nonexistent before the block that touched it. Fresh
writes are an UPPER bound on net growth because post-values are not visible;
a slot that is touched again with an empty pre-value after an earlier touch
in the window was cleared in between, which gives a LOWER bound on deletions
(computed for the detailed addresses only).

Slot keys are raw storage keys in v1 history, so a key < 2^32 is a fixed
storage variable (bounded) and anything else is a mapping/array entry
(unbounded). v0 records store keccak(key) and will all look like mapping keys.

Usage:
    python3 report.py --input tmp-state --out report.md
    python3 report.py --input tmp-state/state-2026-10-13 --top 30 --csv-dir csv/
    python3 report.py --input tmp-state --anchor 95432454:1760000000 --labels labels.csv
    python3 report.py --input today --csv-dir csv/today --baseline-csv csv/yesterday/addresses.csv

Requirements: pandas, pyarrow, numpy.
"""
import argparse
import csv
import glob
import os
import sys
from collections import defaultdict
from datetime import datetime, timezone

import numpy as np
import pandas as pd
import pyarrow as pa
import pyarrow.compute as pc
import pyarrow.parquet as pq

# bor mainnet target block time; timestamps in the report are estimates
BLOCK_SECONDS = 2.0
BATCH_ROWS = 2_000_000

# the contract whose storage blowup motivated this analysis
XEN_LABEL = "XEN"

# storage key < 2^32 -> fixed variable (counter, oracle value, lock); else mapping/array
FIXED_KEY_PREFIX = "0x" + "0" * 56
ERC1967_IMPL = "0x360894a13ba1a3210667c828492db98dca3e2076cc3735a920a3ca505d382bbc"
ERC1967_BEACON = "0xa3f0ad74e5423aebfd80d3ef4346578335a9a72aeaee59ff6cb3582b35133d50"

SPARK = "▁▂▃▄▅▆▇█"

# Seed labels (Polygon PoS). Extend with --labels address,label CSV; the file wins.
LABELS = {
    "0x2791bca1f2de4661ed88a30c99a7a9449aa84174": "USDC.e",
    "0x3c499c542cef5e3811e1192ce70d8cc03d5c3359": "USDC",
    "0xc2132d05d31c914a87c6611c10748aeb04b58e8f": "USDT",
    "0x8f3cf7ad23cd3cadbd9735aff958023239c6a063": "DAI",
    "0x7ceb23fd6bc0add59e62ac25578270cff1b9f619": "WETH",
    "0x0d500b1d8e8ef31e21c99d1db9a6444d3adf1270": "WPOL",
    "0x4d97dcd97ec945f40cf65f87097ace5ea0476045": "Polymarket CTF",
    "0x0000000071727de22e5e9d8baf0edac6f37da032": "ERC-4337 EntryPoint 0.7",
    "0x5ff137d4b0fdcd49dca30c7cf57e578a026d2789": "ERC-4337 EntryPoint 0.6",
    "0x0000f90827f1c53a10cb7a02335b175320002935": "EIP-2935 history",
    "0x000000000022d473030f116ddee9f6b43ac78ba3": "Permit2",
    "0xca11bde05977b3631167028862be2a173976ca11": "Multicall3",
}


# --------------------------------------------------------------------------- io
def resolve_inputs(path):
    """Accept a file prefix, a directory, or individual files."""
    if os.path.isdir(path):
        cands = sorted(glob.glob(os.path.join(path, "*.records.parquet")))
        if not cands:
            sys.exit(f"no *.records.parquet under {path}")
        prefix = cands[-1][: -len(".records.parquet")]  # latest by name
    else:
        prefix = path[: -len(".records.parquet")] if path.endswith(".records.parquet") else path
    files = {k: f"{prefix}.{k}.parquet" for k in ("records", "accounts", "slots")}
    for k, f in files.items():
        if not os.path.exists(f):
            sys.exit(f"missing {k} file: {f}")
    return prefix, files


def load_labels(path):
    labels = dict(LABELS)
    if not path:
        return labels
    with open(path, newline="") as f:
        for row in csv.reader(f):
            if len(row) >= 2 and row[0].startswith("0x"):
                labels[row[0].strip().lower()] = row[1].strip()
    return labels


def load_baseline(path):
    """addresses.csv written by a previous run; returns DataFrame indexed by address."""
    if not path:
        return None
    b = pd.read_csv(path, dtype={"address": str}).set_index("address")
    return b[["slot_writes", "fresh_slots"]]


# ----------------------------------------------------------------- streaming
def stream_accounts(path, split_block):
    """One pass over accounts.parquet.

    Returns (acc, totals, blk, top):
      acc    per-address: touches per half, created/contract/started_storage flags,
             first/last block, nonce min/max (present rows only), created_max_block
      totals global counters per half
      blk    per-block: accounts created, created with storage
      top    per-block: address with most slot writes in that block
    """
    addr_parts, nonce_parts, cre_parts, blk_parts, top_parts = [], [], [], [], []
    totals = {k: [0, 0] for k in ("rows", "created", "created_with_slots",
                                  "decode_failed", "contract_rows", "eoa_rows")}
    pf = pq.ParquetFile(path)
    for batch in pf.iter_batches(batch_size=BATCH_ROWS):
        df = batch.to_pandas()
        df["is_contract"] = df["code_hash"].fillna("").str.len() > 0
        df["created"] = ~df["present"].astype(bool)
        df["created_with_slots"] = df["created"] & (df["n_slots"] > 0)
        df["started_storage"] = (~df["created"]) & df["is_contract"] & \
            (df["storage_root"].fillna("").str.len() == 0) & (df["n_slots"] > 0)
        h2 = (df["block"] >= split_block).to_numpy()
        df["t1"] = ~h2
        df["t2"] = h2
        for i, m in ((0, ~h2), (1, h2)):
            sub = df[m]
            totals["rows"][i] += len(sub)
            totals["created"][i] += int(sub["created"].sum())
            totals["created_with_slots"][i] += int(sub["created_with_slots"].sum())
            totals["decode_failed"][i] += int(sub["decode_failed"].sum())
            totals["contract_rows"][i] += int(sub["is_contract"].sum())
            totals["eoa_rows"][i] += int((~sub["is_contract"] & ~sub["created"]).sum())
        addr_parts.append(df.groupby("address", sort=False).agg(
            t1=("t1", "sum"), t2=("t2", "sum"), created=("created", "max"),
            contract=("is_contract", "max"), created_with_slots=("created_with_slots", "max"),
            started_storage=("started_storage", "max"),
            first_block=("block", "min"), last_block=("block", "max")))
        pres = df[~df["created"]]
        if len(pres):
            nonce_parts.append(pres.groupby("address", sort=False)["nonce"]
                               .agg(nonce_min="min", nonce_max="max"))
        cre = df[df["created"]]
        if len(cre):
            cre_parts.append(cre.groupby("address", sort=False)["block"].max()
                             .rename("created_max_block"))
        blk_parts.append(df.groupby("block", sort=False).agg(
            created=("created", "sum"), created_contract=("created_with_slots", "sum")))
        idx = df.groupby("block", sort=False)["n_slots"].idxmax()
        top_parts.append(df.loc[idx, ["block", "address", "n_slots"]])

    acc = pd.concat(addr_parts).groupby(level=0).agg(
        {"t1": "sum", "t2": "sum", "created": "max", "contract": "max",
         "created_with_slots": "max", "started_storage": "max",
         "first_block": "min", "last_block": "max"})
    if nonce_parts:
        acc = acc.join(pd.concat(nonce_parts).groupby(level=0)
                       .agg({"nonce_min": "min", "nonce_max": "max"}))
    else:
        acc["nonce_min"] = np.nan
        acc["nonce_max"] = np.nan
    if cre_parts:
        acc = acc.join(pd.concat(cre_parts).groupby(level=0).max())
    else:
        acc["created_max_block"] = np.nan
    blk = pd.concat(blk_parts).groupby(level=0).sum()
    # concat keeps each batch's positional labels, which repeat at batch boundaries;
    # reset them or idxmax()+loc can return several rows per block
    top = pd.concat(top_parts).reset_index(drop=True)
    top = top.loc[top.groupby("block")["n_slots"].idxmax()].set_index("block")
    return acc, totals, blk, top


def stream_slots(path, split_block, created_set):
    """One pass over slots.parquet.

    Returns (sl, block_fresh, proxies):
      sl          per-address: writes/fresh per half, fresh_fixed, fixed_writes
      block_fresh per-block fresh-write count
      proxies     created addresses whose creation wrote the ERC-1967 impl/beacon slot
    """
    addr_parts, blk_parts = [], []
    proxies = set()
    pf = pq.ParquetFile(path)
    for batch in pf.iter_batches(batch_size=BATCH_ROWS,
                                 columns=["address", "block", "slot", "value_len"]):
        fresh = pc.equal(batch.column("value_len"), 0)
        fixed = pc.starts_with(batch.column("slot"), pattern=FIXED_KEY_PREFIX)
        block = batch.column("block").to_numpy()
        h2 = block >= split_block
        fr = fresh.to_numpy(zero_copy_only=False)
        fx = fixed.to_numpy(zero_copy_only=False)
        df = pd.DataFrame({
            "address": batch.column("address").to_pandas(),
            "block": block,
            "w1": ~h2, "w2": h2,
            "f1": fr & ~h2, "f2": fr & h2,
            "fresh_fixed": fr & fx, "fixed_writes": fx,
        })
        addr_parts.append(df.groupby("address", sort=False)[
            ["w1", "w2", "f1", "f2", "fresh_fixed", "fixed_writes"]].sum())
        blk_parts.append(df.groupby("block", sort=False)["f1"].sum() + df.groupby("block", sort=False)["f2"].sum())

        is_proxy_slot = pc.or_(pc.equal(batch.column("slot"), ERC1967_IMPL),
                               pc.equal(batch.column("slot"), ERC1967_BEACON))
        m = pc.and_(is_proxy_slot, fresh)
        if pc.any(m).as_py():
            for a in batch.column("address").filter(m).to_pylist():
                if a in created_set:
                    proxies.add(a)

    sl = pd.concat(addr_parts).groupby(level=0).sum()
    block_fresh = pd.concat(blk_parts).groupby(level=0).sum().rename("fresh")
    return sl, block_fresh, proxies


def stream_details(path, addresses, hour_idx, hours_total):
    """Second pass over slots.parquet restricted to `addresses`.

    Per address: distinct slots, distinct 8-byte key prefixes ("bases"),
    retouches, clears (retouch with empty pre-value), first/last block, hourly
    write counts. Full keys are hashed to 64 bits (collision risk negligible).

    bases: array elements and struct fields live at keccak(base)+offset, so
    they share the first bytes of the key. distinct/bases is therefore the
    average width of the arrays/structs being written: ~1 for plain mapping
    entries, large for arrays. (An earlier version of this script truncated
    keys to 8 bytes and badly under-counted distinct slots for exactly this
    reason.)
    """
    store = defaultdict(lambda: ([], [], [], []))
    value_set = pa.array(sorted(addresses))
    pf = pq.ParquetFile(path)
    for batch in pf.iter_batches(batch_size=BATCH_ROWS,
                                 columns=["address", "block", "slot", "value_len"]):
        sub = batch.filter(pc.is_in(batch.column("address"), value_set=value_set))
        if sub.num_rows == 0:
            continue
        slot = sub.column("slot")
        df = pd.DataFrame({
            "address": sub.column("address").to_pandas(),
            "key": pd.util.hash_pandas_object(slot.to_pandas(), index=False).to_numpy(),
            "base": pd.util.hash_pandas_object(pc.utf8_slice_codeunits(slot, 0, 18).to_pandas(),
                                               index=False).to_numpy(),
            "vlen": sub.column("value_len").to_numpy(),
            "block": sub.column("block").to_numpy(),
        })
        for a, grp in df.groupby("address", sort=False):
            ks, bases, vs, bs = store[a]
            ks.append(grp["key"].to_numpy())
            bases.append(grp["base"].to_numpy())
            vs.append(grp["vlen"].to_numpy())
            bs.append(grp["block"].to_numpy())

    out = {}
    for a, (ks, bases, vs, bs) in store.items():
        keys, vlen, blocks = np.concatenate(ks), np.concatenate(vs), np.concatenate(bs)
        order = np.argsort(blocks, kind="stable")
        keys, vlen, blocks = keys[order], vlen[order], blocks[order]
        dup = pd.Series(keys).duplicated().to_numpy()
        out[a] = {
            "total": len(keys),
            "distinct": int((~dup).sum()),
            "bases": int(pd.Series(np.concatenate(bases)).nunique()),
            "retouch": int(dup.sum()),
            "clears": int((dup & (vlen == 0)).sum()),
            "first": int(blocks.min()),
            "last": int(blocks.max()),
            "hourly": np.bincount(hour_idx(blocks), minlength=hours_total),
        }
    return out


def stream_account_blocks(path, addresses):
    """Filtered pass over accounts.parquet: the set of blocks each address was
    touched in. Used to check factory candidates against visible creations."""
    store = defaultdict(list)
    value_set = pa.array(sorted(addresses))
    pf = pq.ParquetFile(path)
    for batch in pf.iter_batches(batch_size=BATCH_ROWS, columns=["address", "block"]):
        sub = batch.filter(pc.is_in(batch.column("address"), value_set=value_set))
        if sub.num_rows == 0:
            continue
        df = pd.DataFrame({"address": sub.column("address").to_pandas(),
                           "block": sub.column("block").to_numpy()})
        for a, grp in df.groupby("address", sort=False):
            store[a].append(grp["block"].to_numpy())
    return {a: np.unique(np.concatenate(v)) for a, v in store.items()}


# ---------------------------------------------------------------- analysis
def find_episodes(records, block_fresh, block_top, min_z, window=1801, gap=15):
    """Cluster blocks whose n_slots is anomalously high vs a rolling median,
    merging anomalous blocks that are at most `gap` blocks apart (15 blocks =
    30s, so a batcher that submits every few blocks is one episode).
    Returns a list of dicts sorted by slots desc."""
    s = records["n_slots"].astype(float)
    med = s.rolling(window, center=True, min_periods=1).median()
    resid = s - med
    mad = float((resid - resid.median()).abs().median()) * 1.4826
    if mad == 0:
        mad = max(float(resid.std()), 1.0)
    z = resid / mad
    anomalous = (z >= min_z) & (s >= 2 * med)
    blocks = records.loc[anomalous, "block"].to_numpy()
    clusters, cur = [], []
    for b in blocks:
        if cur and b - cur[-1] > gap:
            clusters.append(cur)
            cur = []
        cur.append(int(b))
    if cur:
        clusters.append(cur)

    rec = records.set_index("block")
    episodes = []
    for c in clusters:
        rng = range(c[0], c[-1] + 1)
        sub = rec.loc[rec.index.isin(rng)]
        slots = int(sub["n_slots"].sum())
        fresh = int(block_fresh.reindex(sub.index).fillna(0).sum())
        tops = block_top.loc[block_top.index.isin(rng)]
        owner = tops.groupby("address")["n_slots"].sum()
        owner_addr = owner.idxmax() if len(owner) else ""
        owner_slots = int(owner.max()) if len(owner) else 0
        episodes.append({
            "start": c[0], "end": c[-1], "blocks": c[-1] - c[0] + 1,
            "slots": slots, "fresh": fresh, "accounts": int(sub["n_accounts"].sum()),
            "peak": int(sub["n_slots"].max()), "owner": owner_addr, "owner_slots": owner_slots,
        })
    episodes.sort(key=lambda e: -e["slots"])
    return episodes


def share_table(values):
    ranked = np.sort(np.asarray(values, dtype=float))[::-1]
    total = ranked.sum() or 1.0
    n1pct = max(len(ranked) // 100, 1)
    return [100 * ranked[:k].sum() / total for k in (1, 10, 100, n1pct)], n1pct


# -------------------------------------------------------------- formatting
def fmt_int(n):
    return f"{int(n):,}"


def fmt_bytes(b):
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if abs(b) < 1024 or unit == "TB":
            return f"{b:,.1f} {unit}"
        b /= 1024


def pct(d1, d2):
    """Percent change from d1 to d2; 'new' when d1 is zero."""
    if d1 == 0:
        return "new" if d2 else "—"
    return f"{100 * (d2 - d1) / d1:+.0f}%"


def ratio(num, den):
    if not den:
        return "—"
    p = 100 * num / den
    if 0 < p < 0.5:
        return "<1%"
    if 99.5 <= p < 100:
        return ">99%"
    return f"{p:.0f}%"


def spark(counts):
    counts = np.asarray(counts, dtype=float)
    if len(counts) == 0 or counts.max() == 0:
        return "▁" * len(counts)
    idx = np.floor(counts / counts.max() * (len(SPARK) - 1)).astype(int)
    return "".join(SPARK[i] for i in idx)


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--input", required=True, help="statescan output prefix or directory")
    ap.add_argument("--out", default="report.md", help="markdown report path")
    ap.add_argument("--top", type=int, default=20, help="top-N address tables")
    ap.add_argument("--csv-dir", default=None, help="also write per-address CSV here")
    ap.add_argument("--baseline-csv", default=None,
                    help="addresses.csv from a previous run; watchlist compares against it "
                         "instead of first-vs-second half")
    ap.add_argument("--labels", default=None, help="CSV of address,label (lowercase hex)")
    ap.add_argument("--genesis-time", type=int, default=None,
                    help="unix timestamp of block 0 (shorthand for --anchor 0:TS)")
    ap.add_argument("--anchor", default=None,
                    help="BLOCK:UNIXTS of any block near the window; gives UTC times with less drift")
    ap.add_argument("--bytes-per-slot", type=float, default=150.0,
                    help="estimated on-disk bytes per new storage slot (leaf + amortised trie "
                         "nodes); calibrate against real bor DB growth")
    ap.add_argument("--bytes-per-account", type=float, default=250.0,
                    help="estimated on-disk bytes per new account")
    ap.add_argument("--mover-min", type=int, default=5000,
                    help="minimum fresh writes (2nd half / this window) to appear on the watchlist "
                         "via the ≥10× rule")
    ap.add_argument("--accel-min", type=int, default=50000,
                    help="minimum absolute increase in fresh writes (and ≥2×) to appear on the "
                         "watchlist as 'accelerating'")
    ap.add_argument("--full-block-slots", type=int, default=5000,
                    help="n_slots at/above which a block is counted as SSTORE-saturated")
    ap.add_argument("--burst-z", type=float, default=5.0,
                    help="robust z-score above rolling median to flag a block as a burst")
    ap.add_argument("--burst-gap", type=int, default=15,
                    help="anomalous blocks this close are merged into one episode")
    ap.add_argument("--burst-min-slots", type=int, default=10000,
                    help="episodes below this many slot writes are counted but not listed")
    args = ap.parse_args()

    labels = load_labels(args.labels)
    baseline = load_baseline(args.baseline_csv)
    prefix, files = resolve_inputs(args.input)

    print(f"reading {files['records']}", file=sys.stderr)
    records = pd.read_parquet(files["records"]).sort_values("block").reset_index(drop=True)
    lo, hi = int(records["block"].min()), int(records["block"].max())
    n_blocks = len(records)
    split_block = lo + (hi - lo) // 2 + 1  # first block of the second half
    window_hours = (hi - lo + 1) * BLOCK_SECONDS / 3600
    half_hours = (hi - split_block + 1) * BLOCK_SECONDS / 3600

    def hour_idx(blocks):
        return (((np.asarray(blocks, dtype=np.int64) - lo) * BLOCK_SECONDS) // 3600).astype(int)

    hours_total = int(hour_idx(hi)) + 1

    anchor = None
    if args.genesis_time is not None:
        anchor = (0, args.genesis_time)
    if args.anchor:
        b, t = args.anchor.split(":")
        anchor = (int(b), int(t))

    def block_time(b):
        return anchor[1] + (b - anchor[0]) * BLOCK_SECONDS

    def when(b):
        if anchor is None:
            return f"+{(b - lo) * BLOCK_SECONDS / 3600:.1f}h"
        return datetime.fromtimestamp(block_time(b), tz=timezone.utc).strftime("%m-%d %H:%M")

    def hour_label(h):
        if anchor is None:
            return f"+{int(h):02d}h"
        return datetime.fromtimestamp(block_time(lo) + h * 3600, tz=timezone.utc).strftime("%m-%d %H:00")

    def name(addr):
        lab = labels.get(addr)
        if not lab and addr[2:].startswith("0" * 8):
            lab = "vanity/CREATE2"
        return f"`{addr}`" + (f" {lab}" if lab else "")

    # ---- passes ---------------------------------------------------------
    print(f"reading {files['accounts']} (streaming)", file=sys.stderr)
    acc, acc_totals, block_acc, block_top = stream_accounts(files["accounts"], split_block)
    created_set = set(acc.index[acc["created"].astype(bool)])

    print(f"reading {files['slots']} (streaming)", file=sys.stderr)
    sl, block_fresh, proxies = stream_slots(files["slots"], split_block, created_set)

    A = sl.join(acc, how="outer")
    for c in ("w1", "w2", "f1", "f2", "fresh_fixed", "fixed_writes", "t1", "t2"):
        A[c] = A[c].fillna(0).astype("int64")
    for c in ("created", "contract", "created_with_slots", "started_storage"):
        A[c] = A[c].fillna(False).astype(bool)
    A["writes"] = A["w1"] + A["w2"]
    A["fresh"] = A["f1"] + A["f2"]
    A["churn"] = A["writes"] - A["fresh"]
    A["fresh_mapping"] = A["fresh"] - A["fresh_fixed"]
    A["touches"] = A["t1"] + A["t2"]
    A["nonce_delta"] = (A["nonce_max"] - A["nonce_min"]).fillna(0)
    # system/protocol accounts: touched (almost) every block with no fresh writes and at most
    # a couple of slot writes per touch (fee sinks, EIP-2935 ring buffer). A contract touched
    # every block that still adds state (USDC, a CTF) is organic and stays in the main tables.
    A["system"] = (A["touches"] >= 0.99 * n_blocks) & (A["fresh"] == 0) & (A["writes"] <= 2 * A["touches"])
    A["destroyed"] = A["created_max_block"].notna() & (A["created_max_block"] > A["first_block"])
    A["is_contract"] = A["contract"] | A["created_with_slots"]

    total_writes = int(A["writes"].sum())
    total_fresh = int(A["fresh"].sum())
    total_fresh_fixed = int(A["fresh_fixed"].sum())
    fresh_h = [int(A["f1"].sum()), int(A["f2"].sum())]
    writes_h = [int(A["w1"].sum()), int(A["w2"].sum())]

    # watchlist: two ways in. "≥10×" catches things appearing from nothing;
    # "accelerating" catches an already-large writer that doubled or more.
    cand = A[["fresh", "f1", "f2", "fresh_mapping", "churn", "created",
              "started_storage", "is_contract"]].copy()
    if baseline is not None:
        cand["before"] = baseline["fresh_slots"].reindex(A.index).fillna(0).astype(int)
        cand["after"] = cand["fresh"]
        mover_note = "whole window vs baseline run"
    else:
        cand["before"], cand["after"] = cand["f1"], cand["f2"]
        mover_note = "second half vs first half"
    tenx = (cand["after"] >= args.mover_min) & (cand["after"] >= 10 * np.maximum(cand["before"], 1))
    accel = (cand["after"] - cand["before"] >= args.accel_min) & \
            (cand["after"] >= 2 * cand["before"]) & ~tenx
    cand["why"] = np.where(tenx, "≥10×", np.where(accel, "accelerating", ""))
    movers = cand[tenx | accel].sort_values("after", ascending=False)

    # detail set: everything that will appear in an address table
    organic = A[~A["system"]]
    top_growth = organic.nlargest(args.top, "fresh")
    top_churn = organic[organic["churn"] > 0].nlargest(args.top, "churn")
    top_writes = organic.nlargest(args.top, "writes")
    detail_addrs = set(top_growth.index) | set(top_churn.index) | set(top_writes.index) \
        | set(movers.index[: args.top])
    print(f"re-reading slots for {len(detail_addrs)} detailed addresses", file=sys.stderr)
    det = stream_details(files["slots"], detail_addrs, hour_idx, hours_total)

    # per-block enrichment
    records["fresh"] = block_fresh.reindex(records["block"]).fillna(0).to_numpy().astype(int)
    records["created"] = block_acc["created"].reindex(records["block"]).fillna(0).to_numpy().astype(int)
    records["hour"] = hour_idx(records["block"].to_numpy())
    episodes = find_episodes(records, block_fresh, block_top, args.burst_z, gap=args.burst_gap)
    big_episodes = [e for e in episodes if e["slots"] >= args.burst_min_slots]
    if episodes:
        ep_owner = pd.DataFrame(episodes).groupby("owner").agg(
            episodes=("start", "size"), blocks=("blocks", "sum"), slots=("slots", "sum"),
            fresh=("fresh", "sum"), peak=("peak", "max")).sort_values("slots", ascending=False)
    else:
        ep_owner = pd.DataFrame()

    # growth ledger numbers
    created_h = acc_totals["created"]
    created_contract_total = int((A["created"] & A["is_contract"]).sum())
    created_eoa_total = int((A["created"] & ~A["is_contract"]).sum())
    bytes_h = [created_h[i] * args.bytes_per_account + fresh_h[i] * args.bytes_per_slot for i in (0, 1)]
    bytes_total = sum(bytes_h)
    bytes_per_day = bytes_total / window_hours * 24
    factories = A[A["is_contract"] & (A["nonce_delta"] >= 1)].sort_values("nonce_delta", ascending=False)
    # A contract nonce can only rise by CREATE, so creations should be visible in the
    # blocks it was touched in. When they are not, the "contract" is an EIP-7702
    # delegated EOA (its nonce counts transactions) or it deploys same-tx ephemeral
    # contracts that never reach state. Check the top candidates.
    fac_check = factories.head(max(args.top * 4, 100))
    print(f"re-reading accounts for {len(fac_check)} factory candidates", file=sys.stderr)
    fac_blocks = stream_account_blocks(files["accounts"], set(fac_check.index))
    visible = pd.Series({a: int(block_acc["created"].reindex(b).fillna(0).sum())
                         for a, b in fac_blocks.items()}, dtype="int64")
    factories = factories.join(visible.rename("visible_children"))
    factories["verified"] = factories["visible_children"].notna() & \
        (factories["visible_children"] >= 0.8 * factories["nonce_delta"])
    nonce_creates = int(factories["nonce_delta"].sum())
    verified_creates = int(factories.loc[factories["verified"], "nonce_delta"].sum())
    started_storage = A[A["started_storage"]].sort_values("fresh", ascending=False)
    destroyed_n = int(A["destroyed"].sum())
    senders = A[~A["is_contract"] & ~A["created"] & (A["nonce_delta"] >= 1)] \
        .sort_values("nonce_delta", ascending=False)

    def active_hours(addr, half=None):
        d = det.get(addr)
        if d is None:
            return None
        h = d["hourly"]
        if half == 2:
            h = h[int(hour_idx(split_block)):]
        return int((h > 0).sum()), len(h)

    # ---- assemble report -------------------------------------------------
    L = []
    L.append("# State growth report\n")
    L.append(f"**source**: `{os.path.basename(prefix)}` · blocks **{fmt_int(lo)}–{fmt_int(hi)}** "
             f"({fmt_int(n_blocks)} blocks, ~{window_hours:.1f}h at {BLOCK_SECONDS:.0f}s/block"
             + (f", {when(lo)} → {when(hi)} UTC" if anchor else "") + ")\n")
    L.append(f"**halves**: first **{fmt_int(lo)}–{fmt_int(split_block - 1)}**, "
             f"second **{fmt_int(split_block)}–{fmt_int(hi)}** (~{half_hours:.1f}h each). "
             "Δ columns are the percent change between halves unless stated otherwise.\n")

    # TL;DR
    L.append("## TL;DR\n")
    tl = []
    tl.append(f"State grew by an estimated **{fmt_bytes(bytes_total)}** "
              f"(≈ {fmt_bytes(bytes_per_day)}/day, {fmt_bytes(bytes_per_day * 365)}/yr at this pace): "
              f"**{fmt_int(sum(created_h))}** new accounts ({fmt_int(created_contract_total)} contracts, "
              f"{fmt_int(created_eoa_total)} EOAs) and **{fmt_int(total_fresh)}** fresh slots, "
              f"{ratio(total_fresh - total_fresh_fixed, total_fresh)} of them mapping keys (unbounded). "
              f"Fresh slots {pct(fresh_h[0], fresh_h[1])} second half vs first; churn writes "
              f"{pct(writes_h[0] - fresh_h[0], writes_h[1] - fresh_h[1])}.")
    if len(top_growth):
        g1 = top_growth.index[0]
        top3 = top_growth["fresh"].head(3).sum()
        tl.append(f"Top 3 growth sources account for {ratio(top3, total_fresh)} of fresh slots; "
                  f"#1 is {name(g1)} with {fmt_int(top_growth['fresh'].iloc[0])} "
                  f"({ratio(top_growth['fresh'].iloc[0], total_fresh)}), "
                  f"{ratio(top_growth['fresh_mapping'].iloc[0], top_growth['fresh'].iloc[0])} mapping keys.")
    if episodes:
        e = episodes[0]
        o = ep_owner.index[0]
        tl.append(f"{len(big_episodes)} burst episode(s) ≥{fmt_int(args.burst_min_slots)} slots "
                  f"({len(episodes)} in all) owned by {len(ep_owner)} address(es); the largest ran "
                  f"{e['blocks']} block(s) from {fmt_int(e['start'])} ({when(e['start'])}), "
                  f"{fmt_int(e['slots'])} slot writes, {ratio(e['fresh'], e['slots'])} fresh. "
                  f"Biggest burst owner: {name(o) if o else '?'} "
                  f"({int(ep_owner['episodes'].iloc[0])} episodes, {fmt_int(ep_owner['slots'].iloc[0])} slots).")
    else:
        tl.append("No burst episodes above the threshold.")
    tl.append(f"{fmt_int(int(factories['verified'].sum()))} verified factories deployed "
              f"{fmt_int(verified_creates)} children in-window; {fmt_int(len(proxies))} new ERC-1967 proxies; "
              f"{fmt_int(len(started_storage))} existing contracts wrote storage for the first time. "
              + (f"Contract-flagged nonces rose by {fmt_int(nonce_creates)} in total against "
                 f"{fmt_int(sum(created_h))} created accounts, so most of that is EIP-7702 EOAs sending "
                 f"transactions or same-tx ephemeral CREATEs, not state growth."
                 if nonce_creates > 1.5 * sum(created_h) else ""))
    tl.append(f"Watchlist ({mover_note}; ≥10× with ≥{fmt_int(args.mover_min)}, or +{fmt_int(args.accel_min)} "
              f"and ≥2×): **{len(movers)}** address(es)"
              + (f", led by {name(movers.index[0])} ({movers['why'].iloc[0]})." if len(movers) else "."))
    for t in tl:
        L.append(f"- {t}")
    L.append("")

    # growth ledger
    L.append("## Growth ledger\n")
    L.append("*What was added to state. Fresh slots are an upper bound on net growth (clears are "
             "not visible in a reverse diff); see the clear counts in the attribution table for a "
             "lower bound on deletions among the big writers.*\n")
    L.append("| metric | 1st half | 2nd half | Δ | window |")
    L.append("|---|---|---|---|---|")

    def row(label, a, b, fmt=fmt_int, total=None):
        t = fmt(total if total is not None else a + b)
        L.append(f"| {label} | {fmt(a)} | {fmt(b)} | {pct(a, b)} | {t} |")

    row("accounts created", created_h[0], created_h[1])
    row("— created with storage in the same block", acc_totals["created_with_slots"][0],
        acc_totals["created_with_slots"][1])
    row("fresh slot writes", fresh_h[0], fresh_h[1])
    row("slot writes (all)", writes_h[0], writes_h[1])
    row("— churn (rewrites of existing slots)", writes_h[0] - fresh_h[0], writes_h[1] - fresh_h[1])
    row("estimated bytes added", bytes_h[0], bytes_h[1], fmt=fmt_bytes)
    L.append(f"| accounts touched (rows) | {fmt_int(acc_totals['rows'][0])} | {fmt_int(acc_totals['rows'][1])} "
             f"| {pct(acc_totals['rows'][0], acc_totals['rows'][1])} | {fmt_int(sum(acc_totals['rows']))} |")
    L.append(f"| distinct addresses touched | {fmt_int(int((A['t1'] > 0).sum()))} | "
             f"{fmt_int(int((A['t2'] > 0).sum()))} | {pct(int((A['t1'] > 0).sum()), int((A['t2'] > 0).sum()))} "
             f"| {fmt_int(len(A))} |")
    L.append("")
    L.append(f"- Of the {fmt_int(sum(created_h))} created accounts: **{fmt_int(created_contract_total)} contracts** "
             f"(storage at creation or code seen on a later touch) and **{fmt_int(created_eoa_total)} EOAs**; "
             f"{fmt_int(len(proxies))} of the contracts are ERC-1967 proxies (impl/beacon slot written at creation). "
             "A contract created with no storage and never touched again (an unused clone, say) is "
             "indistinguishable from an EOA here and lands in the EOA count.")
    L.append(f"- Fresh slots by key shape: **{fmt_int(total_fresh - total_fresh_fixed)} mapping/array keys** "
             f"(unbounded) vs **{fmt_int(total_fresh_fixed)} fixed-variable keys** (key < 2³², bounded). "
             f"v0 records make every key look like a mapping key.")
    L.append(f"- Byte estimate uses {args.bytes_per_slot:.0f} B/slot and {args.bytes_per_account:.0f} B/account "
             f"(`--bytes-per-slot`, `--bytes-per-account`); calibrate once against measured bor DB growth.")
    if destroyed_n:
        L.append(f"- {fmt_int(destroyed_n)} address(es) were touched, then appeared as non-existent again "
                 f"later in the window (destroyed/recreated).")
    L.append("")

    # growth attribution
    L.append(f"## Growth attribution — top {args.top} by fresh slot writes\n")
    L.append("*Who is adding state. `map%` = share of fresh writes to mapping-shaped keys (unbounded growth). "
             "`width` = distinct slots ÷ distinct 8-byte key prefixes: ≈1 means plain mapping entries, "
             "larger means arrays or structs (keccak base + offset) — a width of 20 is a 20-field struct "
             "per user, or arrays being appended to. "
             "`clear` = retouches of a slot that came back empty, i.e. it was deleted in between — a high "
             "clear/fresh ratio means the state is ephemeral. `active` = hours with ≥1 write. "
             "System accounts touched every block are listed separately.*\n")
    L.append("| address | fresh | share | map% | 1st → 2nd | churn | distinct | width | clear | active | hourly | notes |")
    L.append("|---|---|---|---|---|---|---|---|---|---|---|---|")
    for addr, r in top_growth.iterrows():
        d = det.get(addr, {})
        notes = []
        if r["created"]:
            notes.append("**created in window**")
        if r["started_storage"]:
            notes.append("**first storage**")
        if addr in proxies:
            notes.append("proxy")
        if not r["is_contract"]:
            notes.append("EOA?")
        if r["nonce_delta"] >= 1 and r["is_contract"]:
            notes.append(f"factory +{fmt_int(r['nonce_delta'])}")
        ah = active_hours(addr)
        width = f"{d['distinct'] / d['bases']:.1f}" if d.get("bases") else "?"
        L.append(f"| {name(addr)} | {fmt_int(r['fresh'])} | {ratio(r['fresh'], total_fresh)} | "
                 f"{ratio(r['fresh_mapping'], r['fresh'])} | {fmt_int(r['f1'])} → {fmt_int(r['f2'])} ({pct(r['f1'], r['f2'])}) | "
                 f"{fmt_int(r['churn'])} | {fmt_int(d.get('distinct', 0))} | {width} | {fmt_int(d.get('clears', 0))} | "
                 f"{ah[0]}/{ah[1]}h | {spark(d.get('hourly', []))} | {', '.join(notes)} |")
    L.append("")

    # watchlist
    L.append(f"## Watchlist — is a new {XEN_LABEL} forming?\n")
    L.append(f"*Two ways in ({mover_note}): fresh writes rose ≥10× and reached ≥{fmt_int(args.mover_min)}, "
             f"or rose by ≥{fmt_int(args.accel_min)} and at least doubled (`accelerating`). "
             "Sorted by absolute fresh writes, not ratio. `sustained` = active in at least half the hours of "
             "the comparison period, `spike` otherwise. {n} matched.*\n".replace("{n}", str(len(movers))))
    if len(movers):
        L.append("| address | before | after | Δ | why | map% | churn | pattern | notes |")
        L.append("|---|---|---|---|---|---|---|---|---|")
        for addr, r in movers.head(args.top).iterrows():
            ah = active_hours(addr, half=None if baseline is not None else 2)
            if ah is None:
                pattern = "?"
            else:
                pattern = "sustained" if ah[0] >= max(ah[1] // 2, 1) else "spike"
                pattern += f" ({ah[0]}/{ah[1]}h)"
            notes = []
            if r["created"]:
                notes.append("**created in window**")
            if r["started_storage"]:
                notes.append("**first storage**")
            if addr in proxies:
                notes.append("proxy")
            L.append(f"| {name(addr)} | {fmt_int(r['before'])} | {fmt_int(r['after'])} | {pct(r['before'], r['after'])} | "
                     f"{r['why']} | {ratio(r['fresh_mapping'], r['fresh'])} | {fmt_int(r['churn'])} | {pattern} | "
                     f"{', '.join(notes)} |")
    else:
        L.append("_Nothing matched — no address jumped 10× above the floor._")
    L.append("")

    # factories
    L.append("## Factories and new contracts\n")
    L.append("*A contract's nonce only increments on CREATE/CREATE2, so its in-window nonce delta should equal the "
             "child contracts it deployed between its first and last touch. `visible children` counts accounts "
             "created in the blocks the candidate was touched in (an upper bound, shared with any other factory "
             "active in those blocks). When it falls well short of the nonce delta, the address is not a factory: "
             "it is an EIP-7702 delegated EOA (code hash set, nonce counts its transactions) or it deploys "
             "same-transaction ephemeral contracts that never persist. Only `✓` rows add accounts to state.*\n")
    if len(factories):
        L.append(f"- Nonce deltas across all {fmt_int(len(factories))} contract-flagged addresses sum to "
                 f"**{fmt_int(nonce_creates)}**; **{fmt_int(verified_creates)}** of that is from "
                 f"{fmt_int(int(factories['verified'].sum()))} verified factories. The window created "
                 f"{fmt_int(sum(created_h))} accounts in total.\n")
        L.append("| address | nonce Δ | visible children | verdict | lifetime nonce | touches | fresh slots | notes |")
        L.append("|---|---|---|---|---|---|---|---|")
        shown = factories[factories["visible_children"].notna()].sort_values(
            ["verified", "nonce_delta"], ascending=[False, False]).head(args.top)
        for addr, r in shown.iterrows():
            notes = "**created in window**" if r["created"] else ""
            vis = int(r["visible_children"])
            verdict = "✓ factory" if r["verified"] else (
                "7702 EOA / ephemeral" if vis < 0.2 * r["nonce_delta"] else "partial")
            L.append(f"| {name(addr)} | {fmt_int(r['nonce_delta'])} | {fmt_int(vis)} | {verdict} | "
                     f"{fmt_int(r['nonce_max'])} | {fmt_int(r['touches'])} | {fmt_int(r['fresh'])} | {notes} |")
        L.append("")
    else:
        L.append("_No contract nonce changed in the window._\n")
    if len(started_storage):
        L.append(f"**Existing contracts that wrote storage for the first time** ({fmt_int(len(started_storage))}; top by fresh slots):\n")
        L.append("| address | fresh slots | map% | touches | first seen |")
        L.append("|---|---|---|---|---|")
        for addr, r in started_storage.head(min(args.top, 10)).iterrows():
            L.append(f"| {name(addr)} | {fmt_int(r['fresh'])} | {ratio(r['fresh_mapping'], r['fresh'])} | "
                     f"{fmt_int(r['touches'])} | {fmt_int(r['first_block'])} ({when(r['first_block'])}) |")
        L.append("")
    created_contracts = A[A["created"] & A["is_contract"]].sort_values("fresh", ascending=False)
    if len(created_contracts):
        L.append(f"**Contracts created in window with the most storage** (of {fmt_int(len(created_contracts))}):\n")
        L.append("| address | fresh slots | map% | created at | notes |")
        L.append("|---|---|---|---|---|")
        for addr, r in created_contracts.head(min(args.top, 10)).iterrows():
            notes = "proxy" if addr in proxies else ""
            L.append(f"| {name(addr)} | {fmt_int(r['fresh'])} | {ratio(r['fresh_mapping'], r['fresh'])} | "
                     f"{fmt_int(r['first_block'])} ({when(r['first_block'])}) | {notes} |")
        L.append("")

    # burst episodes
    L.append("## Burst episodes\n")
    L.append(f"*Runs of blocks whose slot writes sit ≥{args.burst_z:.0f} robust σ above a rolling 1h median "
             f"(and ≥2× it), merged when ≤{args.burst_gap} blocks apart. `owner` is the address writing the most "
             "slots across the episode. Low fresh% = churn burst (a hot loop); high fresh% = state is being minted. "
             f"{len(episodes)} episodes found; the full list is in `episodes.csv` with `--csv-dir`.*\n")
    if episodes:
        L.append("**By owner**\n")
        L.append("| owner | episodes | blocks | slots in bursts | fresh% | peak/block | ≈ share of owner's fresh writes (upper bound) |")
        L.append("|---|---|---|---|---|---|---|")
        for o, r in ep_owner.head(min(args.top, 10)).iterrows():
            of = int(A.loc[o, "fresh"]) if o in A.index else 0
            L.append(f"| {name(o) if o else '?'} | {int(r['episodes'])} | {int(r['blocks'])} | {fmt_int(r['slots'])} | "
                     f"{ratio(r['fresh'], r['slots'])} | {fmt_int(r['peak'])} | {ratio(min(r['fresh'], of), of)} |")
        L.append("")
        L.append(f"**Largest episodes** (≥{fmt_int(args.burst_min_slots)} slots; {len(big_episodes)} qualify)\n")
        L.append("| start | when | blocks | slots | fresh% | peak/block | accounts | owner (slots) |")
        L.append("|---|---|---|---|---|---|---|---|")
        for e in big_episodes[: args.top]:
            L.append(f"| {fmt_int(e['start'])} | {when(e['start'])} | {e['blocks']} | {fmt_int(e['slots'])} | "
                     f"{ratio(e['fresh'], e['slots'])} | {fmt_int(e['peak'])} | {fmt_int(e['accounts'])} | "
                     f"{name(e['owner']) if e['owner'] else '?'} ({fmt_int(e['owner_slots'])}) |")
    else:
        L.append("_No bursts above threshold._")
    L.append("")

    # hourly
    hourly = records.groupby("hour").agg(
        slot_writes=("n_slots", "sum"), fresh=("fresh", "sum"), account_writes=("n_accounts", "sum"),
        created=("created", "sum"), data_kb=("data_bytes", lambda s: s.sum() / 1024)).reset_index()
    L.append("## Hourly\n")
    if anchor is None:
        L.append("*hours are relative to the first block of the window; pass `--anchor BLOCK:UNIXTS` for UTC*\n")
    L.append(f"- slot writes `{spark(hourly['slot_writes'])}`")
    L.append(f"- fresh writes `{spark(hourly['fresh'])}`")
    L.append(f"- accounts created `{spark(hourly['created'])}`\n")
    L.append("| hour | slot writes | fresh | fresh% | account writes | created | pre-state KB |")
    L.append("|---|---|---|---|---|---|---|")
    for _, r in hourly.iterrows():
        L.append(f"| {hour_label(r['hour'])} | {fmt_int(r['slot_writes'])} | {fmt_int(r['fresh'])} | "
                 f"{ratio(r['fresh'], r['slot_writes'])} | {fmt_int(r['account_writes'])} | "
                 f"{fmt_int(r['created'])} | {fmt_int(r['data_kb'])} |")
    L.append("")
    L.append("*`pre-state KB` is the size of the previous values, so a block with many slots and few KB is "
             "writing mostly fresh slots.*\n")

    # I/O profile
    L.append("## I/O profile (churn)\n")
    q = records["n_slots"].quantile([0.5, 0.9, 0.99])
    qa = records["n_accounts"].quantile([0.5, 0.9, 0.99])
    full = records[records["n_slots"] >= args.full_block_slots]
    L.append("| per block | p50 | p90 | p99 | max |")
    L.append("|---|---|---|---|---|")
    L.append(f"| slot writes | {fmt_int(q[0.5])} | {fmt_int(q[0.9])} | {fmt_int(q[0.99])} | {fmt_int(records['n_slots'].max())} |")
    L.append(f"| accounts touched | {fmt_int(qa[0.5])} | {fmt_int(qa[0.9])} | {fmt_int(qa[0.99])} | {fmt_int(records['n_accounts'].max())} |")
    L.append("")
    L.append(f"- **{fmt_int(len(full))}** SSTORE-saturated blocks (≥{fmt_int(args.full_block_slots)} slot writes, "
             f"`--full-block-slots`), {ratio(len(full), n_blocks)} of all blocks; "
             f"{ratio(int(full['fresh'].sum()), int(full['n_slots'].sum()))} of their writes were fresh.")
    L.append(f"- Churn (rewrites) is {ratio(total_writes - total_fresh, total_writes)} of all slot writes.\n")

    busiest = records.nlargest(10, "n_slots")
    L.append("**Busiest blocks**\n")
    L.append("| block | when | slots | fresh% | accounts | created | top writer (slots) |")
    L.append("|---|---|---|---|---|---|---|")
    for _, r in busiest.iterrows():
        t = block_top.loc[r["block"]] if r["block"] in block_top.index else None
        tw = f"{name(t['address'])} ({fmt_int(t['n_slots'])})" if t is not None else "?"
        L.append(f"| {fmt_int(r['block'])} | {when(r['block'])} | {fmt_int(r['n_slots'])} | "
                 f"{ratio(r['fresh'], r['n_slots'])} | {fmt_int(r['n_accounts'])} | {fmt_int(r['created'])} | {tw} |")
    L.append("")

    L.append(f"**Churn leaders — top {args.top} by rewrites of existing slots**\n")
    L.append("*`repeat` = 1 − distinct/total: high = hot loop over few slots (oracles, counters, locks); "
             "`fixed%` = share of writes to fixed-variable keys.*\n")
    L.append("| address | churn | fresh | distinct | repeat | fixed% | hourly |")
    L.append("|---|---|---|---|---|---|---|")
    for addr, r in top_churn.iterrows():
        d = det.get(addr, {})
        dist = d.get("distinct", 0)
        rep = ratio(r["writes"] - dist, r["writes"]) if dist else "?"
        L.append(f"| {name(addr)} | {fmt_int(r['churn'])} | {fmt_int(r['fresh'])} | {fmt_int(dist)} | {rep} | "
                 f"{ratio(r['fixed_writes'], r['writes'])} | {spark(d.get('hourly', []))} |")
    L.append("")

    system = A[A["system"]].sort_values("writes", ascending=False)
    if len(system):
        L.append("**System accounts** (touched in ≥99% of blocks with no fresh writes and ≤2 slot writes per "
                 "touch — fee sinks, ring buffers; excluded from the tables above):\n")
        L.append("| address | touches | slot writes | fresh | kind |")
        L.append("|---|---|---|---|---|")
        for addr, r in system.iterrows():
            kind = "contract" if r["is_contract"] else "EOA (balance/nonce only)"
            L.append(f"| {name(addr)} | {fmt_int(r['touches'])} | {fmt_int(r['writes'])} | {fmt_int(r['fresh'])} | {kind} |")
        L.append("")

    # concentration
    sw, n1 = share_table(organic["writes"])
    sf, _ = share_table(organic["fresh"])
    L.append("## Concentration\n")
    L.append("| scope | share of slot writes | share of fresh writes |")
    L.append("|---|---|---|")
    for label, a, b in zip(("top address", "top 10", "top 100", f"top 1% ({fmt_int(n1)} addresses)"), sw, sf):
        L.append(f"| {label} | {a:.1f}% | {b:.1f}% |")
    L.append("")

    # senders
    if len(senders):
        L.append("## Most active senders (EOAs by nonce delta)\n")
        L.append("*An EOA's nonce delta between its first and last touch is the number of transactions it sent "
                 "in between. Bots, relayers and distribution wallets show up here.*\n")
        L.append("| address | txs in window | lifetime nonce | touches |")
        L.append("|---|---|---|---|")
        for addr, r in senders.head(min(args.top, 10)).iterrows():
            L.append(f"| {name(addr)} | {fmt_int(r['nonce_delta'])} | {fmt_int(r['nonce_max'])} | "
                     f"{fmt_int(r['touches'])} |")
        L.append("")

    # footer
    L.append("---\n")
    dq = []
    df_n = sum(acc_totals["decode_failed"])
    if df_n:
        dq.append(f"⚠ {fmt_int(df_n)} account rows failed RLP decode (fields zero).")
    dq.append(f"{fmt_int(sum(acc_totals['contract_rows']))} contract rows, "
              f"{fmt_int(sum(acc_totals['eoa_rows']))} EOA rows, {fmt_int(sum(created_h))} creation rows.")
    dq.append("EIP-7702 delegated EOAs carry a code hash and are counted as contracts.")
    L.append("*All values are PRE-state (reverse diff). "
             f"Timestamps are estimated from block numbers at {BLOCK_SECONDS:.0f}s/block"
             + (" from the given anchor" if anchor else "") + ". " + " ".join(dq) + "*\n")

    report = "\n".join(L)
    with open(args.out, "w") as f:
        f.write(report)
    print(f"wrote {args.out}", file=sys.stderr)

    if args.csv_dir:
        os.makedirs(args.csv_dir, exist_ok=True)
        out = A[["writes", "fresh", "fresh_mapping", "fresh_fixed", "fixed_writes", "churn",
                 "w1", "w2", "f1", "f2", "touches", "t1", "t2", "created", "is_contract",
                 "started_storage", "system", "nonce_min", "nonce_max", "nonce_delta",
                 "first_block", "last_block"]].copy()
        out = out.rename(columns={"writes": "slot_writes", "fresh": "fresh_slots"})
        out["label"] = [labels.get(a, "") for a in out.index]
        out["proxy"] = [a in proxies for a in out.index]
        out["distinct_slots"] = [det[a]["distinct"] if a in det else np.nan for a in out.index]
        out["key_bases"] = [det[a]["bases"] if a in det else np.nan for a in out.index]
        out["retouches"] = [det[a]["retouch"] if a in det else np.nan for a in out.index]
        out["clears"] = [det[a]["clears"] if a in det else np.nan for a in out.index]
        out = out.join(factories[["visible_children", "verified"]].rename(
            columns={"verified": "verified_factory"}))
        out = out.sort_values("fresh_slots", ascending=False)
        out.index.name = "address"
        csv_path = os.path.join(args.csv_dir, "addresses.csv")
        out.to_csv(csv_path)
        print(f"wrote {csv_path} ({len(out)} rows)", file=sys.stderr)
        ep_path = os.path.join(args.csv_dir, "episodes.csv")
        pd.DataFrame(episodes).to_csv(ep_path, index=False)
        blocks_path = os.path.join(args.csv_dir, "blocks.csv")
        records[["block", "n_accounts", "n_slots", "fresh", "created", "data_bytes"]].to_csv(blocks_path, index=False)
        print(f"wrote {ep_path}, {blocks_path}", file=sys.stderr)


if __name__ == "__main__":
    main()
