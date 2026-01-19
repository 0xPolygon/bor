#!/bin/bash
# prometheus_monitor.sh - Monitor Prometheus metrics for Bor performance
# Usage: ./prometheus_monitor.sh [metrics_url] [interval] [output_file]
#
# Metrics collected:
# - chain_inserts: Total block processing time (end-to-end)
# - triedbgo_*: Direct triedb-go Rust library performance metrics (when triedb enabled)
# - pathdb_*/hashdb_*: Traditional MPT performance metrics (when triedb disabled)
# - Cache hit rates: Go-side caching performance
# - Throughput: Blocks processed per second

METRICS_URL="${1:-$(kurtosis port print pos l2-el-1-bor-heimdall-v2-validator metrics)/debug/metrics/prometheus}"
INTERVAL="${2:-1}"
OUTPUT="${3:-prometheus_metrics_$(date +%Y%m%d_%H%M%S).csv}"

echo "═══════════════════════════════════════════════════════════════"
echo "  Bor Performance Monitor (TrieDB-go / PathDB / HashDB)"
echo "═══════════════════════════════════════════════════════════════"
echo "Metrics URL: $METRICS_URL"
echo "Interval: ${INTERVAL}s"
echo "Output: $OUTPUT"
echo ""

# CSV Header - includes both triedb-go and pathdb/hashdb metrics for comparison
cat > "$OUTPUT" << 'EOF'
timestamp,chain_head_block,chain_imports,chain_inserts_p50_ns,chain_validation_p50_ns,chain_execution_p50_ns,chain_write_p50_ns,chain_stateprep_p50_ns,chain_commitprep_p50_ns,chain_account_commits_p50_ns,chain_storage_commits_p50_ns,chain_snapshot_commits_p50_ns,chain_triedb_commits_p50_ns,pathdb_tree_add_p50_ns,pathdb_tree_cap_p50_ns,triedbgo_account_read_p50_ns,triedbgo_storage_read_p50_ns,triedbgo_commit_p50_ns,triedbgo_root_compute_p50_ns,triedbgo_ops_account_reads,triedbgo_ops_storage_reads,pathdb_commit_p50_ns,hashdb_commit_p50_ns,pathdb_clean_state_hit,pathdb_clean_state_miss,pathdb_dirty_state_hit,pathdb_dirty_state_miss,account_cache_hit,account_cache_miss,storage_cache_hit,storage_cache_miss,txpool_pending,txpool_valid
EOF

# Temp file for metrics (avoids bash variable size limits)
METRICS_TMP=$(mktemp)
trap "rm -f $METRICS_TMP" EXIT

# Track previous values for throughput calculation
PREV_BLOCK=0
PREV_TIME=0

get_metric() {
    local name="$1"
    local quantile="$2"

    if [ -n "$quantile" ]; then
        grep "${name}{quantile=\"${quantile}\"}" "$METRICS_TMP" | awk '{print $NF}' | head -1
    else
        grep "^${name} " "$METRICS_TMP" | grep -v "{" | awk '{print $2}' | head -1
    fi
}

# Alternative pattern for metrics with space before brace
get_metric_alt() {
    local name="$1"
    local quantile="$2"

    if [ -n "$quantile" ]; then
        grep "${name} {quantile=\"${quantile}\"}" "$METRICS_TMP" | awk '{print $NF}' | head -1
    else
        grep "^${name} " "$METRICS_TMP" | grep -v "{" | awk '{print $2}' | head -1
    fi
}

ns_to_ms() {
    local ns="$1"
    if [ -n "$ns" ] && [ "$ns" != "0" ]; then
        printf "%.3f" $(echo "$ns / 1000000" | bc -l 2>/dev/null) 2>/dev/null
    else
        echo "0"
    fi
}

ns_to_us() {
    local ns="$1"
    if [ -n "$ns" ] && [ "$ns" != "0" ]; then
        printf "%.1f" $(echo "$ns / 1000" | bc -l 2>/dev/null) 2>/dev/null
    else
        echo "0"
    fi
}

