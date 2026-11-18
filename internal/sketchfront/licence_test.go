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

// TestLicenceWriterKeepsCorpus is ergo test's writer of a module's
// top-level rewrite laws (UpdateLedger; LAWFFI r2 §3.4; LF6b's writer, its
// follow-up), on a library, which has no run build: the licence run
// writes the law's entry; a law false only at the last string class
// (one that ends inside a rune) is refuted there, after 14 cases that
// held, and its case joins ergo.cases; after an edit of a body the law
// reaches, the rerun takes that case first and is refuted at it with no
// case before; an unchanged rerun runs nothing and changes nothing;
// removing the law drops its entry and its corpus line.
//
// Mutants: lf6-library-unlicensed (only a binary's laws are licensed),
// lf6-licence-rerun-current (a current entry is licensed again),
// lf6-corpus-unwritten, lf6-corpus-unreplayed, lf6-top-level-entry-kept
// (the entry of a law no longer declared stays), lf6-corpus-kept.
func TestLicenceWriterKeepsCorpus(t *testing.T) {
	const cut = "func Cut(s string) string { if len(s) >= 2 && s[len(s)-2] == 0xe4 { \"\" } else { s } }\n"
	const cutEdited = "func Cut(s string) string {\n\tlet n = len(s)\n\tif n >= 2 && s[n-2] == 0xe4 { \"\" } else { s }\n}\n"
	const law = "\nlaw CutIsId(s string) { Cut(s) } == { s }\n"
	dir := t.TempDir()
	ledger := filepath.Join(dir, lower.LedgerFile)
	cases := filepath.Join(dir, CorpusFile)
	write := func(step, src string) (int, string) {
		t.Helper()
		var stderr strings.Builder
		n, err := UpdateLedger(checked(t, src), BuildRequest{Source: "/ergo-test/licence.ergo", Ledger: ledger, Stderr: &stderr})
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		return n, stderr.String()
	}
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(data)
	}
	n, out := write("first run", cut+law)
	if n == 0 || !strings.Contains(read(ledger), ".CutIsId refuted by eval ") {
		t.Fatalf("first run: %d changed, stderr %q, ledger %q; want the law refuted", n, out, read(ledger))
	}
	if !strings.Contains(out, "refuted") || !strings.Contains(out, "; 14 cases") {
		t.Errorf("first run: %q; want a refutation after the 14 classes before the last", out)
	}
	if c := read(cases); !strings.Contains(c, `.CutIsId ("a\xe4\xb8")`) {
		t.Fatalf("first run: ergo.cases %q; want the refuted case", c)
	}
	n, out = write("rerun after an edit", cutEdited+law)
	if n == 0 || !strings.Contains(out, "refuted") || !strings.Contains(out, "; 0 cases") {
		t.Errorf("rerun after an edit: %d changed, %q; want the corpus case refuting it first, with no case before", n, out)
	}
	if n, out = write("unchanged rerun", cutEdited+law); n != 0 || out != "" {
		t.Errorf("unchanged rerun: %d changed, %q; want nothing run and nothing changed", n, out)
	}
	// The package keeps a law, a property, so a law of the same package
	// is still declared and only the rewrite law's name tells.
	const property = "\nlaw CutTwice(s string) { Cut(Cut(s)) == Cut(s) }\n"
	if n, _ = write("law removed", cutEdited+property); n == 0 || strings.Contains(read(ledger), "CutIsId") || strings.Contains(read(cases), "CutIsId") {
		t.Errorf("law removed: %d changed, ledger %q, ergo.cases %q; want both without the law", n, read(ledger), read(cases))
	}
}
