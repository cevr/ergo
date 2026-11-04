/* fixture.h: a small C library for the tests of internal/bindc. It has one
 * case of every binding class: an ABI-version function and macro, a status
 * type with wrong-thread and stale-handle codes, a thread-affine opaque
 * context with create and destroy, node handles passed by const pointer,
 * an options struct with struct_size and abi_version, a struct by value
 * with a fixed array, an input buffer pair, an output triple, an
 * out-parameter, a const struct whose struct holds a pointer (raw-only)
 * and callbacks: two call-scoped ones keyed by a void * token (one with a
 * borrowed buffer and an int64_t result, the OpenTUI writer's shape), one
 * registered one keyed by the fields of fx_node with an out array (the
 * OpenTUI measure's shape), the C functions that call them, invocation
 * counters, the calling thread's id, and four callbacks that stay raw (a
 * float result, a struct by value, seven integer parameters, no contract
 * entry).
 *
 * Build: cc -std=c11 -shared -fPIC -o libfixture.so fixture.c
 * -DFIXTURE_ABI=3 builds a library whose ABI version is not this header's.
 * -DFIXTURE_OMIT=<name> leaves the function <name> out of the library. */
#ifndef FIXTURE_H
#define FIXTURE_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

#define FX_ABI_VERSION UINT32_C(2)

typedef int32_t fx_status;
#define FX_OK INT32_C(0)
#define FX_INVALID_ARGUMENT (-INT32_C(1))
#define FX_WRONG_THREAD (-INT32_C(2))
#define FX_STALE_HANDLE (-INT32_C(3))
#define FX_UNSUPPORTED_VERSION (-INT32_C(4))
#define FX_OUT_OF_MEMORY (-INT32_C(5))

/* Limits and flags: the constant forms of the subset. */
#define FX_NAME_MAX UINT32_C(16)
#define FX_NODE_MAX (FX_NAME_MAX * 4)
#define FX_FLAG_VISIBLE (UINT32_C(1) << 0)
#define FX_FLAG_FOCUSED (UINT32_C(1) << 1)
#define FX_FLAGS_ALL (FX_FLAG_VISIBLE | FX_FLAG_FOCUSED)
#define FX_NO_SLOT UINT32_C(0xFFFFFFFF)
#define FX_TICKET_NONE UINT64_C(0xFFFFFFFFFFFFFFFF)
#define FX_DEPTH_MIN (-INT64_C(9223372036854775807) - 1)
#define FX_SMALL INT8_C(127)
#define FX_WIDE UINT16_C(65535)
#define FX_OFFSET (FX_NAME_MAX - 32)
#define FX_HEX 0x7fffffff
#define FX_HEX_UNSIGNED 0x80000000
#define FX_LARGE 3000000000
#define FX_ALIAS FX_STALE_HANDLE

typedef uint64_t fx_ticket;
typedef bool fx_flag;
typedef double fx_seconds;

/* The context: only its creating OS thread may call its functions. */
typedef struct fx_context fx_context;

/* A node handle: a destroyed node's handle is stale. */
typedef struct fx_node {
    uint64_t context_id;
    uint32_t slot;
    uint32_t generation;
} fx_node;

/* The C side checks struct_size and abi_version. */
typedef struct fx_context_options {
    uint32_t struct_size;
    uint32_t abi_version;
    uint32_t node_capacity;
    bool strict;
} fx_context_options;

typedef struct fx_point {
    float x;
    float y;
} fx_point;

/* A struct by value with fixed arrays, a nested struct and padding. */
typedef struct fx_box {
    uint32_t struct_size;
    uint32_t abi_version;
    uint8_t kind;
    double origin[2];
    fx_point corner;
    uint16_t color[4];
    int8_t layer;
    int16_t depth;
    int64_t z;
    fx_flag visible;
    fx_seconds age;
    uint8_t name[FX_NAME_MAX];
} fx_box;

/* A struct that holds a pointer: Go memory with Go pointers must not reach
 * C, so a function that takes it stays raw-only. */
typedef struct fx_text {
    const uint8_t *bytes;
    uint32_t byte_count;
} fx_text;

/* An untagged struct. */
typedef struct {
    fx_node node;
    fx_ticket ticket;
} fx_visit;

/* Called once per live node; user_data is the caller's. */
typedef void (*fx_visit_callback)(void *user_data, const fx_visit *visit);

/* Call scope: C calls it only during the call that passes it (the OpenTUI
 * writer's shape). It returns the bytes it took, 0 for backpressure, or -1
 * for failure; data is valid only during the call. */
