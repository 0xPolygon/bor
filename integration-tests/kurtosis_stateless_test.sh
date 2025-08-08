#!/bin/bash
set -e

# Source utility functions
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/kurtosis_test_utils.sh"

echo "Starting stateless sync tests..."

# Check if required tools are available
check_required_tools

# Define the enclave name
ENCLAVE_NAME=${ENCLAVE_NAME:-"kurtosis-stateless-e2e"}

# Setup service lists
setup_service_lists

# Verify services are accessible
verify_service_accessibility

# Test 1: Check all nodes reach TARGET_BLOCK and have same block hash
test_block_hash_consensus() {
	echo ""
	echo "=== Test 1: Checking all the nodes reach block $TARGET_BLOCK and have the same block hash ==="

	SECONDS=0
	start_time=$SECONDS

	while true; do
		current_time=$SECONDS
		elapsed=$((current_time - start_time))

		# Timeout check
		if [ $elapsed -gt $TEST_TIMEOUT_SECONDS ]; then
			echo "Timeout waiting for block $TARGET_BLOCK (after ${TEST_TIMEOUT_SECONDS}s)"
			return 1
		fi

		# Get block numbers from all services.
		block_numbers=()
		max_block=0

		# Check all services (stateless_sync validators + legacy validators + RPC).
		ALL_TEST_SERVICES=("${STATELESS_SYNC_VALIDATORS[@]}" "${LEGACY_VALIDATORS[@]}" "${STATELESS_RPC_SERVICES[@]}")

		for service in "${ALL_TEST_SERVICES[@]}"; do
			block_num=$(get_block_number $service)
			if [[ "$block_num" =~ ^[0-9]+$ ]]; then
				block_numbers+=($block_num)
				if [ $block_num -gt $max_block ]; then
					max_block=$block_num
				fi
			fi
		done

		echo "Current max block: $max_block ($(printf '%02dm:%02ds\n' $((elapsed / 60)) $((elapsed % 60)))) [${#block_numbers[@]} nodes responding]"

		# Check if all nodes have reached the target block.
		min_block=${block_numbers[0]}
		for block in "${block_numbers[@]}"; do
			if [ $block -lt $min_block ]; then
				min_block=$block
			fi
		done

		if [ $min_block -ge $TARGET_BLOCK ]; then
			echo "All nodes have reached block $TARGET_BLOCK, checking block hash consensus..."

			# Get block hash for block TARGET_BLOCK from all services.
			block_hashes=()
			reference_hash=""
			hash_mismatch=false

			for service in "${ALL_TEST_SERVICES[@]}"; do
				block_hash=$(get_block_hash $service $TARGET_BLOCK)
				if [ -n "$block_hash" ]; then
					block_hashes+=("$service:$block_hash")

					# Set reference hash from first service.
					if [ -z "$reference_hash" ]; then
						reference_hash=$block_hash
						echo "Reference hash from $service: $reference_hash"
					else
						# Compare with reference hash.
						if [ "$block_hash" != "$reference_hash" ]; then
							echo "❌ Hash mismatch! $service has hash: $block_hash (expected: $reference_hash)"
							hash_mismatch=true
						else
							echo "✅ $service has matching hash: $block_hash"
						fi
					fi
				else
					echo "❌ Failed to get hash for block $TARGET_BLOCK from $service"
					hash_mismatch=true
				fi
			done

			if [ "$hash_mismatch" = true ]; then
				echo "❌ Block hash verification failed for block $TARGET_BLOCK"
				echo "All hashes collected:"
				for hash_entry in "${block_hashes[@]}"; do
					echo "  $hash_entry"
				done
				return 1
			else
				echo "✅ All nodes have reached block $TARGET_BLOCK with the same hash: $reference_hash"
				break
			fi
		fi

		sleep $SLEEP_INTERVAL
	done
}

