#include "ergo_rt.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int g_failures = 0;

#define CHECK(cond)                                                              \
    do {                                                                         \
        if (!(cond)) {                                                           \
            fprintf(stderr, "FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond);      \
            g_failures++;                                                        \
        }                                                                        \
    } while (0)

/* ---- Layout verification matching compiler/nativeabi ---- */

static void emit_and_check_layouts(bool verbose) {
    CHECK(sizeof(ErgoRCHeader) == 16u);
    CHECK(_Alignof(ErgoRCHeader) == 4u);
    CHECK(offsetof(ErgoRCHeader, rc) == 0u);
    CHECK(offsetof(ErgoRCHeader, flags) == 4u);
    CHECK(offsetof(ErgoRCHeader, payload_size) == 8u);
    CHECK(offsetof(ErgoRCHeader, type_tag) == 12u);

    CHECK(sizeof(ErgoStr) == 16u);
    CHECK(_Alignof(ErgoStr) == 8u);
    CHECK(offsetof(ErgoStr, ptr) == 0u);
    CHECK(offsetof(ErgoStr, len) == 8u);

    CHECK(sizeof(ErgoBytes) == 24u);
    CHECK(_Alignof(ErgoBytes) == 8u);
    CHECK(offsetof(ErgoBytes, ptr) == 0u);
    CHECK(offsetof(ErgoBytes, len) == 8u);
    CHECK(offsetof(ErgoBytes, cap) == 16u);

    CHECK(sizeof(ErgoSlice) == 24u);
    CHECK(_Alignof(ErgoSlice) == 8u);
    CHECK(offsetof(ErgoSlice, ptr) == 0u);
    CHECK(offsetof(ErgoSlice, len) == 8u);
    CHECK(offsetof(ErgoSlice, cap) == 16u);

    CHECK(sizeof(ErgoError) == 24u);
    CHECK(_Alignof(ErgoError) == 8u);
    CHECK(offsetof(ErgoError, tag) == 0u);
    CHECK(offsetof(ErgoError, _pad) == 4u);
    CHECK(offsetof(ErgoError, msg) == 8u);

    CHECK(sizeof(ErgoArenaMark) == 24u);
    CHECK(_Alignof(ErgoArenaMark) == 8u);
    CHECK(offsetof(ErgoArenaMark, chunk) == 0u);
    CHECK(offsetof(ErgoArenaMark, offset) == 8u);
    CHECK(offsetof(ErgoArenaMark, total_used) == 16u);

    CHECK(sizeof(ErgoActorLimits) == 16u);
    CHECK(_Alignof(ErgoActorLimits) == 8u);
    CHECK(offsetof(ErgoActorLimits, limit_bytes) == 0u);
    CHECK(offsetof(ErgoActorLimits, limit_steps) == 8u);

    CHECK(sizeof(ErgoActorHeap) == 40u);
    CHECK(_Alignof(ErgoActorHeap) == 8u);
    CHECK(offsetof(ErgoActorHeap, slabs) == 0u);
    CHECK(offsetof(ErgoActorHeap, current) == 8u);
    CHECK(offsetof(ErgoActorHeap, limit_bytes) == 16u);
    CHECK(offsetof(ErgoActorHeap, used_bytes) == 24u);
    CHECK(offsetof(ErgoActorHeap, peak_bytes) == 32u);

    CHECK(sizeof(ErgoCrashReport) == 24u);
    CHECK(_Alignof(ErgoCrashReport) == 8u);
    CHECK(offsetof(ErgoCrashReport, panicked) == 0u);
    CHECK(offsetof(ErgoCrashReport, code) == 4u);
    CHECK(offsetof(ErgoCrashReport, msg) == 8u);
    CHECK(offsetof(ErgoCrashReport, heap_peak_bytes) == 16u);

    if (verbose) {
        printf("LAYOUT ErgoRCHeader size=%zu align=%zu rc=%zu flags=%zu payload_size=%zu type_tag=%zu\n",
            sizeof(ErgoRCHeader), _Alignof(ErgoRCHeader),
            offsetof(ErgoRCHeader, rc), offsetof(ErgoRCHeader, flags),
            offsetof(ErgoRCHeader, payload_size), offsetof(ErgoRCHeader, type_tag));
        printf("LAYOUT ErgoStr size=%zu align=%zu ptr=%zu len=%zu\n",
            sizeof(ErgoStr), _Alignof(ErgoStr),
            offsetof(ErgoStr, ptr), offsetof(ErgoStr, len));
        printf("LAYOUT ErgoBytes size=%zu align=%zu ptr=%zu len=%zu cap=%zu\n",
            sizeof(ErgoBytes), _Alignof(ErgoBytes),
            offsetof(ErgoBytes, ptr), offsetof(ErgoBytes, len), offsetof(ErgoBytes, cap));
        printf("LAYOUT ErgoSlice size=%zu align=%zu ptr=%zu len=%zu cap=%zu\n",
            sizeof(ErgoSlice), _Alignof(ErgoSlice),
            offsetof(ErgoSlice, ptr), offsetof(ErgoSlice, len), offsetof(ErgoSlice, cap));
        printf("LAYOUT ErgoError size=%zu align=%zu tag=%zu _pad=%zu msg=%zu\n",
            sizeof(ErgoError), _Alignof(ErgoError),
            offsetof(ErgoError, tag), offsetof(ErgoError, _pad), offsetof(ErgoError, msg));
        printf("LAYOUT ErgoArenaMark size=%zu align=%zu chunk=%zu offset=%zu total_used=%zu\n",
            sizeof(ErgoArenaMark), _Alignof(ErgoArenaMark),
            offsetof(ErgoArenaMark, chunk), offsetof(ErgoArenaMark, offset), offsetof(ErgoArenaMark, total_used));
        printf("LAYOUT ErgoActorLimits size=%zu align=%zu limit_bytes=%zu limit_steps=%zu\n",
            sizeof(ErgoActorLimits), _Alignof(ErgoActorLimits),
            offsetof(ErgoActorLimits, limit_bytes), offsetof(ErgoActorLimits, limit_steps));
        printf("LAYOUT ErgoActorHeap size=%zu align=%zu slabs=%zu current=%zu limit_bytes=%zu used_bytes=%zu peak_bytes=%zu\n",
            sizeof(ErgoActorHeap), _Alignof(ErgoActorHeap),
            offsetof(ErgoActorHeap, slabs), offsetof(ErgoActorHeap, current),
            offsetof(ErgoActorHeap, limit_bytes), offsetof(ErgoActorHeap, used_bytes),
            offsetof(ErgoActorHeap, peak_bytes));
        printf("LAYOUT ErgoCrashReport size=%zu align=%zu panicked=%zu code=%zu msg=%zu heap_peak_bytes=%zu\n",
            sizeof(ErgoCrashReport), _Alignof(ErgoCrashReport),
            offsetof(ErgoCrashReport, panicked), offsetof(ErgoCrashReport, code),
            offsetof(ErgoCrashReport, msg), offsetof(ErgoCrashReport, heap_peak_bytes));
    }
}

/* ---- Perceus RC + own unique reuse tests ---- */

enum {
    TAG_NODE = 101u,
    TAG_SMALL = 102u,
    TAG_MEDIUM = 103u,
    TAG_LARGE = 104u
};

typedef struct TestNode {
    int32_t id;
    struct TestNode *left;
    struct TestNode *right;
} TestNode;

static int g_drop_log[16];
static int g_drop_count = 0;

static void reset_drop_log(void) {
    memset(g_drop_log, 0, sizeof(g_drop_log));
    g_drop_count = 0;
}

static void drop_node(void *payload) {
    TestNode *n = (TestNode *)payload;
    if (g_drop_count < 16) {
        g_drop_log[g_drop_count++] = n->id;
    }
    if (n->left != NULL) {
        ergo_rc_drop(n->left, drop_node);
        n->left = NULL;
    }
    if (n->right != NULL) {
        ergo_rc_drop(n->right, drop_node);
        n->right = NULL;
    }
}

static TestNode *alloc_node(int32_t id, TestNode *left, TestNode *right, uint32_t flags) {
    TestNode *n = (TestNode *)ergo_rc_alloc((uint32_t)sizeof(TestNode), TAG_NODE, flags);
    CHECK(n != NULL);
    CHECK(((uintptr_t)n % _Alignof(max_align_t)) == 0u);
    n->id = id;
    n->left = left;
    n->right = right;
    return n;
}

typedef struct StaticWrapper {
    _Alignas(max_align_t) ErgoRCHeader hdr;
    int32_t value;
} StaticWrapper;

static StaticWrapper g_static_obj = {
    ERGO_RC_STATIC_HEADER(sizeof(int32_t), 777u),
    42
};

static int g_static_drop_calls = 0;

static void drop_static_should_not_run(void *p) {
    (void)p;
    g_static_drop_calls++;
}

static void test_rc_dup_drop_recursive(void) {
    reset_drop_log();

    TestNode *leaf = alloc_node(1, NULL, NULL, 0u);
    CHECK(ergo_rc_count(leaf) == 1u);
    CHECK(ergo_rc_is_unique(leaf));
    CHECK(ergo_rc_type_tag(leaf) == TAG_NODE);
    CHECK(ergo_rc_payload_size(leaf) == sizeof(TestNode));
    CHECK(ergo_rc_capacity(leaf) == sizeof(TestNode));

    /* Shared diamond DAG: root has two refs to mid, mid references leaf */
    TestNode *mid = alloc_node(2, (TestNode *)ergo_rc_dup(leaf), NULL, 0u);
    CHECK(ergo_rc_count(leaf) == 2u);
    CHECK(!ergo_rc_is_unique(leaf));

    TestNode *root = alloc_node(3, (TestNode *)ergo_rc_dup(mid), (TestNode *)ergo_rc_dup(mid), 0u);
    CHECK(ergo_rc_count(mid) == 3u);

    /* Drop caller refs to leaf and mid; root still keeps both alive */
    ergo_rc_drop(leaf, drop_node);
    ergo_rc_drop(mid, drop_node);
    CHECK(ergo_rc_count(leaf) == 1u);
    CHECK(ergo_rc_count(mid) == 2u);
    CHECK(g_drop_count == 0);

    /* Drop root: drops root (3), decrements mid (2->1), decrements mid (1->0 -> drops 2 -> drops leaf 1) */
    ergo_rc_drop(root, drop_node);
    CHECK(g_drop_count == 3);
    CHECK(g_drop_log[0] == 3);
    CHECK(g_drop_log[1] == 2);
    CHECK(g_drop_log[2] == 1);
}

static void test_rc_shared_and_static(void) {
    reset_drop_log();
    TestNode *s1 = alloc_node(10, NULL, NULL, ERGO_RC_FLAG_SHARED);
    CHECK(ergo_rc_flags(s1) == ERGO_RC_FLAG_SHARED);
    CHECK(ergo_rc_is_unique(s1));
    CHECK(ergo_rc_dup(s1) == s1);
    CHECK(ergo_rc_count(s1) == 2u);
    CHECK(!ergo_rc_is_unique(s1));
    ergo_rc_drop(s1, drop_node);
    CHECK(ergo_rc_count(s1) == 1u);
    CHECK(ergo_rc_is_unique(s1));
    ergo_rc_drop(s1, drop_node);
    CHECK(g_drop_count == 1 && g_drop_log[0] == 10);

    /* Immortal static object: dup/drop are no-ops, never unique, never reused in place */
    void *imm = (void *)(&g_static_obj.hdr + 1);
    CHECK(ergo_rc_flags(imm) == ERGO_RC_FLAG_STATIC);
    CHECK(ergo_rc_count(imm) == 1u);
    CHECK(!ergo_rc_is_unique(imm));
    CHECK(ergo_rc_dup(imm) == imm);
    CHECK(ergo_rc_count(imm) == 1u);
    ergo_rc_drop(imm, drop_static_should_not_run);
    ergo_rc_drop(imm, drop_static_should_not_run);
    CHECK(g_static_drop_calls == 0);

    void *fresh = ergo_rc_reuse_or_alloc(imm, 8u, TAG_SMALL, drop_static_should_not_run);
    CHECK(fresh != NULL);
    CHECK(fresh != imm);
    CHECK(g_static_drop_calls == 0);
    CHECK(ergo_rc_flags(fresh) == 0u);
    CHECK(ergo_rc_is_unique(fresh));
    ergo_rc_drop(fresh, NULL);
}

static void test_rc_reuse_or_alloc(void) {
    reset_drop_log();

    /* Allocate 64 bytes with a child reference to ensure drop_old cleans children on reuse */
    TestNode *child = alloc_node(55, NULL, NULL, 0u);
    void *p = ergo_rc_alloc(64u, TAG_NODE, 0u);
    CHECK(p != NULL);
    TestNode *as_node = (TestNode *)p;
    as_node->id = 66;
    as_node->left = child;
    as_node->right = NULL;

    /* rc == 1, shrink to 16 bytes -> MUST return exact same pointer and preserve cap=64 */
    void *p_shrunk = ergo_rc_reuse_or_alloc(p, 16u, TAG_SMALL, drop_node);
    CHECK(p_shrunk == p);
    CHECK(g_drop_count == 2); /* node 66 then child 55 */
    CHECK(g_drop_log[0] == 66 && g_drop_log[1] == 55);
    CHECK(ergo_rc_type_tag(p_shrunk) == TAG_SMALL);
    CHECK(ergo_rc_payload_size(p_shrunk) == 16u);
    CHECK(ergo_rc_capacity(p_shrunk) == 64u);
    CHECK(ergo_rc_count(p_shrunk) == 1u);
    memset(p_shrunk, 0xAB, 16u);

    /* rc == 1, regrow to 48 bytes (<= preserved cap 64) -> MUST return exact same pointer without ASan overflow */
    void *p_regrown = ergo_rc_reuse_or_alloc(p_shrunk, 48u, TAG_MEDIUM, NULL);
    CHECK(p_regrown == p);
    CHECK(ergo_rc_type_tag(p_regrown) == TAG_MEDIUM);
    CHECK(ergo_rc_payload_size(p_regrown) == 48u);
    CHECK(ergo_rc_capacity(p_regrown) == 64u);
    memset(p_regrown, 0xCD, 48u);

    /* rc == 2 -> MUST NOT reuse even for smaller size; drops old ref and allocates fresh pointer */
    void *extra_ref = ergo_rc_dup(p_regrown);
    CHECK(ergo_rc_count(p_regrown) == 2u);
    void *p_aliased = ergo_rc_reuse_or_alloc(p_regrown, 8u, TAG_SMALL, NULL);
    CHECK(p_aliased != NULL);
    CHECK(p_aliased != p_regrown);
    CHECK(ergo_rc_count(extra_ref) == 1u);
    CHECK(ergo_rc_count(p_aliased) == 1u);
    ergo_rc_drop(extra_ref, NULL);

    /* rc == 1, grow beyond cap (8 -> 128) -> allocates new pointer and frees old */
    void *p_grown = ergo_rc_reuse_or_alloc(p_aliased, 128u, TAG_LARGE, NULL);
    CHECK(p_grown != NULL);
    CHECK(p_grown != p_aliased);
    CHECK(ergo_rc_payload_size(p_grown) == 128u);
    CHECK(ergo_rc_capacity(p_grown) == 128u);
    memset(p_grown, 0xEF, 128u);
    ergo_rc_drop(p_grown, NULL);

    /* NULL old_ptr allocates fresh */
    void *from_null = ergo_rc_reuse_or_alloc(NULL, 32u, TAG_SMALL, NULL);
    CHECK(from_null != NULL);
    CHECK(ergo_rc_count(from_null) == 1u);
    ergo_rc_drop(from_null, NULL);
}

/* ---- Scoped O(1) Bump Arena tests ---- */

static void test_arena(void) {
    /* Lazy arena (initial_cap = 0), mark before first chunk */
    ErgoArena *lazy = ergo_arena_create(0u);
    CHECK(lazy != NULL);
    CHECK(ergo_arena_used(lazy) == 0u);
    ErgoArenaMark m0 = ergo_arena_mark(lazy);
    void *a0 = ergo_arena_alloc(lazy, 64u, 32u);
    CHECK(a0 != NULL);
    CHECK(((uintptr_t)a0 & 31u) == 0u);
    memset(a0, 0x11, 64u);
    CHECK(ergo_arena_used(lazy) >= 64u);
    ergo_arena_reset(lazy, m0);
    CHECK(ergo_arena_used(lazy) == 0u);
    void *a1 = ergo_arena_alloc(lazy, 64u, 32u);
    CHECK(a1 == a0);
    ergo_arena_destroy(lazy);

    /* Chunked arena with small initial cap forcing multi-chunk growth + O(1) mark/reset */
    ErgoArena *arena = ergo_arena_create(128u);
    CHECK(arena != NULL);
    void *p1 = ergo_arena_alloc(arena, 64u, 16u);
    CHECK(p1 != NULL);
    CHECK(((uintptr_t)p1 & 15u) == 0u);
    memset(p1, 0x22, 64u);

    ErgoArenaMark mark = ergo_arena_mark(arena);
    size_t used_at_mark = ergo_arena_used(arena);
    CHECK(used_at_mark == 64u);

    /* Cross chunk boundaries repeatedly */
    void *first_after_mark = ergo_arena_alloc(arena, 100u, 8u);
    CHECK(first_after_mark != NULL);
    for (int i = 0; i < 8; i++) {
        void *blk = ergo_arena_alloc(arena, 200u, 64u);
        CHECK(blk != NULL);
        CHECK(((uintptr_t)blk & 63u) == 0u);
        memset(blk, (int)(0x30 + i), 200u);
    }
    CHECK(ergo_arena_used(arena) > used_at_mark + 1600u);

    /* O(1) reset restores exact byte accounting and reuses existing chunks */
    ergo_arena_reset(arena, mark);
    CHECK(ergo_arena_used(arena) == used_at_mark);
    void *reused_after_mark = ergo_arena_alloc(arena, 100u, 8u);
    CHECK(reused_after_mark == first_after_mark);
    CHECK(((const uint8_t *)p1)[0] == 0x22 && ((const uint8_t *)p1)[63] == 0x22);

    ergo_arena_destroy(arena);
}

/* ---- BEAM/OTP Per-Actor Bounded Heap & die isolation tests ---- */

static void test_actor_heap_limits(void) {
    ErgoActorHeap heap;
    ergo_actor_heap_init(&heap, 256u);

    ErgoStatus st = ERGO_STATUS_OK;
    void *b1 = ergo_actor_heap_alloc(&heap, 128u, 8u, &st);
    CHECK(b1 != NULL && st == ERGO_STATUS_OK);
    CHECK(heap.used_bytes == 128u && heap.peak_bytes == 128u);
    memset(b1, 0x41, 128u);

    void *b2 = ergo_actor_heap_alloc(&heap, 128u, 8u, &st);
    CHECK(b2 != NULL && st == ERGO_STATUS_OK);
    CHECK(heap.used_bytes == 256u && heap.peak_bytes == 256u);
    memset(b2, 0x42, 128u);

    /* Exact ceiling refusal before exceeding limit_bytes */
    void *b3 = ergo_actor_heap_alloc(&heap, 1u, 1u, &st);
    CHECK(b3 == NULL && st == ERGO_STATUS_OVER_BYTES);
    CHECK(heap.used_bytes == 256u && heap.peak_bytes == 256u);

    ergo_actor_heap_destroy(&heap);
    CHECK(heap.used_bytes == 0u && heap.peak_bytes == 256u);

    /* Unlimited heap (limit_bytes = 0) across multiple slabs + jumbo */
    ErgoActorHeap unlimited;
    ergo_actor_heap_init(&unlimited, 0u);
    for (int i = 0; i < 16; i++) {
        void *s = ergo_actor_heap_alloc(&unlimited, 1024u, 16u, &st);
        CHECK(s != NULL && st == ERGO_STATUS_OK);
        memset(s, i, 1024u);
    }
    void *jumbo = ergo_actor_heap_alloc(&unlimited, 131072u, 32u, &st);
    CHECK(jumbo != NULL && st == ERGO_STATUS_OK);
    CHECK(((uintptr_t)jumbo & 31u) == 0u);
    memset(jumbo, 0x77, 131072u);
    CHECK(unlimited.peak_bytes >= 16u * 1024u + 131072u);
    ergo_actor_heap_destroy(&unlimited);
}

static void actor_normal_fn(void *arg) {
    int *out = (int *)arg;
    void *buf = ergo_actor_alloc(128u, 8u);
    CHECK(buf != NULL);
    memset(buf, 0x55, 128u);
    ergo_actor_step(4u);
    ergo_actor_step(6u);
    CHECK(ergo_actor_steps_used() == 10u);
    *out = 99;
}

static void actor_die_with_heap_msg_fn(void *arg) {
    (void)arg;
    for (int i = 0; i < 6; i++) {
        void *slab_fill = ergo_actor_alloc(2048u, 16u);
        memset(slab_fill, i, 2048u);
    }
    const char *text = "boom from actor heap";
    size_t len = strlen(text);
    char *heap_msg = (char *)ergo_actor_alloc(len + 1u, 1u);
    memcpy(heap_msg, text, len + 1u);
    ergo_die(42u, ergo_str_make(heap_msg, (int64_t)len));
}

static void actor_over_bytes_fn(void *arg) {
    (void)arg;
    for (;;) {
        (void)ergo_actor_alloc(128u, 8u);
    }
}

static void actor_over_steps_fn(void *arg) {
    int *ticks = (int *)arg;
    for (;;) {
        ergo_actor_step(3u);
        (*ticks)++;
    }
}

static void actor_outer_spawns_inner_fn(void *arg) {
    int *outer_done = (int *)arg;
    void *outer_mem = ergo_actor_alloc(256u, 8u);
    memset(outer_mem, 0x1A, 256u);

    char inner_buf[128];
    ErgoActorLimits inner_lim = { 1024u, 5u };
    int inner_ticks = 0;
    ErgoCrashReport inner_rep = ergo_actor_run_isolated(
        actor_over_steps_fn, &inner_ticks, inner_lim, inner_buf, sizeof(inner_buf)
    );
    CHECK(inner_rep.panicked);
    CHECK(inner_rep.code == ERGO_DIE_OVER_STEPS);
    CHECK(inner_ticks == 1);

    /* Outer actor context must be restored intact */
    ergo_actor_step(2u);
    CHECK(ergo_actor_steps_used() == 2u);
    CHECK(((const uint8_t *)outer_mem)[0] == 0x1A);
    *outer_done = 1;
}

static void test_actor_isolation_and_steps(void) {
    char msg_buf[64];

    /* 1. Normal completion within Limits.Bytes and Limits.Steps */
    int result = 0;
    ErgoActorLimits ok_lim = { 4096u, 10u };
    ErgoCrashReport ok_rep = ergo_actor_run_isolated(
        actor_normal_fn, &result, ok_lim, msg_buf, sizeof(msg_buf)
    );
    CHECK(!ok_rep.panicked);
    CHECK(ok_rep.code == 0u);
    CHECK(ok_rep.msg == NULL);
    CHECK(ok_rep.heap_peak_bytes == 128u);
    CHECK(result == 99);

    /* 2. ergo_die with actor-heap-allocated message: copied before heap destroy, zero UAF/leaks */
    ErgoActorLimits die_lim = { 0u, 0u };
    ErgoCrashReport die_rep = ergo_actor_run_isolated(
        actor_die_with_heap_msg_fn, NULL, die_lim, msg_buf, sizeof(msg_buf)
    );
    CHECK(die_rep.panicked);
    CHECK(die_rep.code == 42u);
    CHECK(die_rep.msg != NULL && strcmp(die_rep.msg, "boom from actor heap") == 0);
    CHECK(die_rep.heap_peak_bytes >= 6u * 2048u);

    /* 3. Truncation safety for short caller buffer */
    char tiny_buf[8];
    ErgoCrashReport trunc_rep = ergo_actor_run_isolated(
        actor_die_with_heap_msg_fn, NULL, die_lim, tiny_buf, sizeof(tiny_buf)
    );
    CHECK(trunc_rep.panicked);
    CHECK(trunc_rep.msg != NULL && strcmp(trunc_rep.msg, "boom fr") == 0);

    /* 4. Limits.Bytes trap via ergo_actor_alloc */
    ErgoActorLimits bytes_lim = { 512u, 0u };
    ErgoCrashReport bytes_rep = ergo_actor_run_isolated(
        actor_over_bytes_fn, NULL, bytes_lim, msg_buf, sizeof(msg_buf)
    );
    CHECK(bytes_rep.panicked);
    CHECK(bytes_rep.code == ERGO_DIE_OVER_BYTES);
    CHECK(bytes_rep.heap_peak_bytes <= 512u);
    CHECK(bytes_rep.heap_peak_bytes == 512u);

    /* 5. Limits.Steps trap via ergo_actor_step */
    int ticks = 0;
    ErgoActorLimits steps_lim = { 1024u, 10u };
    ErgoCrashReport steps_rep = ergo_actor_run_isolated(
        actor_over_steps_fn, &ticks, steps_lim, msg_buf, sizeof(msg_buf)
    );
    CHECK(steps_rep.panicked);
    CHECK(steps_rep.code == ERGO_DIE_OVER_STEPS);
    CHECK(ticks == 3); /* 3*3=9 ok, 4th step (+3=12 > 10) traps before incrementing ticks */

    /* 6. Nested isolated actors */
    int outer_done = 0;
    ErgoActorLimits outer_lim = { 4096u, 10u };
    ErgoCrashReport outer_rep = ergo_actor_run_isolated(
        actor_outer_spawns_inner_fn, &outer_done, outer_lim, msg_buf, sizeof(msg_buf)
    );
    CHECK(!outer_rep.panicked);
    CHECK(outer_done == 1);
}

int main(int argc, char **argv) {
    bool verbose_layout = (argc > 1 && strcmp(argv[1], "--layout") == 0);
    emit_and_check_layouts(verbose_layout);
    test_rc_dup_drop_recursive();
    test_rc_shared_and_static();
    test_rc_reuse_or_alloc();
    test_arena();
    test_actor_heap_limits();
    test_actor_isolation_and_steps();

    if (g_failures != 0) {
        fprintf(stderr, "ergo_rt_test: %d check(s) failed\n", g_failures);
        return 1;
    }
    printf("ergo_rt_test: all checks passed\n");
    return 0;
}
