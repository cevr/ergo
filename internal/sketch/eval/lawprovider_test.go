package eval

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestLawProvidersAreBuiltHonestly: a law case never draws a provider
// from its private fields when its package has a constructor for it
// (LAW-PROVIDER-HONEST; INPLACE-REFUTED). A record with a private field
// and a function of its package whose one result is the record, of any
// name and rows, is built only by that function. If no case can call it
// (it fails, needs a capability, or takes what no case draws), the
// binding is not run. TestConstructorOnlyLawIsFedAcrossPackages covers
// the context run and the unfed diagnostic when no context can build it.
// A row-free New<P> still builds each case. A function that takes a P
// is no constructor, and a record with no private field is still drawn.
func TestLawProvidersAreBuiltHonestly(t *testing.T) {
	const ops = `type Counter {
	Next() int,

	law Positive() { Next() > 0 },
}

type Neg {}

`
	cases := []struct{ name, decls string }{
		{"row-free NewBox", "type Box { n int } implements Counter\n\nfunc Next(b Box) int { b.n + 1 }\n\nfunc NewBox(n int) Box { Box{n: n * n} }\n"},
		{"only a With", "type Box { n int } implements Counter\n\nfunc Next(b Box) int { b.n + 1 }\n\nfunc Bump(b Box) Box { Box{n: b.n + 1} }\n"},
		{"no private field", "type Box { N int } implements Counter\n\nfunc Next(b Box) int { b.N + 1 }\n\nfunc Make(n int) Box fails Neg {\n\tif n < 0 {\n\t\treturn Neg\n\t}\n\tBox{N: n}\n}\n"},
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
		if lp.Unrun != "" {
			t.Errorf("%s: binding cannot run: %s", c.name, lp.Unrun)
		}
		if c.name == "row-free NewBox" && (lp.Ctor == nil || lp.Ctor.Name != "NewBox") {
			t.Errorf("%s: the case does not call NewBox (Ctor %v)", c.name, lp.Ctor)
		}
	}
}
