#!/usr/bin/env python3
import argparse
import csv
import json
import math
import os
import sys
import time
from collections import defaultdict

BYTES_PER_MB = 1024 * 1024  # MiB


def bytes_to_mb(x):
    return x / BYTES_PER_MB


def parse_line(line):
    line = line.strip()
    if not line:
        return None
    try:
        obj = json.loads(line)
    except json.JSONDecodeError:
        return None

    if "a" in obj:
        identity = obj.get("a")
        nodes = obj.get("n", 0)
        size_bytes = obj.get("b", 0)
        max_depth = obj.get("d", 0)
        depths = obj.get("p")
    else:
        identity = obj.get("identity")
        nodes = obj.get("nodes", 0)
        size_bytes = obj.get("bytes", 0)
        max_depth = obj.get("maxDepth", 0)
        depths = obj.get("depths")

    if identity is None:
        return None
    return identity, int(nodes), int(size_bytes), int(max_depth), depths


def ensure_dir(path):
    if path == "":
        return
    os.makedirs(path, exist_ok=True)


def write_csv(path, header, rows):
    with open(path, "w", newline="") as f:
        writer = csv.writer(f)
        writer.writerow(header)
        for row in rows:
            writer.writerow(row)


def safe_log10_mb(size_bytes):
    # Convert bytes->MB then log10, with a tiny floor to avoid log(0)
    mb = bytes_to_mb(size_bytes)
    if mb <= 0:
        mb = 1e-12
    return math.log10(mb)