typedef int64_t (*fx_write_callback)(void *user_data, const uint8_t *data, uint32_t data_bytes);

/* Registered: C keeps it until a replacement, a clear (NULL), the
 * destruction of its node, or the destruction of its context (the OpenTUI
 * measure's shape). out_size is two floats (width, height), NaN before the
 * call. */
typedef void (*fx_measure_callback)(uint64_t context_id, uint32_t slot, uint32_t generation,
    float width, uint32_t width_mode, float height, uint32_t height_mode, float *out_size);

/* Shapes that stay raw: a float result, a struct by value, seven integer
 * parameters (one on the stack on amd64), and an admitted shape that the
 * contract does not name. */
typedef float (*fx_float_callback)(void *user_data);
typedef void (*fx_point_callback)(void *user_data, fx_point point);
typedef void (*fx_wide_callback)(void *user_data, uint64_t a, uint64_t b, uint64_t c, uint64_t d,
    uint64_t e, uint64_t f);
typedef void (*fx_unbound_callback)(void *user_data, uint32_t value);

/* The callback invocations of a context: started, and returned to C. */
typedef struct fx_counters {
    uint64_t started;
    uint64_t returned;
} fx_counters;

uint32_t fx_abi_version(void);
fx_status fx_context_create(const fx_context_options *options, fx_context **out_context);
fx_status fx_context_destroy(fx_context *context);
fx_status fx_node_create(fx_context *, uint32_t kind, fx_node *out_node);
fx_status fx_node_destroy(fx_context *, const fx_node *node);
fx_status fx_node_count(fx_context *, uint32_t *out_count);
fx_status fx_node_set_box(fx_context *, const fx_node *node, const fx_box *box);
fx_status fx_node_get_box(fx_context *, const fx_node *node, fx_box *out_box);
fx_status fx_node_set_name(fx_context *, const fx_node *node, const uint8_t *name, uint32_t name_bytes);
fx_status fx_node_read_name(fx_context *, const fx_node *node, uint8_t *bytes, uint32_t capacity, uint32_t *out_count);
fx_status fx_node_set_text(fx_context *, const fx_node *node, const fx_text *text);
fx_status fx_context_visit(fx_context *, fx_visit_callback callback, void *user_data);
float fx_point_length(fx_point point);
fx_seconds fx_context_age(const fx_context *context);

/* Gives text to writer in spans of at most max_bytes until it is all taken,
 * the writer returns 0, or the writer fails. out_written is the bytes
 * taken. A result of -1 or above the span is FX_INVALID_ARGUMENT. */
fx_status fx_context_write(fx_context *, const uint8_t *text, uint32_t text_bytes, uint32_t max_bytes,
    uint32_t *out_written, void *user_data, fx_write_callback writer);
/* Calls writer times times with the one byte 'x'; out_taken is the sum of
 * its results. */
fx_status fx_context_repeat(fx_context *, uint32_t times, int64_t *out_taken, void *user_data,
    fx_write_callback writer);
/* Sets the measure callback of node, or clears it with NULL. A set calls the
 * new callback once before it returns; a clear calls the old one once
 * before it clears it. */
fx_status fx_node_set_measure(fx_context *, const fx_node *node, fx_measure_callback measure);
/* Calls the measure callback of node; out_size is what it wrote (NaN where
 * it wrote nothing). FX_INVALID_ARGUMENT when node has none. */
fx_status fx_node_measure(fx_context *, const fx_node *node, float width, float height, fx_point *out_size);
/* Calls the measure callback of node with the key of as instead of its own
 * (a probe of a key that the binding does not know); out_size as above. */
fx_status fx_node_measure_as(fx_context *, const fx_node *node, const fx_node *as, fx_point *out_size);
/* Calls the measure callback of every live node that has one, in slot
 * order; out_count is how many it called. */
fx_status fx_context_measure_all(fx_context *, float width, float height, uint32_t *out_count);
fx_status fx_context_counters(fx_context *, fx_counters *out_counters);
/* The id of the calling OS thread. */
uint64_t fx_thread_id(void);
fx_status fx_context_float(fx_context *, void *user_data, fx_float_callback callback);
fx_status fx_context_point(fx_context *, void *user_data, fx_point_callback callback);
fx_status fx_context_wide(fx_context *, void *user_data, fx_wide_callback callback);
fx_status fx_context_unbound(fx_context *, void *user_data, fx_unbound_callback callback);

#ifdef __cplusplus
}
#endif

#endif
