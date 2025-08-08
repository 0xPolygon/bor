#!/bin/bash

# Kurtosis smoke tests for Bor.

set -e

ENCLAVE_NAME=${ENCLAVE_NAME:-"kurtosis-e2e"}
HEIMDALL_SERVICE_NAME=${HEIMDALL_SERVICE_NAME:-"l2-cl-1-heimdall-v2-bor-validator"}

get_http_url() {
	local service_name=$1
	kurtosis port print $ENCLAVE_NAME $service_name http 2>/dev/null || echo ""
}

test_checkpoint() {
	echo "Starting checkpoint test…"

	local http_url=$(get_http_url $HEIMDALL_SERVICE_NAME)

	if [ -z "$http_url" ]; then
		echo "❌ Failed to get HTTP URL for service: $HEIMDALL_SERVICE_NAME"
		echo "Available services in enclave:"
		kurtosis enclave inspect $ENCLAVE_NAME 2>/dev/null || echo "Could not inspect enclave"
		return 1
	fi

	echo "Using Heimdall HTTP URL: $http_url"

	local max_attempts=100
	local attempt=0

	while [ $attempt -lt $max_attempts ]; do
		checkpointID=$(curl -s "${http_url}/checkpoints/latest" | jq -r '.checkpoint.id' 2>/dev/null || echo "null")

		if [ "$checkpointID" != "null" ] && [ "$checkpointID" != "" ]; then
			echo "✅ Checkpoint created! ID: $checkpointID"
			return 0
		else
			echo "Current checkpoint: none (polling… attempt $((attempt + 1))/$max_attempts)"
			sleep 5
			((attempt++))
		fi
	done

	echo "❌ Timeout: No checkpoint created after $((max_attempts * 5)) seconds"
	return 1
}

test_milestone() {
	echo "Starting milestones test…"

	local http_url=$(get_http_url $HEIMDALL_SERVICE_NAME)

	if [ -z "$http_url" ]; then
		echo "❌ Failed to get HTTP URL for service: $HEIMDALL_SERVICE_NAME"
		return 1
	fi

	echo "Using Heimdall HTTP URL: $http_url"

	local initial_count=$(curl -s "${http_url}/milestones/count" | jq -r '.count' 2>/dev/null || echo "0")

	if [ "$initial_count" = "null" ] || [ "$initial_count" = "" ]; then
		initial_count=0
	fi

	echo "Initial milestone count: $initial_count"
	local target_count=$((initial_count + 10))
	echo "Target milestone count: $target_count"

	local max_attempts=20
	local attempt=0

	while [ $attempt -lt $max_attempts ]; do
		current_count=$(curl -s "${http_url}/milestones/count" | jq -r '.count' 2>/dev/null || echo "0")

		if [ "$current_count" = "null" ] || [ "$current_count" = "" ]; then
			current_count=0
		fi

		if [ "$current_count" -ge "$target_count" ]; then
			echo "✅ Milestone target reached! Current count: $current_count (increased by $((current_count - initial_count)))"
			return 0
		else
			echo "Current milestone count: $current_count (need $((target_count - current_count)) more, polling… attempt $((attempt + 1))/$max_attempts)"
			sleep 5
			((attempt++))
		fi
	done

	echo "❌ Timeout: Only $((current_count - initial_count)) milestones created in 100 seconds (expected 10)"
	return 1
}

main() {
	echo "🚀 Starting Kurtosis Bor Smoke Test"
	echo "Enclave: $ENCLAVE_NAME"
	echo "Service: $HEIMDALL_SERVICE_NAME"
	echo ""

	if test_checkpoint; then
		echo ""
		echo "🎉 Checkpoint test passed — Heimdall checkpoint looks good!"
		echo ""

		if test_milestone; then
			echo ""
			echo "🎉 Milestone test passed — Heimdall milestones are being created!"
			echo "✅ All Kurtosis smoke tests completed successfully!"
			exit 0
		else
			echo ""
			echo "❌ Milestone test failed"
			exit 1
		fi
	else
		echo ""
		echo "❌ Checkpoint test failed"
		exit 1
	fi
}

main "$@"
