#!/usr/bin/env python3
"""report.py — in-depth state-activity report from statescan parquet dumps.

Reads the three parquet files written by cmd/statescan (records, accounts,
slots) and produces a markdown report plus optional CSV tables, without
ever holding the full slots table in memory (streamed with pyarrow).

Semantics reminder: every value in the dump is PRE-state (the state history
is a reverse diff). "created" below means the account/slot did not exist
before that block; "fresh write" means the slot was zero/nonexistent before
the block that touched it.

Usage:
    python3 report.py --input tmp-state --out report.md
    python3 report.py --input tmp-state/state-2026-10-13 --top 30 --csv-dir csv/

Requirements: pandas, pyarrow (nothing else).
"""
import argparse
import glob
import os
import sys
from collections import defaultdict

import pandas as pd
import pyarrow.parquet as pq

# bor mainnet target block time; timestamps in the report are estimates
BLOCK_SECONDS = 2.0
BATCH_ROWS = 2_000_000


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


def stream_counts(path, key_col, extra_counts=None):
    """One pass over a parquet file: value_counts(key_col) merged into a dict,
    plus optional per-row boolean counts."""
    counts = defaultdict(int)
    other = defaultdict(int)
    pf = pq.ParquetFile(path)
    for batch in pf.iter_batches(batch_size=BATCH_ROWS):
        tab = batch.to_pandas()
        vc = tab[key_col].value_counts()
        idx = vc.index.to_numpy()
        for a, n in zip(idx, vc.to_numpy()):
            counts[a] += int(n)
        if extra_counts:
            for name, mask in extra_counts(tab).items():
                other[name] += int(mask.sum())
    return counts, other


def stream_account_stats(path):
    """Per-address: touches, created-any, contract-any (has code hash),
    plus global counters."""
    info = {}  # address -> [touches, created, is_contract, decode_failed]
    totals = defaultdict(int)
    pf = pq.ParquetFile(path)
    for batch in pf.iter_batches(batch_size=BATCH_ROWS):
        tab = batch.to_pandas()
        totals["rows"] += len(tab)
        totals["created"] += int((~tab["present"]).sum())
        totals["decode_failed"] += int(tab["decode_failed"].sum())
        is_contract = tab["code_hash"].fillna("").str.len() > 0
        totals["contract_rows"] += int(is_contract.sum())
        grouped = tab.groupby("address", sort=False).agg(
            touches=("address", "size"),
            created=("present", lambda s: int((~s).any())),
            contract=("code_hash", lambda s: int(s.fillna("").str.len().max() > 0)),
        )
        for addr, row in grouped.iterrows():
            e = info.get(addr)
            if e is None:
                info[addr] = [int(row.touches), int(row.created), int(row.contract), 0]
            else:
                e[0] += int(row.touches)
                e[1] = max(e[1], int(row.created))
                e[2] = max(e[2], int(row.contract))
    return info, totals


def top_address_details(path, addresses):
    """Second pass over slots.parquet restricted to `addresses`: distinct
    slots, first/last seen block."""
    import pyarrow.compute as pc
    import pyarrow as pa

    value_set = pa.array(list(addresses))
    distinct = defaultdict(set)  # addr -> set of truncated slot hashes
    first, last = {}, {}
    pf = pq.ParquetFile(path)
    for batch in pf.iter_batches(batch_size=BATCH_ROWS):
        tab = batch
        mask = pc.is_in(tab.column("address"), value_set=value_set)
        tab = tab.filter(mask)
        if tab.num_rows == 0:
            continue
        addr = tab.column("address").to_pylist()
        slot = tab.column("slot").to_pylist()
        block = tab.column("block").to_pylist()
        for a, s, b in zip(addr, slot, block):
            # 64-bit truncation of the slot key: collision risk is negligible
            distinct[a].add(int(s[2:18], 16))
            if a not in first or b < first[a]:
                first[a] = b
            if a not in last or b > last[a]:
                last[a] = b
    return distinct, first, last


def fmt_int(n):
    return f"{n:,}"


