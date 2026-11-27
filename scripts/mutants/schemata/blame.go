package schemata

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
)

// Blame is the ids of the folded mutants a compile error at line of the
// folded file at path rests on: the mutant whose body holds the line, or
// every mutant of the function whose dispatcher holds it. None when the
// line is outside both (the baseline's own code, or the switch).
func (r Result) Blame(path string, line int) []int {
	var data []byte
	for _, f := range r.Files {
		if f.Path == path {
			data = f.Data
		}
	}
	if data == nil {
		return nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	variants := map[string][]int{}
	var holder *ast.FuncDecl
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		key := funcKey(fd)
		if base, id, ok := strings.Cut(key, "__ergomut"); ok {
			if n, err := strconv.Atoi(id); err == nil {
				variants[base] = append(variants[base], n)
			}
		}
		if fset.PositionFor(fd.Pos(), false).Line <= line && line <= fset.PositionFor(fd.End(), false).Line {
			holder = fd
		}
	}
	if holder == nil {
		return nil
	}
	key := funcKey(holder)
	if _, id, ok := strings.Cut(key, "__ergomut"); ok {
		if n, err := strconv.Atoi(id); err == nil {
			return []int{n}
		}
	}
	return variants[key]
}
