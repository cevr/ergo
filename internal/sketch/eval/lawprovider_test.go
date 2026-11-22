package eval

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestLawProvidersAreBuiltHonestly: a law case never draws a provider
// from its private fields when its package has a constructor for it
// (LAW-PROVIDER-HONEST; INPLACE-REFUTED). A record with a private field
// and a function of its package whose one result is the record, of any
// name and rows, is built only by that function. If no case can call it
// (it fails, needs a capability, or takes what no case draws), the
// binding is not run, with the constructor's name, and is never refuted.
// A row-free New<P> still builds each case. A function that takes a P
// is no constructor, and a record with no private field is still drawn.
// In place, std ratelimit/memory's Limiter (New fails BadRule) and
// fstest's Folder (NewFolder needs TestAmbient) are not run, so their
// laws are no longer refuted on values no program holds.
// Mutants it kills (testdata/mutants.json): lawprov-draws-past-ctor,
// lawprov-ctor-name-only, lawprov-eval-ignores-unrun.
func TestLawProvidersAreBuiltHonestly(t *testing.T) {
	const ops = `type Counter {
	Next() int,

	law Positive() { Next() > 0 },
}

type Neg {}

`
	cases := []struct{ name, decls, unrun string }{
		{"failing Make", "type Box { n int } implements Counter\n\nfunc Next(b Box) int { b.n + 1 }\n\nfunc Make(n int) Box fails Neg {\n\tif n < 0 {\n\t\treturn Neg\n\t}\n\tBox{n: n}\n}\n", "Box is built only by its constructor Make, which fails Neg"},
		{"row-free NewBox", "type Box { n int } implements Counter\n\nfunc Next(b Box) int { b.n + 1 }\n\nfunc NewBox(n int) Box { Box{n: n * n} }\n", ""},
		{"only a With", "type Box { n int } implements Counter\n\nfunc Next(b Box) int { b.n + 1 }\n\nfunc Bump(b Box) Box { Box{n: b.n + 1} }\n", ""},
		{"no private field", "type Box { N int } implements Counter\n\nfunc Next(b Box) int { b.N + 1 }\n\nfunc Make(n int) Box fails Neg {\n\tif n < 0 {\n\t\treturn Neg\n\t}\n\tBox{N: n}\n}\n", ""},
	}
	for _, c := range cases {
		pkg := checked(t, ops+c.decls+"\nfunc main() {}\n")
		var lp *check.LawProvider
		for _, r := range pkg.LawRuns {
			for _, b := range r.Bindings {
				if r.Law.Name == "Positive" && len(b) == 1 && b[0].Type.Obj.Name == "Box" {
					lp = &b[0]
				}
			}
		}
		if lp == nil {
			t.Fatalf("%s: no binding of Positive for Box", c.name)
		}
		if c.unrun == "" && lp.Unrun != "" || !strings.Contains(lp.Unrun, c.unrun) {
			t.Errorf("%s: the binding's Unrun is %q, want %q", c.name, lp.Unrun, c.unrun)
		}
		if c.name == "row-free NewBox" && (lp.Ctor == nil || lp.Ctor.Name != "NewBox") {
			t.Errorf("%s: the case does not call NewBox (Ctor %v)", c.name, lp.Ctor)
		}
		if c.unrun == "" {
			continue
		}
		rep, err := Run(pkg, DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rep.Results {
			if strings.HasPrefix(r.Name, "law Counter.Positive") && (r.Status != Skipped || !strings.Contains(r.Reason, c.unrun)) {
				t.Errorf("%s: %s is %s (%s), want skipped: %s", c.name, r.Name, r.Status, r.Reason, c.unrun)
			}
		}
	}
	for _, s := range []struct{ path, law, ctor string }{
		{"ratelimit/memory", "RetryAfterIsExact", "constructor New,"},
		{"fstest", "OutsideIsDenied", "constructor NewFolder,"},
		{"fstest", "InsideIsWritten", "constructor NewFolder,"},
	} {
		pkg, err := check.Std.Import(s.path)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := Run(pkg, DefaultBudget())
		if err != nil {
			t.Fatal(err)
		}
		seen := false
		for _, r := range rep.Results {
			if !strings.Contains(r.Name, s.law) {
				continue
			}
			if r.Status == Refuted {
				t.Errorf("std %s in place: %s is refuted (%s)", s.path, r.Name, r.Reason)
			}
			if strings.Contains(r.Reason, s.ctor) {
				seen = true
			}
		}
		if !seen {
			t.Errorf("std %s in place: no run of %s is skipped for its %s", s.path, s.law, strings.TrimSuffix(s.ctor, ","))
		}
	}
}
