#include "ergo_rt.h"

#include <stdlib.h>
#include <string.h>

enum {
    ERGO_ACTOR_DEFAULT_SLAB = 4096u,
    ERGO_ACTOR_MAX_SLAB = 65536u
};

struct ErgoActorSlab {
    struct ErgoActorSlab *next;
    size_t cap;
    size_t used;
};

static _Thread_local ErgoActorContext *ergo_tls_actor_ctx = NULL;

ErgoActorContext *ergo_actor_tls_get(void) {
    return ergo_tls_actor_ctx;
}

static size_t actor_slab_hdr_size(void) {
    size_t a = _Alignof(max_align_t);
    return (sizeof(ErgoActorSlab) + (a - 1u)) & ~(a - 1u);
}

static uint8_t *actor_slab_data(ErgoActorSlab *s) {
    return ((uint8_t *)s) + actor_slab_hdr_size();
}

static bool actor_is_pow2(size_t x) {
    return x != 0u && (x & (x - 1u)) == 0u;
}

static ErgoActorSlab *actor_alloc_slab(size_t cap) {
    size_t hdr = actor_slab_hdr_size();
    if (cap > SIZE_MAX - hdr) {
        return NULL;
    }
    ErgoActorSlab *s = (ErgoActorSlab *)malloc(hdr + cap);
    if (s == NULL) {
        return NULL;
    }
    s->next = NULL;
    s->cap = cap;
    s->used = 0u;
    return s;
}

static bool actor_slab_needed(const ErgoActorSlab *s, size_t size, size_t align, uintptr_t *out_addr, size_t *out_needed) {
    uintptr_t base = (uintptr_t)actor_slab_data((ErgoActorSlab *)(uintptr_t)s);
    uintptr_t cur = base + (uintptr_t)s->used;
    uintptr_t mask = (uintptr_t)(align - 1u);
    if (cur > UINTPTR_MAX - mask) {
        return false;
    }
    uintptr_t aligned = (cur + mask) & ~mask;
    size_t pad = (size_t)(aligned - cur);
    if (pad > SIZE_MAX - size) {
        return false;
    }
    size_t needed = pad + size;
    if (needed > s->cap - s->used) {
        return false;
    }
    *out_addr = aligned;
    *out_needed = needed;
    return true;
}

void ergo_actor_heap_init(ErgoActorHeap *h, size_t limit_bytes) {
    if (h == NULL) {
        return;
    }
    h->slabs = NULL;
    h->current = NULL;
    h->limit_bytes = limit_bytes;
    h->used_bytes = 0u;
    h->peak_bytes = 0u;
}

void *ergo_actor_heap_alloc(ErgoActorHeap *h, size_t size, size_t align, ErgoStatus *out_status) {
    if (h == NULL) {
        if (out_status != NULL) {
            *out_status = ERGO_STATUS_INVALID_ARG;
        }
        return NULL;
    }
    if (align == 0u) {
        align = 1u;
    }
    if (!actor_is_pow2(align)) {
        if (out_status != NULL) {
            *out_status = ERGO_STATUS_INVALID_ARG;
        }
        return NULL;
    }
    if (h->limit_bytes > 0u && size > h->limit_bytes - h->used_bytes) {
        if (out_status != NULL) {
            *out_status = ERGO_STATUS_OVER_BYTES;
        }
        return NULL;
    }

    if (h->current != NULL) {
        uintptr_t addr = 0u;
        size_t needed = 0u;
        if (actor_slab_needed(h->current, size, align, &addr, &needed)) {
            if (h->limit_bytes > 0u && needed > h->limit_bytes - h->used_bytes) {
                if (out_status != NULL) {
                    *out_status = ERGO_STATUS_OVER_BYTES;
                }
                return NULL;
            }
            h->current->used += needed;
            h->used_bytes += needed;
            if (h->used_bytes > h->peak_bytes) {
                h->peak_bytes = h->used_bytes;
            }
            if (out_status != NULL) {
                *out_status = ERGO_STATUS_OK;
            }
            return (void *)addr;
        }
    }

    size_t worst_pad = (align > _Alignof(max_align_t)) ? (align - 1u) : 0u;
    if (size > SIZE_MAX - worst_pad) {
        if (out_status != NULL) {
            *out_status = ERGO_STATUS_OOM;
        }
        return NULL;
    }
    size_t min_cap = size + worst_pad;
    size_t slab_cap = (size_t)ERGO_ACTOR_DEFAULT_SLAB;
    if (h->current != NULL && h->current->cap >= slab_cap) {
        slab_cap = h->current->cap <= (size_t)(ERGO_ACTOR_MAX_SLAB / 2u)
            ? h->current->cap * 2u
            : (size_t)ERGO_ACTOR_MAX_SLAB;
    }
    if (h->limit_bytes > 0u) {
        size_t rem = h->limit_bytes - h->used_bytes;
        if (slab_cap > rem) {
            slab_cap = rem;
        }
    }
    if (slab_cap < min_cap) {
        slab_cap = min_cap;
    }
    if (slab_cap == 0u) {
        slab_cap = 16u;
    }

    ErgoActorSlab *fresh = actor_alloc_slab(slab_cap);
    if (fresh == NULL) {
        if (out_status != NULL) {
            *out_status = ERGO_STATUS_OOM;
        }
        return NULL;
    }

    uintptr_t addr = 0u;
    size_t needed = 0u;
    if (!actor_slab_needed(fresh, size, align, &addr, &needed)) {
        free(fresh);
        if (out_status != NULL) {
            *out_status = ERGO_STATUS_OOM;
        }
        return NULL;
    }
    if (h->limit_bytes > 0u && needed > h->limit_bytes - h->used_bytes) {
        free(fresh);
        if (out_status != NULL) {
            *out_status = ERGO_STATUS_OVER_BYTES;
        }
        return NULL;
    }

    fresh->used += needed;
    fresh->next = h->slabs;
    h->slabs = fresh;
    if (h->current == NULL || (fresh->cap - fresh->used) >= (h->current->cap - h->current->used)) {
        h->current = fresh;
    }
    h->used_bytes += needed;
    if (h->used_bytes > h->peak_bytes) {
        h->peak_bytes = h->used_bytes;
    }
    if (out_status != NULL) {
        *out_status = ERGO_STATUS_OK;
    }
    return (void *)addr;
}

