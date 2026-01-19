#!/usr/bin/env python3
"""
Compare TrieDB-go vs MPT (PathDB/HashDB) metrics.
Usage: python3 compare.py <triedb.csv> <mpt.csv>

The script automatically detects which backend each CSV is using based on
which commit metrics have non-zero values.
"""
import sys
import csv

def load(f):
    rows = list(csv.DictReader(open(f)))
    return rows

def avg(rows, col):
    vals = [float(r[col]) for r in rows if r.get(col) and float(r[col]) > 0]
    return sum(vals)/len(vals) if vals else 0

def total(rows, col):
    """Sum of values (for delta-based metrics like cache hits)."""
    vals = [float(r[col]) for r in rows if r.get(col)]
    return sum(vals)

def last_value(rows, col):
    """Last value (for cumulative counters)."""
    for r in reversed(rows):
        if r.get(col) and float(r[col]) > 0:
            return float(r[col])
    return 0

def ns_to_ms(ns):
    return ns / 1_000_000

def ns_to_us(ns):
    return ns / 1_000

def pct_diff(a, b):
    if b == 0: return 0
    return ((a - b) / b) * 100

def detect_backend(rows):
    """Detect which state backend is used based on commit metrics and counters."""
    tdb_commit = avg(rows, 'triedbgo_commit_p50_ns')
    pathdb_commit = avg(rows, 'pathdb_commit_p50_ns')
    hashdb_commit = avg(rows, 'hashdb_commit_p50_ns')

    # Also check counters (more reliable since they're cumulative)
    tdb_reads = last_value(rows, 'triedbgo_ops_account_reads')
    pathdb_hits = last_value(rows, 'pathdb_clean_state_hit')

    if tdb_commit > 0 or tdb_reads > 0:
        return "TrieDB-go", tdb_commit if tdb_commit > 0 else avg(rows, 'chain_inserts_p50_ns')
    elif pathdb_commit > 0 or pathdb_hits > 0:
        return "PathDB", pathdb_commit if pathdb_commit > 0 else avg(rows, 'chain_inserts_p50_ns')
    elif hashdb_commit > 0:
        return "HashDB", hashdb_commit
    else:
        return "Unknown", 0

if len(sys.argv) != 3:
    print("Usage: python3 compare.py <file1.csv> <file2.csv>")
    print("       Compares two Bor runs with different state backends")
    sys.exit(1)

data1 = load(sys.argv[1])
data2 = load(sys.argv[2])

backend1, commit1_ns = detect_backend(data1)
backend2, commit2_ns = detect_backend(data2)

print("=" * 75)
print("  Bor State Backend Performance Comparison")
print("=" * 75)
print(f"  File 1: {sys.argv[1]}")
print(f"    Backend: {backend1}, Samples: {len(data1)}")
print(f"  File 2: {sys.argv[2]}")
print(f"    Backend: {backend2}, Samples: {len(data2)}")
print()

# Main comparison metrics
print("BLOCK PROCESSING (End-to-End)")
print("-" * 75)
print(f"{'Metric':<30} {backend1:>12} {backend2:>12} {'Diff':>15}")
print("-" * 75)

insert1 = ns_to_ms(avg(data1, 'chain_inserts_p50_ns'))
insert2 = ns_to_ms(avg(data2, 'chain_inserts_p50_ns'))
diff = pct_diff(insert1, insert2)
sign = "faster" if diff < 0 else "slower"
print(f"{'Block Insert (p50)':<30} {insert1:>10.3f}ms {insert2:>10.3f}ms {abs(diff):>+10.1f}% {sign}")

# Block insert breakdown (if available)
stateprep1 = ns_to_ms(avg(data1, 'chain_stateprep_p50_ns'))
stateprep2 = ns_to_ms(avg(data2, 'chain_stateprep_p50_ns'))
validation1 = ns_to_ms(avg(data1, 'chain_validation_p50_ns'))
validation2 = ns_to_ms(avg(data2, 'chain_validation_p50_ns'))
execution1 = ns_to_ms(avg(data1, 'chain_execution_p50_ns'))
execution2 = ns_to_ms(avg(data2, 'chain_execution_p50_ns'))
commitprep1 = ns_to_ms(avg(data1, 'chain_commitprep_p50_ns'))
commitprep2 = ns_to_ms(avg(data2, 'chain_commitprep_p50_ns'))
write1 = ns_to_ms(avg(data1, 'chain_write_p50_ns'))
write2 = ns_to_ms(avg(data2, 'chain_write_p50_ns'))

