#!/usr/bin/env python3
"""report.py — in-depth state-activity report from statescan parquet dumps.

Reads the three parquet files written by cmd/statescan (records, accounts,
slots) and produces a markdown report plus optional CSV tables, without ever
holding the full slots table in memory (streamed with pyarrow).

The scan window is split into two halves at the midpoint block and most
metrics are reported per half with a percent change. 90k blocks at ~2s is
~48h, so the default statescan dump gives two full days of day-over-day
comparison.

Semantics reminder: every value in the dump is PRE-state (the state history
is a reverse diff). "created" below means the account/slot did not exist
before that block; "fresh write" means the slot was zero/nonexistent before
the block that touched it.

Usage:
    python3 report.py --input tmp-state --out report.md
    python3 report.py --input tmp-state/state-2026-10-13 --top 30 --csv-dir csv/
    python3 report.py --input tmp-state --genesis-time <unix ts of block 0>

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


def stream_slot_counts(path, split_block):
    """One pass over slots.parquet: per-address slot writes split into
    [first half, second half], plus fresh-write counts per half."""
    counts = {}  # address -> [d1, d2]
    fresh = [0, 0]
    pf = pq.ParquetFile(path)
    for batch in pf.iter_batches(batch_size=BATCH_ROWS):
        addrs = batch.column("address").to_pylist()
        blocks = batch.column("block").to_pylist()
        lens = batch.column("value_len").to_pylist()
        for a, b, vlen in zip(addrs, blocks, lens):
            i = 1 if b >= split_block else 0
            e = counts.get(a)
            if e is None:
                counts[a] = e = [0, 0]
            e[i] += 1
            if vlen == 0:
                fresh[i] += 1
    return counts, fresh


def stream_account_stats(path, split_block):
    """Per-address: account writes per half, created-any, contract-any, plus
    global counters per half."""
    info = {}  # address -> [d1, d2, created, is_contract]
    totals = {"rows": [0, 0], "created": [0, 0], "decode_failed": [0, 0],
              "contract_rows": [0, 0]}
    pf = pq.ParquetFile(path)
    for batch in pf.iter_batches(batch_size=BATCH_ROWS):
        tab = batch.to_pandas()
        half = (tab["block"] >= split_block).astype(int)
        for i in (0, 1):
            sub = tab[half == i]
            totals["rows"][i] += len(sub)
            totals["created"][i] += int((~sub["present"]).sum())
            totals["decode_failed"][i] += int(sub["decode_failed"].sum())
            is_contract = sub["code_hash"].fillna("").str.len() > 0
            totals["contract_rows"][i] += int(is_contract.sum())
            grouped = sub.groupby("address", sort=False).agg(
                touches=("address", "size"),
                created=("present", lambda s: int((~s).any())),
                contract=("code_hash", lambda s: int(s.fillna("").str.len().max() > 0)),
            )
            for addr, row in grouped.iterrows():
                e = info.get(addr)
                if e is None:
                    e = [0, 0, int(row.created), int(row.contract)]
                    info[addr] = e
                e[i] += int(row.touches)
                e[2] = max(e[2], int(row.created))
                e[3] = max(e[3], int(row.contract))
    return info, totals


def top_address_details(path, addresses):
    """Second pass over slots.parquet restricted to `addresses`: distinct
    slots (whole window) and first/last seen block."""
    import pyarrow as pa
    import pyarrow.compute as pc

    value_set = pa.array(list(addresses))
    distinct = defaultdict(set)  # addr -> set of truncated slot hashes
    first, last = {}, {}
    pf = pq.ParquetFile(path)
    for batch in pf.iter_batches(batch_size=BATCH_ROWS):
        mask = pc.is_in(batch.column("address"), value_set=value_set)
        tab = batch.filter(mask)
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


def pct(d1, d2):
    """Percent change from the first half to the second; 'new' when the
    first half is zero."""
    if d1 == 0:
        return "new" if d2 else "—"
    return f"{100 * (d2 - d1) / d1:+.0f}%"


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
    records = pd.read_parquet(files["records"]).sort_values("state_id")

    lo, hi = int(records["block"].min()), int(records["block"].max())
    split_block = lo + (hi - lo) // 2 + 1  # first block of the second half
    rec1 = records[records["block"] < split_block]
    rec2 = records[records["block"] >= split_block]

    print(f"reading {files['accounts']} (streaming)", file=sys.stderr)
    acc_info, acc_totals = stream_account_stats(files["accounts"], split_block)

    print(f"reading {files['slots']} (streaming)", file=sys.stderr)
    slot_writes, fresh = stream_slot_counts(files["slots"], split_block)

    top_n = sorted(slot_writes.items(), key=lambda kv: -(kv[1][0] + kv[1][1]))[: args.top]
    print(f"re-reading slots for top {len(top_n)} addresses", file=sys.stderr)
    distinct, first, last = top_address_details(files["slots"], [a for a, _ in top_n])

    def slot_sum(i):
        return sum(v[i] for v in slot_writes.values())

    def touched_addrs(i):
        return sum(1 for v in acc_info.values() if v[i] > 0)

    # ---- assemble report -------------------------------------------------
    L = []
    L.append("# State activity report\n")
    L.append(f"**source**: `{os.path.basename(prefix)}` · blocks **{fmt_int(lo)}–{fmt_int(hi)}** "
             f"(~{(hi - lo + 1) * BLOCK_SECONDS / 3600:.1f}h at {BLOCK_SECONDS:.0f}s/block)\n")
    L.append(f"**halves**: first **{fmt_int(lo)}–{fmt_int(split_block - 1)}**, "
             f"second **{fmt_int(split_block)}–{fmt_int(hi)}** "
             f"(~{(hi - split_block + 1) * BLOCK_SECONDS / 3600:.1f}h each). "
             "Δ columns are the percent change between halves.\n")

    # overview
    L.append("## Overview\n")
    L.append("| metric | 1st half | 2nd half | Δ |")
    L.append("|---|---|---|---|")

    def row(label, a, b, fmt=fmt_int):
        L.append(f"| {label} | {fmt(a)} | {fmt(b)} | {pct(a, b)} |")

    row("blocks with state changes", len(rec1), len(rec2))
    row("account touch rows", acc_totals["rows"][0], acc_totals["rows"][1])
    row("distinct touched addresses", touched_addrs(0), touched_addrs(1))
    row("accounts created by a block", acc_totals["created"][0], acc_totals["created"][1])
    row("contract account rows (has code hash)", acc_totals["contract_rows"][0], acc_totals["contract_rows"][1])
    row("slot writes", slot_sum(0), slot_sum(1))
    row("— fresh writes (slot previously zero)", fresh[0], fresh[1])
    row("avg slot writes / changed block",
        slot_sum(0) / max(len(rec1), 1), slot_sum(1) / max(len(rec2), 1),
        fmt=lambda x: f"{x:,.0f}")
    if acc_totals["decode_failed"][0] or acc_totals["decode_failed"][1]:
        row("⚠ decode failures", acc_totals["decode_failed"][0], acc_totals["decode_failed"][1])
    L.append("")

    # concentration, per half
    def shares(i):
        ranked = sorted((v[i] for v in slot_writes.values()), reverse=True)
        total = sum(ranked) or 1
        return [100 * sum(ranked[:k]) / total for k in (1, 10, 100, max(len(ranked) // 100, 1))]

    s1, s2 = shares(0), shares(1)
    n_addr = max(len(slot_writes) // 100, 1)
    L.append("## Write concentration\n")
    L.append("| scope | share of slot writes (1st half) | share (2nd half) | Δ pp |")
    L.append("|---|---|---|---|")
    for label, a, b in zip(("top address", "top 10", "top 100",
                            f"top 1% of touched addresses ({fmt_int(n_addr)})"), s1, s2):
        L.append(f"| {label} | {a:.1f}% | {b:.1f}% | {b - a:+.1f} |")
    L.append("")

    # hourly series. Without --genesis-time the hour numbers are anchored to
    # the first block of the window; with it they are UTC hours.
    if args.genesis_time is not None:
        records["hour"] = ((args.genesis_time + records["block"] * BLOCK_SECONDS) // 3600).astype("int64")
        hour_fmt = lambda h: pd.to_datetime(h * 3600, unit="s", utc=True).strftime("%Y-%m-%d %H:00")
    else:
        records["hour"] = ((records["block"] - lo) * BLOCK_SECONDS // 3600).astype("int64")
        hour_fmt = lambda h: f"hour {int(h):+d}"
    hourly = records.groupby("hour").agg(
        blocks=("block", "size"), slot_writes=("n_slots", "sum"),
        account_writes=("n_accounts", "sum"), data_kb=("data_bytes", lambda s: s.sum() / 1024),
    ).reset_index()
    L.append("## Hourly activity (block-time estimated)\n")
    if args.genesis_time is None:
        L.append("*hours are relative to the first block of the window; pass "
                 "--genesis-time <unix ts of block 0> for UTC dates*\n")
    L.append("| hour | blocks | slot writes | account writes | data KB |")
    L.append("|---|---|---|---|---|")
    for _, r in hourly.iterrows():
        L.append(f"| {hour_fmt(r['hour'])} | {int(r['blocks'])} | {int(r['slot_writes']):,} | "
                 f"{int(r['account_writes']):,} | {int(r['data_kb']):,} |")
    L.append("")

    # top addresses by slot writes, with halves
    L.append(f"## Top {args.top} addresses by slot writes\n")
    L.append("| address | 1st half | 2nd half | Δ | total | distinct slots | repeat rate | type |")
    L.append("|---|---|---|---|---|---|---|---|")
    for addr, w in top_n:
        info = acc_info.get(addr, [0, 0, 0, 0])
        total = w[0] + w[1]
        d = len(distinct.get(addr, ()))
        rep = 100 * (1 - d / total) if total else 0
        kind = []
        if info[3]:
            kind.append("contract")
        if info[2]:
            kind.append("**created in window**")
        L.append(f"| `{addr}` | {fmt_int(w[0])} | {fmt_int(w[1])} | {pct(w[0], w[1])} | "
                 f"{fmt_int(total)} | {fmt_int(d)} | {rep:.0f}% | {', '.join(kind) or 'EOA?'} |")
    L.append("")
    L.append("*repeat rate = 1 − distinct/total: high = hot loop over few slots; "
             "low = broad sweep over many slots (Polymarket-like).*\n")

    # movers: biggest half-over-half increases
    movers = [(a, w) for a, w in slot_writes.items()
              if w[1] >= 10 * max(w[0], 1) and w[1] >= 1000]
    movers.sort(key=lambda kv: -kv[1][1])
    L.append("## Movers: biggest half-over-half increases\n")
    L.append(f"*addresses with ≥10× more slot writes in the second half — "
             f"the \"is a new {ZEN_LABEL} forming\" watchlist. "
             f"{len(movers)} matched.*\n")
    L.append("| address | 1st half | 2nd half | Δ | type |")
    L.append("|---|---|---|---|---|")
    for addr, w in movers[: args.top]:
        info = acc_info.get(addr, [0, 0, 0, 0])
        kind = "contract" if info[3] else ("**created in window**" if info[2] else "EOA?")
        L.append(f"| `{addr}` | {fmt_int(w[0])} | {fmt_int(w[1])} | {pct(w[0], w[1])} | {kind} |")
    L.append("")

    # busiest blocks
    busiest = records.nlargest(10, "n_slots")
    L.append("## Busiest blocks\n")
    L.append("| block | accounts | slots | data KB |")
    L.append("|---|---|---|---|")
    for _, r in busiest.iterrows():
        L.append(f"| {fmt_int(r['block'])} | {int(r['n_accounts']):,} | {int(r['n_slots']):,} | {int(r['data_bytes'] / 1024):,} |")
    L.append("")

    # account-write leaders
    touch_rank = sorted(acc_info.items(), key=lambda kv: -(kv[1][0] + kv[1][1]))[: args.top]
    L.append(f"## Top {args.top} addresses by account writes\n")
    L.append("| address | 1st half | 2nd half | Δ | slot writes (total) | type |")
    L.append("|---|---|---|---|---|---|")
    for addr, e in touch_rank:
        kind = "contract" if e[3] else ("**created in window**" if e[2] else "EOA?")
        w = slot_writes.get(addr, [0, 0])
        L.append(f"| `{addr}` | {fmt_int(e[0])} | {fmt_int(e[1])} | {pct(e[0], e[1])} | "
                 f"{fmt_int(w[0] + w[1])} | {kind} |")
    L.append("")

    L.append("---\n")
    L.append("*All values are PRE-state (reverse diff). \"created in window\" means the "
             "address did not exist before the block that first touched it. "
             f"Timestamps are estimated from block numbers at {BLOCK_SECONDS:.0f}s/block. "
             "Δ columns compare the first half of the window to the second.*\n")

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
                "address": addr,
                "slot_writes_h1": w[0], "slot_writes_h2": w[1],
                "distinct_slots": d,
                "account_writes_h1": info[0], "account_writes_h2": info[1],
                "created_in_window": info[2], "is_contract": info[3],
            })
        df = pd.DataFrame(rows).sort_values("slot_writes_h2", ascending=False)
        csv_path = os.path.join(args.csv_dir, "addresses.csv")
        df.to_csv(csv_path, index=False)
        print(f"wrote {csv_path} ({len(df)} rows)", file=sys.stderr)


# the contract whose storage blowup motivated this analysis
ZEN_LABEL = "Zen"

if __name__ == "__main__":
    main()
