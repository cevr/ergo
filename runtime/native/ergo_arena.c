#include "ergo_rt.h"

#include <stdlib.h>

enum {
    ERGO_ARENA_DEFAULT_CAP = 4096u
};

struct ErgoArenaChunk {
    struct ErgoArenaChunk *prev;
    struct ErgoArenaChunk *next;
    size_t cap;
    size_t used;
};

struct ErgoArena {
    ErgoArenaChunk *head;
    ErgoArenaChunk *current;
    size_t default_cap;
    size_t used;
};

static size_t arena_chunk_hdr_size(void) {
    size_t a = _Alignof(max_align_t);
    return (sizeof(ErgoArenaChunk) + (a - 1u)) & ~(a - 1u);
}

static uint8_t *arena_chunk_data(ErgoArenaChunk *c) {
    return ((uint8_t *)c) + arena_chunk_hdr_size();
}

static ErgoArenaChunk *arena_alloc_chunk(size_t cap) {
    size_t hdr = arena_chunk_hdr_size();
    if (cap > SIZE_MAX - hdr) {
        return NULL;
    }
    ErgoArenaChunk *c = (ErgoArenaChunk *)malloc(hdr + cap);
    if (c == NULL) {
        return NULL;
    }
    c->prev = NULL;
    c->next = NULL;
    c->cap = cap;
    c->used = 0u;
    return c;
}

static bool is_power_of_two(size_t x) {
    return x != 0u && (x & (x - 1u)) == 0u;
}

static bool arena_try_bump(ErgoArena *a, ErgoArenaChunk *c, size_t size, size_t align, void **out_ptr) {
    uintptr_t base = (uintptr_t)arena_chunk_data(c);
    uintptr_t cur = base + (uintptr_t)c->used;
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
    if (needed > c->cap - c->used) {
        return false;
    }
    c->used += needed;
    a->used += needed;
    *out_ptr = (void *)aligned;
    return true;
}

ErgoArena *ergo_arena_create(size_t initial_cap) {
    ErgoArena *a = (ErgoArena *)calloc(1u, sizeof(ErgoArena));
    if (a == NULL) {
        return NULL;
    }
    a->default_cap = initial_cap > 0u ? initial_cap : (size_t)ERGO_ARENA_DEFAULT_CAP;
    if (initial_cap > 0u) {
        ErgoArenaChunk *c = arena_alloc_chunk(initial_cap);
        if (c == NULL) {
            free(a);
            return NULL;
        }
        a->head = c;
        a->current = c;
    }
    return a;
}

void *ergo_arena_alloc(ErgoArena *a, size_t size, size_t align) {
    if (a == NULL) {
        return NULL;
    }
    if (align == 0u) {
        align = 1u;
    }
    if (!is_power_of_two(align)) {
        return NULL;
    }
    void *ptr = NULL;
    if (a->current != NULL && arena_try_bump(a, a->current, size, align, &ptr)) {
        return ptr;
    }
    while (a->current != NULL && a->current->next != NULL) {
        ErgoArenaChunk *reuse = a->current->next;
        reuse->used = 0u;
        if (arena_try_bump(a, reuse, size, align, &ptr)) {
            a->current = reuse;
            return ptr;
        }
        ErgoArenaChunk *after = reuse->next;
        a->current->next = after;
        if (after != NULL) {
            after->prev = a->current;
        }
        free(reuse);
    }
    if (a->current == NULL && a->head != NULL) {
        a->head->used = 0u;
        if (arena_try_bump(a, a->head, size, align, &ptr)) {
            a->current = a->head;
            return ptr;
        }
    }

    size_t worst_pad = (align > _Alignof(max_align_t)) ? (align - 1u) : 0u;
    if (size > SIZE_MAX - worst_pad) {
        return NULL;
    }
    size_t min_cap = size + worst_pad;
    size_t next_cap = a->current != NULL ? a->current->cap : a->default_cap;
    if (next_cap < a->default_cap) {
        next_cap = a->default_cap;
    }
    while (next_cap < min_cap && next_cap <= SIZE_MAX / 2u) {
        next_cap *= 2u;
    }
    if (next_cap < min_cap) {
        next_cap = min_cap;
    }

    ErgoArenaChunk *fresh = arena_alloc_chunk(next_cap);
    if (fresh == NULL) {
        return NULL;
    }
    if (a->head == NULL) {
        a->head = fresh;
        a->current = fresh;
    } else if (a->current == NULL) {
        fresh->next = a->head;
        a->head->prev = fresh;
        a->head = fresh;
        a->current = fresh;
    } else {
        ErgoArenaChunk *after = a->current->next;
        fresh->prev = a->current;
        fresh->next = after;
        a->current->next = fresh;
        if (after != NULL) {
            after->prev = fresh;
        }
        a->current = fresh;
    }
    if (!arena_try_bump(a, fresh, size, align, &ptr)) {
        return NULL;
    }
    return ptr;
}

ErgoArenaMark ergo_arena_mark(const ErgoArena *a) {
    ErgoArenaMark mark;
    mark.chunk = NULL;
    mark.offset = 0u;
    mark.total_used = 0u;
    if (a == NULL) {
        return mark;
    }
    mark.chunk = a->current;
    mark.offset = a->current != NULL ? a->current->used : 0u;
    mark.total_used = a->used;
    return mark;
}

void ergo_arena_reset(ErgoArena *a, ErgoArenaMark mark) {
    if (a == NULL) {
        return;
    }
    if (mark.chunk == NULL) {
        a->current = a->head;
        if (a->current != NULL) {
            a->current->used = 0u;
        }
        a->used = 0u;
        return;
    }
    a->current = mark.chunk;
    a->current->used = mark.offset;
    a->used = mark.total_used;
}

void ergo_arena_destroy(ErgoArena *a) {
    if (a == NULL) {
        return;
    }
    ErgoArenaChunk *c = a->head;
    while (c != NULL) {
        ErgoArenaChunk *next = c->next;
        free(c);
        c = next;
    }
    free(a);
}

size_t ergo_arena_used(const ErgoArena *a) {
    return a != NULL ? a->used : 0u;
}
