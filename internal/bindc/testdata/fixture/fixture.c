/* fixture.c: the library of fixture.h. See fixture.h for the build flags.
 * It needs POSIX threads for the owner-thread check, and gettid (linux) or
 * pthread_threadid_np (darwin) for fx_thread_id. */
#define _POSIX_C_SOURCE 200809L
#define _DEFAULT_SOURCE
#define _DARWIN_C_SOURCE
#include "fixture.h"

#include <math.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#if !defined(__APPLE__)
#include <sys/syscall.h>
#endif

#ifndef FIXTURE_ABI
#define FIXTURE_ABI 2
#endif

/* -DFIXTURE_OMIT=<name> drops the definition of <name>: each function has
 * an id, and OMITTED(id) is true for the id of <name>. */
#define FIXTURE_CAT_(a, b) a##b
#define FIXTURE_CAT(a, b) FIXTURE_CAT_(a, b)
#define FIXTURE_ID_fx_abi_version 1
#define FIXTURE_ID_fx_context_create 2
#define FIXTURE_ID_fx_context_destroy 3
#define FIXTURE_ID_fx_node_create 4
#define FIXTURE_ID_fx_node_destroy 5
#define FIXTURE_ID_fx_node_count 6
#define FIXTURE_ID_fx_node_set_box 7
#define FIXTURE_ID_fx_node_get_box 8
#define FIXTURE_ID_fx_node_set_name 9
#define FIXTURE_ID_fx_node_read_name 10
#define FIXTURE_ID_fx_node_set_text 11
#define FIXTURE_ID_fx_context_visit 12
#define FIXTURE_ID_fx_point_length 13
#define FIXTURE_ID_fx_context_age 14
#define FIXTURE_ID_fx_context_write 15
#define FIXTURE_ID_fx_context_repeat 16
#define FIXTURE_ID_fx_node_set_measure 17
#define FIXTURE_ID_fx_node_measure 18
#define FIXTURE_ID_fx_context_measure_all 19
#define FIXTURE_ID_fx_context_counters 20
#define FIXTURE_ID_fx_thread_id 21
#define FIXTURE_ID_fx_context_float 22
#define FIXTURE_ID_fx_context_point 23
#define FIXTURE_ID_fx_context_wide 24
#define FIXTURE_ID_fx_context_unbound 25
#ifdef FIXTURE_OMIT
#if FIXTURE_CAT(FIXTURE_ID_, FIXTURE_OMIT) == 0
#error FIXTURE_OMIT does not name a function of fixture.h
#endif
#define OMITTED(id) (FIXTURE_CAT(FIXTURE_ID_, FIXTURE_OMIT) == (id))
#else
#define OMITTED(id) 0
#endif

typedef struct fx_slot {
    uint32_t generation;
    bool live;
    fx_box box;
    uint32_t name_bytes;
    uint8_t name[FX_NAME_MAX * 4];
    fx_measure_callback measure;
} fx_slot;

struct fx_context {
    uint64_t id;
    pthread_t owner;
    uint32_t capacity;
    fx_slot *slots;
    fx_counters counters;
};

/* INVOKE counts one callback invocation of c around the call: started
 * before it, returned after it. A callback that unwinds through C skips
 * the second count. */
#define INVOKE(c, call)             \
    do {                            \
        (c)->counters.started++;    \
        call;                       \
        (c)->counters.returned++;   \
    } while (0)

static uint64_t next_context_id = 1;

static bool on_owner(const fx_context *c) { return pthread_equal(pthread_self(), c->owner) != 0; }

static fx_status lookup(fx_context *c, const fx_node *node, fx_slot **out) {
    if (c == NULL || node == NULL) return FX_INVALID_ARGUMENT;
    if (!on_owner(c)) return FX_WRONG_THREAD;
    if (node->context_id != c->id || node->slot >= c->capacity) return FX_STALE_HANDLE;
    fx_slot *s = &c->slots[node->slot];
    if (!s->live || s->generation != node->generation) return FX_STALE_HANDLE;
    *out = s;
    return FX_OK;
}

#if !OMITTED(1)
uint32_t fx_abi_version(void) { return FIXTURE_ABI; }
#endif

