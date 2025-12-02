#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
EVMONE_SRC="${EVMONE_SRC:-/Users/raneet/Desktop/evmone}"
EVMONE_BUILD_DIR="${EVMONE_BUILD_DIR:-${EVMONE_SRC}/build-bor}"
EVMONE_OUTPUT_DIR="${BOR_EVMONE_OUTPUT:-${ROOT_DIR}/build/evmone}"
EVMONE_CMAKE_FLAGS="${EVMONE_CMAKE_FLAGS:--DEVMONE_BUILD_SHARED=ON -DEVMONE_BUILD_TESTS=OFF -DEVMONE_BUILD_BENCHMARKS=OFF -DEVMONE_BUILD_TOOLS=OFF}"

if [[ ! -d "${EVMONE_SRC}" ]]; then
  echo "error: EVMONE source directory not found (${EVMONE_SRC})." >&2
  echo "Set EVMONE_SRC to the local evmone clone (default /Users/raneet/Desktop/evmone)." >&2
  exit 1
fi

mkdir -p "${EVMONE_BUILD_DIR}"
echo "Configuring evmone build in ${EVMONE_BUILD_DIR} ..."
cmake -S "${EVMONE_SRC}" -B "${EVMONE_BUILD_DIR}" ${EVMONE_CMAKE_FLAGS}

if [[ -z "${EVMONE_BUILD_JOBS:-}" ]]; then
  if command -v sysctl >/dev/null 2>&1; then
    EVMONE_BUILD_JOBS="$(sysctl -n hw.logicalcpu 2>/dev/null || echo 4)"
  else
    EVMONE_BUILD_JOBS="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)"
  fi
fi

echo "Building evmone (jobs=${EVMONE_BUILD_JOBS}) ..."
cmake --build "${EVMONE_BUILD_DIR}" --target evmone --config Release --parallel "${EVMONE_BUILD_JOBS}"

LIB_NAME="libevmone.dylib"
if [[ ! -f "${EVMONE_BUILD_DIR}/lib/${LIB_NAME}" ]]; then
  LIB_NAME="libevmone.so"
fi

LIB_PATH="${EVMONE_BUILD_DIR}/lib/${LIB_NAME}"
if [[ ! -f "${LIB_PATH}" ]]; then
  echo "error: could not find built evmone library under ${EVMONE_BUILD_DIR}/lib." >&2
  exit 1
fi

mkdir -p "${EVMONE_OUTPUT_DIR}"
cp "${LIB_PATH}" "${EVMONE_OUTPUT_DIR}/"
echo "evmone shared library copied to ${EVMONE_OUTPUT_DIR}/${LIB_NAME}"
echo "Use --vm.evm=${EVMONE_OUTPUT_DIR}/${LIB_NAME} when starting bor."

