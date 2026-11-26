//go:build cgo && unix

package kernelprobe

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef int (*kp_tool_cb)(uintptr_t ctx_id, const char* tool_name, const char* args_json, int start_async, char** out_json);
typedef void (*kp_free_cb)(char* p);

typedef void* (*fn_kp_new)(kp_tool_cb cb, kp_free_cb free_cb, uintptr_t ctx_id);
typedef void (*fn_kp_destroy)(void* e);
typedef char* (*fn_kp_eval)(void* e, const char* req_json);
typedef char* (*fn_kp_restore)(void* e, const char* snap_json);
typedef char* (*fn_kp_snapshot)(void* e);
typedef char* (*fn_kp_cut)(void* e, const char* reason);
typedef void (*fn_kp_kill)(void* e);
typedef void (*fn_kp_free)(char* s);

typedef struct {
    void* handle;
    void* eng;
    fn_kp_new kp_new;
    fn_kp_destroy kp_destroy;
    fn_kp_eval kp_eval;
    fn_kp_restore kp_restore;
    fn_kp_snapshot kp_snapshot;
    fn_kp_cut kp_cut;
    fn_kp_kill kp_kill;
    fn_kp_free kp_free;
} kp_instance;

extern int kpGoToolBridge(uintptr_t ctx_id, char* tool_name, char* args_json, int start_async, char** out_json);

static void kp_set_out(char** out, char* val) {
    if (out) {
        *out = val;
    } else if (val) {
        free(val);
    }
}

static kp_instance* kp_open_instance(const char* path, uintptr_t ctx_id, char** err_out) {
    void* h = dlopen(path, RTLD_NOW | RTLD_LOCAL);
    if (!h) {
        const char* e = dlerror();
        if (err_out) *err_out = strdup(e ? e : "dlopen failed");
        return NULL;
    }
    kp_instance* inst = (kp_instance*)calloc(1, sizeof(kp_instance));
    if (!inst) {
        dlclose(h);
        if (err_out) *err_out = strdup("out of memory");
        return NULL;
    }
    inst->handle = h;
    inst->kp_new = (fn_kp_new)dlsym(h, "kp_new");
    inst->kp_destroy = (fn_kp_destroy)dlsym(h, "kp_destroy");
    inst->kp_eval = (fn_kp_eval)dlsym(h, "kp_eval");
    inst->kp_restore = (fn_kp_restore)dlsym(h, "kp_restore");
    inst->kp_snapshot = (fn_kp_snapshot)dlsym(h, "kp_snapshot");
    inst->kp_cut = (fn_kp_cut)dlsym(h, "kp_cut");
    inst->kp_kill = (fn_kp_kill)dlsym(h, "kp_kill");
    inst->kp_free = (fn_kp_free)dlsym(h, "kp_free");
    if (!inst->kp_new || !inst->kp_destroy || !inst->kp_eval || !inst->kp_restore ||
        !inst->kp_snapshot || !inst->kp_cut || !inst->kp_kill || !inst->kp_free) {
        if (err_out) *err_out = strdup("missing required kp_* symbol in shared library");
        dlclose(h);
        free(inst);
        return NULL;
    }
    inst->eng = inst->kp_new((kp_tool_cb)kpGoToolBridge, (kp_free_cb)free, ctx_id);
    if (!inst->eng) {
        if (err_out) *err_out = strdup("kp_new returned null");
        dlclose(h);
        free(inst);
        return NULL;
    }
    return inst;
}

static void kp_close_instance(kp_instance* inst) {
    if (!inst) return;
    if (inst->eng && inst->kp_destroy) {
        inst->kp_destroy(inst->eng);
        inst->eng = NULL;
    }
    if (inst->handle) {
        dlclose(inst->handle);
        inst->handle = NULL;
    }
    free(inst);
}

static char* kp_inst_eval(kp_instance* inst, const char* req) {
    return inst->kp_eval(inst->eng, req);
}
static char* kp_inst_restore(kp_instance* inst, const char* snap) {
    return inst->kp_restore(inst->eng, snap);
}
static char* kp_inst_snapshot(kp_instance* inst) {
    return inst->kp_snapshot(inst->eng);
}
static char* kp_inst_cut(kp_instance* inst, const char* reason) {
    return inst->kp_cut(inst->eng, reason);
}
static void kp_inst_kill(kp_instance* inst) {
    inst->kp_kill(inst->eng);
}
static void kp_inst_free(kp_instance* inst, char* p) {
    inst->kp_free(p);
}
static void kp_free_c(char* p) {
    free(p);
}
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

//export kpGoToolBridge
func kpGoToolBridge(ctxID C.uintptr_t, cTool *C.char, cArgs *C.char, startAsync C.int, outJSON **C.char) C.int {
	b := lookupBridge(uintptr(ctxID))
	if b == nil {
		C.kp_set_out(outJSON, C.CString(`{"kind":"MissingBridge","message":"engine bridge gone"}`))
		return -1
	}
	rc, out := b.handleTool(C.GoString(cTool), C.GoString(cArgs), startAsync != 0)
	if out != "" {
		C.kp_set_out(outJSON, C.CString(out))
	}
	return C.int(rc)
}

