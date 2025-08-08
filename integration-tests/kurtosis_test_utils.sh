#!/bin/bash

# Utility functions for Kurtosis stateless sync tests

# =============================================================================
# Configuration parameters - adjust these as needed
# =============================================================================

# Test configuration
TARGET_BLOCK=${TARGET_BLOCK:-380}
TARGET_BLOCK_HF=${TARGET_BLOCK_HF:-384}
TARGET_BLOCK_POST_HF=${TARGET_BLOCK_POST_HF:-420}
TEST_TIMEOUT_SECONDS=${TEST_TIMEOUT_SECONDS:-1800}  # 30 minutes
SLEEP_INTERVAL=${SLEEP_INTERVAL:-5}
LATENCY_CHECK_ITERATIONS=${LATENCY_CHECK_ITERATIONS:-10}
NORMAL_SETTLEMENT_LATENCY_SECONDS=${NORMAL_SETTLEMENT_LATENCY_SECONDS:-5}
MAX_SETTLEMENT_LATENCY_SECONDS=${MAX_SETTLEMENT_LATENCY_SECONDS:-10}

# Service configuration
STATELESS_VALIDATORS_START=${STATELESS_VALIDATORS_START:-1}
STATELESS_VALIDATORS_END=${STATELESS_VALIDATORS_END:-7}
LEGACY_VALIDATOR_ID=${LEGACY_VALIDATOR_ID:-8}
RPC_SERVICES_START=${RPC_SERVICES_START:-9}
RPC_SERVICES_END=${RPC_SERVICES_END:-11}

# Service naming patterns
SERVICE_PREFIX_VALIDATOR=${SERVICE_PREFIX_VALIDATOR:-"l2-el"}
SERVICE_SUFFIX_VALIDATOR=${SERVICE_SUFFIX_VALIDATOR:-"bor-heimdall-v2-validator"}
SERVICE_SUFFIX_RPC=${SERVICE_SUFFIX_RPC:-"bor-heimdall-v2-rpc"}

# Network configuration (from apply_global_network_latency.sh)
NETWORK_LATENCY_DURATION=${NETWORK_LATENCY_DURATION:-"30s"}
INTERFACE=${INTERFACE:-"eth0"}
DELAY_EL=${DELAY_EL:-300}
JITTER_EL=${JITTER_EL:-50}
DELAY_CL=${DELAY_CL:-200}
JITTER_CL=${JITTER_CL:-50}

# Extreme network latency configuration for recovery tests
EXTREME_DELAY_EL=${EXTREME_DELAY_EL:-4000}
EXTREME_JITTER_EL=${EXTREME_JITTER_EL:-100}
EXTREME_DELAY_CL=${EXTREME_DELAY_CL:-500}
EXTREME_JITTER_CL=${EXTREME_JITTER_CL:-50}
EXTREME_LATENCY_DURATION=${EXTREME_LATENCY_DURATION:-"30s"}

# Check if required tools are available
check_required_tools() {
    if ! command -v cast &>/dev/null; then
        echo "Error: 'cast' command not found. Please install Foundry toolkit."
        exit 1
    fi

    if ! command -v kurtosis &>/dev/null; then
        echo "Error: 'kurtosis' command not found. Please install Kurtosis."
        exit 1
    fi
}

# Function to get RPC URL for a service
get_rpc_url() {
    local service_name=$1
    kurtosis port print $ENCLAVE_NAME $service_name rpc 2>/dev/null || echo ""
}

# Function to get block number from a specific service using cast
get_block_number() {
    local service_name=$1
    local rpc_url=$(get_rpc_url $service_name)
    if [ -n "$rpc_url" ]; then
        cast block --rpc-url "$rpc_url" 2>/dev/null | grep "number" | awk '{print $2}' | sed 's/,//' || echo "0"
    else
        echo "0"
    fi
}

# Function to get block timestamp using cast
get_block_timestamp() {
    local service_name=$1
    local block_number=$2
    local rpc_url=$(get_rpc_url $service_name)
    if [ -n "$rpc_url" ]; then
        cast block $block_number --rpc-url "$rpc_url" 2>/dev/null | grep "timestamp" | awk '{print $2}' | sed 's/,//' || echo "0"
    else
        echo "0"
    fi
}

