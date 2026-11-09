package check_test

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A constructor whose parameters reach its own type is not the type's
// constructor (tests.go rule 3): a generator that called it would draw a
// Box to build a Box, with no end. The type is built field by field.
// Mutant killed: a constructor that reaches its type is taken (fu6a M2).
func TestGeneratorConstructor(t *testing.T) {
	src := "type Box { n int }\n\nfunc NewBox(b Box) Box { b }\n\nlaw Kept(b Box) { b.n == b.n }\n"
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, nil)
	for _, d := range ds {
		t.Errorf("diagnostic: %v", d)
	}
	for _, tn := range pkg.Types {
		if tn.Name == "Box" && tn.Constructor() != nil {
			t.Fatalf("Box's constructor is %s, whose parameter is a Box", tn.Constructor().Name)
		}
	}
}