#if !OMITTED(2)
fx_status fx_context_create(const fx_context_options *options, fx_context **out_context) {
    if (options == NULL || out_context == NULL) return FX_INVALID_ARGUMENT;
    if (options->struct_size != sizeof(fx_context_options) || options->abi_version != FX_ABI_VERSION) {
        return FX_UNSUPPORTED_VERSION;
    }
    if (options->node_capacity == 0 || options->node_capacity > FX_NODE_MAX) return FX_INVALID_ARGUMENT;
    fx_context *c = calloc(1, sizeof *c);
    if (c == NULL) return FX_OUT_OF_MEMORY;
    c->slots = calloc(options->node_capacity, sizeof *c->slots);
    if (c->slots == NULL) {
        free(c);
        return FX_OUT_OF_MEMORY;
    }
    c->id = next_context_id++;
    c->owner = pthread_self();
    c->capacity = options->node_capacity;
    *out_context = c;
    return FX_OK;
}
#endif

#if !OMITTED(3)
fx_status fx_context_destroy(fx_context *context) {
    if (context == NULL) return FX_INVALID_ARGUMENT;
    if (!on_owner(context)) return FX_WRONG_THREAD;
    free(context->slots);
    free(context);
    return FX_OK;
}
#endif

#if !OMITTED(4)
fx_status fx_node_create(fx_context *c, uint32_t kind, fx_node *out_node) {
    if (c == NULL || out_node == NULL) return FX_INVALID_ARGUMENT;
    if (!on_owner(c)) return FX_WRONG_THREAD;
    for (uint32_t i = 0; i < c->capacity; i++) {
        fx_slot *s = &c->slots[i];
        if (s->live) continue;
        s->live = true;
        s->generation++;
        memset(&s->box, 0, sizeof s->box);
        s->box.kind = (uint8_t)kind;
        s->name_bytes = 0;
        out_node->context_id = c->id;
        out_node->slot = i;
        out_node->generation = s->generation;
        return FX_OK;
    }
    return FX_OUT_OF_MEMORY;
}
#endif

#if !OMITTED(5)
fx_status fx_node_destroy(fx_context *c, const fx_node *node) {
    fx_slot *s;
    fx_status st = lookup(c, node, &s);
    if (st != FX_OK) return st;
    s->live = false;
    s->measure = NULL;
    return FX_OK;
}
#endif

#if !OMITTED(6)
fx_status fx_node_count(fx_context *c, uint32_t *out_count) {
    if (c == NULL || out_count == NULL) return FX_INVALID_ARGUMENT;
    if (!on_owner(c)) return FX_WRONG_THREAD;
    uint32_t n = 0;
    for (uint32_t i = 0; i < c->capacity; i++) n += c->slots[i].live ? 1 : 0;
    *out_count = n;
    return FX_OK;
}
#endif

#if !OMITTED(7)
fx_status fx_node_set_box(fx_context *c, const fx_node *node, const fx_box *box) {
    fx_slot *s;
    fx_status st = lookup(c, node, &s);
    if (st != FX_OK) return st;
    if (box == NULL) return FX_INVALID_ARGUMENT;
    if (box->struct_size != sizeof(fx_box) || box->abi_version != FX_ABI_VERSION) return FX_UNSUPPORTED_VERSION;
    s->box = *box;
    return FX_OK;
}
#endif

#if !OMITTED(8)
fx_status fx_node_get_box(fx_context *c, const fx_node *node, fx_box *out_box) {
    fx_slot *s;
    fx_status st = lookup(c, node, &s);
    if (st != FX_OK) return st;
    if (out_box == NULL) return FX_INVALID_ARGUMENT;
    *out_box = s->box;
    out_box->struct_size = sizeof(fx_box);
    out_box->abi_version = FX_ABI_VERSION;
    return FX_OK;
}
#endif

