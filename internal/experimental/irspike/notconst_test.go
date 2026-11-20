package irspike

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRNotOfConstant checks the decoder's ! of a constant (#42,
// ergoir_expr.go): main's if condition in testdata/notconst.ergo is
// rewritten to !false, which the IR build must run as true ("then").
// Go's writer reads Not(x) as x's static value (staticBool), which Go
// itself never reaches with a constant x, since types2 folds !c; the
// decoder folds nothing, so it parenthesizes a !'s operand. Mutant NOT1
// (only a binary operand in parentheses, as before #42) prints "else":
// every assert of a constant ran inverted (assert 1 == 2 passed).
//
// It builds Go because the decoder is the patched compiler's: no
// in-process check reaches it.
func TestIRNotOfConstant(t *testing.T) {
	_, ir := needsBuild(t)
	u, err := Load(filepath.Join("testdata", "notconst.ergo"))
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
	rewritten := false
	funcs, _ := doc["funcs"].([]any)
	for _, f := range funcs {
		fn, _ := f.(map[string]any)
		body, _ := fn["body"].([]any)
		for _, s := range body {
			st, _ := s.(map[string]any)
			if cond, _ := st["x"].(map[string]any); st["k"] == "if" && !rewritten {
				st["x"] = map[string]any{"k": "un", "op": "!", "pos": cond["pos"],
					"x": map[string]any{"k": "const", "t": "bool", "v": "false", "pos": cond["pos"]}}
				rewritten = true
			}
		}
	}
	if !rewritten {
		t.Fatalf("no IR body has an if:\n%s", data)
	}
	if data, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if _, err := Build(ir, IR, dir, bin, BuildOptions{}); err != nil {
		t.Fatalf("the IR build of !false: %v", err)
	}
	if o := run(t, bin); o.stdout != "then\n" || o.exit != 0 {
		t.Fatalf("if !false printed %+v, want then", o)
	}
}
