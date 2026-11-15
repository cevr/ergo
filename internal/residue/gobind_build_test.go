package residue_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestGobindStdBuildIsFullTier holds internal/gobind's tests to the fast
// tier's budget (gate-speed GS5): a test that binds the whole standard
// library (gobind.StdBindings: go list -export compiles every std package
// for both platforms, 1310 compiles and 277 CPU-s from an empty Go cache)
// calls needsBuild first, so only the full tier runs it. The mapping
// property, which binds a few generated packages, stays in the fast tier.
func TestGobindStdBuildIsFullTier(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob(filepath.Join(root, "internal", "gobind", "*_test.go"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no test files in internal/gobind (%v)", err)
	}
	builders := 0
	for _, name := range names {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			bindsStd := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "StdBindings" {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "gobind" {
						bindsStd = true
					}
				}
				return !bindsStd
			})
			if !bindsStd {
				continue
			}
			builders++
			if !startsWithNeedsBuild(fn.Body) {
				t.Errorf("%s: %s binds the standard library (gobind.StdBindings) but does not call needsBuild(t) first: the fast tier would compile std for both platforms", fset.Position(fn.Pos()), fn.Name.Name)
			}
		}
	}
	if builders == 0 {
		t.Error("no internal/gobind test calls gobind.StdBindings; the rule checks nothing (update or drop it)")
	}
}

func startsWithNeedsBuild(body *ast.BlockStmt) bool {
	if len(body.List) == 0 {
		return false
	}
	stmt, ok := body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := stmt.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	ident, ok := call.Fun.(*ast.Ident)
	return ok && ident.Name == "needsBuild"
}
