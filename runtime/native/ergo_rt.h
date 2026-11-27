#ifndef ERGO_RT_H
#define ERGO_RT_H

#include <setjmp.h>
#include <stdalign.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

_Static_assert(sizeof(void *) == 8, "Ergo native runtime requires 64-bit pointers");
_Static_assert(sizeof(size_t) == 8, "Ergo native runtime requires 64-bit size_t");
_Static_assert(sizeof(bool) == 1 && _Alignof(bool) == 1, "bool must be 1 byte");
_Static_assert(_Alignof(max_align_t) <= 16, "max_align_t must be <= 16 bytes");

/* Flat C-ABI value contracts */

typedef struct ErgoStr {
    const uint8_t *ptr;
    int64_t len;
} ErgoStr;

_Static_assert(sizeof(ErgoStr) == 16, "ErgoStr must be 16 bytes");
_Static_assert(_Alignof(ErgoStr) == 8, "ErgoStr must be 8-byte aligned");
_Static_assert(offsetof(ErgoStr, ptr) == 0, "ErgoStr.ptr offset");
_Static_assert(offsetof(ErgoStr, len) == 8, "ErgoStr.len offset");

typedef struct ErgoBytes {
    uint8_t *ptr;
    int64_t len;
    int64_t cap;
} ErgoBytes;

_Static_assert(sizeof(ErgoBytes) == 24, "ErgoBytes must be 24 bytes");
_Static_assert(_Alignof(ErgoBytes) == 8, "ErgoBytes must be 8-byte aligned");
_Static_assert(offsetof(ErgoBytes, ptr) == 0, "ErgoBytes.ptr offset");
_Static_assert(offsetof(ErgoBytes, len) == 8, "ErgoBytes.len offset");
_Static_assert(offsetof(ErgoBytes, cap) == 16, "ErgoBytes.cap offset");

typedef struct ErgoSlice {
    void *ptr;
    int64_t len;
    int64_t cap;
} ErgoSlice;

_Static_assert(sizeof(ErgoSlice) == 24, "ErgoSlice must be 24 bytes");
_Static_assert(_Alignof(ErgoSlice) == 8, "ErgoSlice must be 8-byte aligned");
_Static_assert(offsetof(ErgoSlice, ptr) == 0, "ErgoSlice.ptr offset");
_Static_assert(offsetof(ErgoSlice, len) == 8, "ErgoSlice.len offset");
_Static_assert(offsetof(ErgoSlice, cap) == 16, "ErgoSlice.cap offset");

typedef struct ErgoError {
    uint32_t tag;
    uint32_t _pad;
    ErgoStr msg;
} ErgoError;

_Static_assert(sizeof(ErgoError) == 24, "ErgoError must be 24 bytes");
_Static_assert(_Alignof(ErgoError) == 8, "ErgoError must be 8-byte aligned");
_Static_assert(offsetof(ErgoError, tag) == 0, "ErgoError.tag offset");
_Static_assert(offsetof(ErgoError, _pad) == 4, "ErgoError._pad offset");
_Static_assert(offsetof(ErgoError, msg) == 8, "ErgoError.msg offset");

static inline ErgoStr ergo_str_make(const char *ptr, int64_t len) {
    ErgoStr s;
    s.ptr = (const uint8_t *)ptr;
    s.len = len;
    return s;
}

#define ERGO_STR_LIT(lit) ergo_str_make((lit), (int64_t)(sizeof(lit) - 1))

/* Perceus reference-counting header and unique-reuse */

enum {
    ERGO_RC_FLAG_SHARED = 1u << 0,
    ERGO_RC_FLAG_STATIC = 1u << 1,
    ERGO_RC_FLAG_CAP_SHIFTED = 1u << 2,
    ERGO_RC_FLAGS_MASK = 0x3Fu,
    ERGO_RC_USER_FLAGS_MASK = ERGO_RC_FLAG_SHARED | ERGO_RC_FLAG_STATIC,
    ERGO_RC_CAP_SHIFT = 6u,
    ERGO_RC_CAP_MAX_EXACT = 0x03FFFFFFu,
    ERGO_RC_CAP_PAGE_SHIFT = 12u
};

