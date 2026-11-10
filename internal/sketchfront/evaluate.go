package sketchfront

import (
	"errors"

	"ergo.local/prototype/internal/sketch/eval"
)

// Evaluate runs the root package's pure obligations, its examples first,
// with the checked syntax and Info this analysis already holds (L7; no Go
// build, no emitted module, no child process). It is a separate step from
// Check, which stays static: only an analysis with no diagnostic is
// evaluated, and the budget bounds its work.
func (a Analysis) Evaluate(b eval.Budget) (eval.Report, error) {
	pkgs := a.Checked()
	if len(pkgs) == 0 {
		return eval.Report{}, errors.New("sketchfront: evaluation needs a program that checks")
	}
	return eval.Run(pkgs[len(pkgs)-1], b)
}
