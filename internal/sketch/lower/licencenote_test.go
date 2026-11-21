package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestRewriteLawRunsAsLicence (LICENCE-ADT, step 4): a rewrite law in
// scope whose cases the test binary cannot draw (a function parameter) is
// decided by its licence run, which ergo test runs as it writes the
// ledger. The binary prints a note for it, never a skip line, so it is no
// unexpected skip. A property law with the same gap is still a skip.
// It checks the lowered Go's text in process: the line is a constant the
// runner prints.
//
// Mutant: ladt-rewrite-skips (the note's branch removed, so the rewrite
// law prints a skip line).
func TestRewriteLawRunsAsLicence(t *testing.T) {
	const src = `func Twice(f func(x int) int, x int) int { f(f(x)) }

law TwiceUnfolds(f func(x int) int, x int) { Twice(f, x) } == { f(f(x)) }

law TwiceIsTwice(f func(x int) int, x int) { Twice(f, x) == f(f(x)) }

func main() {}
`
	p := lowerTestBuild(t, "licencenote", src)
	var text strings.Builder
	for _, f := range p.files {
		text.Write(f.Data)
	}
	// Every law has a `--skip-law` line; the reason line is the skip.
	got := strings.ReplaceAll(text.String(), `: --skip-law"`, "")
	if !strings.Contains(got, `"law TwiceUnfolds runs as its licence run (ergo.laws): `) {
		t.Errorf("no licence note for TwiceUnfolds in the runner")
	}
	if strings.Contains(got, `"skip law TwiceUnfolds:`) {
		t.Errorf("the runner prints a skip line for the rewrite law TwiceUnfolds")
	}
	if !strings.Contains(got, `"skip law TwiceIsTwice:`) {
		t.Errorf("the property law TwiceIsTwice is no longer a skip; the test lost its control")
	}
	// The summary counts the rewrite law neither as a run nor as a skip.
	var r check.LawRun
	for _, lr := range p.pkg.LawRuns {
		if lr.Law.Name == "TwiceUnfolds" {
			r = lr
		}
	}
	if r.Law == nil {
		t.Fatal("no law run for TwiceUnfolds")
	}
	if runs, skips := lawCounts(r); runs != 0 || skips != 0 {
		t.Errorf("lawCounts(TwiceUnfolds) = %d runs, %d skips; want 0, 0", runs, skips)
	}
}