fetch_and_display() {
    # Fetch metrics to temp file
    if ! curl -s "$METRICS_URL" > "$METRICS_TMP" 2>/dev/null; then
        echo "ERROR: Could not fetch metrics from $METRICS_URL"
        return 1
    fi

    # Check if we got data
    if [ ! -s "$METRICS_TMP" ]; then
        echo "ERROR: Empty response from $METRICS_URL"
        return 1
    fi

    # Extract chain values
    local head_block=$(get_metric "chain_head_block")
    local imports=$(get_metric "chain_imports")

    # Block insert time (p50) - try both patterns
    local inserts_p50=$(get_metric "chain_inserts" "0.5")
    if [ -z "$inserts_p50" ]; then
        inserts_p50=$(get_metric_alt "chain_inserts" "0.5")
    fi

    # Block processing breakdown metrics (p50)
    local validation_p50=$(get_metric "chain_validation" "0.5")
    if [ -z "$validation_p50" ]; then
        validation_p50=$(get_metric_alt "chain_validation" "0.5")
    fi

    local execution_p50=$(get_metric "chain_execution" "0.5")
    if [ -z "$execution_p50" ]; then
        execution_p50=$(get_metric_alt "chain_execution" "0.5")
    fi

    local write_p50=$(get_metric "chain_write" "0.5")
    if [ -z "$write_p50" ]; then
        write_p50=$(get_metric_alt "chain_write" "0.5")
    fi

    # Commit time breakdown metrics (p50)
    local account_commits_p50=$(get_metric "chain_account_commits" "0.5")
    if [ -z "$account_commits_p50" ]; then
        account_commits_p50=$(get_metric_alt "chain_account_commits" "0.5")
    fi

    local storage_commits_p50=$(get_metric "chain_storage_commits" "0.5")
    if [ -z "$storage_commits_p50" ]; then
        storage_commits_p50=$(get_metric_alt "chain_storage_commits" "0.5")
    fi

    local snapshot_commits_p50=$(get_metric "chain_snapshot_commits" "0.5")
    if [ -z "$snapshot_commits_p50" ]; then
        snapshot_commits_p50=$(get_metric_alt "chain_snapshot_commits" "0.5")
    fi

    local triedb_commits_p50=$(get_metric "chain_triedb_commits" "0.5")
    if [ -z "$triedb_commits_p50" ]; then
        triedb_commits_p50=$(get_metric_alt "chain_triedb_commits" "0.5")
    fi

    # Additional breakdown metrics
    local stateprep_p50=$(get_metric "chain_stateprep" "0.5")
    if [ -z "$stateprep_p50" ]; then
        stateprep_p50=$(get_metric_alt "chain_stateprep" "0.5")
    fi

    local commitprep_p50=$(get_metric "chain_commitprep" "0.5")
    if [ -z "$commitprep_p50" ]; then
        commitprep_p50=$(get_metric_alt "chain_commitprep" "0.5")
    fi

    # PathDB tree operation metrics
    local tree_add_p50=$(get_metric "pathdb_tree_add" "0.5")
    if [ -z "$tree_add_p50" ]; then
        tree_add_p50=$(get_metric_alt "pathdb_tree_add" "0.5")
    fi

    local tree_cap_p50=$(get_metric "pathdb_tree_cap" "0.5")
    if [ -z "$tree_cap_p50" ]; then
        tree_cap_p50=$(get_metric_alt "pathdb_tree_cap" "0.5")
    fi

    # TrieDB-go metrics - use gauge metrics (persistent) with timer fallback
    # Gauges don't reset between Prometheus scrapes, so they're more reliable
    local tdb_acc_read_p50=$(get_metric "triedbgo_avg_account_read_ns")
    if [ -z "$tdb_acc_read_p50" ] || [ "$tdb_acc_read_p50" = "0" ]; then
        tdb_acc_read_p50=$(get_metric "triedbgo_account_read" "0.5")
        if [ -z "$tdb_acc_read_p50" ]; then
            tdb_acc_read_p50=$(get_metric_alt "triedbgo_account_read" "0.5")
        fi
    fi
    local tdb_sto_read_p50=$(get_metric "triedbgo_avg_storage_read_ns")
    if [ -z "$tdb_sto_read_p50" ] || [ "$tdb_sto_read_p50" = "0" ]; then
        tdb_sto_read_p50=$(get_metric "triedbgo_storage_read" "0.5")
        if [ -z "$tdb_sto_read_p50" ]; then
            tdb_sto_read_p50=$(get_metric_alt "triedbgo_storage_read" "0.5")
        fi
    fi
    local tdb_commit_p50=$(get_metric "triedbgo_avg_commit_ns")
    if [ -z "$tdb_commit_p50" ] || [ "$tdb_commit_p50" = "0" ]; then
        tdb_commit_p50=$(get_metric "triedbgo_commit" "0.5")
        if [ -z "$tdb_commit_p50" ]; then
            tdb_commit_p50=$(get_metric_alt "triedbgo_commit" "0.5")
        fi
    fi
    local tdb_root_p50=$(get_metric "triedbgo_avg_root_compute_ns")
    if [ -z "$tdb_root_p50" ] || [ "$tdb_root_p50" = "0" ]; then
        tdb_root_p50=$(get_metric "triedbgo_root_compute" "0.5")
        if [ -z "$tdb_root_p50" ]; then
            tdb_root_p50=$(get_metric_alt "triedbgo_root_compute" "0.5")
        fi
    fi

    # TrieDB-go counters (use new non-colliding names)
    local tdb_acc_read_count=$(get_metric "triedbgo_ops_account_reads")
    local tdb_sto_read_count=$(get_metric "triedbgo_ops_storage_reads")

    # PathDB metrics (p50) - for traditional MPT with path-based state scheme
    # Note: pathdb/commit/time becomes pathdb_commit_time in Prometheus
    local pathdb_commit_p50=$(get_metric "pathdb_commit_time" "0.5")
    if [ -z "$pathdb_commit_p50" ]; then
        pathdb_commit_p50=$(get_metric_alt "pathdb_commit_time" "0.5")
    fi

    # HashDB metrics (p50) - for traditional MPT with hash-based state scheme
    # Note: hashdb/memcache/commit/time becomes hashdb_memcache_commit_time
    local hashdb_commit_p50=$(get_metric "hashdb_memcache_commit_time" "0.5")
    if [ -z "$hashdb_commit_p50" ]; then
        hashdb_commit_p50=$(get_metric_alt "hashdb_memcache_commit_time" "0.5")
    fi

    # PathDB state cache metrics (Meters - these are rate metrics)
    # pathdb/clean/state/hit becomes pathdb_clean_state_hit
    local pathdb_clean_state_hit=$(get_metric "pathdb_clean_state_hit")
    local pathdb_clean_state_miss=$(get_metric "pathdb_clean_state_miss")
    local pathdb_dirty_state_hit=$(get_metric "pathdb_dirty_state_hit")
    local pathdb_dirty_state_miss=$(get_metric "pathdb_dirty_state_miss")

    # Go-side cache metrics
    local acc_cache_hit=$(get_metric "chain_account_reads_cache_process_hit")
    local acc_cache_miss=$(get_metric "chain_account_reads_cache_process_miss")
    local sto_cache_hit=$(get_metric "chain_storage_reads_cache_process_hit")
    local sto_cache_miss=$(get_metric "chain_storage_reads_cache_process_miss")

    # Txpool
    local pending=$(get_metric "txpool_pending")
    local valid=$(get_metric "txpool_valid")

    # Convert to human readable
    local inserts_ms=$(ns_to_ms "$inserts_p50")
    local validation_ms=$(ns_to_ms "$validation_p50")
    local execution_ms=$(ns_to_ms "$execution_p50")
    local write_ms=$(ns_to_ms "$write_p50")
    local stateprep_ms=$(ns_to_ms "$stateprep_p50")
    local commitprep_ms=$(ns_to_ms "$commitprep_p50")
    local account_commits_ms=$(ns_to_ms "$account_commits_p50")
    local storage_commits_ms=$(ns_to_ms "$storage_commits_p50")
    local snapshot_commits_ms=$(ns_to_ms "$snapshot_commits_p50")
    local triedb_commits_ms=$(ns_to_ms "$triedb_commits_p50")
    local tree_add_ms=$(ns_to_ms "$tree_add_p50")
    local tree_cap_ms=$(ns_to_ms "$tree_cap_p50")
    local tdb_acc_read_us=$(ns_to_us "$tdb_acc_read_p50")
    local tdb_sto_read_us=$(ns_to_us "$tdb_sto_read_p50")
    local tdb_commit_ms=$(ns_to_ms "$tdb_commit_p50")
    local tdb_root_ms=$(ns_to_ms "$tdb_root_p50")
    local pathdb_commit_ms=$(ns_to_ms "$pathdb_commit_p50")
    local hashdb_commit_ms=$(ns_to_ms "$hashdb_commit_p50")

    # Cache hit rates
    local acc_total=$((${acc_cache_hit:-0} + ${acc_cache_miss:-0}))
    local sto_total=$((${sto_cache_hit:-0} + ${sto_cache_miss:-0}))
    local acc_hit_rate="N/A"
    local sto_hit_rate="N/A"

    if [ "$acc_total" -gt 0 ] 2>/dev/null; then
        acc_hit_rate=$(printf "%.1f" $(echo "${acc_cache_hit:-0} * 100 / $acc_total" | bc -l) 2>/dev/null)
    fi
    if [ "$sto_total" -gt 0 ] 2>/dev/null; then
        sto_hit_rate=$(printf "%.1f" $(echo "${sto_cache_hit:-0} * 100 / $sto_total" | bc -l) 2>/dev/null)
    fi

    # PathDB state cache hit rate
    local pathdb_clean_total=$((${pathdb_clean_state_hit:-0} + ${pathdb_clean_state_miss:-0}))
    local pathdb_dirty_total=$((${pathdb_dirty_state_hit:-0} + ${pathdb_dirty_state_miss:-0}))
    local pathdb_clean_rate="N/A"
    local pathdb_dirty_rate="N/A"

    if [ "$pathdb_clean_total" -gt 0 ] 2>/dev/null; then
        pathdb_clean_rate=$(printf "%.1f" $(echo "${pathdb_clean_state_hit:-0} * 100 / $pathdb_clean_total" | bc -l) 2>/dev/null)
    fi
    if [ "$pathdb_dirty_total" -gt 0 ] 2>/dev/null; then
        pathdb_dirty_rate=$(printf "%.1f" $(echo "${pathdb_dirty_state_hit:-0} * 100 / $pathdb_dirty_total" | bc -l) 2>/dev/null)
    fi

    # Calculate throughput (blocks/sec)
    local current_time=$(date +%s)
    local throughput="N/A"
    if [ "$PREV_TIME" -gt 0 ] && [ "${head_block:-0}" -gt "$PREV_BLOCK" ]; then
        local time_diff=$((current_time - PREV_TIME))
        local block_diff=$((${head_block:-0} - PREV_BLOCK))
        if [ "$time_diff" -gt 0 ]; then
            throughput=$(printf "%.2f" $(echo "$block_diff / $time_diff" | bc -l) 2>/dev/null)
        fi
    fi
    PREV_BLOCK=${head_block:-0}
    PREV_TIME=$current_time

    # Detect which backend is active (check counters too since timers reset)
    local backend="Unknown"
    if [ -n "$tdb_acc_read_count" ] && [ "${tdb_acc_read_count:-0}" -gt 0 ] 2>/dev/null; then
        backend="TrieDB-go"
    elif [ -n "$tdb_commit_p50" ] && [ "$tdb_commit_p50" != "0" ]; then
        backend="TrieDB-go"
    elif [ -n "$pathdb_commit_p50" ] && [ "$pathdb_commit_p50" != "0" ]; then
        backend="PathDB (MPT)"
    elif [ -n "$pathdb_clean_state_hit" ] && [ "${pathdb_clean_state_hit:-0}" -gt 0 ] 2>/dev/null; then
        backend="PathDB (MPT)"
    elif [ -n "$hashdb_commit_p50" ] && [ "$hashdb_commit_p50" != "0" ]; then
        backend="HashDB (MPT)"
    fi

    # Display
    clear
    echo "═══════════════════════════════════════════════════════════════"
    echo "  Bor Performance Monitor - $(date '+%Y-%m-%d %H:%M:%S')"
    echo "  Backend: $backend"
    echo "═══════════════════════════════════════════════════════════════"

    echo ""
    echo "CHAIN STATUS"
    echo "────────────────────────────────────────"
    printf "  %-25s %s\n" "Head Block:" "${head_block:-N/A}"
    printf "  %-25s %s\n" "Total Imports:" "${imports:-N/A}"
    printf "  %-25s %s blocks/sec\n" "Throughput:" "${throughput}"

    echo ""
    echo "BLOCK INSERT TIME (p50)"
    echo "────────────────────────────────────────"
    printf "  %-25s %s ms\n" "Total Block Insert:" "${inserts_ms}"

    echo ""
    echo "BLOCK INSERT BREAKDOWN (p50)"
    echo "────────────────────────────────────────"
    printf "  %-25s %s ms\n" "State Prep:" "${stateprep_ms:-N/A}"
    printf "  %-25s %s ms\n" "Validation:" "${validation_ms:-N/A}"
    printf "  %-25s %s ms\n" "Execution:" "${execution_ms:-N/A}"
    printf "  %-25s %s ms\n" "Commit Prep:" "${commitprep_ms:-N/A}"
    printf "  %-25s %s ms\n" "Write:" "${write_ms:-N/A}"

    echo ""
    echo "COMMIT TIME BREAKDOWN (p50)"
    echo "────────────────────────────────────────"
    printf "  %-25s %s ms\n" "Account Commits:" "${account_commits_ms:-N/A}"
    printf "  %-25s %s ms\n" "Storage Commits:" "${storage_commits_ms:-N/A}"
    printf "  %-25s %s ms\n" "Snapshot Commits:" "${snapshot_commits_ms:-N/A}"
    printf "  %-25s %s ms\n" "TrieDB Commits:" "${triedb_commits_ms:-N/A}"
    if [ -n "$tree_add_ms" ] && [ "$tree_add_ms" != "0" ]; then
        printf "  %-25s %s ms\n" "  - Tree Add:" "${tree_add_ms}"
    fi
    if [ -n "$tree_cap_ms" ] && [ "$tree_cap_ms" != "0" ]; then
        printf "  %-25s %s ms\n" "  - Tree Cap:" "${tree_cap_ms}"
    fi

    # Show backend-specific metrics
    if [ "$backend" = "TrieDB-go" ]; then
        echo ""
        echo "TRIEDB-GO STATE OPERATIONS (p50)"
        echo "────────────────────────────────────────"
        printf "  %-25s %s µs\n" "Account Read:" "${tdb_acc_read_us:-N/A}"
        printf "  %-25s %s µs\n" "Storage Read:" "${tdb_sto_read_us:-N/A}"
        printf "  %-25s %s ms\n" "Commit:" "${tdb_commit_ms:-N/A}"
        printf "  %-25s %s ms\n" "Root Compute:" "${tdb_root_ms:-N/A}"
        echo ""
        printf "  %-25s %s\n" "Account Reads (total):" "${tdb_acc_read_count:-0}"
        printf "  %-25s %s\n" "Storage Reads (total):" "${tdb_sto_read_count:-0}"
    elif [ "$backend" = "PathDB (MPT)" ]; then
        echo ""
        echo "PATHDB STATE OPERATIONS (p50)"
        echo "────────────────────────────────────────"
        printf "  %-25s %s ms\n" "Commit:" "${pathdb_commit_ms:-N/A}"
        echo ""
        printf "  %-25s %s%%\n" "Clean State Cache Hit:" "${pathdb_clean_rate}"
        printf "  %-25s %s%%\n" "Dirty State Cache Hit:" "${pathdb_dirty_rate}"
    elif [ "$backend" = "HashDB (MPT)" ]; then
        echo ""
        echo "HASHDB STATE OPERATIONS (p50)"
        echo "────────────────────────────────────────"
        printf "  %-25s %s ms\n" "Commit:" "${hashdb_commit_ms:-N/A}"
    fi

    echo ""
    echo "GO-SIDE CACHE PERFORMANCE"
    echo "────────────────────────────────────────"
    printf "  %-25s %s%%\n" "Account Cache Hit:" "${acc_hit_rate}"
    printf "  %-25s %s%%\n" "Storage Cache Hit:" "${sto_hit_rate}"

    echo ""
    echo "TRANSACTION POOL"
    echo "────────────────────────────────────────"
    printf "  %-25s %s\n" "Pending:" "${pending:-0}"
    printf "  %-25s %s\n" "Valid (total):" "${valid:-0}"

    # Write to CSV
    local timestamp=$(date +%s)
    echo "${timestamp},${head_block:-0},${imports:-0},${inserts_p50:-0},${validation_p50:-0},${execution_p50:-0},${write_p50:-0},${stateprep_p50:-0},${commitprep_p50:-0},${account_commits_p50:-0},${storage_commits_p50:-0},${snapshot_commits_p50:-0},${triedb_commits_p50:-0},${tree_add_p50:-0},${tree_cap_p50:-0},${tdb_acc_read_p50:-0},${tdb_sto_read_p50:-0},${tdb_commit_p50:-0},${tdb_root_p50:-0},${tdb_acc_read_count:-0},${tdb_sto_read_count:-0},${pathdb_commit_p50:-0},${hashdb_commit_p50:-0},${pathdb_clean_state_hit:-0},${pathdb_clean_state_miss:-0},${pathdb_dirty_state_hit:-0},${pathdb_dirty_state_miss:-0},${acc_cache_hit:-0},${acc_cache_miss:-0},${sto_cache_hit:-0},${sto_cache_miss:-0},${pending:-0},${valid:-0}" >> "$OUTPUT"

    echo ""
    echo "═══════════════════════════════════════════════════════════════"
    echo "  Recording to: $OUTPUT | Press Ctrl+C to stop"
    echo "═══════════════════════════════════════════════════════════════"
}

echo "Starting monitoring (Ctrl+C to stop)..."
echo ""

while true; do
    fetch_and_display
    sleep "$INTERVAL"
done
