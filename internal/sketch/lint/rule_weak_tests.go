package lint

import "ergo.local/prototype/internal/sketch/syntax"

// The weak-test rules of the strict preset (SKILL-RULES SR-ERGO-7; the
// owner's list: tests that "just test constants and things that things
// are what they are", the same case twice). Each reads tests and laws
// through testweak.go. A test with no assertion (SR-T10) is vacuous-test's
// (L138), which reports it already.

// constantRestated: an assert that compares two constants of one value.
var constantRestated = &Rule{
	Name:    "constant-restated",
	Code:    "L153",
	Doc:     "An assert compares two constants of one value, such as a constant and the literal it was declared as: it passes by construction and checks nothing.",
	Instead: "assert what a function computes from the constant, or delete the assert",
	Default: Hint,
	Check:   checkConstantRestated,
}

// repeatedCase: an assert that repeats one just before it.
var repeatedCase = &Rule{
	Name:    "repeated-case",
	Code:    "L154",
	Doc:     "An assert repeats an earlier one of its block, with only asserts between and no effect in it: it checks the same thing twice.",
	Instead: "delete the repeat, or assert the case it was meant to be",
	Default: Hint,
	Check:   checkRepeatedCase,
}

// testBodies is each test's and each law's body in the file.
func testBodies(f *syntax.File, visit func(body *syntax.BlockStmt)) {
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *syntax.TestDecl:
			if d.Body != nil {
				visit(d.Body)
			}
		case *syntax.LawDecl:
			if d.Body != nil && d.Derived == nil && d.Pair == nil {
				visit(d.Body)
			}
		}
	}
}

func checkConstantRestated(p *Pass) {
	testBodies(p.File, func(body *syntax.BlockStmt) {
		walkNode(body, func(n syntax.Node) bool {
			if a, ok := n.(*syntax.AssertStmt); ok && restated(p.Info, a) {
				p.Report(a.Pos(), a.End(), "both sides of this assert are constants of one value")
			}
			return true
		})
	})
}

func checkRepeatedCase(p *Pass) {
	testBodies(p.File, func(body *syntax.BlockStmt) {
		for _, a := range repeatedAsserts(p.Info, body) {
			p.Report(a.Pos(), a.End(), "this assert repeats one above it")
		}
	})
}