if validation1 > 0 or validation2 > 0 or execution1 > 0 or execution2 > 0 or stateprep1 > 0 or stateprep2 > 0:
    print()
    print("BLOCK INSERT BREAKDOWN (p50)")
    print("-" * 75)
    print(f"{'Metric':<30} {backend1:>12} {backend2:>12} {'Diff':>15}")
    print("-" * 75)

    if stateprep1 > 0 or stateprep2 > 0:
        prep_diff = pct_diff(stateprep1, stateprep2)
        prep_sign = "faster" if prep_diff < 0 else "slower"
        print(f"{'State Prep Time':<30} {stateprep1:>10.3f}ms {stateprep2:>10.3f}ms {abs(prep_diff):>+10.1f}% {prep_sign}")

    if validation1 > 0 or validation2 > 0:
        val_diff = pct_diff(validation1, validation2)
        val_sign = "faster" if val_diff < 0 else "slower"
        print(f"{'Validation Time':<30} {validation1:>10.3f}ms {validation2:>10.3f}ms {abs(val_diff):>+10.1f}% {val_sign}")

    if execution1 > 0 or execution2 > 0:
        exec_diff = pct_diff(execution1, execution2)
        exec_sign = "faster" if exec_diff < 0 else "slower"
        print(f"{'Execution Time':<30} {execution1:>10.3f}ms {execution2:>10.3f}ms {abs(exec_diff):>+10.1f}% {exec_sign}")

    if commitprep1 > 0 or commitprep2 > 0:
        cprep_diff = pct_diff(commitprep1, commitprep2)
        cprep_sign = "faster" if cprep_diff < 0 else "slower"
        print(f"{'Commit Prep Time':<30} {commitprep1:>10.3f}ms {commitprep2:>10.3f}ms {abs(cprep_diff):>+10.1f}% {cprep_sign}")

    if write1 > 0 or write2 > 0:
        write_diff = pct_diff(write1, write2)
        write_sign = "faster" if write_diff < 0 else "slower"
        print(f"{'Write Time':<30} {write1:>10.3f}ms {write2:>10.3f}ms {abs(write_diff):>+10.1f}% {write_sign}")

# Throughput
blocks1 = int(data1[-1]['chain_head_block']) - int(data1[0]['chain_head_block']) if data1 else 0
blocks2 = int(data2[-1]['chain_head_block']) - int(data2[0]['chain_head_block']) if data2 else 0
duration1 = int(data1[-1]['timestamp']) - int(data1[0]['timestamp']) if data1 else 1
duration2 = int(data2[-1]['timestamp']) - int(data2[0]['timestamp']) if data2 else 1

bps1 = blocks1 / duration1 if duration1 > 0 else 0
bps2 = blocks2 / duration2 if duration2 > 0 else 0
bps_diff = pct_diff(bps1, bps2)
sign = "faster" if bps_diff > 0 else "slower"
print(f"{'Throughput (blocks/sec)':<30} {bps1:>12.2f} {bps2:>12.2f} {abs(bps_diff):>+10.1f}% {sign}")

# State commit time comparison
print()
print("STATE COMMIT TIME (p50) - Comparable Metric")
print("-" * 75)
commit1_ms = ns_to_ms(commit1_ns)
commit2_ms = ns_to_ms(commit2_ns)
commit_diff = pct_diff(commit1_ms, commit2_ms)
sign = "faster" if commit_diff < 0 else "slower"
print(f"{'Commit Time':<30} {commit1_ms:>10.3f}ms {commit2_ms:>10.3f}ms {abs(commit_diff):>+10.1f}% {sign}")

# Commit breakdown (if available)
acc_commit1 = ns_to_ms(avg(data1, 'chain_account_commits_p50_ns'))
acc_commit2 = ns_to_ms(avg(data2, 'chain_account_commits_p50_ns'))
sto_commit1 = ns_to_ms(avg(data1, 'chain_storage_commits_p50_ns'))
sto_commit2 = ns_to_ms(avg(data2, 'chain_storage_commits_p50_ns'))
snap_commit1 = ns_to_ms(avg(data1, 'chain_snapshot_commits_p50_ns'))
snap_commit2 = ns_to_ms(avg(data2, 'chain_snapshot_commits_p50_ns'))
tdb_commit1 = ns_to_ms(avg(data1, 'chain_triedb_commits_p50_ns'))
tdb_commit2 = ns_to_ms(avg(data2, 'chain_triedb_commits_p50_ns'))