# Test 2: Check nodes continue syncing after block TARGET_BLOCK_HF (veblop HF)
test_post_veblop_hf_behavior() {
	echo ""
	echo "=== Test 2: Checking post-veblop HF behavior (after block $TARGET_BLOCK_HF) ==="
	echo "Waiting for block $TARGET_BLOCK_POST_HF to ensure we're past veblop HF..."

	while true; do
		current_time=$SECONDS
		elapsed=$((current_time - start_time))

		# Timeout check
		if [ $elapsed -gt $TEST_TIMEOUT_SECONDS ]; then
			echo "Timeout waiting for post-HF block $TARGET_BLOCK_POST_HF (after ${TEST_TIMEOUT_SECONDS}s)"
			return 1
		fi

		# Check stateless_sync services (should continue syncing after HF).
		max_stateless_block=0
		for service in "${STATELESS_SYNC_VALIDATORS[@]}" "${STATELESS_RPC_SERVICES[@]}"; do
			block_num=$(get_block_number $service)
			if [[ "$block_num" =~ ^[0-9]+$ ]] && [ $block_num -gt $max_stateless_block ]; then
				max_stateless_block=$block_num
			fi
		done

		# Check legacy services (might stop syncing after HF).
		max_legacy_block=0
		for service in "${LEGACY_VALIDATORS[@]}"; do
			block_num=$(get_block_number $service)
			if [[ "$block_num" =~ ^[0-9]+$ ]] && [ $block_num -gt $max_legacy_block ]; then
				max_legacy_block=$block_num
			fi
		done

		echo "Current stateless_sync max block: $max_stateless_block"
		echo "Current legacy max block: $max_legacy_block"

		if [ $max_stateless_block -ge $TARGET_BLOCK_POST_HF ]; then
			echo "✅ Stateless sync nodes continued syncing past veblop HF"

			# Check if legacy nodes stopped progressing.
			if [ $max_legacy_block -lt $TARGET_BLOCK_HF ]; then
				echo "✅ Legacy nodes appropriately stopped syncing after veblop HF (at block $max_legacy_block)"
			else
				echo "⚠️  Legacy nodes are still running (at block $max_legacy_block) - forked off from stateless sync validators"
			fi

			# Check block hash consensus for stateless sync services at block TARGET_BLOCK_POST_HF.
			echo "Checking block hash consensus for stateless sync services at block $TARGET_BLOCK_POST_HF..."

			# Only check stateless sync validators and RPC services (not legacy validators).
			STATELESS_SERVICES=("${STATELESS_SYNC_VALIDATORS[@]}" "${STATELESS_RPC_SERVICES[@]}")

			# Get block hash for block TARGET_BLOCK_POST_HF from all stateless sync services.
			block_hashes=()
			reference_hash=""
			hash_mismatch=false

			for service in "${STATELESS_SERVICES[@]}"; do
				block_hash=$(get_block_hash $service $TARGET_BLOCK_POST_HF)
				if [ -n "$block_hash" ]; then
					block_hashes+=("$service:$block_hash")

					# Set reference hash from first service.
					if [ -z "$reference_hash" ]; then
						reference_hash=$block_hash
						echo "Reference hash from $service: $reference_hash"
					else
						# Compare with reference hash.
						if [ "$block_hash" != "$reference_hash" ]; then
							echo "❌ Hash mismatch! $service has hash: $block_hash (expected: $reference_hash)"
							hash_mismatch=true
						else
							echo "✅ $service has matching hash: $block_hash"
						fi
					fi
				else
					echo "❌ Failed to get hash for block $TARGET_BLOCK_POST_HF from $service"
					hash_mismatch=true
				fi
			done

			if [ "$hash_mismatch" = true ]; then
				echo "❌ Block hash verification failed for block $TARGET_BLOCK_POST_HF"
				echo "All hashes collected:"
				for hash_entry in "${block_hashes[@]}"; do
					echo "  $hash_entry"
				done
				return 1
			else
				echo "✅ All stateless sync services have the same hash for block $TARGET_BLOCK_POST_HF: $reference_hash"
			fi

			break
		fi

		sleep $SLEEP_INTERVAL
	done
}

