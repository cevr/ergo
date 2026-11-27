#include "ergo_rt.h"

#include <stdlib.h>
#include <string.h>

_Noreturn void ergo_die(uint32_t code, ErgoStr msg) {
    ErgoActorContext *ctx = ergo_actor_tls_get();
    if (ctx != NULL && ctx->active) {
        if (ctx->msg_buf != NULL && ctx->msg_buf_cap > 0u) {
            size_t copy_len = 0u;
            if (msg.ptr != NULL && msg.len > 0) {
                size_t raw_len = (size_t)msg.len;
                copy_len = raw_len < (ctx->msg_buf_cap - 1u) ? raw_len : (ctx->msg_buf_cap - 1u);
                memcpy(ctx->msg_buf, msg.ptr, copy_len);
            }
            ctx->msg_buf[copy_len] = '\0';
        }
        ctx->die_code = code;
        ctx->panicked = true;
        longjmp(ctx->jmp, 1);
    }
    abort();
}
