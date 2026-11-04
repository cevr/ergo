package bindc

import (
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// This file holds the guards of the handle-key rules (bind-c-2 round 2,
// findings 2b M1 and M2). A handle key gives the trampoline only the key's
// fields, so one key must name one live registration:
//   - a function binds at most one position of a handle-keyed typedef;
//   - a registered handle-keyed typedef has at most one function that
//     registers it typed;
//   - a call-scoped handle-keyed callback binds only in a function that
//     takes a handle with thread owner, so two calls with one key overlap
//     only by nesting (which the restore of the replaced entry handles);
//   - a registered handle-keyed callback binds only when its owner handle
//     has thread owner (round 3, 2b r2 M1).
// Otherwise Bind makes the function (or the typedef) raw-only with the
// reason. The verifier's probe headers (verify-2b/receipts/probes) are the
// fixed cases; the live tests run the probes' scenarios on the forms that
// stay typed. Round 3 adds the owner's thread to the registry key, the
// thread check (ErrWrongThread) and the refusal of a key that another
// handle of the thread holds (ErrKeyInUse): TestLiveCallbackOwnerKeys.

// keyProbeShared is the verifier's shared-key probe header (M1).
const keyProbeShared = `#define G_FAIL INT32_C(1)
typedef void (*g_m)(uint64_t context_id, uint32_t slot, uint32_t generation, float w);
g_status g_two(g_h *h, const g_node *node, g_m a, g_m b);
g_status g_call_a(g_h *h, const g_node *node);
g_status g_call_b(g_h *h, const g_node *node);
g_status g_fail_next(g_h *h);
g_status g_set_x(g_h *h, const g_node *node, g_m cb);
g_status g_set_y(g_h *h, const g_node *node, g_m cb);
g_status g_call_x(g_h *h, const g_node *node);
g_status g_call_y(g_h *h, const g_node *node);
`

// keyProbeOverlap is the verifier's overlap probe header (M2), with an
// owner-thread variant g_visit_h.
const keyProbeOverlap = `typedef int32_t (*g_v)(uint64_t context_id, uint32_t slot, uint32_t generation, uint32_t v);
g_status g_visit(const g_node *node, g_v cb);
g_status g_visit_h(g_h *h, const g_node *node, g_v cb);
`

var (
	keyRegistered = CallbackRule{Typedef: "g_m", Scope: ScopeRegistered, Owner: "g_h",
		Key: KeyRule{Params: []string{"context_id", "slot", "generation"}, Source: "node"}, Why: "probe"}
	keyCall = CallbackRule{Typedef: "g_v", Scope: ScopeCall, Default: []byte("-1"),
		Key: KeyRule{Params: []string{"context_id", "slot", "generation"}, Source: "node"}, Why: "probe"}
)

// keyBind binds cbPrelude+decls with the entries, and g_h's thread.
func keyBind(t *testing.T, decls, thread string, entries ...CallbackRule) *Binding {
	t.Helper()
	src := cbPrelude + decls
	c := cbContract(src)
	c.Handles[0].Thread = thread
	c.Callbacks = entries
	b, err := cbBind(t, src, c)
	if err != nil {
		t.Fatalf("Bind: %v\n%s", err, src)
	}
	return b
}

// rawOnly is the raw-only reason of function name in b ("" when typed).
func rawOnly(t *testing.T, b *Binding, name string) string {
	t.Helper()
	for _, f := range b.Funcs {
		if f.C.Name == name {
			return f.RawOnly
		}
	}
	t.Fatalf("no function %s", name)
	return ""
}

// TestCallbackHandleKeys: the verifier's probe headers bind, and each
// function whose callbacks would share one registry entry is raw-only with
// the reason; the forms that keep one key per live registration stay typed.
func TestCallbackHandleKeys(t *testing.T) {
	type want struct {
		fn, reason string // reason "" means typed
	}
	cases := []struct {
		name, decls, thread string
		entries             []CallbackRule
		typedef             string // the typedef's raw reason, or ""
		funcs               []want
	}{
		{name: "the shared-key probe: three registering functions", decls: keyProbeShared, thread: "owner",
			entries: []CallbackRule{keyRegistered},
			typedef: "g_m has a handle key and 3 functions that register it (g_two, g_set_x, g_set_y)",
			funcs: []want{{"g_two", "callback parameter: g_m has a handle key"}, {"g_set_x", "callback parameter: g_m has a handle key"},
				{"g_set_y", "callback parameter: g_m has a handle key"}, {"g_call_a", ""}}},
		{name: "two positions of one registered typedef", decls: "typedef void (*g_m)(uint64_t context_id, uint32_t slot, uint32_t generation, float w);\n" +
			"g_status g_two(g_h *h, const g_node *node, g_m a, g_m b);\n", thread: "owner",
			entries: []CallbackRule{keyRegistered},
			funcs:   []want{{"g_two", "param 3 (b): its handle key param 1 (node) also serves param 2 (a)"}}},
		{name: "two positions of one call-scoped typedef", decls: "typedef int32_t (*g_v)(uint64_t context_id, uint32_t slot, uint32_t generation, uint32_t v);\n" +
			"g_status g_two(g_h *h, const g_node *node, g_v a, g_v b);\n", thread: "owner",
			entries: []CallbackRule{keyCall},
			funcs:   []want{{"g_two", "param 3 (b): its handle key param 1 (node) also serves param 2 (a)"}}},
		{name: "two registering functions", decls: "typedef void (*g_m)(uint64_t context_id, uint32_t slot, uint32_t generation, float w);\n" +
			"g_status g_set_x(g_h *h, const g_node *node, g_m cb);\ng_status g_set_y(g_h *h, const g_node *node, g_m cb);\n", thread: "owner",
			entries: []CallbackRule{keyRegistered},
			typedef: "g_m has a handle key and 2 functions that register it (g_set_x, g_set_y)",
			funcs:   []want{{"g_set_x", "g_m has a handle key"}, {"g_set_y", "g_m has a handle key"}}},
		{name: "one registering function and a function without the key", decls: "typedef void (*g_m)(uint64_t context_id, uint32_t slot, uint32_t generation, float w);\n" +
			"g_status g_set_x(g_h *h, const g_node *node, g_m cb);\ng_status g_other(g_h *h, g_m cb);\n", thread: "owner",
			entries: []CallbackRule{keyRegistered},
			funcs:   []want{{"g_set_x", ""}, {"g_other", "the key (context_id, slot, generation) of g_m has no source"}}},
		{name: "the overlap probe: owner thread", decls: keyProbeOverlap, thread: "owner", entries: []CallbackRule{keyCall},
			funcs: []want{{"g_visit", "call-scoped g_v has a handle key, so it needs a parameter `H *` of a handle with thread owner"},
				{"g_visit_h", ""}}},
		{name: "the overlap probe: a handle with thread any", decls: keyProbeOverlap, thread: "any", entries: []CallbackRule{keyCall},
			funcs: []want{{"g_visit", "needs a parameter `H *` of a handle with thread owner"},
				{"g_visit_h", "needs a parameter `H *` of a handle with thread owner"}}},
		{name: "a registered handle key whose owner has thread any", decls: "typedef void (*g_m)(uint64_t context_id, uint32_t slot, uint32_t generation, float w);\n" +
			"g_status g_set_x(g_h *h, const g_node *node, g_m cb);\n", thread: "any",
			entries: []CallbackRule{keyRegistered},
			typedef: "g_m has a handle key, and its owner g_h has thread any: the registry key holds the owner's OS thread",
			funcs:   []want{{"g_set_x", "g_m has a handle key, and its owner g_h has thread any"}}},
		{name: "two call-scoped functions with an owner-thread handle", decls: "typedef int32_t (*g_v)(uint64_t context_id, uint32_t slot, uint32_t generation, uint32_t v);\n" +
			"g_status g_visit_a(g_h *h, const g_node *node, g_v cb);\ng_status g_visit_b(g_h *h, const g_node *node, g_v cb);\n", thread: "owner",
			entries: []CallbackRule{keyCall},
			funcs:   []want{{"g_visit_a", ""}, {"g_visit_b", ""}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := keyBind(t, tc.decls, tc.thread, tc.entries...)
			for _, bc := range b.Callbacks {
				if bc.Decl.Name != tc.entries[0].Typedef {
					continue
				}
				if tc.typedef == "" && !bc.Bound() {
					t.Errorf("%s is raw: %s; want bound", bc.Decl.Name, bc.RawOnly)
				}
				if tc.typedef != "" && !strings.Contains(bc.RawOnly, tc.typedef) {
					t.Errorf("%s: raw reason %q; want %q", bc.Decl.Name, bc.RawOnly, tc.typedef)
				}
			}
			for _, w := range tc.funcs {
				got := rawOnly(t, b, w.fn)
				switch {
				case w.reason == "" && got != "":
					t.Errorf("%s is raw-only: %s; want typed", w.fn, got)
				case w.reason != "" && !strings.Contains(got, w.reason):
					t.Errorf("%s: raw-only %q; want a reason with %q", w.fn, got, w.reason)
				}
			}
			// The report states each reason.
			rep := string(Report(b))
			for _, w := range tc.funcs {
				if w.reason != "" && !strings.Contains(rep, w.fn+": ") {
					t.Errorf("the report does not name %s", w.fn)
				}
			}
		})
	}
}

// ownerKeyDecls is the verifier's two-owner probe (bind-c-2 r2, 2b M1): the
// key (slot, generation) of g_m and g_v does not name the owner g_h, so its
// values repeat from one handle to the next. g_drop ends a registration of
// g_m; g_calls counts the g_set and g_visit calls C saw for a handle.
const ownerKeyDecls = `#define G_FAIL INT32_C(1)
typedef struct g_k { uint32_t slot; uint32_t generation; } g_k;
typedef void (*g_m)(uint32_t slot, uint32_t generation, float w);
typedef int32_t (*g_v)(uint32_t slot, uint32_t generation, uint32_t v);
g_status g_set(g_h *h, const g_k *node, g_m cb);
g_status g_drop(g_h *h, const g_k *node);
g_status g_call(g_h *h, const g_k *node);
g_status g_visit(g_h *h, const g_k *node, g_v cb);
g_status g_calls(g_h *h, uint32_t *out_n);
`

const ownerKeyC = `#include "cb.h"
#include <stddef.h>
#include <stdlib.h>
struct g_h { g_m m[4]; uint32_t calls; };
uint32_t g_abi(void) { return 1; }
g_status g_create(g_h **out_h) { *out_h = calloc(1, sizeof(g_h)); return *out_h ? G_OK : G_FAIL; }
g_status g_destroy(g_h *h) { free(h); return G_OK; }
g_status g_set(g_h *h, const g_k *n, g_m cb) {
    h->calls++;
    if (n->slot >= 4) return G_FAIL;
    h->m[n->slot] = cb;
    return G_OK;
}
g_status g_drop(g_h *h, const g_k *n) {
    if (n->slot >= 4) return G_FAIL;
    h->m[n->slot] = NULL;
    return G_OK;
}
g_status g_call(g_h *h, const g_k *n) {
    if (n->slot >= 4 || h->m[n->slot] == NULL) return G_FAIL;
    h->m[n->slot](n->slot, n->generation, 1.0f);
    return G_OK;
}
g_status g_visit(g_h *h, const g_k *n, g_v cb) {
    h->calls++;
    if (cb == NULL) return G_OK;
    cb(n->slot, n->generation, 1);
    cb(n->slot, n->generation, 2);
    return G_OK;
}
g_status g_calls(g_h *h, uint32_t *out_n) { *out_n = h->calls; return G_OK; }
`

// TestLiveCallbackOwnerKeys (ERGO_BINDC_LIVE=1; bind-c-2 r2, 2b M1): a
// handle key whose fields do not name the owner (testdata/live/ownerkeys).
// The registry key holds the owner's thread, so on two owner threads with
// equal key values each handle runs its own func, and a destroy of one
// keeps the other's registration; on one thread a second handle with an
// equal key value gets ErrKeyInUse (no registry change, no C call); and a
// call from a thread that did not create the handle gets ErrWrongThread
// (no registry change, no C call). Then max(200, bindc.cases/5) generated
// sequences of up to 40 commands (set, clear, end, invoke, call-scoped
// call, destroy) over 4 handles on 2 owner threads with 2 key values, from
// the right and the wrong thread, run against a reference model: each
// invocation runs the func the model has for its owner thread and key, and
// LiveCallbacks equals the model's count after each command.
func TestLiveCallbackOwnerKeys(t *testing.T) {
	e := newLiveEnv(t)
	cc := requireCC(t)
	src := cbPrelude + ownerKeyDecls
	c := cbContract(src)
	c.Callbacks = []CallbackRule{
		{Typedef: "g_m", Scope: ScopeRegistered, Owner: "g_h", Ends: []string{"g_drop"},
			Key: KeyRule{Params: []string{"slot", "generation"}, Source: "node"}, Why: "probe"},
		{Typedef: "g_v", Scope: ScopeCall, Default: []byte("-1"),
			Key: KeyRule{Params: []string{"slot", "generation"}, Source: "node"}, Why: "probe"},
	}
	b, err := cbBind(t, src, c)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	for _, f := range b.Funcs {
		if f.RawOnly != "" {
			t.Fatalf("%s is raw-only: %s", f.C.Name, f.RawOnly)
		}
	}
	name, gosrc, err := GoPackage(b)
	if err != nil {
		t.Fatalf("GoPackage: %v", err)
	}
	e.write(filepath.Join(e.mod, "cb", name), gosrc)
	cdir := filepath.Join(e.root, "c")
	e.write(filepath.Join(cdir, "cb.h"), []byte(src))
	e.write(filepath.Join(cdir, "gen.c"), []byte(ownerKeyC))
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	lib := filepath.Join(e.root, "libcb"+ext)
	e.run(cdir, "", nil, cc, "-std=c11", "-Wall", "-Werror", "-shared", "-fPIC", "-o", lib, "gen.c")
	e.write(filepath.Join(e.mod, "driver", "main.go"), readInput(t, "testdata/live/ownerkeys/main.go"))
	e.run(e.mod, "", nil, e.goTool, "vet", "./...")
	bin := filepath.Join(e.root, "driver-bin")
	e.run(e.mod, "", nil, e.goTool, "build", "-o", bin, "./driver")
	n := max(200, *propCases/5)
	out, err := e.try(e.root, "", nil, bin, lib, strconv.FormatUint(*propSeed, 10), strconv.Itoa(n))
	want := []string{
		"two threads, one key value: h1 runs A and h2 runs B",
		"two threads: after GDestroy(h2), h1 still runs A and LiveCallbacks is 1",
		"two threads, call scope: overlapping calls with one key value each run their own func",
		"one thread, one key value: a second handle gets ErrKeyInUse, no C call, and h1 still runs A",
		"one thread: an end and a destroy of the second handle keep h1's registration",
		"one thread, call scope: a nested call of another handle with one key value gets ErrKeyInUse",
		"another thread: each call that changes h1's registrations gets ErrWrongThread, and nothing changes",
		"generated sequences: each invocation ran the func the model has for its owner thread and key, and LiveCallbacks matched",
	}
	if got := okLines(out); err != nil || !slices.Equal(got, want) {
		t.Fatalf("the driver (seed %d, %d sequences): %v\ngot  %q\nwant %q\n%s", *propSeed, n, err, got, want, tail(out, 3000))
	}
	t.Log(strings.TrimSpace(out[strings.Index(out, "model: "):strings.Index(out, "\nok: generated")]))
}

// keyLiveDecls are the probes' scenarios in the forms that stay typed: two
// typedefs for the two positions of g_two, one typedef per setter, and a
// call-scoped g_visit that takes the owner-thread handle.
const keyLiveDecls = `#define G_FAIL INT32_C(1)
typedef void (*g_ma)(uint64_t context_id, uint32_t slot, uint32_t generation, float w);
typedef void (*g_mb)(uint64_t context_id, uint32_t slot, uint32_t generation, float w);
typedef void (*g_mx)(uint64_t context_id, uint32_t slot, uint32_t generation, float w);
typedef void (*g_my)(uint64_t context_id, uint32_t slot, uint32_t generation, float w);
typedef int32_t (*g_v)(uint64_t context_id, uint32_t slot, uint32_t generation, uint32_t v);
g_status g_two(g_h *h, const g_node *node, g_ma a, g_mb b);
g_status g_call_a(g_h *h, const g_node *node);
g_status g_call_b(g_h *h, const g_node *node);
g_status g_fail_next(g_h *h);
g_status g_set_x(g_h *h, const g_node *node, g_mx cb);
g_status g_set_y(g_h *h, const g_node *node, g_my cb);
g_status g_call_x(g_h *h, const g_node *node);
g_status g_call_y(g_h *h, const g_node *node);
g_status g_visit(g_h *h, const g_node *node, g_v cb);
`

const keyLiveC = `#include "cb.h"
#include <stddef.h>
struct g_h { int x; };
static struct g_h the_h;
static g_ma sa;
static g_mb sb;
static g_mx sx;
static g_my sy;
static int fail_next;
uint32_t g_abi(void) { return 1; }
g_status g_create(g_h **out_h) { *out_h = &the_h; return G_OK; }
g_status g_destroy(g_h *h) { (void)h; return G_OK; }
g_status g_two(g_h *h, const g_node *node, g_ma a, g_mb b) {
    (void)h; (void)node;
    if (fail_next) { fail_next = 0; return G_FAIL; }
    sa = a; sb = b; return G_OK;
}
static g_status call(g_ma f, const g_node *n) {
    if (f == NULL) return G_FAIL;
    f(n->context_id, n->slot, n->generation, 1.0f);
    return G_OK;
}
g_status g_call_a(g_h *h, const g_node *node) { (void)h; return call(sa, node); }
g_status g_call_b(g_h *h, const g_node *node) { (void)h; return call(sb, node); }
g_status g_fail_next(g_h *h) { (void)h; fail_next = 1; return G_OK; }
g_status g_set_x(g_h *h, const g_node *node, g_mx cb) { (void)h; (void)node; sx = cb; return G_OK; }
g_status g_set_y(g_h *h, const g_node *node, g_my cb) { (void)h; (void)node; sy = cb; return G_OK; }
g_status g_call_x(g_h *h, const g_node *node) { (void)h; return call(sx, node); }
g_status g_call_y(g_h *h, const g_node *node) { (void)h; return call(sy, node); }
g_status g_visit(g_h *h, const g_node *n, g_v cb) {
    (void)h;
    if (cb == NULL) return G_OK;
    if (cb(n->context_id, n->slot, n->generation, 1) < 0) return G_FAIL;
    if (cb(n->context_id, n->slot, n->generation, 2) < 0) return G_FAIL;
    return G_OK;
}
`

// TestLiveCallbackHandleKeys (ERGO_BINDC_LIVE=1): the verifier's probe
// headers make Go packages that build and vet with the real purego for
// linux and darwin on amd64 and arm64, with the colliding functions in the
// raw layer only; and the probes' scenarios run against C in the typed
// forms (testdata/live/handlekeys/main.go): each invocation runs the Go
// function of its own registration, a clear of one position keeps the
// other, a failed call leaves the registry as it was, a nested call-scoped
// call on one key restores the outer entry, and two goroutines that call
// through the Owner never overlap on one key. LiveCallbacks is back to 0
// after each scenario.
func TestLiveCallbackHandleKeys(t *testing.T) {
	e := newLiveEnv(t)
	cc := requireCC(t)
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	// The probe headers: Go packages with the colliding functions raw.
	for i, p := range []struct {
		decls string
		entry CallbackRule
		raw   []string
	}{
		{keyProbeShared, keyRegistered, []string{"GTwo", "GSetX", "GSetY"}},
		{keyProbeOverlap, keyCall, []string{"GVisit"}},
	} {
		b := keyBind(t, p.decls, "owner", p.entry)
		b.Contract.Go = GoTarget{Package: "probe" + string(rune('a'+i)), ImportPath: "ergo.local/bind/probe" + string(rune('a'+i))}
		name, src, err := GoPackage(b)
		if err != nil {
			t.Fatalf("GoPackage: %v", err)
		}
		for _, fn := range p.raw {
			if strings.Contains(string(src), "\nfunc "+fn+"(") {
				t.Errorf("probe %d: the Go package has a typed %s", i, fn)
			}
		}
		e.write(filepath.Join(e.mod, b.Contract.Go.Package, name), src)
	}

	src := cbPrelude + keyLiveDecls
	c := cbContract(src)
	for _, td := range []string{"g_ma", "g_mb", "g_mx", "g_my"} {
		r := keyRegistered
		r.Typedef = td
		c.Callbacks = append(c.Callbacks, r)
	}
	c.Callbacks = append(c.Callbacks, keyCall)
	b, err := cbBind(t, src, c)
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	for _, f := range b.Funcs {
		if f.RawOnly != "" && slices.Contains([]string{"g_two", "g_set_x", "g_set_y", "g_visit"}, f.C.Name) {
			t.Fatalf("%s is raw-only: %s", f.C.Name, f.RawOnly)
		}
	}
	name, gosrc, err := GoPackage(b)
	if err != nil {
		t.Fatalf("GoPackage: %v", err)
	}
	e.write(filepath.Join(e.mod, "cb", name), gosrc)
	cdir := filepath.Join(e.root, "c")
	e.write(filepath.Join(cdir, "cb.h"), []byte(src))
	e.write(filepath.Join(cdir, "gen.c"), []byte(keyLiveC))
	lib := filepath.Join(e.root, "libcb"+ext)
	e.run(cdir, "", nil, cc, "-std=c11", "-Wall", "-Werror", "-shared", "-fPIC", "-o", lib, "gen.c")
	e.write(filepath.Join(e.mod, "driver", "main.go"), readInput(t, "testdata/live/handlekeys/main.go"))
	for _, goos := range []string{"linux", "darwin"} {
		for _, goarch := range []string{"amd64", "arm64"} {
			e.run(e.mod, "", []string{"GOOS=" + goos, "GOARCH=" + goarch}, e.goTool, "vet", "./...")
		}
	}
	bin := filepath.Join(e.root, "driver-bin")
	e.run(e.mod, "", nil, e.goTool, "build", "-o", bin, "./driver")
	out, err := e.try(e.root, "", nil, bin, lib)
	want := []string{
		"S1 C calls a: Go runs a", "S1 C calls b: Go runs b", "S2 clear of b keeps a", "S2 live 1",
		"S3 failed GTwo: live unchanged", "S3 failed replacement: the old a runs",
		"S4 C calls x: Go runs x", "S4 C calls y: Go runs y",
		"nest: f1 2 f2 2", "nest: live 0", "owner: the second call began after the first returned",
		"owner: f1 2 f2 2", "owner: live 0",
	}
	if got := okLines(out); err != nil || !slices.Equal(got, want) {
		t.Fatalf("the driver: %v\ngot  %q\nwant %q\n%s", err, got, want, tail(out, 3000))
	}
}
