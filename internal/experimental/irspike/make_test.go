package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRMake checks the decoder's make of a slice (IR-MAKE,
// ergoir_builtin.go). sized's and presized's IR bodies, an empty []int
// literal, are rewritten to make([]int, n) and make([]int, 0, n):
//   - the IR build prints both slices' len and cap ("3 3 0 5"); mutant MK1
//     (the decoder has no make: "the builtin make") fails it;
//   - a constant length above a constant capacity, make([]int, 4, 2), is
//     the decoder's error at the .ergo position, as Go's checker refuses it
//     in source; mutant MK2 (no constant check: the build passes and the
//     program panics in makeslice) fails it.
//
// It builds Go because the decoder is the patched compiler's: no
// in-process check reaches it.
func TestIRMake(t *testing.T) {
	_, ir := needsBuild(t)
	local := func(i int) map[string]any { return map[string]any{"k": "local", "i": i} }
	num := func(v string) map[string]any { return map[string]any{"k": "const", "t": "int", "v": v} }
	mk := func(args ...any) map[string]any {
		return map[string]any{"k": "builtin", "name": "make", "t": "[]int", "args": args}
	}
	t.Run("sizes", func(t *testing.T) {
		dir := makeIR(t, map[string]map[string]any{"sized": mk(local(0)), "presized": mk(num("0"), local(0))})
		bin := filepath.Join(dir, "bin")
		if _, err := Build(ir, IR, dir, bin, BuildOptions{}); err != nil {
			t.Fatalf("the IR build of make: %v", err)
		}
		if o := run(t, bin); o.stdout != "3 3 0 5\n" || o.exit != 0 {
			t.Fatalf("make printed %+v, want 3 3 0 5", o)
		}
	})
	t.Run("length above capacity", func(t *testing.T) {
		dir := makeIR(t, map[string]map[string]any{"presized": mk(num("4"), num("2"))})
		_, err := Build(ir, IR, dir, filepath.Join(dir, "bin"), BuildOptions{})
		if err == nil || !strings.Contains(err.Error(), "make.ergo:19:") || !strings.Contains(err.Error(), "ergoir: make length 4 above capacity 2") {
			t.Fatalf("want the ergoir error at make.ergo:19, got %v", err)
		}
	})
}

// makeIR lowers testdata/make.ergo to the IR and replaces the empty
// slice literal each named function returns with its expression, keeping
// the literal's position.
func makeIR(t *testing.T, with map[string]map[string]any) string {
	t.Helper()
	u, err := Load(filepath.Join("testdata", "make.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "ir")
	if _, err := Emit(u, IR, dir); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, lower.IRFileName)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	done := map[string]bool{}
	funcs, _ := doc["funcs"].([]any)
	for _, f := range funcs {
		fn, _ := f.(map[string]any)
		name, _ := fn["name"].(string)
		x, ok := with[name]
		if !ok {
			continue
		}
		body, _ := fn["body"].([]any)
		for _, s := range body {
			st, _ := s.(map[string]any)
			if lit, _ := st["x"].(map[string]any); st["k"] == "ret" && lit["k"] == "lit" && lit["t"] == "[]int" {
				x["pos"] = lit["pos"]
				st["x"] = x
				done[name] = true
			}
		}
	}
	for name := range with {
		if !done[name] {
			t.Fatalf("%s's IR body returns no []int literal:\n%s", name, data)
		}
	}
	data, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