def fmt_addr(a):
    return a


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--input", required=True, help="statescan output prefix or directory")
    ap.add_argument("--out", default="report.md", help="markdown report path")
    ap.add_argument("--top", type=int, default=20, help="top-N address tables")
    ap.add_argument("--csv-dir", default=None, help="also write per-address CSV here")
    ap.add_argument("--genesis-time", type=int, default=None,
                    help="unix timestamp of block 0; when set, hourly labels are UTC dates, "
                         "otherwise hours are relative to the first block of the window")
    args = ap.parse_args()

    prefix, files = resolve_inputs(args.input)
    print(f"reading {files['records']}", file=sys.stderr)
    records = pd.read_parquet(files["records"])
    records = records.sort_values("state_id")

    print(f"reading {files['accounts']} (streaming)", file=sys.stderr)
    acc_info, acc_totals = stream_account_stats(files["accounts"])

    print(f"reading {files['slots']} (streaming)", file=sys.stderr)
    slot_writes, slot_other = stream_counts(
        files["slots"], "address",
        extra_counts=lambda t: {"fresh": t["value_len"] == 0},
    )

    top_n = sorted(slot_writes.items(), key=lambda kv: -kv[1])[: args.top]
    print(f"re-reading slots for top {len(top_n)} addresses", file=sys.stderr)
    distinct, first, last = top_address_details(files["slots"], [a for a, _ in top_n])

    # ---- assemble report -------------------------------------------------
    lo, hi = int(records["block"].min()), int(records["block"].max())
    span_blocks = hi - lo + 1
    total_slots = sum(slot_writes.values())
    L = []
    L.append(f"# State activity report\n")
    L.append(f"**source**: `{os.path.basename(prefix)}` · blocks **{fmt_int(lo)}–{fmt_int(hi)}** "
             f"(~{span_blocks * BLOCK_SECONDS / 3600:.1f}h at {BLOCK_SECONDS:.0f}s/block)\n")

    L.append("## Overview\n")
    L.append("| metric | value |")
    L.append("|---|---|")
    L.append(f"| history records (blocks with state changes) | {fmt_int(len(records))} |")
    L.append(f"| account touch rows | {fmt_int(acc_totals['rows'])} |")
    L.append(f"| distinct touched addresses | {fmt_int(len(acc_info))} |")
    L.append(f"| accounts created by a block | {fmt_int(acc_totals['created'])} |")
    L.append(f"| contract account rows (has code hash) | {fmt_int(acc_totals['contract_rows'])} |")
    L.append(f"| slot writes | {fmt_int(total_slots)} |")
    L.append(f"| — fresh writes (slot previously zero/nonexistent) | {fmt_int(slot_other['fresh'])} ({100*slot_other['fresh']/max(total_slots,1):.1f}%) |")
    L.append(f"| avg slot writes / changed block | {total_slots / max(len(records),1):,.0f} |")
    if acc_totals["decode_failed"]:
        L.append(f"| ⚠ decode failures | {fmt_int(acc_totals['decode_failed'])} |")
    L.append("")

    # concentration
    ranked = sorted(slot_writes.values(), reverse=True)
    def share(k):
        return 100 * sum(ranked[:k]) / max(total_slots, 1)
    L.append("## Write concentration\n")
    L.append("| scope | share of slot writes |")
    L.append("|---|---|")
    L.append(f"| top address | {share(1):.1f}% |")
    L.append(f"| top 10 | {share(10):.1f}% |")
    L.append(f"| top 100 | {share(100):.1f}% |")
    L.append(f"| top 1% of touched addresses ({fmt_int(max(len(ranked)//100,1))}) | {share(max(len(ranked)//100,1)):.1f}% |")
    L.append("")

    # hourly series. Without --genesis-time the hour numbers are anchored to
    # the first block of the window (0 = first hour of the dump); with it they
    # are UTC hours computed from the chain's block-0 timestamp.
    if args.genesis_time is not None:
        hour_zero = args.genesis_time
        hour_of = lambda b: int((args.genesis_time + b * BLOCK_SECONDS) // 3600)
        records["hour"] = records["block"].map(hour_of)
    else:
        start = int(records["block"].min())
        hour_zero = int(start * BLOCK_SECONDS // 3600)  # unused without genesis
        records["hour"] = ((records["block"] - start) * BLOCK_SECONDS // 3600).astype("int64")
    hourly = records.groupby("hour").agg(
        blocks=("block", "size"), slot_writes=("n_slots", "sum"),
        account_writes=("n_accounts", "sum"), data_kb=("data_bytes", lambda s: s.sum()/1024),
    ).reset_index()
    L.append("## Hourly activity (block-time estimated)\n")
    if args.genesis_time is None:
        L.append("*hours are relative to the first block of the window; pass "
                 "--genesis-time <unix ts of block 0> for UTC dates*\n")
    L.append("| hour | blocks | slot writes | account writes | data KB |")
    L.append("|---|---|---|---|---|")
    for _, r in hourly.iterrows():
        if args.genesis_time is not None:
            hour_label = pd.to_datetime(r["hour"] * 3600, unit="s", utc=True).strftime("%Y-%m-%d %H:00")
        else:
            hour_label = f"hour {int(r['hour']):+d}"
        L.append(f"| {hour_label} | {int(r['blocks'])} | {int(r['slot_writes']):,} | "
                 f"{int(r['account_writes']):,} | {int(r['data_kb']):,} |")
    L.append("")

    # top addresses
    L.append(f"## Top {args.top} addresses by slot writes\n")
    L.append("| address | slot writes | distinct slots | repeat rate | first→last block | account writes | type |")
    L.append("|---|---|---|---|---|---|---|")
    for addr, w in top_n:
        info = acc_info.get(addr, [0, 0, 0, 0])
        d = len(distinct.get(addr, ()))
        rep = 100 * (1 - d / w) if w else 0
        kind = []
        if info[2]:
            kind.append("contract")
        if info[1]:
            kind.append("**created in window**")
        first_b = first.get(addr, "")
        last_b = last.get(addr, "")
        span = f"{fmt_int(first_b)}→{fmt_int(last_b)}" if first_b else ""
        L.append(f"| `{fmt_addr(addr)}` | {fmt_int(w)} | {fmt_int(d)} | {rep:.0f}% | {span} | {fmt_int(info[0])} | {', '.join(kind) or 'EOA?'} |")
    L.append("")
    L.append("*repeat rate = 1 − distinct/writes: high = hot loop over few slots; "
             "low = broad sweep over many slots (Polymarket-like).*\n")

    # busiest blocks
    busiest = records.nlargest(10, "n_slots")
    L.append("## Busiest blocks\n")
    L.append("| block | accounts | slots | data KB |")
    L.append("|---|---|---|---|")
    for _, r in busiest.iterrows():
        L.append(f"| {fmt_int(r['block'])} | {int(r['n_accounts']):,} | {int(r['n_slots']):,} | {int(r['data_bytes']/1024):,} |")
    L.append("")

    # account-touch leaders
    touch_rank = sorted(acc_info.items(), key=lambda kv: -kv[1][0])[: args.top]
    L.append(f"## Top {args.top} addresses by account writes\n")
    L.append("| address | account writes | slot writes | type |")
    L.append("|---|---|---|---|")
    for addr, e in touch_rank:
        kind = "contract" if e[2] else ("**created in window**" if e[1] else "EOA?")
        L.append(f"| `{fmt_addr(addr)}` | {fmt_int(e[0])} | {fmt_int(slot_writes.get(addr,0))} | {kind} |")
    L.append("")

    L.append("---\n")
    L.append("*All values are PRE-state (reverse diff). “created in window” means the "
             "address did not exist before the block that first touched it. "
             "Timestamps are estimated from block numbers at "
             f"{BLOCK_SECONDS:.0f}s/block.*\n")

    report = "\n".join(L)
    with open(args.out, "w") as f:
        f.write(report)
    print(f"wrote {args.out}", file=sys.stderr)

    if args.csv_dir:
        os.makedirs(args.csv_dir, exist_ok=True)
        rows = []
        for addr, w in slot_writes.items():
            info = acc_info.get(addr, [0, 0, 0, 0])
            d = len(distinct.get(addr, ())) if addr in distinct else ""
            rows.append({
                "address": addr, "slot_writes": w,
                "distinct_slots": d,
                "account_writes": info[0], "created_in_window": info[1],
                "is_contract": info[2],
            })
        df = pd.DataFrame(rows).sort_values("slot_writes", ascending=False)
        csv_path = os.path.join(args.csv_dir, "addresses.csv")
        df.to_csv(csv_path, index=False)
        print(f"wrote {csv_path} ({len(df)} rows)", file=sys.stderr)


if __name__ == "__main__":
    main()
