package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
	v1 "ergo.local/prototype/runtime/v1"
)

// The result-alias summary of a reviewed Go body (OPT6; types.md,
// Bindings, Result aliasing). A std extern bound to a runtime/v1 function
// has no Ergo body for resultDeps to walk, but its Go body ships with the
// compiler. When every return statement of that body gives Go's make(…),
// or nil, the result is memory the call made: no parameter and no other
// binding reaches a Map or slice of it, so it is fresh (values.go,
// freshResult), as the result of an Ergo body is when every value it
// returns is fresh. make gives zero elements and zero entries, so the
// result holds no part of anything else. Any other return (a parameter, a
// field, a call, a composite literal whose elements may share) leaves the
// result shared. The fact reads the body's form only, never its name
// (internal/residue, TestLoweringKeysOnNoStdName).
//
// Prior art: Go's own escape analysis summarizes each function's results
// ("leaking param content", "make(...) escapes to heap" for a new
// result) and reads the summary at the call; GHC and MLton keep a demand
// and allocation summary per binding; Gleam, Elixir and ReScript need none,
// since their lists and maps are immutable.
// goBodies are runtime/v1's top-level functions by name, parsed once.
var goBodies = sync.OnceValue(func() map[string]*ast.FuncDecl {
	out := map[string]*ast.FuncDecl{}
	names, err := fs.Glob(v1.Source, "*.go")
	if err != nil {
		return out
	}
	fset := token.NewFileSet()
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := fs.ReadFile(v1.Source, name)
		if err != nil {
			continue
		}
		f, err := parser.ParseFile(fset, name, data, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Body != nil {
				out[fd.Name.Name] = fd
			}
		}
	}
	return out
})

// goBodyMakes reports whether the runtime/v1 function name makes its
// result (above): one result, not named, and every return gives make(…)
// or nil.
func goBodyMakes(name string) bool {
	fd := goBodies()[name]
	if fd == nil || fd.Type.Results == nil || len(fd.Type.Results.List) != 1 || len(fd.Type.Results.List[0].Names) > 0 {
		return false
	}
	returns := 0
	ok := true
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return false // a literal's returns are its own
		case *ast.ReturnStmt:
			returns++
			if len(x.Results) != 1 || !madeValue(x.Results[0]) {
				ok = false
			}
		}
		return ok
	})
	return ok && returns > 0
}

// madeValue reports a Go expression whose value is new memory with no
// part of anything else: make(…) or nil.
func madeValue(x ast.Expr) bool {
	for {
		p, ok := x.(*ast.ParenExpr)
		if !ok {
			break
		}
		x = p.X
	}
	switch x := x.(type) {
	case *ast.Ident:
		return x.Name == "nil"
	case *ast.CallExpr:
		id, ok := x.Fun.(*ast.Ident)
		return ok && id.Name == "make"
	}
	return false
}

// madeResult reports a std extern whose runtime/v1 body makes its result
// (goBodyMakes), so a call's result is fresh. Only std binds runtime/v1
// (a user extern binds Go's standard library).
func (l *lowerer) madeResult(f *check.Func) bool {
	if f == nil || f.Decl == nil || f.Decl.Extern == nil {
		return false
	}
	path, name := f.Decl.Extern.Split()
	return path == runtimeImport && goBodyMakes(name)
}
