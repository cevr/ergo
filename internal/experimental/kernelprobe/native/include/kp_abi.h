#ifndef KP_ABI_H
#define KP_ABI_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct kp_engine kp_engine;

// kp_tool_cb invokes a bound host tool in Go.
// Return values:
//   0  -> completed (*out_json holds {"value": ...})
//   1  -> unfinished (*out_json holds {"handle": ...}; VM must stop without running later lines when !start_async)
//   2  -> cut requested externally
//  -1  -> error (*out_json holds {"kind": "...", "message": "..."})
typedef int (*kp_tool_cb)(uintptr_t ctx_id, const char* tool_name, const char* args_json, int start_async, char** out_json);
typedef void (*kp_free_cb)(char* p);

kp_engine* kp_new(kp_tool_cb cb, kp_free_cb free_cb, uintptr_t ctx_id);
void kp_destroy(kp_engine* e);
char* kp_eval(kp_engine* e, const char* req_json);
char* kp_restore(kp_engine* e, const char* snap_json);
char* kp_snapshot(kp_engine* e);
char* kp_cut(kp_engine* e, const char* reason);
void kp_kill(kp_engine* e);
void kp_free(char* s);

#ifdef __cplusplus
}
#endif

#endif