void ergo_actor_heap_destroy(ErgoActorHeap *h) {
    if (h == NULL) {
        return;
    }
    ErgoActorSlab *s = h->slabs;
    while (s != NULL) {
        ErgoActorSlab *next = s->next;
        free(s);
        s = next;
    }
    h->slabs = NULL;
    h->current = NULL;
    h->used_bytes = 0u;
}

ErgoCrashReport ergo_actor_run_isolated(
    ErgoActorFn fn,
    void *arg,
    ErgoActorLimits limits,
    char *msg_buf,
    size_t msg_buf_cap
) {
    ErgoCrashReport report;
    report.panicked = false;
    report.code = 0u;
    report.msg = NULL;
    report.heap_peak_bytes = 0u;

    if (msg_buf != NULL && msg_buf_cap > 0u) {
        msg_buf[0] = '\0';
    }

    ErgoActorContext *ctx = (ErgoActorContext *)calloc(1u, sizeof(ErgoActorContext));
    if (ctx == NULL) {
        report.panicked = true;
        report.code = ERGO_DIE_OVER_BYTES;
        if (msg_buf != NULL && msg_buf_cap > 0u) {
            const char *oom = "actor context allocation failed";
            size_t n = strlen(oom);
            if (n >= msg_buf_cap) {
                n = msg_buf_cap - 1u;
            }
            memcpy(msg_buf, oom, n);
            msg_buf[n] = '\0';
            report.msg = msg_buf;
        }
        return report;
    }

    ergo_actor_heap_init(&ctx->heap, limits.limit_bytes);
    ctx->limit_steps = limits.limit_steps;
    ctx->steps_used = 0u;
    ctx->msg_buf = msg_buf;
    ctx->msg_buf_cap = msg_buf_cap;
    ctx->prev = ergo_tls_actor_ctx;
    ctx->die_code = 0u;
    ctx->panicked = false;
    ctx->active = true;
    ergo_tls_actor_ctx = ctx;

    ErgoActorContext * volatile vctx = ctx;
    if (setjmp(vctx->jmp) == 0) {
        if (fn != NULL) {
            fn(arg);
        }
    }

    ErgoActorContext *done_ctx = vctx;
    ergo_tls_actor_ctx = done_ctx->prev;
    done_ctx->active = false;

    report.panicked = done_ctx->panicked;
    report.code = done_ctx->panicked ? done_ctx->die_code : 0u;
    report.msg = (done_ctx->panicked && done_ctx->msg_buf != NULL && done_ctx->msg_buf_cap > 0u)
        ? done_ctx->msg_buf
        : NULL;
    report.heap_peak_bytes = done_ctx->heap.peak_bytes;

    ergo_actor_heap_destroy(&done_ctx->heap);
    free(done_ctx);
    return report;
}

void ergo_actor_step(uint32_t cost) {
    ErgoActorContext *ctx = ergo_tls_actor_ctx;
    if (ctx == NULL || !ctx->active) {
        return;
    }
    uint64_t add = (uint64_t)cost;
    if (ctx->limit_steps > 0u) {
        if (add > ctx->limit_steps || ctx->steps_used > ctx->limit_steps - add) {
            ctx->steps_used = ctx->limit_steps;
            ergo_die(ERGO_DIE_OVER_STEPS, ERGO_STR_LIT("actor exceeded step budget"));
        }
    }
    ctx->steps_used += add;
}

void *ergo_actor_alloc(size_t size, size_t align) {
    ErgoActorContext *ctx = ergo_tls_actor_ctx;
    if (ctx == NULL || !ctx->active) {
        return NULL;
    }
    ErgoStatus st = ERGO_STATUS_OK;
    void *p = ergo_actor_heap_alloc(&ctx->heap, size, align, &st);
    if (p == NULL) {
        if (st == ERGO_STATUS_OVER_BYTES) {
            ergo_die(ERGO_DIE_OVER_BYTES, ERGO_STR_LIT("actor exceeded byte limit"));
        }
        ergo_die(ERGO_DIE_PANIC, ERGO_STR_LIT("actor heap allocation failed"));
    }
    return p;
}

ErgoActorHeap *ergo_actor_current_heap(void) {
    ErgoActorContext *ctx = ergo_tls_actor_ctx;
    if (ctx == NULL || !ctx->active) {
        return NULL;
    }
    return &ctx->heap;
}

uint64_t ergo_actor_steps_used(void) {
    ErgoActorContext *ctx = ergo_tls_actor_ctx;
    if (ctx == NULL || !ctx->active) {
        return 0u;
    }
    return ctx->steps_used;
}
