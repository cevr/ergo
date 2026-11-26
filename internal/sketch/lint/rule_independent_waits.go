package lint

import "ergo.local/prototype/internal/sketch/syntax"

// independentWaits is the speed hint for two waits in a row that need not
// wait for each other (SKILL-RULES SR-ERGO-6, SR-P05: independent awaits
// run together): two adjacent lets in a function body whose values have
// known footprints that share no capability, the second reading nothing
// the first binds, the first never leaving early (waits.go). TypeScript has to guess that two awaits
// commute; here the footprints say what each touches. Whether the order
// still matters (a log before a write) is the author's: the hint says
// "may run together" and never rewrites. Tests and laws are skipped.
var independentWaits = &Rule{
	Name:    "independent-waits",
	Code:    "L152",
	Doc:     "Two lets in a row whose values exercise disjoint capabilities, the second reading nothing the first binds: they wait one after the other but need not.",
	Instead: "start the first with task.Go and wait for it after the second, if their order does not matter",
	Default: Hint,
	Check:   checkIndependentWaits,
}

func checkIndependentWaits(p *Pass) {
	fi := p.fileIndex()
	for bi, b := range fi.blocks {
		if fi.blockFlags[bi]&nodeInFuncBody == 0 || len(b.List) < 2 {
			continue
		}
		for i := 0; i+1 < len(b.List); i++ {
			if _, ok := b.List[i].(*syntax.DeclStmt); !ok {
				continue
			}
			if _, ok := b.List[i+1].(*syntax.DeclStmt); !ok {
				continue
			}
			first, ok := waitOf(p.Info, b.List[i])
			if !ok || first.leave {
				continue
			}
			second, ok := waitOf(p.Info, b.List[i+1])
			if ok && independent(p.Info, first, second, b.List[i+1]) {
				s := b.List[i+1]
				p.Report(s.Pos(), s.End(), "this wait and the one above share no capability, and this one reads nothing the first binds")
			}
		}
	}
}
