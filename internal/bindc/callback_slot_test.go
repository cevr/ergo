package bindc

import (
	"fmt"
	"go/importer"
	gotoken "go/token"
	"strings"
	"testing"
)

// cbVariety is callback typedef i of a generated header with many bound
// callbacks: its typedef, its registering function (and end function), its
// entry and its exceptions. The six variants cover each scope with each
// key form, every result kind the admission rule takes, and each callback
// class: scalar, value, in_buffer with its length, out and out_array.
func cbVariety(i int) (decls string, entry CallbackRule, exc []Exception) {
	name := fmt.Sprintf("g_cb%d", i)
	token := KeyRule{Params: []string{"user_data"}, Source: "user_data"}
	handle := KeyRule{Params: []string{"context_id", "slot", "generation"}, Source: "node"}
	entry = CallbackRule{Typedef: name, Scope: ScopeCall, Key: token, Why: "generated"}
	var w strings.Builder
	switch i % 6 {
	case 0: // call, token, void, a scalar
		fmt.Fprintf(&w, "typedef void (*%s)(void *user_data, uint32_t value);\n", name)
		fmt.Fprintf(&w, "g_status g_reg%d(g_h *h, void *user_data, %s cb);\n", i, name)
	case 1: // call, token, int64_t, a borrowed buffer and its byte length
		fmt.Fprintf(&w, "typedef int64_t (*%s)(void *user_data, const uint8_t *data, uint32_t data_bytes);\n", name)
		fmt.Fprintf(&w, "g_status g_reg%d(g_h *h, void *user_data, %s cb);\n", i, name)
		entry.Default = []byte("-1")
	case 2: // registered, handle key, an out_array, an end function
		fmt.Fprintf(&w, "typedef void (*%s)(uint64_t context_id, uint32_t slot, uint32_t generation, float width, float *out_size);\n", name)
		fmt.Fprintf(&w, "g_status g_reg%d(g_h *h, const g_node *node, %s cb);\n", i, name)
		fmt.Fprintf(&w, "g_status g_end%d(g_h *h, const g_node *node);\n", i)
		entry.Scope, entry.Owner, entry.Key, entry.Ends = ScopeRegistered, "g_h", handle, []string{fmt.Sprintf("g_end%d", i)}
		exc = append(exc, Exception{Callback: name, Position: "out_size", Class: ClassOutArray, ArrayLength: 2, Why: "generated"})
	case 3: // registered, token key, bool, a double
		fmt.Fprintf(&w, "typedef bool (*%s)(void *user_data, double x);\n", name)
		fmt.Fprintf(&w, "g_status g_reg%d(g_h *h, void *user_data, %s cb);\n", i, name)
		entry.Scope, entry.Owner, entry.Default = ScopeRegistered, "g_h", []byte("false")
	case 4: // call, handle key, a pointer result, a value and an out
		fmt.Fprintf(&w, "typedef void *(*%s)(uint64_t context_id, uint32_t slot, uint32_t generation, const g_pt *pt, int32_t *out_n);\n", name)
		fmt.Fprintf(&w, "g_status g_reg%d(const g_node *node, %s cb);\n", i, name)
		entry.Key, entry.Default = handle, []byte("0")
	case 5: // registered, handle key, a registering function without a status
		fmt.Fprintf(&w, "typedef int8_t (*%s)(uint64_t context_id, uint32_t slot, uint32_t generation, int16_t a, uint64_t b);\n", name)
		fmt.Fprintf(&w, "void g_reg%d(g_h *h, const g_node *node, %s cb);\n", i, name)
		entry.Scope, entry.Owner, entry.Key, entry.Default = ScopeRegistered, "g_h", handle, []byte("-128")
	}
	return w.String(), entry, exc
}

// cbVarietyHeader is a header of n cbVariety typedefs and its contract.
func cbVarietyHeader(n int) (string, *Contract) {
	var w strings.Builder
	w.WriteString(cbPrelude)
	var entries []CallbackRule
	var excs []Exception
	for i := range n {
		d, e, x := cbVariety(i)
		w.WriteString(d)
		entries = append(entries, e)
		excs = append(excs, x...)
	}
	src := w.String()
	c := cbContract(src)
	c.Callbacks, c.Exceptions = entries, excs
	return src, c
}

// TestCallbackSlotBound (A4): a generated header with MaxBoundCallbacks
// (64) bound callback typedefs binds, every registering function is typed,
// and its Go package type-checks on amd64 and arm64 with every generated
// name in the inventory of names.go; with 65 bound typedefs Bind refuses it
// with the count and the bound. A typedef without an entry is not bound and
// does not count.
func TestCallbackSlotBound(t *testing.T) {
	if MaxBoundCallbacks != 64 {
		t.Fatalf("MaxBoundCallbacks is %d, want 64", MaxBoundCallbacks)
	}
	src, bound := cbVarietyHeader(MaxBoundCallbacks)
	// One more typedef that has no entry: raw, so it takes no slot.
	src += "typedef void (*g_raw)(void *user_data);\ng_status g_raw_reg(g_h *h, void *user_data, g_raw cb);\n"
	c := cbContract(src)
	c.Callbacks, c.Exceptions = bound.Callbacks, bound.Exceptions
	b, err := cbBind(t, src, c)
	if err != nil {
		t.Fatalf("Bind of %d bound callback typedefs: %v", MaxBoundCallbacks, err)
	}
	if n := b.boundCallbacks(); n != MaxBoundCallbacks {
		t.Fatalf("%d bound callback typedefs, want %d", n, MaxBoundCallbacks)
	}
	for _, f := range b.Funcs {
		if strings.HasPrefix(f.C.Name, "g_reg") && f.RawOnly != "" {
			t.Errorf("%s is raw-only: %s", f.C.Name, f.RawOnly)
		}
	}
	name, gosrc, err := GoPackage(b)
	if err != nil {
		t.Fatalf("GoPackage: %v", err)
	}
	if got := strings.Count(string(gosrc), "\n\t{\"g_cb"); got != MaxBoundCallbacks {
		t.Errorf("cbFuncs has %d trampolines, want %d", got, MaxBoundCallbacks)
	}
	std := importer.ForCompiler(gotoken.NewFileSet(), "source", nil)
	for _, arch := range []string{"amd64", "arm64"} {
		if err := checkGoPackage(t, std, b.Contract.Go.ImportPath, arch, map[string][]byte{name: gosrc}); err != nil {
			t.Fatalf("the Go package does not type-check on %s:\n%v", arch, err)
		}
	}
	checkGoInventory(t, b, gosrc)

	src, c = cbVarietyHeader(MaxBoundCallbacks + 1)
	_, err = cbBind(t, src, c)
	if err == nil {
		t.Fatalf("Bind of %d bound callback typedefs succeeded; want the bound of %d", MaxBoundCallbacks+1, MaxBoundCallbacks)
	}
	if !strings.Contains(err.Error(), "65 bound callback typedefs") || !strings.Contains(err.Error(), "bound of 64") {
		t.Fatalf("Bind of 65: %v; want the count 65 and the bound 64", err)
	}
}