typedef struct ErgoRCHeader {
    _Atomic uint32_t rc;
    uint32_t flags;
    uint32_t payload_size;
    uint32_t type_tag;
} ErgoRCHeader;

_Static_assert(sizeof(ErgoRCHeader) == 16, "ErgoRCHeader must be 16 bytes");
_Static_assert(_Alignof(ErgoRCHeader) == 4, "ErgoRCHeader must be 4-byte aligned");
_Static_assert(sizeof(ErgoRCHeader) >= _Alignof(max_align_t), "ErgoRCHeader must preserve max_align_t");
_Static_assert((sizeof(ErgoRCHeader) % 16u) == 0, "ErgoRCHeader size must be multiple of 16");
_Static_assert(offsetof(ErgoRCHeader, rc) == 0, "ErgoRCHeader.rc offset");
_Static_assert(offsetof(ErgoRCHeader, flags) == 4, "ErgoRCHeader.flags offset");
_Static_assert(offsetof(ErgoRCHeader, payload_size) == 8, "ErgoRCHeader.payload_size offset");
_Static_assert(offsetof(ErgoRCHeader, type_tag) == 12, "ErgoRCHeader.type_tag offset");

#define ERGO_RC_STATIC_HEADER(sz, tag) { 1u, ERGO_RC_FLAG_STATIC, (uint32_t)(sz), (uint32_t)(tag) }

typedef void (*ErgoDropFn)(void *payload);

void *ergo_rc_alloc(uint32_t payload_size, uint32_t type_tag, uint32_t flags);
void *ergo_rc_dup(void *p);
void ergo_rc_drop(void *p, ErgoDropFn drop_fn);
bool ergo_rc_is_unique(const void *p);
uint32_t ergo_rc_count(const void *p);
void *ergo_rc_reuse_or_alloc(void *old_ptr, uint32_t new_size, uint32_t new_type_tag, ErgoDropFn drop_old);

uint32_t ergo_rc_type_tag(const void *p);
uint32_t ergo_rc_payload_size(const void *p);
uint32_t ergo_rc_capacity(const void *p);
uint32_t ergo_rc_flags(const void *p);

/* Scoped O(1) chunked bump arena */

typedef struct ErgoArena ErgoArena;
typedef struct ErgoArenaChunk ErgoArenaChunk;

typedef struct ErgoArenaMark {
    ErgoArenaChunk *chunk;
    size_t offset;
    size_t total_used;
} ErgoArenaMark;

_Static_assert(sizeof(ErgoArenaMark) == 24, "ErgoArenaMark must be 24 bytes");
_Static_assert(_Alignof(ErgoArenaMark) == 8, "ErgoArenaMark must be 8-byte aligned");
_Static_assert(offsetof(ErgoArenaMark, chunk) == 0, "ErgoArenaMark.chunk offset");
_Static_assert(offsetof(ErgoArenaMark, offset) == 8, "ErgoArenaMark.offset offset");
_Static_assert(offsetof(ErgoArenaMark, total_used) == 16, "ErgoArenaMark.total_used offset");

ErgoArena *ergo_arena_create(size_t initial_cap);
void *ergo_arena_alloc(ErgoArena *a, size_t size, size_t align);
ErgoArenaMark ergo_arena_mark(const ErgoArena *a);
void ergo_arena_reset(ErgoArena *a, ErgoArenaMark mark);
void ergo_arena_destroy(ErgoArena *a);
size_t ergo_arena_used(const ErgoArena *a);

/* BEAM/OTP per-actor bounded heap and crash isolation */

typedef enum ErgoStatus {
    ERGO_STATUS_OK = 0,
    ERGO_STATUS_OVER_BYTES = 1,
    ERGO_STATUS_OVER_STEPS = 2,
    ERGO_STATUS_INVALID_ARG = 3,
    ERGO_STATUS_OOM = 4
} ErgoStatus;

typedef enum ErgoDieCode {
    ERGO_DIE_PANIC = 1,
    ERGO_DIE_OVER_BYTES = 2,
    ERGO_DIE_OVER_STEPS = 3
} ErgoDieCode;