# Test 3: Check milestone settlement latency without network latency
test_baseline_milestone_settlement_latency() {
	echo ""
	echo "=== Test 3: Checking baseline milestone settlement latency ==="

	if ! test_milestone_settlement_latency "baseline (no network latency)" "$LATENCY_CHECK_ITERATIONS" "$NORMAL_SETTLEMENT_LATENCY_SECONDS"; then
		return 1
	fi
}

# Test 4: Network latency test
test_network_latency_resilience() {
	echo ""
	echo "=== Test 4: Network latency resilience test ==="

	# Set up cleanup trap to ensure network latency is stopped
	cleanup_network_latency() {
		echo "Cleaning up network latency..."
		wait_for_pending_network_latency
	}
	trap cleanup_network_latency EXIT

	# Start network latency in background with explicit parameters
	if ! start_network_latency "$DELAY_EL" "$JITTER_EL" "$DELAY_CL" "$JITTER_CL" "$NETWORK_LATENCY_DURATION"; then
		echo "❌ Failed to start network latency, skipping network latency test"
		return 1
	fi

	# Wait a bit for network latency to take effect
	echo "Waiting 10 seconds for network latency to stabilize..."
	sleep 10

	# Test milestone settlement latency with network latency applied
	echo "Testing milestone settlement latency with network latency applied..."
	if ! test_milestone_settlement_latency "with network latency" "$LATENCY_CHECK_ITERATIONS" "$MAX_SETTLEMENT_LATENCY_SECONDS"; then
		echo "❌ Network latency test failed - settlement latency exceeded threshold"
		return 1
	fi

	# Wait for network latency to complete
	wait_for_pending_network_latency

	echo "✅ Network latency test passed - milestone settlement latency remained acceptable despite network delays"
}

# Test 5: Extreme network latency recovery test
test_extreme_network_latency_recovery() {
	echo ""
	echo "=== Test 5: Extreme network latency recovery test ==="

	# Get initial block numbers before applying extreme latency
	echo "Recording initial block numbers before extreme latency..."
	initial_max_block=$(get_max_block_from_services "${STATELESS_SYNC_VALIDATORS[@]}" "${STATELESS_RPC_SERVICES[@]}")
	echo "Initial max block: $initial_max_block"

	# Start extreme network latency
	echo "Applying extreme network latency (EL: ${EXTREME_DELAY_EL}ms±${EXTREME_JITTER_EL}ms, CL: ${EXTREME_DELAY_CL}ms±${EXTREME_JITTER_CL}ms)..."
	if ! start_network_latency "$EXTREME_DELAY_EL" "$EXTREME_JITTER_EL" "$EXTREME_DELAY_CL" "$EXTREME_JITTER_CL" "$EXTREME_LATENCY_DURATION"; then
		echo "❌ Failed to start extreme network latency, skipping extreme latency recovery test"
		return 1
	fi

	# Wait for network latency to complete
	wait_for_pending_network_latency

	# Test that nodes can recover and generate new blocks after extreme latency is removed
	echo "Testing recovery after extreme network latency removal..."
	if ! test_sync_recovery "after extreme network latency" 300 5; then
		echo "❌ Extreme network latency recovery test failed"
		return 1
	fi

	echo "✅ Extreme network latency recovery test passed - nodes successfully recovered and resumed block generation"
}


