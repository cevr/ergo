#include "ergo_rt.h"

#include <stdlib.h>

static uint32_t rc_encode_flags(uint32_t user_flags, uint32_t cap) {
    uint32_t masked = user_flags & ERGO_RC_USER_FLAGS_MASK;
    if (cap <= ERGO_RC_CAP_MAX_EXACT) {
        return masked | (cap << ERGO_RC_CAP_SHIFT);
    }
    return masked | ERGO_RC_FLAG_CAP_SHIFTED | ((cap >> ERGO_RC_CAP_PAGE_SHIFT) << ERGO_RC_CAP_SHIFT);
}

static uint32_t rc_header_cap(const ErgoRCHeader *hdr) {
    if ((hdr->flags & ERGO_RC_FLAG_STATIC) != 0 && (hdr->flags >> ERGO_RC_CAP_SHIFT) == 0u) {
        return hdr->payload_size;
    }
    uint32_t raw = hdr->flags >> ERGO_RC_CAP_SHIFT;
    if ((hdr->flags & ERGO_RC_FLAG_CAP_SHIFTED) != 0) {
        return raw << ERGO_RC_CAP_PAGE_SHIFT;
    }
    return raw;
}

static ErgoRCHeader *rc_hdr_mut(void *p) {
    return ((ErgoRCHeader *)p) - 1;
}

static const ErgoRCHeader *rc_hdr_const(const void *p) {
    return ((const ErgoRCHeader *)p) - 1;
}

void *ergo_rc_alloc(uint32_t payload_size, uint32_t type_tag, uint32_t flags) {
    size_t total = sizeof(ErgoRCHeader) + (size_t)payload_size;
    ErgoRCHeader *hdr = (ErgoRCHeader *)calloc(1u, total);
    if (hdr == NULL) {
        return NULL;
    }
    atomic_init(&hdr->rc, 1u);
    hdr->flags = rc_encode_flags(flags, payload_size);
    hdr->payload_size = payload_size;
    hdr->type_tag = type_tag;
    return (void *)(hdr + 1);
}

void *ergo_rc_dup(void *p) {
    if (p == NULL) {
        return NULL;
    }
    ErgoRCHeader *hdr = rc_hdr_mut(p);
    if ((hdr->flags & ERGO_RC_FLAG_STATIC) != 0) {
        return p;
    }
    if ((hdr->flags & ERGO_RC_FLAG_SHARED) != 0) {
        atomic_fetch_add_explicit(&hdr->rc, 1u, memory_order_relaxed);
    } else {
        uint32_t cur = atomic_load_explicit(&hdr->rc, memory_order_relaxed);
        atomic_store_explicit(&hdr->rc, cur + 1u, memory_order_relaxed);
    }
    return p;
}

void ergo_rc_drop(void *p, ErgoDropFn drop_fn) {
    if (p == NULL) {
        return;
    }
    ErgoRCHeader *hdr = rc_hdr_mut(p);
    if ((hdr->flags & ERGO_RC_FLAG_STATIC) != 0) {
        return;
    }
    bool free_now = false;
    if ((hdr->flags & ERGO_RC_FLAG_SHARED) != 0) {
        uint32_t prev = atomic_fetch_sub_explicit(&hdr->rc, 1u, memory_order_release);
        if (prev == 1u) {
            atomic_thread_fence(memory_order_acquire);
            free_now = true;
        }
    } else {
        uint32_t cur = atomic_load_explicit(&hdr->rc, memory_order_relaxed);
        if (cur <= 1u) {
            free_now = true;
        } else {
            atomic_store_explicit(&hdr->rc, cur - 1u, memory_order_relaxed);
        }
    }
    if (free_now) {
        if (drop_fn != NULL) {
            drop_fn(p);
        }
        free(hdr);
    }
}

uint32_t ergo_rc_count(const void *p) {
    if (p == NULL) {
        return 0u;
    }
    const ErgoRCHeader *hdr = rc_hdr_const(p);
    ErgoRCHeader *mhdr = (ErgoRCHeader *)(uintptr_t)hdr;
    if ((hdr->flags & ERGO_RC_FLAG_SHARED) != 0) {
        return atomic_load_explicit(&mhdr->rc, memory_order_acquire);
    }
    return atomic_load_explicit(&mhdr->rc, memory_order_relaxed);
}

bool ergo_rc_is_unique(const void *p) {
    if (p == NULL) {
        return false;
    }
    const ErgoRCHeader *hdr = rc_hdr_const(p);
    if ((hdr->flags & ERGO_RC_FLAG_STATIC) != 0) {
        return false;
    }
    return ergo_rc_count(p) == 1u;
}

void *ergo_rc_reuse_or_alloc(void *old_ptr, uint32_t new_size, uint32_t new_type_tag, ErgoDropFn drop_old) {
    if (old_ptr == NULL) {
        return ergo_rc_alloc(new_size, new_type_tag, 0u);
    }
    ErgoRCHeader *hdr = rc_hdr_mut(old_ptr);
    uint32_t user_flags = hdr->flags & ERGO_RC_USER_FLAGS_MASK;
    bool is_static = (user_flags & ERGO_RC_FLAG_STATIC) != 0;
    if (!is_static) {
        uint32_t rc = ((user_flags & ERGO_RC_FLAG_SHARED) != 0)
            ? atomic_load_explicit(&hdr->rc, memory_order_acquire)
            : atomic_load_explicit(&hdr->rc, memory_order_relaxed);
        uint32_t cap = rc_header_cap(hdr);
        if (rc == 1u && cap >= new_size) {
            if (drop_old != NULL) {
                drop_old(old_ptr);
            }
            hdr->payload_size = new_size;
            hdr->type_tag = new_type_tag;
            return old_ptr;
        }
    }
    ergo_rc_drop(old_ptr, drop_old);
    return ergo_rc_alloc(new_size, new_type_tag, is_static ? 0u : (user_flags & ERGO_RC_FLAG_SHARED));
}

uint32_t ergo_rc_type_tag(const void *p) {
    if (p == NULL) {
        return 0u;
    }
    return rc_hdr_const(p)->type_tag;
}

uint32_t ergo_rc_payload_size(const void *p) {
    if (p == NULL) {
        return 0u;
    }
    return rc_hdr_const(p)->payload_size;
}

uint32_t ergo_rc_capacity(const void *p) {
    if (p == NULL) {
        return 0u;
    }
    return rc_header_cap(rc_hdr_const(p));
}

uint32_t ergo_rc_flags(const void *p) {
    if (p == NULL) {
        return 0u;
    }
    return rc_hdr_const(p)->flags & ERGO_RC_USER_FLAGS_MASK;
}
