package check_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// The frame clients must share the boundary decision. Reading a trailing
// block's signature or enumerating literal frames is fine; pruning either
// subtree in a client would silently lose enclosing-frame effects/returns.
func TestWireOwnershipFrameOwner(t *testing.T) {
	clients := map[string]bool{"providerResults": false, "ownedBlock": false, "walk": false}
	for _, name := range []string{"withown.go", "footprint.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "inspectFrame" {
						if _, client := clients[fn.Name.Name]; client {
							clients[fn.Name.Name] = true
						}
					}
				}
				var boundary, branch ast.Node
				switch n := n.(type) {
				case *ast.IfStmt:
					boundary, branch = n.Cond, n.Body
				case *ast.CaseClause:
					for _, expr := range n.List {
						if p, ok := expr.(*ast.StarExpr); ok {
							if sel, ok := p.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "FuncLit" {
								boundary, branch = expr, n
							}
						}
					}
				}
				if boundary == nil {
					return true
				}
				prunesFrame := false
				ast.Inspect(boundary, func(n ast.Node) bool {
					if sel, ok := n.(*ast.SelectorExpr); ok && (sel.Sel.Name == "Trailing" || sel.Sel.Name == "FuncLit") {
						prunesFrame = true
					}
					return true
				})
				if prunesFrame {
					ast.Inspect(branch, func(n ast.Node) bool {
						if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
							if id, ok := r.Results[0].(*ast.Ident); ok && id.Name == "false" {
								t.Errorf("%s:%s prunes a frame boundary outside inspectFrame", name, fn.Name.Name)
							}
						}
						return true
					})
				}
				return true
			})
		}
	}
	for name, uses := range clients {
		if !uses {
			t.Errorf("%s must use inspectFrame", name)
		}
	}
}
