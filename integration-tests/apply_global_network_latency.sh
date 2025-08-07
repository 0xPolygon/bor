#!/bin/bash
set -e

# Source utility functions and configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/kurtosis_test_utils.sh"

echo "Network latency configuration:"
echo "- Duration: $NETWORK_LATENCY_DURATION"
echo "- Interface: $INTERFACE"
echo "- L2-EL delay: ${DELAY_EL}ms, jitter: ${JITTER_EL}ms"
echo "- L2-CL delay: ${DELAY_CL}ms, jitter: ${JITTER_CL}ms"

# Function to run Pumba netem for containers with a given prefix and delay parameters
run_pumba() {
    local prefix="$1"
    local delay="$2"
    local jitter="$3"
    TARGET_FLAGS=()
    echo "Finding IPs for containers with prefix '${prefix}'..."
    for name in $(docker ps --format "{{.Names}}" | grep "^${prefix}"); do
        ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$name")
        TARGET_FLAGS+=(--target "${ip}/32")
    done
    echo "Running Pumba netem for '${prefix}' with delay ${delay}ms and jitter ${jitter}ms on targets: ${TARGET_FLAGS[*]}"
    docker run -i --rm \
      -v /var/run/docker.sock:/var/run/docker.sock \
      gaiaadm/pumba:0.10.1 netem "${TARGET_FLAGS[@]}" --tc-image gaiadocker/iproute2 --duration "$NETWORK_LATENCY_DURATION" --interface "$INTERFACE" delay --time "$delay" --jitter "$jitter" "re2:^${prefix}"
}

# Trap SIGINT (Ctrl+C) to kill background jobs and exit gracefully
trap "echo 'Ctrl+C pressed. Terminating all Pumba processes...'; kill $(jobs -p) 2>/dev/null; exit 0" SIGINT

# Run Pumba for each group concurrently with their respective parameters
run_pumba "l2-el" "$DELAY_EL" "$JITTER_EL" &
run_pumba "l2-cl" "$DELAY_CL" "$JITTER_CL" &

# Wait for both background jobs to finish
wait
