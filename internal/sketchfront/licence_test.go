package sketchfront

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

// TestCheckAdvisesUnlicensedRewrite is ergo check's advice at a top-level
// rewrite law in scope (LAWFFI r2 §3.6, Report; LF6c) over the ledger's
// states, as ergo test's writer leaves them: no ledger gives the advice;
// after UpdateLedger's licence run (validated by eval) none; an edit of a
// body the law reaches makes the entry stale and the advice says so; an
// edit of a body it does not reach leaves no advice; a current entry
// whose verdict is tested gives the advice with that verdict. Each edit
// stays a true law, so the run validates it.
//
// Mutants: lf6c-advice-on-validated (a current validated entry advised),
// lf6c-stale-entry-unadvised (a stale entry read as current).
func TestCheckAdvisesUnlicensedRewrite(t *testing.T) {
	const law = "\nlaw IdOnce(s string) { Id(Id(s)) } == { Id(s) }\n"
	const id, idEdited = "func Id(s string) string { s }\n", "func Id(s string) string { let t = s\n\tt\n}\n"
	const other, otherEdited = "\nfunc Other(s string) string { s }\n", "\nfunc Other(s string) string { s + s }\n"
	path := filepath.Join(t.TempDir(), lower.LedgerFile)
	advice := func(src string) string {
		t.Helper()
		a := checked(t, src)
		e, err := a.Evaluation(eval.DefaultBudget(), "/ergo-test/licence.ergo")
		if err != nil {
			t.Fatal(err)
		}
		if err := a.AdviseLicences(&e, nil, path); err != nil {
			t.Fatal(err)
		}
		got := ""
		for _, o := range e.Obligations {
			if o.Advice != "" {
				if got != "" || o.Name != "law IdOnce" {
					t.Fatalf("advice on %s, or twice: %+v", o.Name, e.Obligations)
				}
				got = o.Advice
			}
		}
		return got
	}
	want := func(step, src, part string) {
		t.Helper()
		got := advice(src)
		switch {
		case part == "" && got != "":
			t.Errorf("%s: advice %q, want none\n%s", step, got, src)
		case part != "" && !strings.Contains(got, part):
			t.Errorf("%s: advice %q, want one that says %q\n%s", step, got, part, src)
		}
	}
	base := id + other + law
	want("no ledger", base, "has no entry for it")
	if n, err := UpdateLedger(checked(t, base), BuildRequest{Source: "/ergo-test/licence.ergo", Ledger: path}); err != nil || n != 1 {
		t.Fatalf("ergo test's writer: %d entries changed, %v; want 1", n, err)
	}
	text, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(text), " validated by eval ") {
		t.Fatalf("the ledger after the licence run: %q, %v", text, err)
	}
	want("validated", base, "")
	want("a reached body edited", idEdited+other+law, "is stale")
	want("an unreached body edited", id+otherEdited+law, "")
	if err := os.WriteFile(path, []byte(strings.Replace(string(text), " validated by eval ", " tested by eval ", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	want("tested", base, "is tested")
}