def main():
    parser = argparse.ArgumentParser(description="Stream and summarize contractstatsdepth logs.")
    parser.add_argument("--input", required=True, help="Path to JSON-lines log file")
    parser.add_argument("--output-dir", default="contractstatsdepth_out", help="Directory for summaries/plots")
    parser.add_argument("--progress-every", type=int, default=100000, help="Log progress every N lines")
    parser.add_argument("--no-plots", action="store_true", help="Skip plot generation")

    # Heatmap controls
    parser.add_argument("--heatmap-xbins", type=int, default=240, help="Number of X bins (log10 MB)")
    parser.add_argument(
        "--heatmap-log",
        action="store_true",
        default=True,
        help="Plot log10(count+1) (default: on)",
    )
    args = parser.parse_args()

    ensure_dir(args.output_dir)

    total_contracts = 0
    total_nodes = 0
    total_bytes = 0

    maxdepth_count = defaultdict(int)
    maxdepth_nodes = defaultdict(int)
    maxdepth_bytes = defaultdict(int)

    depth_nodes = defaultdict(int)
    depth_bytes = defaultdict(int)

    top_contracts = []
    top_limit = 50

    # For heatmap range discovery (pass 1)
    xlog_min = None
    xlog_max = None
    y_min = None
    y_max = None

    start = time.time()

    # -------- Pass 1: summaries + discover ranges for heatmap --------
    with open(args.input, "r", encoding="utf-8") as f:
        for idx, line in enumerate(f, 1):
            parsed = parse_line(line)
            if parsed is None:
                continue
            identity, nodes, size_bytes, max_depth, depths = parsed
            if size_bytes == 0:
                continue

            total_contracts += 1
            total_nodes += nodes
            total_bytes += size_bytes

            maxdepth_count[max_depth] += 1
            maxdepth_nodes[max_depth] += nodes
            maxdepth_bytes[max_depth] += size_bytes

            # per-depth breakdown from "depths"
            if isinstance(depths, dict):
                for d_str, stat in depths.items():
                    try:
                        depth = int(d_str)
                    except (TypeError, ValueError):
                        continue
                    n_val = int(stat.get("nodes", stat.get("n", 0)))
                    b_val = int(stat.get("bytes", stat.get("b", 0)))
                    depth_nodes[depth] += n_val
                    depth_bytes[depth] += b_val

            # track heatmap ranges
            xl = safe_log10_mb(size_bytes)
            if xlog_min is None or xl < xlog_min:
                xlog_min = xl
            if xlog_max is None or xl > xlog_max:
                xlog_max = xl
            if y_min is None or max_depth < y_min:
                y_min = max_depth
            if y_max is None or max_depth > y_max:
                y_max = max_depth

            # top contracts by size
            if len(top_contracts) < top_limit:
                top_contracts.append((size_bytes, nodes, max_depth, identity))
                top_contracts.sort(reverse=True)
            else:
                if size_bytes > top_contracts[-1][0]:
                    top_contracts[-1] = (size_bytes, nodes, max_depth, identity)
                    top_contracts.sort(reverse=True)

            if args.progress_every > 0 and idx % args.progress_every == 0:
                now = time.time()
                rate = idx / max(1e-9, now - start)
                sys.stderr.write(
                    f"[pass1] lines={idx} contracts={total_contracts} rate={rate:.1f}/s elapsed={now-start:.0f}s\n"
                )
                sys.stderr.flush()

    # Write summaries (keep bytes in outputs)
    maxdepth_rows = []
    for depth in sorted(maxdepth_count.keys()):
        count = maxdepth_count[depth]
        nodes_sum = maxdepth_nodes[depth]
        bytes_sum = maxdepth_bytes[depth]
        avg_bytes = bytes_sum / count if count else 0
        avg_nodes = nodes_sum / count if count else 0
        maxdepth_rows.append([depth, count, nodes_sum, bytes_sum, f"{avg_nodes:.2f}", f"{avg_bytes:.2f}"])

    # IMPORTANT FIX:
    # Keep avg_bytes_per_node as a float (no rounding-to-string before plotting).
    depth_rows = []
    for depth in sorted(depth_nodes.keys()):
        nodes_sum = depth_nodes[depth]
        bytes_sum = depth_bytes[depth]
        avg_bpn = (bytes_sum / nodes_sum) if nodes_sum else 0.0  # bytes per node
        depth_rows.append([depth, nodes_sum, bytes_sum, avg_bpn])

    write_csv(
        os.path.join(args.output_dir, "maxdepth_summary.csv"),
        ["maxDepth", "contracts", "nodes_sum", "bytes_sum", "avg_nodes", "avg_bytes"],
        maxdepth_rows,
    )
    write_csv(
        os.path.join(args.output_dir, "depth_summary.csv"),
        ["depth", "nodes_sum", "bytes_sum", "avg_bytes_per_node"],
        [[d, n, b, f"{avg:.4f}"] for d, n, b, avg in depth_rows],
    )
    write_csv(
        os.path.join(args.output_dir, "top_contracts.csv"),
        ["bytes", "nodes", "maxDepth", "identity"],
        top_contracts,
    )

    with open(os.path.join(args.output_dir, "overall_summary.json"), "w", encoding="utf-8") as f:
        json.dump(
            {
                "contracts": total_contracts,
                "nodes": total_nodes,
                "bytes": total_bytes,
            },
            f,
        )
        f.write("\n")

    if args.no_plots:
        return

    # If no valid points found
    if xlog_min is None or xlog_max is None or y_min is None or y_max is None:
        sys.stderr.write("No valid data points found for plotting.\n")
        return

    # -------- Pass 2: streaming heatmap histogram (fixed memory) --------
    try:
        import numpy as np
        import matplotlib.pyplot as plt
        from matplotlib.ticker import FuncFormatter
    except Exception as exc:
        sys.stderr.write(f"plot deps not available (numpy/matplotlib): {exc}\n")
        return

    xbins = max(10, int(args.heatmap_xbins))
    ybins = int(y_max - y_min + 1)  # depth is discrete int bins
    H = np.zeros((ybins, xbins), dtype=np.int64)

    # avoid division by zero if all x are identical
    x_span = xlog_max - xlog_min
    if x_span <= 0:
        x_span = 1e-12

    start2 = time.time()
    with open(args.input, "r", encoding="utf-8") as f:
        for idx, line in enumerate(f, 1):
            parsed = parse_line(line)
            if parsed is None:
                continue
            _identity, _nodes, size_bytes, max_depth, _depths = parsed
            if size_bytes == 0:
                continue

            xl = safe_log10_mb(size_bytes)

            # x bin
            x_pos = (xl - xlog_min) / x_span
            xi = int(x_pos * xbins)
            if xi < 0:
                xi = 0
            elif xi >= xbins:
                xi = xbins - 1

            # y bin (integer depth)
            yi = max_depth - y_min
            if 0 <= yi < ybins:
                H[yi, xi] += 1

            if args.progress_every > 0 and idx % args.progress_every == 0:
                now = time.time()
                rate = idx / max(1e-9, now - start2)
                sys.stderr.write(
                    f"[pass2] lines={idx} rate={rate:.1f}/s elapsed={now-start2:.0f}s\n"
                )
                sys.stderr.flush()

    # Prepare heatmap values
    if args.heatmap_log:
        Z = np.log10(H.astype(np.float64) + 1.0)
        cb_label = "log10(count + 1)"
    else:
        Z = H.astype(np.float64)
        cb_label = "count"

    # Plot heatmap
    plt.figure(figsize=(9, 5))
    extent = [xlog_min, xlog_max, y_min - 0.5, y_max + 0.5]
    plt.imshow(
        Z,
        aspect="auto",
        origin="lower",
        interpolation="nearest",
        extent=extent,
    )
    cb = plt.colorbar()
    cb.set_label(cb_label)

    # Format x-axis tick labels back into MB
    def fmt_mb_from_log10(v, _pos):
        mb = 10 ** v
        if mb >= 1000:
            return f"{mb:.0f}"
        if mb >= 10:
            return f"{mb:.1f}".rstrip("0").rstrip(".")
        return f"{mb:.2g}"

    plt.gca().xaxis.set_major_formatter(FuncFormatter(fmt_mb_from_log10))
    plt.xlabel("storage trie size (MB, log)")
    plt.ylabel("max depth")
    plt.title("Trie size vs max depth (heatmap)")
    plt.tight_layout()
    plt.savefig(os.path.join(args.output_dir, "bytes_vs_maxdepth_heatmap.png"), dpi=160)
    plt.close()

    # Plot: total size per depth (MB)
    if depth_rows:
        depths = [int(r[0]) for r in depth_rows]
        mb_vals = [bytes_to_mb(int(r[2])) for r in depth_rows]
        plt.figure(figsize=(8, 4))
        plt.plot(depths, mb_vals, marker="o", linewidth=1)
        plt.xlabel("depth")
        plt.ylabel("total size (MB)")
        plt.title("Total size per depth")
        plt.tight_layout()
        plt.savefig(os.path.join(args.output_dir, "bytes_per_depth.png"), dpi=160)
        plt.close()

    # Plot: avg size per node per depth (BYTES, not MB)
    if depth_rows:
        depths = [int(r[0]) for r in depth_rows]
        avg_bpn = [float(r[3]) for r in depth_rows]  # already bytes per node (float)
        plt.figure(figsize=(8, 4))
        plt.plot(depths, avg_bpn, marker="o", linewidth=1)
        plt.xlabel("depth")
        plt.ylabel("avg size per node (bytes)")
        plt.title("Average node size by depth")
        plt.tight_layout()
        plt.savefig(os.path.join(args.output_dir, "avg_bytes_per_node.png"), dpi=160)
        plt.close()


if __name__ == "__main__":
    main()