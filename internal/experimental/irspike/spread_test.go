package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRAppendSpread checks the decoder's append of a slice
// (APPEND-SPREAD, ergoir_builtin.go): cat's IR body, append(a, s...) of a
// string's bytes, is rewritten to append(a, b...) of its slice parameter
// b, and the IR build runs it, printing b's bytes after a's ("5 15"; the
// string's form prints "3 123"). It builds Go because the decoder is the
// patched compiler's: no in-process check reaches it. It catches the
// derivative's mutant AS1 (the decoder refuses a slice operand, the ledger's
// about).
func TestIRAppendSpread(t *testing.T) {
	_, ir := needsBuild(t)
	u, err := Load(filepath.Join("testdata", "spread.ergo"))
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
	rewritten := 0
	funcs, _ := doc["funcs"].([]any)
	for _, f := range funcs {
		fn, _ := f.(map[string]any)
		if fn["name"] != "cat" {
			continue
		}
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				if x["k"] == "builtin" && x["name"] == "append" {
					args, _ := x["args"].([]any)
					if len(args) == 2 {
						s, _ := args[1].(map[string]any)
						if s["k"] == "local" {
							b := map[string]any{"k": "local", "i": 1} // cat's second parameter, b
							if p, ok := s["pos"]; ok {
								b["pos"] = p
							}
							args[1] = b
							rewritten++
						}
					}
				}
				for _, e := range x {
					walk(e)
				}
			case []any:
				for _, e := range x {
					walk(e)
				}
			}
		}
		walk(fn["body"])
	}
	if rewritten != 1 {
		t.Fatalf("cat's IR body has %d appends of a string's bytes, want 1:\n%s", rewritten, data)
	}
	data, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if _, err := Build(ir, IR, dir, bin, BuildOptions{}); err != nil {
		t.Fatalf("the IR build of append(a, b...): %v", err)
	}
	if o := run(t, bin); o.stdout != "5 15\n" || o.exit != 0 {
		t.Fatalf("append(a, b...) printed %+v, want 5 15", o)
	}
}
