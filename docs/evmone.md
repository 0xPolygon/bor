## Using evmone with Bor

`evmone` is an [EVMC](https://github.com/ethereum/evmc) compatible EVM implementation.  
Bor can load any EVMC engine via the `vm.evm` flag or config block.

### 1. Build the shared library

```
make evmone
```

The helper script defaults to the local checkout at `/Users/raneet/Desktop/evmone`.  
Override the paths if your clone lives elsewhere:

```
EVMONE_SRC=/path/to/evmone \
BOR_EVMONE_OUTPUT=/custom/output/dir \
EVMONE_BUILD_DIR=/tmp/evmone-build \
make evmone
```

Artifacts are copied to `build/evmone/libevmone.dylib` on macOS (`.so` on Linux).  
The script performs a Release build with shared libraries only; enable tests or
benchmarks by extending `EVMONE_CMAKE_FLAGS`.

### 2. Run bor with evmone

Pass the shared library path via CLI:

```
build/bin/geth --vm.evm=$(pwd)/build/evmone/libevmone.dylib [...]
```

or inside the server config:

```toml
[vm]
evm = "/Users/raneet/Desktop/bor-temp/bor/build/evmone/libevmone.dylib"
```

The node log should include:

```
EVMC VM loaded  name=evmone  version=0.x.y  path=...
```

Use the optional `,key=value` suffix (for example `--vm.evm=.../libevmone.dylib,mode=advanced`)
to toggle evmone options.

### 3. Troubleshooting

- Ensure `cmake` and a C++20 toolchain are available (`brew install cmake ninja llvm` on macOS).
- If the script cannot locate the source checkout, set `EVMONE_SRC` explicitly.
- When cross-compiling or packaging, copy the produced library next to the bor binary
  and point the `vm.evm` flag/config to the new location.