static fx_status set_name(fx_context *c, const fx_node *node, const uint8_t *name, uint32_t name_bytes) {
    fx_slot *s;
    fx_status st = lookup(c, node, &s);
    if (st != FX_OK) return st;
    if (name == NULL && name_bytes != 0) return FX_INVALID_ARGUMENT;
    if (name_bytes > sizeof s->name) return FX_INVALID_ARGUMENT;
    if (name_bytes != 0) memcpy(s->name, name, name_bytes);
    s->name_bytes = name_bytes;
    return FX_OK;
}

#if !OMITTED(9)
fx_status fx_node_set_name(fx_context *c, const fx_node *node, const uint8_t *name, uint32_t name_bytes) {
    return set_name(c, node, name, name_bytes);
}
#endif

#if !OMITTED(10)
fx_status fx_node_read_name(fx_context *c, const fx_node *node, uint8_t *bytes, uint32_t capacity, uint32_t *out_count) {
    fx_slot *s;
    fx_status st = lookup(c, node, &s);
    if (st != FX_OK) return st;
    if (out_count == NULL || (bytes == NULL && capacity != 0)) return FX_INVALID_ARGUMENT;
    uint32_t n = s->name_bytes < capacity ? s->name_bytes : capacity;
    if (n != 0) memcpy(bytes, s->name, n);
    *out_count = n;
    return FX_OK;
}
#endif

#if !OMITTED(11)
fx_status fx_node_set_text(fx_context *c, const fx_node *node, const fx_text *text) {
    if (text == NULL) return FX_INVALID_ARGUMENT;
    return set_name(c, node, text->bytes, text->byte_count);
}
#endif

#if !OMITTED(12)
fx_status fx_context_visit(fx_context *c, fx_visit_callback callback, void *user_data) {
    if (c == NULL || callback == NULL) return FX_INVALID_ARGUMENT;
    if (!on_owner(c)) return FX_WRONG_THREAD;
    for (uint32_t i = 0; i < c->capacity; i++) {
        if (!c->slots[i].live) continue;
        fx_visit v = {{c->id, i, c->slots[i].generation}, (fx_ticket)i};
        INVOKE(c, callback(user_data, &v));
    }
    return FX_OK;
}
#endif

#if !OMITTED(13)
float fx_point_length(fx_point point) { return sqrtf(point.x * point.x + point.y * point.y); }
#endif

#if !OMITTED(14)
fx_seconds fx_context_age(const fx_context *context) { return context == NULL ? -1.0 : (double)context->id; }
#endif

static fx_status owned(fx_context *c) {
    if (c == NULL) return FX_INVALID_ARGUMENT;
    return on_owner(c) ? FX_OK : FX_WRONG_THREAD;
}

#if !OMITTED(15)
fx_status fx_context_write(fx_context *c, const uint8_t *text, uint32_t text_bytes, uint32_t max_bytes,
    uint32_t *out_written, void *user_data, fx_write_callback writer) {
    fx_status st = owned(c);
    if (st != FX_OK) return st;
    if (writer == NULL || out_written == NULL || max_bytes == 0 || (text == NULL && text_bytes != 0)) return FX_INVALID_ARGUMENT;
    uint32_t done = 0;
    while (done < text_bytes) {
        uint32_t span = text_bytes - done < max_bytes ? text_bytes - done : max_bytes;
        int64_t took;
        INVOKE(c, took = writer(user_data, text + done, span));
        if (took < 0 || took > (int64_t)span) {
            *out_written = done;
            return FX_INVALID_ARGUMENT;
        }
        if (took == 0) break;
        done += (uint32_t)took;
    }
    *out_written = done;
    return FX_OK;
}
#endif

#if !OMITTED(16)
fx_status fx_context_repeat(fx_context *c, uint32_t times, int64_t *out_taken, void *user_data, fx_write_callback writer) {
    fx_status st = owned(c);
    if (st != FX_OK) return st;
    if (writer == NULL || out_taken == NULL) return FX_INVALID_ARGUMENT;
    static const uint8_t x = 'x';
    int64_t sum = 0;
    for (uint32_t i = 0; i < times; i++) {
        int64_t took;
        INVOKE(c, took = writer(user_data, &x, 1));
        sum += took;
    }
    *out_taken = sum;
    return FX_OK;
}
#endif

