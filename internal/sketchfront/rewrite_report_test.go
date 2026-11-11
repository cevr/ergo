package sketchfront

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestRewriteLawReported is the report row of a rewrite law (types.md,
// Laws 14; owner q115): ergo inspect lists it, member name and package,
// as tested and not applied, with its run's bounds, and ergo check's
// evaluation skips it with the same verdict text, never as passed.
//
// Mutants it kills (no other test reads these reports): inspect lists no
// rewrite law; inspect says applied or proved; check's evaluation passes
// a rewrite law or skips it with another reason.
func TestRewriteLawReported(t *testing.T) {
	src := "import sync\n\ntype Counter {\n\tAdd(n int),\n\tRead() int,\n\n\tlaw Joins(a int, b int) { Add(a); Add(b) } == { Add(a + b) },\n}\n\n" +
		"type Tally { s sync.Ref[int] }\n\nfunc NewTally() Tally { Tally{s: sync.NewRef[int](0)} }\n\n" +
		"func Add(t Tally, n int) { t.s.Set(t.s.Get() + n) }\n\nfunc Read(t Tally) int { t.s.Get() }\n\nfunc main() {}\n"
	file, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, diags := check.Check("main", []*syntax.File{file}, nil)
	if len(diags) > 0 {
		t.Fatal(diags)
	}
	got := InspectGo([]*check.Package{pkg}).RewriteLaws
	want := RewriteLawNote{Package: "main", Law: "Counter.Joins", Evidence: "tested", Cases: eval.RewriteCases, Prefix: eval.RewritePrefix - 1, Applied: false, Reason: eval.RewriteVerdict()}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("inspect's rewrite laws are %+v, want [%+v]", got, want)
	}
	rep, err := eval.Run(pkg, eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rep.Results {
		if r.Name == "law Counter.Joins for Tally" {
			if r.Status != eval.Skipped || r.Reason != eval.RewriteVerdict() {
				t.Fatalf("check's evaluation of the rewrite law: %s, %q", r.Status, r.Reason)
			}
			return
		}
	}
	t.Fatalf("check's evaluation has no obligation for the rewrite law: %+v", rep.Results)
}