if acc_commit1 > 0 or acc_commit2 > 0 or sto_commit1 > 0 or sto_commit2 > 0:
    print()
    print("COMMIT TIME BREAKDOWN (p50)")
    print("-" * 75)
    print(f"{'Metric':<30} {backend1:>12} {backend2:>12} {'Diff':>15}")
    print("-" * 75)

    if acc_commit1 > 0 or acc_commit2 > 0:
        acc_diff = pct_diff(acc_commit1, acc_commit2)
        acc_sign = "faster" if acc_diff < 0 else "slower"
        print(f"{'Account Commits':<30} {acc_commit1:>10.3f}ms {acc_commit2:>10.3f}ms {abs(acc_diff):>+10.1f}% {acc_sign}")

    if sto_commit1 > 0 or sto_commit2 > 0:
        sto_diff = pct_diff(sto_commit1, sto_commit2)
        sto_sign = "faster" if sto_diff < 0 else "slower"
        print(f"{'Storage Commits':<30} {sto_commit1:>10.3f}ms {sto_commit2:>10.3f}ms {abs(sto_diff):>+10.1f}% {sto_sign}")

    if snap_commit1 > 0 or snap_commit2 > 0:
        snap_diff = pct_diff(snap_commit1, snap_commit2)
        snap_sign = "faster" if snap_diff < 0 else "slower"
        print(f"{'Snapshot Commits':<30} {snap_commit1:>10.3f}ms {snap_commit2:>10.3f}ms {abs(snap_diff):>+10.1f}% {snap_sign}")

    if tdb_commit1 > 0 or tdb_commit2 > 0:
        tdb_diff = pct_diff(tdb_commit1, tdb_commit2)
        tdb_sign = "faster" if tdb_diff < 0 else "slower"
        print(f"{'TrieDB Commits':<30} {tdb_commit1:>10.3f}ms {tdb_commit2:>10.3f}ms {abs(tdb_diff):>+10.1f}% {tdb_sign}")

    # PathDB tree operation breakdown
    tree_add1 = ns_to_ms(avg(data1, 'pathdb_tree_add_p50_ns'))
    tree_add2 = ns_to_ms(avg(data2, 'pathdb_tree_add_p50_ns'))
    tree_cap1 = ns_to_ms(avg(data1, 'pathdb_tree_cap_p50_ns'))
    tree_cap2 = ns_to_ms(avg(data2, 'pathdb_tree_cap_p50_ns'))

    if tree_add1 > 0 or tree_add2 > 0 or tree_cap1 > 0 or tree_cap2 > 0:
        print()
        print("PATHDB TRIEDB COMMITS BREAKDOWN (p50)")
        print("-" * 75)
        print(f"{'Metric':<30} {backend1:>12} {backend2:>12} {'Diff':>15}")
        print("-" * 75)

        if tree_add1 > 0 or tree_add2 > 0:
            add_diff = pct_diff(tree_add1, tree_add2)
            add_sign = "faster" if add_diff < 0 else "slower"
            print(f"{'Tree Add':<30} {tree_add1:>10.3f}ms {tree_add2:>10.3f}ms {abs(add_diff):>+10.1f}% {add_sign}")

        if tree_cap1 > 0 or tree_cap2 > 0:
            cap_diff = pct_diff(tree_cap1, tree_cap2)
            cap_sign = "faster" if cap_diff < 0 else "slower"
            print(f"{'Tree Cap (flush)':<30} {tree_cap1:>10.3f}ms {tree_cap2:>10.3f}ms {abs(cap_diff):>+10.1f}% {cap_sign}")

# Backend-specific detailed metrics
if backend1 == "TrieDB-go":
    print()
    print(f"TRIEDB-GO DETAILED METRICS ({backend1} only)")
    print("-" * 75)

    acc_read = ns_to_us(avg(data1, 'triedbgo_account_read_p50_ns'))
    sto_read = ns_to_us(avg(data1, 'triedbgo_storage_read_p50_ns'))
    root_compute = ns_to_ms(avg(data1, 'triedbgo_root_compute_p50_ns'))
    acc_count = last_value(data1, 'triedbgo_ops_account_reads')
    sto_count = last_value(data1, 'triedbgo_ops_storage_reads')

    print(f"  {'Account Read (p50):':<25} {acc_read:>10.1f} µs")
    print(f"  {'Storage Read (p50):':<25} {sto_read:>10.1f} µs")
    print(f"  {'Root Compute (p50):':<25} {root_compute:>10.3f} ms")
    print(f"  {'Account Read Count:':<25} {acc_count:>10.0f}")
    print(f"  {'Storage Read Count:':<25} {sto_count:>10.0f}")

