package lower

import (
	"encoding/json"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestTestBuildCallsCopies is the owner's ruling for MONO-HOOKS-MIN (A,
// 2026-11-19: "tests should run what ships"): the IR of a test build, as
// of a run build, calls a known instance's monomorphic copy, which Go
// compiles once in its package, never the generic function, which Go
// compiles again in every package and shard that names the instance. It
// lowers mono-instance's witness both ways and reads the IR in process.
//
// Mutant it alone catches: mono-test-build-stencils (the registry row
// without inTests: the test build calls the generic, and
// TestTestBuildsTakeNoShortcut then holds it to no site).
func TestTestBuildCallsCopies(t *testing.T) {
	src := shortcutWitness["mono-instance"]
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatalf("%v", diags)
	}
	u := Unit{Package: pkg, Files: []*syntax.File{file}, Sources: []Source{{Path: testSourcePath, Text: src}}}
	for _, tests := range []bool{false, true} {
		lower := LowerIR
		if tests {
			lower = LowerIRTests
		}
		files, _, err := lower(u)
		if err != nil {
			t.Fatal(err)
		}
		calls := map[string]bool{}
		for _, f := range files {
			if f.Path != IRFileName {
				continue
			}
			var v any
			if err := json.Unmarshal(f.Data, &v); err != nil {
				t.Fatal(err)
			}
			irCallNames(v, calls)
		}
		copies := false
		for name := range calls {
			if strings.HasPrefix(name, "countMono_i") {
				copies = true
			}
		}
		if !copies || calls["count"] {
			t.Errorf("tests=%v: the IR calls %v; want count's copy (countMono_i…) and never the generic count\n%s", tests, calls, src)
		}
	}
}

// irCallNames adds the name of each "call" in v, a decoded IR file, to out.
func irCallNames(v any, out map[string]bool) {
	switch v := v.(type) {
	case map[string]any:
		if v["k"] == "call" {
			name, _ := v["name"].(string)
			out[name] = true
		}
		for _, x := range v {
			irCallNames(x, out)
		}
	case []any:
		for _, x := range v {
			irCallNames(x, out)
		}
	}
}
