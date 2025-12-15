// SPDX-License-Identifier: Apache-2.0
// Minimal mirror of evmc/bor_trace.h for cgo build

#pragma once

#include <evmc/evmc.h>
#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct evmone_trace_step {
    uint32_t pc;
    uint8_t opcode;
    uint8_t reserved[3];
    int64_t gas;
    int32_t depth;
    int32_t reserved2;
} evmone_trace_step;

typedef struct evmone_trace_result {
    size_t count;
    evmc_release_result_fn previous_release;
    struct evmone_trace_step steps[1];
} evmone_trace_result;

#ifdef __cplusplus
}
#endif
