package check_test

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// checkSourceInfer is check.CheckSource with ergo fix's inference
// (check.Options.Infer), for the tests of the inference engine, which
// writes ergo fix's clauses (EXPLICIT F9).
func checkSourceInfer(src string) (*check.Package, []check.Diagnostic) {
	file, err := syntax.Parse(src)
	if err != nil {
		return nil, []check.Diagnostic{{Code: check.CodeSyntax, Message: err.Error()}}
	}
	return check.CheckOptions("main", []*syntax.File{file}, nil, check.Options{Infer: true})
}
