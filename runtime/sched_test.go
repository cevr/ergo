package runtimefiles

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestSchedHooksCostNothingLive is X5's promise for the live build
// (runtime.md, Explored schedules): the scheduling points cost nothing
// without the ergoexplore tag. Each hook of runtime/rt/sched_live.go is an
// empty function, and Exploring is the constant false; every call of a
// hook in the emitted runtime passes only plain values (names, fields,
// addresses, literals of them), so the inliner removes the call and its
// arguments with it, or it sits inside `if Exploring`, which the compiler
// drops. A hook with a body, or an argument that calls or allocates
// outside the guard, fails here. It reads the embedded sources, the bytes
// an emitted module compiles.
func TestSchedHooksCostNothingLive(t *testing.T) {
	files, err := Files("runtime/v1")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	hooks := map[string]bool{}
	var parsed []*ast.File
	for _, f := range files {
		file, err := parser.ParseFile(fset, f.Path, f.Data, 0)
		if err != nil {
			t.Fatal(err)
		}
		if f.Path == "runtime/rt/sched_live.go" {
			for _, d := range file.Decls {
				switch d := d.(type) {
				case *ast.FuncDecl:
					hooks[d.Name.Name] = true
					if len(d.Body.List) != 0 {
						t.Errorf("%s: the live hook %s has a body", fset.Position(d.Pos()), d.Name.Name)
					}
				case *ast.GenDecl:
					for _, s := range d.Specs {
						if v, ok := s.(*ast.ValueSpec); ok && len(v.Names) == 1 && v.Names[0].Name == "Exploring" {
							if id, ok := v.Values[0].(*ast.Ident); !ok || id.Name != "false" || d.Tok != token.CONST {
								t.Errorf("%s: Exploring is not the constant false", fset.Position(v.Pos()))
							}
						}
					}
				}
			}
			continue
		}
		if !strings.HasPrefix(f.Path, "runtime/rt/sched") {
			parsed = append(parsed, file)
		}
	}
	if !hooks["Point"] || !hooks["Block"] {
		t.Fatalf("no hooks in runtime/rt/sched_live.go: %v", hooks)
	}
	calls := 0
	for _, file := range parsed {
		var stack []ast.Node
		ast.Inspect(file, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			call, ok := n.(*ast.CallExpr)
			if !ok || !hooks[hookName(call.Fun, file.Name.Name)] {
				return true
			}
			calls++
			if guarded(stack) {
				return true
			}
			for _, a := range call.Args {
				if !plain(a) {
					t.Errorf("%s: a hook's argument is not a plain value outside `if Exploring`", fset.Position(a.Pos()))
				}
			}
			return true
		})
	}
	if calls == 0 {
		t.Fatal("no hook calls in the runtime")
	}
}

// hookName is the hook a call's function names: Name in package rt, or
// rt.Name elsewhere; "" otherwise.
func hookName(fun ast.Expr, pkg string) string {
	switch f := fun.(type) {
	case *ast.Ident:
		if pkg == "rt" {
			return f.Name
		}
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok && x.Name == "rt" {
			return f.Sel.Name
		}
	}
	return ""
}

// guarded reports whether the innermost node of stack lies in the body of
// an `if Exploring` or `if rt.Exploring`.
func guarded(stack []ast.Node) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		s, ok := stack[i].(*ast.IfStmt)
		if !ok || stack[i+1] != s.Body {
			continue
		}
		switch c := s.Cond.(type) {
		case *ast.Ident:
			if c.Name == "Exploring" {
				return true
			}
		case *ast.SelectorExpr:
			if x, ok := c.X.(*ast.Ident); ok && x.Name == "rt" && c.Sel.Name == "Exploring" {
				return true
			}
		}
	}
	return false
}

// plain reports whether x evaluates with no call, no closure and no
// allocation: a name, a field, an address of one, a basic literal, or a
// composite literal of those.
func plain(x ast.Expr) bool {
	switch x := x.(type) {
	case *ast.Ident, *ast.BasicLit:
		return true
	case *ast.SelectorExpr:
		return plain(x.X)
	case *ast.ParenExpr:
		return plain(x.X)
	case *ast.UnaryExpr:
		// An address of a name or a field; &T{} allocates.
		_, lit := x.X.(*ast.CompositeLit)
		return x.Op == token.AND && !lit && plain(x.X)
	case *ast.CompositeLit:
		for _, e := range x.Elts {
			if kv, ok := e.(*ast.KeyValueExpr); ok {
				e = kv.Value
			}
			if !plain(e) {
				return false
			}
		}
		return true
	}
	return false
}
