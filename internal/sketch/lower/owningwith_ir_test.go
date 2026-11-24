package lower

import (
	"os"
	"slices"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// TestOwningWithTakesTheIR is the in-process half of the IR leg of a with
// that owns resources (the with lifetime owner): the with opens its scope
// on the frame's (OpenWith), builds each provider it does not lend in one
// masked acquisition that registers its checked Close (WithAcquire), or
// passes the scope to its named context's builder, and ends the scope
// after its block (EndWith), as the emitter's with does. Every function of
// the fixture has an IR body: a with built in place, one of a named
// context left mid-block by a failure, one that lends a resource, a with
// inside another, and owning withs as a function's result and failing
// tail. The fixture runs on both paths, printing its release order, in
// TestIRReprosMatchSource/owningwith (irspike).
//
// Mutant (testdata/mutants.json): ir-owning-with-refused.
func TestOwningWithTakesTheIR(t *testing.T) {
	src, err := os.ReadFile("../../experimental/irspike/testdata/owningwith.ergo")
	if err != nil {
		t.Fatal(err)
	}
	o := mustLower(t, "owningwith", string(src))
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: string(src)}}}
	_, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Fallback) != 0 {
		t.Errorf("bodies keep their source body: %v", rep.Fallback)
	}
	for _, name := range []string{"inline", "midway", "lent", "nested", "valued", "tail"} {
		if !slices.Contains(rep.IR, name) {
			t.Errorf("%s has no IR body (fallbacks %v; IR %v)", name, rep.Fallback, rep.IR)
		}
	}
}
