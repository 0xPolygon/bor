#!/bin/bash
# Pipeline-specific assertions for the pipeline-enabled kurtosis leg
# (POS-3697). Runs after the pos-workflows stateless suite has passed, so
# lockstep and hash consensus for participants 1-9 are already proven; this
# script checks the pipeline metrics per node role and brings the released-
# image baseline (participant 10, outside the pos-workflows service lists)
# into the hash-consensus check.
set -euo pipefail

ENCLAVE_NAME=${ENCLAVE_NAME:-"kurtosis-pipeline-e2e"}
TARGET_BLOCK=${TARGET_BLOCK:-127}

# Nodes by role (indices fixed by the args file).
PIPELINED_WITNESS_VALIDATORS=(l2-el-1-bor-heimdall-v2-validator l2-el-2-bor-heimdall-v2-validator l2-el-3-bor-heimdall-v2-validator)
STATELESS_VALIDATORS=(l2-el-4-bor-heimdall-v2-validator l2-el-5-bor-heimdall-v2-validator)
PIPELINED_PLAIN_RPC=l2-el-6-bor-heimdall-v2-rpc
PIPELINED_WITNESS_RPC=l2-el-7-bor-heimdall-v2-rpc
STATELESS_RPC=l2-el-8-bor-heimdall-v2-rpc
BASELINE_VALIDATOR=l2-el-10-bor-heimdall-v2-validator
REFERENCE_NODE=l2-el-1-bor-heimdall-v2-validator

failures=0
fail() {
  echo "❌ $1"
  failures=$((failures + 1))
}

metric() {
  local service=$1 name=$2
  local url
  url=$(kurtosis port print "$ENCLAVE_NAME" "$service" metrics)
  curl -s -m 10 "$url/debug/metrics/prometheus" | awk -v m="$name" '$1 == m {print $2; found=1} END {if (!found) print 0}'
}

block_hash_at() {
  local service=$1 number=$2
  local url
  url=$(kurtosis port print "$ENCLAVE_NAME" "$service" rpc)
  cast block "$number" --rpc-url "$url" --json 2>/dev/null | jq -r .hash
}

echo "=== Pipeline metrics per node role ==="
for svc in "${PIPELINED_WITNESS_VALIDATORS[@]}"; do
  src=$(metric "$svc" chain_imports_pipelined_src_count)
  mismatch=$(metric "$svc" chain_imports_pipelined_root_mismatch)
  witness=$(metric "$svc" chain_witness_size_bytes_count)
  echo "$svc: src=$src mismatch=$mismatch witness=$witness"
  [ "${src%.*}" -gt 0 ] || fail "$svc: pipeline not active (src=$src)"
  [ "${mismatch%.*}" -eq 0 ] || fail "$svc: root mismatch detected ($mismatch)"
  [ "${witness%.*}" -gt 0 ] || fail "$svc: no witnesses produced"
done

for svc in "${STATELESS_VALIDATORS[@]}" "$STATELESS_RPC"; do
  src=$(metric "$svc" chain_imports_pipelined_src_count)
  echo "$svc: src=$src (stateless — pipeline must self-gate off)"
  [ "${src%.*}" -eq 0 ] || fail "$svc: pipeline ran on a stateless-sync node (src=$src)"
done

src=$(metric "$PIPELINED_PLAIN_RPC" chain_imports_pipelined_src_count)
mismatch=$(metric "$PIPELINED_PLAIN_RPC" chain_imports_pipelined_root_mismatch)
witness=$(metric "$PIPELINED_PLAIN_RPC" chain_witness_size_bytes_count)
echo "$PIPELINED_PLAIN_RPC: src=$src mismatch=$mismatch witness=$witness"
[ "${src%.*}" -gt 0 ] || fail "$PIPELINED_PLAIN_RPC: pipeline not active"
[ "${mismatch%.*}" -eq 0 ] || fail "$PIPELINED_PLAIN_RPC: root mismatch detected"
[ "${witness%.*}" -eq 0 ] || fail "$PIPELINED_PLAIN_RPC: produced witnesses with witness production off"

# Witness provenance: on a non-mining full-sync witness producer, every
# witness must come from the pipelined SRC completion path — the counters
# track each other 1:1 (small tolerance for the in-flight block at sample
# time). This is the assertion that proves stateless nodes are consuming
# pipelined-SRC-produced witnesses rather than inline ones.
src=$(metric "$PIPELINED_WITNESS_RPC" chain_imports_pipelined_src_count)
mismatch=$(metric "$PIPELINED_WITNESS_RPC" chain_imports_pipelined_root_mismatch)
witness=$(metric "$PIPELINED_WITNESS_RPC" chain_witness_size_bytes_count)
echo "$PIPELINED_WITNESS_RPC: src=$src mismatch=$mismatch witness=$witness"
[ "${mismatch%.*}" -eq 0 ] || fail "$PIPELINED_WITNESS_RPC: root mismatch detected"
diff=$((${witness%.*} - ${src%.*}))
[ "${diff#-}" -le 2 ] || fail "$PIPELINED_WITNESS_RPC: witness/src divergence ($witness vs $src) — witnesses not coming from the pipelined SRC path"
[ "${src%.*}" -gt 0 ] || fail "$PIPELINED_WITNESS_RPC: pipeline not active"

echo "=== Released-image baseline consensus check ==="
ref_hash=$(block_hash_at "$REFERENCE_NODE" "$TARGET_BLOCK")
base_hash=$(block_hash_at "$BASELINE_VALIDATOR" "$TARGET_BLOCK")
echo "block $TARGET_BLOCK: reference=$ref_hash baseline=$base_hash"
if [ -z "$base_hash" ] || [ "$base_hash" = "null" ]; then
  fail "$BASELINE_VALIDATOR: could not fetch block $TARGET_BLOCK (baseline lagging or down)"
elif [ "$base_hash" != "$ref_hash" ]; then
  fail "$BASELINE_VALIDATOR: hash mismatch vs pipelined reference at block $TARGET_BLOCK"
fi

if [ "$failures" -gt 0 ]; then
  echo "❌ $failures pipeline check(s) failed"
  exit 1
fi
echo "✅ all pipeline checks passed"
