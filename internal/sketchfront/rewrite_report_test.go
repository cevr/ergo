package sketchfront

import (
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lower"
)

// TestRewriteLawReported is the report row of a rewrite law (types.md,
// Laws 14; runtime.md, Tests 10): ergo inspect lists each rewrite site with
// its law, provider and constants, its ledger entry's verdict and state,
// and whether the build applies it, for a current, a stale and a missing
// entry; and ergo check's evaluation skips the law with the rewrite
// verdict text, never as passed.
//
// Mutants it kills (no other test reads these reports): inspect lists no
// site; inspect says applied for an entry that is not current; check's
// evaluation passes a rewrite law or skips it with another reason.
func TestRewriteLawReported(t *testing.T) {
	saved, savedChecks := compilerIdentity, lawChecks
	compilerIdentity = func() (string, error) { return "test", nil }
	lawChecks = fakeLawChecks
	defer func() { compilerIdentity, lawChecks = saved, savedChecks }()
	p := ledgerProgram{depth: 1, sites: [][2]int{{2, 3}}}
	base := checked(t, p.source())
	path := filepath.Join(t.TempDir(), lower.LedgerFile)
	if _, err := UpdateLedger(base, BuildRequest{Mode: build.ModeBuild, Source: "/ergo-test/ledger.ergo", Ledger: path}); err != nil {
		t.Fatal(err)
	}
	p.addSwap = true
	edited := checked(t, p.source())
	for _, c := range []struct {
		a      Analysis
		ledger string
		want   lower.RewriteSite
	}{
		{base, path, lower.RewriteSite{Law: "main.Counter.Joins", Provider: "main.Tally", Instance: "(2, 3)", Verdict: "proved", State: "current", Applied: true}},
		{edited, path, lower.RewriteSite{Law: "main.Counter.Joins", Provider: "main.Tally", Instance: "(2, 3)", Verdict: "proved", State: "stale"}},
		{base, "", lower.RewriteSite{Law: "main.Counter.Joins", Provider: "main.Tally", Instance: "(2, 3)", State: "missing"}},
	} {
		got, err := InspectRewrites(c.a, "/ergo-test/ledger.ergo", c.ledger)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("inspect's rewrite sites are %+v, want [%+v]", got, c.want)
		}
	}
	rep, err := eval.Run(base.pkg, eval.DefaultBudget())
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