# Function to get finalized block number using cast
get_finalized_block() {
    local service_name=$1
    local rpc_url=$(get_rpc_url $service_name)
    if [ -n "$rpc_url" ]; then
        cast block finalized --rpc-url "$rpc_url" 2>/dev/null | grep "number" | awk '{print $2}' | sed 's/,//' || echo "0"
    else
        echo "0"
    fi
}

# Function to get block hash using cast
get_block_hash() {
    local service_name=$1
    local block_number=$2
    local rpc_url=$(get_rpc_url $service_name)
    if [ -n "$rpc_url" ]; then
        cast block $block_number --rpc-url "$rpc_url" 2>/dev/null | grep "^hash" | awk '{print $2}' | sed 's/,//' || echo ""
    else
        echo ""
    fi
}

# Function to setup service lists based on kurtosis configuration
setup_service_lists() {
    echo "Setting up service lists based on kurtosis configuration..."

    # Configuration summary
    echo "Config: Stateless validators ($STATELESS_VALIDATORS_START-$STATELESS_VALIDATORS_END), Legacy validator ($LEGACY_VALIDATOR_ID), RPC services ($RPC_SERVICES_START-$RPC_SERVICES_END)"

    STATELESS_SYNC_VALIDATORS=()
    LEGACY_VALIDATORS=()
    STATELESS_RPC_SERVICES=()

    # Stateless sync validators
    for ((i=STATELESS_VALIDATORS_START; i<=STATELESS_VALIDATORS_END; i++)); do
        STATELESS_SYNC_VALIDATORS+=("$SERVICE_PREFIX_VALIDATOR-$i-$SERVICE_SUFFIX_VALIDATOR")
    done

    # Legacy validator
    LEGACY_VALIDATORS+=("$SERVICE_PREFIX_VALIDATOR-$LEGACY_VALIDATOR_ID-$SERVICE_SUFFIX_VALIDATOR")

    # RPC nodes
    for ((i=RPC_SERVICES_START; i<=RPC_SERVICES_END; i++)); do
        STATELESS_RPC_SERVICES+=("$SERVICE_PREFIX_VALIDATOR-$i-$SERVICE_SUFFIX_RPC")
    done

    echo "Stateless sync validators ($STATELESS_VALIDATORS_START-$STATELESS_VALIDATORS_END): ${STATELESS_SYNC_VALIDATORS[*]}"
    echo "Legacy validator ($LEGACY_VALIDATOR_ID): ${LEGACY_VALIDATORS[*]}"
    echo "RPC services ($RPC_SERVICES_START-$RPC_SERVICES_END): ${STATELESS_RPC_SERVICES[*]}"
}

# Function to verify service accessibility
verify_service_accessibility() {
    echo "Verifying service accessibility..."
    for service in "${STATELESS_SYNC_VALIDATORS[@]}" "${LEGACY_VALIDATORS[@]}" "${STATELESS_RPC_SERVICES[@]}"; do
        rpc_url=$(get_rpc_url $service)
        if [ -n "$rpc_url" ]; then
            echo "✅ $service: $rpc_url"
        else
            echo "❌ $service: No RPC URL found"
        fi
    done
}

# Function to check block hash consensus for a list of services
check_block_hash_consensus() {
    local target_block=$1
    shift
    local services=("$@")
    
    local block_hashes=()
    local reference_hash=""
    local hash_mismatch=false

    for service in "${services[@]}"; do
        block_hash=$(get_block_hash $service $target_block)
        if [ -n "$block_hash" ]; then
            block_hashes+=("$service:$block_hash")

            # Set reference hash from first service
            if [ -z "$reference_hash" ]; then
                reference_hash=$block_hash
                echo "Reference hash from $service: $reference_hash"
            else
                # Compare with reference hash
                if [ "$block_hash" != "$reference_hash" ]; then
                    echo "❌ Hash mismatch! $service has hash: $block_hash (expected: $reference_hash)"
                    hash_mismatch=true
                else
                    echo "✅ $service has matching hash: $block_hash"
                fi
            fi
        else
            echo "❌ Failed to get hash for block $target_block from $service"
            hash_mismatch=true
        fi
    done

    if [ "$hash_mismatch" = true ]; then
        echo "❌ Block hash verification failed for block $target_block"
        echo "All hashes collected:"
        for hash_entry in "${block_hashes[@]}"; do
            echo "  $hash_entry"
        done
        return 1
    else
        echo "✅ All services have the same hash for block $target_block: $reference_hash"
        return 0
    fi
}