if backend2 == "TrieDB-go":
    print()
    print(f"TRIEDB-GO DETAILED METRICS ({backend2} only)")
    print("-" * 75)

    acc_read = ns_to_us(avg(data2, 'triedbgo_account_read_p50_ns'))
    sto_read = ns_to_us(avg(data2, 'triedbgo_storage_read_p50_ns'))
    root_compute = ns_to_ms(avg(data2, 'triedbgo_root_compute_p50_ns'))
    acc_count = last_value(data2, 'triedbgo_ops_account_reads')
    sto_count = last_value(data2, 'triedbgo_ops_storage_reads')

    print(f"  {'Account Read (p50):':<25} {acc_read:>10.1f} µs")
    print(f"  {'Storage Read (p50):':<25} {sto_read:>10.1f} µs")
    print(f"  {'Root Compute (p50):':<25} {root_compute:>10.3f} ms")
    print(f"  {'Account Read Count:':<25} {acc_count:>10.0f}")
    print(f"  {'Storage Read Count:':<25} {sto_count:>10.0f}")

# PathDB-specific metrics
if backend1 == "PathDB" or backend2 == "PathDB":
    print()
    print("PATHDB STATE CACHE METRICS")
    print("-" * 75)

    for name, data, backend in [(sys.argv[1], data1, backend1), (sys.argv[2], data2, backend2)]:
        if backend == "PathDB":
            clean_hit = total(data, 'pathdb_clean_state_hit')
            clean_miss = total(data, 'pathdb_clean_state_miss')
            dirty_hit = total(data, 'pathdb_dirty_state_hit')
            dirty_miss = total(data, 'pathdb_dirty_state_miss')

            clean_rate = clean_hit / (clean_hit + clean_miss) * 100 if (clean_hit + clean_miss) > 0 else 0
            dirty_rate = dirty_hit / (dirty_hit + dirty_miss) * 100 if (dirty_hit + dirty_miss) > 0 else 0

            print(f"  {backend} Clean State Cache Hit: {clean_rate:.1f}%")
            print(f"  {backend} Dirty State Cache Hit: {dirty_rate:.1f}%")

# Cache hit rates comparison
print()
print("GO-SIDE CACHE PERFORMANCE")
print("-" * 75)
print(f"{'Cache Type':<30} {backend1:>12} {backend2:>12} {'Diff':>15}")
print("-" * 75)

acc_hit1 = total(data1, 'account_cache_hit')
acc_miss1 = total(data1, 'account_cache_miss')
acc_hit2 = total(data2, 'account_cache_hit')
acc_miss2 = total(data2, 'account_cache_miss')

acc_rate1 = acc_hit1 / (acc_hit1 + acc_miss1) * 100 if (acc_hit1 + acc_miss1) > 0 else 0
acc_rate2 = acc_hit2 / (acc_hit2 + acc_miss2) * 100 if (acc_hit2 + acc_miss2) > 0 else 0
print(f"{'Account Cache Hit Rate':<30} {acc_rate1:>11.1f}% {acc_rate2:>11.1f}% {acc_rate1 - acc_rate2:>+14.1f}%")

sto_hit1 = total(data1, 'storage_cache_hit')
sto_miss1 = total(data1, 'storage_cache_miss')
sto_hit2 = total(data2, 'storage_cache_hit')
sto_miss2 = total(data2, 'storage_cache_miss')

sto_rate1 = sto_hit1 / (sto_hit1 + sto_miss1) * 100 if (sto_hit1 + sto_miss1) > 0 else 0
sto_rate2 = sto_hit2 / (sto_hit2 + sto_miss2) * 100 if (sto_hit2 + sto_miss2) > 0 else 0
print(f"{'Storage Cache Hit Rate':<30} {sto_rate1:>11.1f}% {sto_rate2:>11.1f}% {sto_rate1 - sto_rate2:>+14.1f}%")

# Summary
print()
print("=" * 75)
print("  SUMMARY")
print("=" * 75)

winner = backend1 if insert1 < insert2 else backend2
insert_diff = abs(pct_diff(insert1, insert2))
print(f"  Block Insert: {winner} is {insert_diff:.1f}% faster ({insert1:.3f}ms vs {insert2:.3f}ms)")

tp_winner = backend1 if bps1 > bps2 else backend2
tp_diff = abs(pct_diff(bps1, bps2))
print(f"  Throughput:   {tp_winner} is {tp_diff:.1f}% faster ({bps1:.2f} vs {bps2:.2f} blocks/sec)")

commit_winner = backend1 if commit1_ms < commit2_ms else backend2
commit_pct = abs(pct_diff(commit1_ms, commit2_ms))
print(f"  State Commit: {commit_winner} is {commit_pct:.1f}% faster ({commit1_ms:.3f}ms vs {commit2_ms:.3f}ms)")

print("=" * 75)