typedef struct ErgoActorSlab ErgoActorSlab;

typedef struct ErgoActorHeap {
    ErgoActorSlab *slabs;
    ErgoActorSlab *current;
    size_t limit_bytes;
    size_t used_bytes;
    size_t peak_bytes;
} ErgoActorHeap;

_Static_assert(sizeof(ErgoActorHeap) == 40, "ErgoActorHeap must be 40 bytes");
_Static_assert(_Alignof(ErgoActorHeap) == 8, "ErgoActorHeap must be 8-byte aligned");
_Static_assert(offsetof(ErgoActorHeap, slabs) == 0, "ErgoActorHeap.slabs offset");
_Static_assert(offsetof(ErgoActorHeap, current) == 8, "ErgoActorHeap.current offset");
_Static_assert(offsetof(ErgoActorHeap, limit_bytes) == 16, "ErgoActorHeap.limit_bytes offset");
_Static_assert(offsetof(ErgoActorHeap, used_bytes) == 24, "ErgoActorHeap.used_bytes offset");
_Static_assert(offsetof(ErgoActorHeap, peak_bytes) == 32, "ErgoActorHeap.peak_bytes offset");

typedef struct ErgoActorLimits {
    size_t limit_bytes;
    uint64_t limit_steps;
} ErgoActorLimits;

_Static_assert(sizeof(ErgoActorLimits) == 16, "ErgoActorLimits must be 16 bytes");
_Static_assert(_Alignof(ErgoActorLimits) == 8, "ErgoActorLimits must be 8-byte aligned");
_Static_assert(offsetof(ErgoActorLimits, limit_bytes) == 0, "ErgoActorLimits.limit_bytes offset");
_Static_assert(offsetof(ErgoActorLimits, limit_steps) == 8, "ErgoActorLimits.limit_steps offset");

typedef struct ErgoCrashReport {
    bool panicked;
    uint32_t code;
    const char *msg;
    size_t heap_peak_bytes;
} ErgoCrashReport;

_Static_assert(sizeof(ErgoCrashReport) == 24, "ErgoCrashReport must be 24 bytes");
_Static_assert(_Alignof(ErgoCrashReport) == 8, "ErgoCrashReport must be 8-byte aligned");
_Static_assert(offsetof(ErgoCrashReport, panicked) == 0, "ErgoCrashReport.panicked offset");
_Static_assert(offsetof(ErgoCrashReport, code) == 4, "ErgoCrashReport.code offset");
_Static_assert(offsetof(ErgoCrashReport, msg) == 8, "ErgoCrashReport.msg offset");
_Static_assert(offsetof(ErgoCrashReport, heap_peak_bytes) == 16, "ErgoCrashReport.heap_peak_bytes offset");

typedef void (*ErgoActorFn)(void *arg);

typedef struct ErgoActorContext {
    ErgoActorHeap heap;
    uint64_t limit_steps;
    uint64_t steps_used;
    char *msg_buf;
    size_t msg_buf_cap;
    struct ErgoActorContext *prev;
    jmp_buf jmp;
    uint32_t die_code;
    bool panicked;
    bool active;
} ErgoActorContext;

void ergo_actor_heap_init(ErgoActorHeap *h, size_t limit_bytes);
void *ergo_actor_heap_alloc(ErgoActorHeap *h, size_t size, size_t align, ErgoStatus *out_status);
void ergo_actor_heap_destroy(ErgoActorHeap *h);

ErgoCrashReport ergo_actor_run_isolated(
    ErgoActorFn fn,
    void *arg,
    ErgoActorLimits limits,
    char *msg_buf,
    size_t msg_buf_cap
);

void ergo_actor_step(uint32_t cost);
void *ergo_actor_alloc(size_t size, size_t align);
ErgoActorHeap *ergo_actor_current_heap(void);
uint64_t ergo_actor_steps_used(void);

_Noreturn void ergo_die(uint32_t code, ErgoStr msg);

/* Thread-local current isolated actor context (internal linkage hook between actor and die) */
ErgoActorContext *ergo_actor_tls_get(void);

#ifdef __cplusplus
}
#endif

#endif /* ERGO_RT_H */