# Test 6: Block producer rotation test
test_block_producer_rotation() {
	echo ""
	echo "=== Test 6: Block producer rotation test ==="

	# Get stateless node 7 for reorg monitoring (l2-el-7-bor-heimdall-v2-validator)
	STATELESS_NODE_7="l2-el-7-bor-heimdall-v2-validator"
	echo "Monitoring stateless node 7: $STATELESS_NODE_7"

	# Check initial reorg count
	initial_reorg_count=$(get_reorg_count "$STATELESS_NODE_7")
	echo "Initial reorg count for $STATELESS_NODE_7: $initial_reorg_count"

	# Run the rotation script 3 times with 15 second intervals
	for rotation_round in {1..3}; do
		echo ""
		echo "--- Rotation round $rotation_round/3 ---"
		
		# Run the rotation script
		echo "Running block producer rotation script (15 seconds)..."
		"$SCRIPT_DIR/rotate_current_block_producer.sh"
		
		echo "Rotation script completed. Waiting 15 seconds before next round..."
		sleep 15
	done

	echo ""
	echo "All 3 rotation rounds completed. Analyzing results..."

	# Wait a bit for blocks to stabilize after rotations
	echo "Waiting 10 seconds for blocks to stabilize..."
	sleep 10

	# Check block author diversity in last 100 blocks
	echo ""
	echo "Checking block author diversity..."
	if ! check_block_author_diversity "$STATELESS_NODE_7" 100 2; then
		echo "❌ Block producer rotation test failed - insufficient author diversity"
		return 1
	fi

	# Check that stateless node 7 didn't have reorgs during rotation
	echo ""
	echo "Checking reorg count after rotation..."
	final_reorg_count=$(get_reorg_count "$STATELESS_NODE_7")
	echo "Final reorg count for $STATELESS_NODE_7: $final_reorg_count"

	if [[ "$initial_reorg_count" =~ ^[0-9]+$ ]] && [[ "$final_reorg_count" =~ ^[0-9]+$ ]]; then
		reorg_diff=$((final_reorg_count - initial_reorg_count))
		echo "Reorg count difference: $reorg_diff"
		
		if [ "$reorg_diff" -eq 0 ]; then
			echo "✅ No reorgs detected on stateless node 7 during block producer rotation"
		else
			echo "❌ Detected $reorg_diff reorgs on stateless node 7 during rotation (expected: 0)"
			return 1
		fi
	else
		echo "❌ Failed to parse reorg counts (initial: $initial_reorg_count, final: $final_reorg_count)"
		return 1
	fi

	echo "✅ Block producer rotation test passed - authors rotated successfully with no reorgs on stateless nodes"
}



# Test 7: Load test with polycli
test_polycli_load_test() {
	echo ""
	echo "=== Test 7: Load test with polycli ==="
	
	polycli_bin=$(which polycli)
	first_rpc_service="${STATELESS_RPC_SERVICES[0]}"
	first_rpc_url=$(get_rpc_url "$first_rpc_service")
	echo "Using RPC service: $first_rpc_service -> $first_rpc_url"

	# Check initial nonce
	test_account="0x97538585a02A3f1B1297EB9979cE1b34ff953f1E"
	num_txs=1000
	initial_nonce=$(cast nonce "$test_account" --rpc-url "$first_rpc_url")
	echo "Initial nonce for account $test_account: $initial_nonce"

	# Run load test
	$polycli_bin loadtest --rpc-url "$first_rpc_url" --private-key "0x2a4ae8c4c250917781d38d95dafbb0abe87ae2c9aea02ed7c7524685358e49c2" --verbosity 500 --requests $num_txs --rate-limit 500  --mode uniswapv3 --gas-price 35000000000

	# Check final nonce after load test
	final_nonce=$(cast nonce "$test_account" --rpc-url "$first_rpc_url")
	echo "Final nonce for account $test_account: $final_nonce"

	# Calculate nonce difference to verify transactions processed
	if [[ "$initial_nonce" =~ ^[0-9]+$ ]] && [[ "$final_nonce" =~ ^[0-9]+$ ]]; then
		nonce_diff=$((final_nonce - initial_nonce))
		echo "Transactions processed: $nonce_diff (nonce increased from $initial_nonce to $final_nonce)"
		
		if [ "$nonce_diff" -gt $num_txs ]; then
			echo "✅ Load test successful - processed $nonce_diff transactions (> $num_txs)"
		else
			echo "❌ Load test failed - only processed $nonce_diff transactions (< $num_txs required)"
			return 1
		fi
	else
		echo "❌ Load test failed - unable to parse nonce values (initial: $initial_nonce, final: $final_nonce)"
		return 1
	fi
}

# Run all tests
test_block_hash_consensus || exit 1
test_post_veblop_hf_behavior || exit 1
test_baseline_milestone_settlement_latency || exit 1
test_network_latency_resilience || exit 1
test_extreme_network_latency_recovery || exit 1
test_block_producer_rotation || exit 1
test_polycli_load_test || exit 1

echo ""
echo "🎉 All stateless sync tests passed successfully!"
