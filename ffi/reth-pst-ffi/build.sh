#!/bin/bash
# Build script for reth-pst-ffi
# Requires Rust nightly toolchain (edition 2024)

set -e

echo "Checking Rust toolchain..."
if ! rustup toolchain list | grep -q nightly; then
    echo "Installing Rust nightly..."
    rustup toolchain install nightly
fi

echo "Setting nightly as default for this directory..."
cd "$(dirname "$0")"
rustup override set nightly

echo "Building reth-pst-ffi..."
cargo build --release

echo "Build complete! Library should be at: target/release/libreth_pst_ffi.dylib (macOS) or .so (Linux)"