/* call_measure calls the measure callback m for the node in slot i of c. */
static fx_point call_measure(fx_context *c, uint32_t i, fx_measure_callback m, float width, float height) {
    float size[2] = {NAN, NAN};
    INVOKE(c, m(c->id, i, c->slots[i].generation, width, 1, height, 1, size));
    fx_point p = {size[0], size[1]};
    return p;
}

#if !OMITTED(17)
fx_status fx_node_set_measure(fx_context *c, const fx_node *node, fx_measure_callback measure) {
    fx_slot *s;
    fx_status st = lookup(c, node, &s);
    if (st != FX_OK) return st;
    if (measure != NULL) {
        s->measure = measure;
        (void)call_measure(c, node->slot, measure, 0, 0); /* the set probe: the new callback */
    } else if (s->measure != NULL) {
        (void)call_measure(c, node->slot, s->measure, 0, 0); /* the clear probe: the old callback */
        s->measure = NULL;
    }
    return FX_OK;
}
#endif

#if !OMITTED(18)
fx_status fx_node_measure(fx_context *c, const fx_node *node, float width, float height, fx_point *out_size) {
    fx_slot *s;
    fx_status st = lookup(c, node, &s);
    if (st != FX_OK) return st;
    if (out_size == NULL || s->measure == NULL) return FX_INVALID_ARGUMENT;
    *out_size = call_measure(c, node->slot, s->measure, width, height);
    return FX_OK;
}
#endif

#if !OMITTED(19)
fx_status fx_context_measure_all(fx_context *c, float width, float height, uint32_t *out_count) {
    fx_status st = owned(c);
    if (st != FX_OK) return st;
    if (out_count == NULL) return FX_INVALID_ARGUMENT;
    uint32_t n = 0;
    for (uint32_t i = 0; i < c->capacity; i++) {
        /* Read the slot again after each call: Go code may change it. */
        if (!c->slots[i].live || c->slots[i].measure == NULL) continue;
        (void)call_measure(c, i, c->slots[i].measure, width, height);
        n++;
    }
    *out_count = n;
    return FX_OK;
}
#endif

#if !OMITTED(20)
fx_status fx_context_counters(fx_context *c, fx_counters *out_counters) {
    fx_status st = owned(c);
    if (st != FX_OK) return st;
    if (out_counters == NULL) return FX_INVALID_ARGUMENT;
    *out_counters = c->counters;
    return FX_OK;
}
#endif

#if !OMITTED(21)
uint64_t fx_thread_id(void) {
#if defined(__APPLE__)
    uint64_t id = 0;
    pthread_threadid_np(NULL, &id);
    return id;
#else
    return (uint64_t)syscall(SYS_gettid);
#endif
}
#endif

#if !OMITTED(22)
fx_status fx_context_float(fx_context *c, void *user_data, fx_float_callback callback) {
    fx_status st = owned(c);
    if (st != FX_OK) return st;
    if (callback == NULL) return FX_INVALID_ARGUMENT;
    INVOKE(c, (void)callback(user_data));
    return FX_OK;
}
#endif

#if !OMITTED(23)
fx_status fx_context_point(fx_context *c, void *user_data, fx_point_callback callback) {
    fx_status st = owned(c);
    if (st != FX_OK) return st;
    if (callback == NULL) return FX_INVALID_ARGUMENT;
    fx_point p = {1, 2};
    INVOKE(c, callback(user_data, p));
    return FX_OK;
}
#endif

#if !OMITTED(24)
fx_status fx_context_wide(fx_context *c, void *user_data, fx_wide_callback callback) {
    fx_status st = owned(c);
    if (st != FX_OK) return st;
    if (callback == NULL) return FX_INVALID_ARGUMENT;
    INVOKE(c, callback(user_data, 1, 2, 3, 4, 5, 6));
    return FX_OK;
}
#endif

#if !OMITTED(25)
fx_status fx_context_unbound(fx_context *c, void *user_data, fx_unbound_callback callback) {
    fx_status st = owned(c);
    if (st != FX_OK) return st;
    if (callback == NULL) return FX_INVALID_ARGUMENT;
    INVOKE(c, callback(user_data, 7));
    return FX_OK;
}
#endif
