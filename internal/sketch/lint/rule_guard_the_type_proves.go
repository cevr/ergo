package lint

import "ergo.local/prototype/internal/sketch/check"

// guardTheTypeProves is the defensive rule of the strict preset (catalog
// CR20 and CR23, redundant guards and checks the type proves): a
// comparison of a value of a refined type (`type Port int where 1 <= it
// && it <= 65535`), or of the length of one whose where bounds len(it),
// with an integer constant, that every value the refinement admits
// answers the same way (check.RefinementDecides): `p < 1` is always false
// for a Port. Only a checked constant and the type's parse function make
// a value of it, so the guard can never take its other branch. Tests and
// laws are out of scope: a law may state what the type promises.
var guardTheTypeProves = &Rule{
	Name:    "guard-the-type-proves",
	Code:    "L128",
	Doc:     "A comparison that the refinement of its operand's type already decides is a guard that cannot fail.",
	Instead: "delete the guard and the branch it cannot take; the type already promises it",
	Default: Warning,
	Check:   checkGuardTheTypeProves,
}

func checkGuardTheTypeProves(p *Pass) {
	fi := p.fileIndex()
	for i, x := range fi.binaries {
		if fi.binaryFlags[i]&(nodeInTestLaw|nodeInTypeDecl) != 0 {
			continue
		}
		if value, ok := check.RefinementDecides(p.Info, x); ok {
			answer := "false"
			if value {
				answer = "true"
			}
			p.Report(x.Pos(), x.End(), "the refined type of the operand makes this comparison always "+answer)
		}
	}
}