# Function to get maximum block number from a list of services
get_max_block_from_services() {
    local services=("$@")
    local max_block=0
    
    for service in "${services[@]}"; do
        block_num=$(get_block_number $service)
        if [[ "$block_num" =~ ^[0-9]+$ ]] && [ $block_num -gt $max_block ]; then
            max_block=$block_num
        fi
    done
    
    echo $max_block
}

# Function to get minimum block number from a list of services
get_min_block_from_services() {
    local services=("$@")
    local min_block=999999999
    local block_numbers=()
    
    for service in "${services[@]}"; do
        block_num=$(get_block_number $service)
        if [[ "$block_num" =~ ^[0-9]+$ ]]; then
            block_numbers+=($block_num)
            if [ $block_num -lt $min_block ]; then
                min_block=$block_num
            fi
        fi
    done
    
    # Return 0 if no valid block numbers found
    if [ ${#block_numbers[@]} -eq 0 ]; then
        echo 0
    else
        echo $min_block
    fi
}

# Function to start network latency in background
start_network_latency() {
    local delay_el=${1:-$DELAY_EL}
    local jitter_el=${2:-$JITTER_EL}
    local delay_cl=${3:-$DELAY_CL}
    local jitter_cl=${4:-$JITTER_CL}
    local duration=${5:-$NETWORK_LATENCY_DURATION}
    
    echo "Starting network latency application..."
    echo "Network latency config: EL=${delay_el}ms±${jitter_el}ms, CL=${delay_cl}ms±${jitter_cl}ms, Duration=${duration}"
    
    # Export the custom parameters so the apply_global_network_latency.sh script can use them
    export DELAY_EL="$delay_el"
    export JITTER_EL="$jitter_el"
    export DELAY_CL="$delay_cl"
    export JITTER_CL="$jitter_cl"
    export NETWORK_LATENCY_DURATION="$duration"
    
    # Start the latency script in background and capture its PID
    "$SCRIPT_DIR/apply_global_network_latency.sh" > /tmp/network_latency.log 2>&1 &
    NETWORK_LATENCY_PID=$!
    echo "Network latency process started with PID $NETWORK_LATENCY_PID"
    
    # Give it a moment to start up
    sleep 10
    
    # Check if the process is still running
    if kill -0 $NETWORK_LATENCY_PID 2>/dev/null; then
        echo "✅ Network latency successfully applied"
        return 0
    else
        echo "❌ Failed to start network latency"
        cat /tmp/network_latency.log 2>/dev/null || echo "No log file found"
        return 1
    fi
}

# Function to wait for network latency to complete
wait_for_pending_network_latency() {
    if [ -n "$NETWORK_LATENCY_PID" ]; then
        echo "Waiting for network latency process to complete (PID: $NETWORK_LATENCY_PID)..."
        
        # Wait for the process to complete naturally
        if wait $NETWORK_LATENCY_PID 2>/dev/null; then
            echo "✅ Network latency process completed successfully"
        else
            echo "✅ Network latency process completed"
        fi
        
        NETWORK_LATENCY_PID=""
    fi
}

# Function to run milestone settlement latency test
test_milestone_settlement_latency() {
    local test_name="$1"
    local iterations=${2:-$LATENCY_CHECK_ITERATIONS}
    local max_latency=${3:-$MAX_SETTLEMENT_LATENCY_SECONDS}
    
    echo "Testing milestone settlement latency for $test_name..."
    echo "Checking $iterations finalized blocks with max latency threshold of ${max_latency}s"
    
    REPRESENTATIVE_SERVICE=${STATELESS_SYNC_VALIDATORS[0]}
    echo "Using service $REPRESENTATIVE_SERVICE for latency testing"
    
    local failed_checks=0
    local total_latency=0
    local successful_checks=0
    
    for ((i=1; i<=iterations; i++)); do
        finalized_block=$(get_finalized_block $REPRESENTATIVE_SERVICE)
        
        if [[ "$finalized_block" =~ ^[0-9]+$ ]] && [ $finalized_block -gt 0 ]; then
            block_timestamp=$(get_block_timestamp $REPRESENTATIVE_SERVICE $finalized_block)
            current_timestamp=$(date +%s)
            
            if [[ "$block_timestamp" =~ ^[0-9]+$ ]]; then
                latency=$((current_timestamp - block_timestamp))
                total_latency=$((total_latency + latency))
                successful_checks=$((successful_checks + 1))
                
                echo "Block $finalized_block: latency=${latency}s"
                
                if [ $latency -gt $max_latency ]; then
                    echo "❌ Settlement latency check $i failed: ${latency}s > ${max_latency}s for block $finalized_block"
                    failed_checks=$((failed_checks + 1))
                fi
            else
                echo "⚠️  Could not get valid timestamp for block $finalized_block"
            fi
        else
            echo "⚠️  Could not get valid finalized block in iteration $i"
        fi
        
        sleep 2
    done
    
    if [ $successful_checks -gt 0 ]; then
        avg_latency=$((total_latency / successful_checks))
        echo "Average latency: ${avg_latency}s (${successful_checks} successful checks)"
    fi
    
    if [ $failed_checks -eq 0 ]; then
        echo "✅ All milestone settlement latency checks passed for $test_name (< ${max_latency}s)"
        return 0
    else
        echo "❌ $failed_checks out of $iterations latency checks failed for $test_name"
        return 1
    fi
}

# Function to test that nodes can recover sync and generate new blocks
test_sync_recovery() {
    local test_name="$1"
    local timeout_seconds=${2:-300}  # 5 minutes default
    local min_block_progress=${3:-5}  # Minimum blocks to consider recovery successful
    
    echo "Testing sync recovery for $test_name..."
    echo "Waiting up to ${timeout_seconds}s for at least ${min_block_progress} new blocks and finalized blocks"
    
    # Get initial block numbers from all stateless services
    local initial_blocks=()
    local initial_finalized_blocks=()
    STATELESS_SERVICES=("${STATELESS_SYNC_VALIDATORS[@]}" "${STATELESS_RPC_SERVICES[@]}")
    
    # Use first validator for finalized block checks
    REPRESENTATIVE_SERVICE=${STATELESS_SYNC_VALIDATORS[0]}
    initial_finalized=$(get_finalized_block "$REPRESENTATIVE_SERVICE")
    
    for service in "${STATELESS_SERVICES[@]}"; do
        initial_block=$(get_block_number "$service")
        if [[ "$initial_block" =~ ^[0-9]+$ ]]; then
            initial_blocks+=("$service:$initial_block")
        fi
    done
    
    echo "Initial block numbers:"
    for entry in "${initial_blocks[@]}"; do
        echo "  $entry"
    done
    echo "Initial finalized block: $initial_finalized"
    
    local start_time=$SECONDS
    local recovery_confirmed=false
    
    while [ $((SECONDS - start_time)) -lt $timeout_seconds ]; do
        local all_progressed=true
        local progress_details=()
        
        # Check regular block progress
        for entry in "${initial_blocks[@]}"; do
            IFS=':' read -r service initial_block <<< "$entry"
            current_block=$(get_block_number "$service")
            
            if [[ "$current_block" =~ ^[0-9]+$ ]]; then
                progress=$((current_block - initial_block))
                progress_details+=("$service: $initial_block → $current_block (+$progress)")
                
                if [ $progress -lt $min_block_progress ]; then
                    all_progressed=false
                fi
            else
                progress_details+=("$service: $initial_block → ERROR (RPC failed)")
                all_progressed=false
            fi
        done
        
        # Check finalized block progress
        current_finalized=$(get_finalized_block "$REPRESENTATIVE_SERVICE")
        if [[ "$current_finalized" =~ ^[0-9]+$ ]] && [[ "$initial_finalized" =~ ^[0-9]+$ ]]; then
            finalized_progress=$((current_finalized - initial_finalized))
            progress_details+=("FINALIZED: $initial_finalized → $current_finalized (+$finalized_progress)")
            
            if [ $finalized_progress -lt $min_block_progress ]; then
                all_progressed=false
            fi
        else
            progress_details+=("FINALIZED: ERROR (unable to get finalized block)")
            all_progressed=false
        fi
        
        elapsed=$((SECONDS - start_time))
        echo "Recovery progress at ${elapsed}s:"
        for detail in "${progress_details[@]}"; do
            echo "  $detail"
        done
        
        if [ "$all_progressed" = true ]; then
            echo "✅ All services have progressed at least $min_block_progress blocks (including finalized blocks)"
            recovery_confirmed=true
            break
        fi
        
        echo "Waiting for more block progress..."
        sleep 10
    done
    
    if [ "$recovery_confirmed" = true ]; then
        echo "✅ Sync recovery test passed for $test_name"
        return 0
    else
        echo "❌ Sync recovery test failed for $test_name - insufficient block progress within ${timeout_seconds}s"
        return 1
    fi
}

# Function to get block author using bor_getAuthor RPC
get_block_author() {
    local service_name=$1
    local block_number=$2
    local rpc_url=$(get_rpc_url "$service_name")
    if [ -n "$rpc_url" ]; then
        # Convert decimal block number to hex
        local block_hex=$(printf "0x%x" "$block_number")
        cast rpc bor_getAuthor "$block_hex" --rpc-url "$rpc_url" 2>/dev/null | tr -d '"' || echo ""
    else
        echo ""
    fi
}

# Function to get reorg count from metrics
get_reorg_count() {
    local service_name=$1
    
    # Get metrics port URL
    local metrics_url=$(kurtosis port print "$ENCLAVE_NAME" "$service_name" metrics 2>/dev/null)
    
    if [ -n "$metrics_url" ]; then
        # Query prometheus metrics and extract reorg count
        local reorg_count=$(curl -s "${metrics_url}/debug/metrics/prometheus" | grep -e "^chain_reorg_executes" | awk '{print $2}' 2>/dev/null)
        if [[ "$reorg_count" =~ ^[0-9]+$ ]]; then
            echo "$reorg_count"
        else
            echo "0"
        fi
    else
        echo "0"
    fi
}

# Function to check block producer rotations in last N blocks
check_block_author_diversity() {
    local service_name=$1
    local num_blocks=${2:-100}
    local min_rotations=${3:-2}
    
    echo "Checking block producer rotations for last $num_blocks blocks on $service_name..."
    
    # Get current block number
    local current_block=$(get_block_number "$service_name")
    if ! [[ "$current_block" =~ ^[0-9]+$ ]] || [ "$current_block" -le 0 ]; then
        echo "❌ Failed to get current block number from $service_name"
        return 1
    fi
    
    local start_block=$((current_block - num_blocks + 1))
    if [ $start_block -lt 1 ]; then
        start_block=1
    fi
    
    echo "Checking blocks $start_block to $current_block (total: $((current_block - start_block + 1)) blocks)"
    
    # Count rotations (author changes between consecutive blocks)
    local rotations=0
    local total_checked=0
    local prev_author=""
    
    for ((i=start_block; i<=current_block; i++)); do
        author=$(get_block_author "$service_name" "$i")
        if [ -n "$author" ] && [ "$author" != "0x0000000000000000000000000000000000000000" ]; then
            total_checked=$((total_checked + 1))
            
            # Check if this is a rotation (author change from previous block)
            if [ -n "$prev_author" ] && [ "$author" != "$prev_author" ]; then
                rotations=$((rotations + 1))
                echo "  Rotation detected at block $i: $prev_author -> $author"
            fi
            
            prev_author="$author"
        fi
        
        # Progress indicator every 10 blocks
        if [ $((i % 10)) -eq 0 ]; then
            echo "  Checked block $i... ($rotations rotations so far)"
        fi
    done
    
    echo "Found $rotations rotations in $total_checked blocks"
    
    if [ "$rotations" -ge "$min_rotations" ]; then
        echo "✅ Block producer rotation check passed: $rotations rotations >= $min_rotations required"
        return 0
    else
        echo "❌ Block producer rotation check failed: $rotations rotations < $min_rotations required"
        return 1
    fi
}