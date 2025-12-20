#include "pevm_ffi.h"
#include "_cgo_export.h"

PevmStorageVTable pevm_make_vtable(void) {
	PevmStorageVTable t;
	t.basic = (int (*)(void*, const PevmAddress*, uint8_t*, PevmU256*, uint64_t*, const char**))go_pevm_basic;
	t.code_hash = (int (*)(void*, const PevmAddress*, uint8_t*, PevmB256*, const char**))go_pevm_code_hash;
	t.code_by_hash = (int (*)(void*, const PevmB256*, const PevmAddress*, PevmBytes*, const char**))go_pevm_code_by_hash;
	t.free_bytes = (void (*)(void*, const uint8_t*, uintptr_t))go_pevm_free_bytes;
	t.has_storage = (int (*)(void*, const PevmAddress*, uint8_t*, const char**))go_pevm_has_storage;
	t.storage = (int (*)(void*, const PevmAddress*, const PevmU256*, PevmU256*, const char**))go_pevm_storage;
	t.block_hash = (int (*)(void*, uint64_t, PevmB256*, const char**))go_pevm_block_hash;
	return t;
}