type dlWire struct {
	mu     sync.Mutex
	closed bool
	inst   *C.kp_instance
}

func (w *dlWire) takeCStr(p *C.char) string {
	if p == nil {
		return ""
	}
	s := C.GoString(p)
	C.kp_inst_free(w.inst, p)
	return s
}

func (w *dlWire) EvalJSON(reqJSON string) (string, error) {
	w.mu.Lock()
	if w.closed || w.inst == nil {
		w.mu.Unlock()
		return "", ErrEngineClosed
	}
	inst := w.inst
	w.mu.Unlock()
	cReq := C.CString(reqJSON)
	defer C.kp_free_c(cReq)
	return w.takeCStr(C.kp_inst_eval(inst, cReq)), nil
}

func (w *dlWire) RestoreJSON(snapJSON string) (string, error) {
	w.mu.Lock()
	if w.closed || w.inst == nil {
		w.mu.Unlock()
		return "", ErrEngineClosed
	}
	inst := w.inst
	w.mu.Unlock()
	cSnap := C.CString(snapJSON)
	defer C.kp_free_c(cSnap)
	return w.takeCStr(C.kp_inst_restore(inst, cSnap)), nil
}

func (w *dlWire) SnapshotJSON() (string, error) {
	w.mu.Lock()
	if w.closed || w.inst == nil {
		w.mu.Unlock()
		return "", ErrEngineClosed
	}
	inst := w.inst
	w.mu.Unlock()
	return w.takeCStr(C.kp_inst_snapshot(inst)), nil
}

func (w *dlWire) CutJSON(reason string) (string, error) {
	w.mu.Lock()
	if w.closed || w.inst == nil {
		w.mu.Unlock()
		return "", ErrEngineClosed
	}
	inst := w.inst
	w.mu.Unlock()
	cReason := C.CString(reason)
	defer C.kp_free_c(cReason)
	return w.takeCStr(C.kp_inst_cut(inst, cReason)), nil
}

func (w *dlWire) Kill() {
	w.mu.Lock()
	if w.closed || w.inst == nil {
		w.mu.Unlock()
		return
	}
	inst := w.inst
	w.mu.Unlock()
	C.kp_inst_kill(inst)
}

func (w *dlWire) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	inst := w.inst
	w.inst = nil
	w.mu.Unlock()
	if inst != nil {
		C.kp_close_instance(inst)
	}
}

func defaultLibDir() string {
	if d := os.Getenv("KERNELPROBE_LIB_DIR"); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "kernelprobe", "lib")
}

func newNativeEngine(name, soName string, tools *ToolRegistry, slots *SlotPool) (KernelEngine, error) {
	if tools == nil {
		tools = NewToolRegistry(nil)
	}
	if slots == nil {
		slots = NewSlotPool(MaxRoleLiveSlots)
	}
	libPath := soName
	if !filepath.IsAbs(libPath) {
		libPath = filepath.Join(defaultLibDir(), soName)
	}
	if _, err := os.Stat(libPath); err != nil {
		return nil, fmt.Errorf("%w: %s (%v)", ErrNativeUnavailable, libPath, err)
	}
	eng := &nativeEngine{
		name:     name,
		libPath:  libPath,
		tools:    tools,
		slots:    slots,
		bindings: make(map[string]any),
	}
	bridge := &evalBridge{
		engine: eng,
		maps:   make(map[string]*mapTracker),
	}
	id := registerBridge(bridge)
	eng.bridge = bridge
	eng.bridgeID = id

	cPath := C.CString(libPath)
	defer C.kp_free_c(cPath)
	var cErr *C.char
	inst := C.kp_open_instance(cPath, C.uintptr_t(id), &cErr)
	if inst == nil {
		unregisterBridge(id)
		msg := "dlopen failed"
		if cErr != nil {
			msg = C.GoString(cErr)
			C.kp_free_c(cErr)
		}
		return nil, fmt.Errorf("%w: %s: %s", ErrNativeUnavailable, libPath, msg)
	}
	eng.wire = &dlWire{inst: inst}
	return eng, nil
}

// NewPsychoscriptEngine loads libpsychoscript_c.so and returns a KernelEngine backed by psychoscript.
func NewPsychoscriptEngine(tools *ToolRegistry, slots *SlotPool) (KernelEngine, error) {
	name := os.Getenv("KERNELPROBE_PSYCHOSCRIPT_LIB")
	if name == "" {
		name = "libpsychoscript_c.so"
	}
	return newNativeEngine("psychoscript", name, tools, slots)
}

// NewLuauEngine loads libluau_c.so and returns a KernelEngine backed by Luau.
func NewLuauEngine(tools *ToolRegistry, slots *SlotPool) (KernelEngine, error) {
	name := os.Getenv("KERNELPROBE_LUAU_LIB")
	if name == "" {
		name = "libluau_c.so"
	}
	return newNativeEngine("luau", name, tools, slots)
}

var _ = errors.Is
