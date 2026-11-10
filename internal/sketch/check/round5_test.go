package check

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
)

// goErrors checks src with go/types and returns its errors.
func goErrors(src string) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		return []string{err.Error()}
	}
	var errs []string
	conf := types.Config{Error: func(e error) { errs = append(errs, e.Error()) }}
	_, _ = conf.Check("p", fset, []*ast.File{file}, nil)
	return errs
}

func mentions(ds []string, sub string) bool {
	for _, d := range ds {
		if strings.Contains(d, sub) {
			return true
		}
	}
	return false
}
