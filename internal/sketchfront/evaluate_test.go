package sketchfront

import (
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
)

// TestEvaluateRefutesFalseExample is the check-time seam's acceptance (L7
// EV1; the planner's evidence/refuted.ergo): a statically valid program
// whose example is false must be refuted by Analysis.Evaluate, at the
// assert's site, with no Go build. The law beside it takes generated cases,
// which the evaluator does not draw yet (EV3), so it is a visible skip,
// never a pass. Mutants eval-bypassed (Evaluate reports without running
// the obligations) and skip-as-pass are caught here and nowhere else.
func TestEvaluateRefutesFalseExample(t *testing.T) {
	src := "func twice(n int) int { n + n }\n\ntest ExampleRefuted {\n\tassert twice(2) == 5\n}\n\nlaw Refuted(n int) { twice(n) - n != n }\n"
	a := Check(src)
	if len(a.Diagnostics) != 0 {
		t.Fatalf("the program does not check: %v", a.Diagnostics)
	}
	rep, err := a.Evaluate(eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 2 {
		t.Fatalf("want 2 obligations, got %+v", rep.Results)
	}
	ex := rep.Results[0]
	if ex.Name != "ExampleRefuted" || ex.Status != eval.Refuted || ex.Site.Line != 4 || ex.Site.Column != 2 {
		t.Errorf("example: want refuted at 4:2, got %+v", ex)
	}
	law := rep.Results[1]
	if law.Name != "law Refuted" || law.Status != eval.Skipped || law.Reason == "" {
		t.Errorf("law: want an explicit skip, got %+v", law)
	}
	if !rep.Refuted() {
		t.Errorf("the report does not count the refutation: %+v", rep)
	}
}
