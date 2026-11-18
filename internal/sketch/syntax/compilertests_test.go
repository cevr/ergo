package syntax_test

import (
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketchfront"
)

// TestCompilerSyntaxTestsPass runs the Ergo tests of compiler/syntax, the
// package syntax.Parse decodes, at check time, as `ergo check
// compiler/syntax` runs them: in process, with no Go build. Each test
// must pass; a skipped or incomplete test fails here, since a skip is not
// a pass. It is the bridge that lets a mutant ledger and the gate run
// Ergo tests until scripts/mutants and the gate run `ergo test` of a
// compiler package themselves (ST-0). The laws of the package are not
// required here: ten of them use the whole step budget at check time.
// Its first tests were TestLexicalErrorInLookahead's rows (wf10 TP-DIAG),
// so it kills that family's mutants: the diag-* rows of
// compiler/syntax/testdata/mutants.json.
func TestCompilerSyntaxTestsPass(t *testing.T) {
	dir, err := filepath.Abs("../../../compiler/syntax")
	if err != nil {
		t.Fatal(err)
	}
	a, err := sketchfront.CheckDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Diagnostics) != 0 {
		t.Fatalf("compiler/syntax does not check: %v", a.Diagnostics)
	}
	rep, err := a.Evaluate(eval.DefaultBudget())
	if err != nil {
		t.Fatal(err)
	}
	tests := 0
	for _, r := range rep.Results {
		if strings.HasPrefix(r.Name, "law ") {
			continue
		}
		tests++
		if r.Status != eval.Passed {
			t.Errorf("%s (%s:%d) is %s: %s", r.Name, r.Site.File, r.Site.Line, r.Status, r.Reason)
		}
	}
	if tests == 0 {
		t.Fatal("compiler/syntax has no test")
	}
}
