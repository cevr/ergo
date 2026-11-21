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
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Body == nil {
			continue
		}
		syntax.Inspect(f.Body, func(n syntax.Node) bool {
			b, ok := n.(*syntax.BlockStmt)
			if !ok {
				return true
			}
			for i := 0; i+1 < len(b.List); i++ {
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
			return true
		})
	}
}
