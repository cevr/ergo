package lower

import (
	"os"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// Scratch (LR3, never committed): the symbolic verdict of each json law.
func TestZZLR3Symbolic(t *testing.T) {
	if os.Getenv("LR3_SYM") == "" {
		t.Skip()
	}
	pkg, err := check.Std.Import("json")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range pkg.Funcs {
		if f.Name == "setComma" || f.Name == "raw" || f.Name == "Bool" {
			t.Logf("func %s: elems=%#v uses=%v %v errors=%v %v fallible=%v", f.Name, f.Sig.Uses.Elems(), f.Sig.Uses, f.Sig.Uses.Empty(), f.Sig.Errors, f.Sig.Errors.Empty(), f.Sig.Fallible)
		}
	}
	for _, f := range pkg.Funcs {
		if f.Name == "setComma" {
			for _, e := range f.Sig.Uses.Elems() {
				if v, ok := e.(*check.RowVar); ok {
					t.Logf("setComma rowvar sol=%v nil=%v", v.Solution(), v.Solution() == nil)
				}
			}
		}
	}
	for _, run := range pkg.LawRuns {
		if run.Law.Owner == nil {
			continue
		}
		for _, b := range run.Bindings {
			for _, p := range b {
				if p.Type == nil || p.Type.Obj.Name != "text" {
					continue
				}
				if run.Law.Rewrite == nil {
					t.Logf("%s: not a rewrite law; unrun=%q", run.Law.Name, p.Unrun)
					continue
				}
				pr := eval.ProveSymbolic(run.Law, p)
				t.Logf("%s: proved=%v how=%s why=%s unrun=%q", run.Law.Name, pr.Proved, pr.How(), pr.Why, p.Unrun)
			}
		}
	}
}
