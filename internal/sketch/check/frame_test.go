package check_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The frame clients must share the boundary decision (inspectFrame in
// frame.go). Reading a trailing block's signature or enumerating literal
// frames is fine; pruning either subtree in a client would silently lose
// enclosing-frame effects and returns. So in withown.go and footprint.go
// every syntax walk either goes through inspectFrame, which owns the
// boundary, or visits the whole tree: a callback given to syntax.Inspect is
// a function literal whose every return is the literal true. Any other
// return (false, a helper's verdict, a stored field, a comparison) or a
// callback that is not a literal could prune a frame boundary, however it
// is spelt (runtime-199 stage 3b r3 t2).
func TestWireOwnershipFrameOwner(t *testing.T) {
	clients := map[string]bool{"providerResults": false, "ownedBlock": false, "walk": false}
	const syntaxPath = "ergo.local/prototype/internal/sketch/syntax"
	walks := 0
	callbacks := syntaxCallbacks(t)
	for _, name := range []string{"withown.go", "footprint.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		// The file's name for the syntax package, renamed or not.
		syntaxName := ""
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == syntaxPath {
				syntaxName = "syntax"
				if imp.Name != nil {
					syntaxName = imp.Name.Name
				}
			}
		}
		if syntaxName == "" || syntaxName == "." || syntaxName == "_" {
			t.Fatalf("%s: imports the syntax package as %q; the scan reads it by a package name", name, syntaxName)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "inspectFrame" {
					if _, client := clients[fn.Name.Name]; client {
						clients[fn.Name.Name] = true
					}
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != syntaxName {
					return true
				}
				for _, i := range callbacks[sel.Sel.Name] {
					if i >= len(call.Args) {
						continue
					}
					arg := call.Args[i]
					walks++
					lit, ok := arg.(*ast.FuncLit)
					if !ok {
						t.Errorf("%s:%s passes %s a callback that is not a function literal; a frame walk goes through inspectFrame", name, fn.Name.Name, sel.Sel.Name)
						continue
					}
					for _, r := range returnsOf(lit.Body) {
						if len(r.Results) != 1 || !isIdent(r.Results[0], "true") {
							t.Errorf("%s:%s: a syntax.%s callback returns something other than true, so it can prune a frame boundary outside inspectFrame", name, fn.Name.Name, sel.Sel.Name)
						}
					}
				}
				return true
			})
		}
	}
	if walks == 0 {
		t.Error("no whole-tree syntax walk found: the scan is vacuous")
	}
	for name, uses := range clients {
		if !uses {
			t.Errorf("%s must use inspectFrame", name)
		}
	}
}

// syntaxCallbacks reads the syntax package's declarations: for each of its
// functions, the positions of the parameters of function type, the
// callbacks a walk calls.
func syntaxCallbacks(t *testing.T) map[string][]int {
	t.Helper()
	out := map[string][]int{}
	files, err := filepath.Glob("../syntax/*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no syntax package sources: %v", err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() {
				continue
			}
			i := 0
			for _, field := range fn.Type.Params.List {
				n := max(len(field.Names), 1)
				if _, ok := field.Type.(*ast.FuncType); ok {
					for j := 0; j < n; j++ {
						out[fn.Name.Name] = append(out[fn.Name.Name], i+j)
					}
				}
				i += n
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("the syntax package declares no walk with a callback: the scan is vacuous")
	}
	return out
}

func isIdent(x ast.Expr, name string) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == name
}

// returnsOf lists the return statements of a function body, not of the
// literals nested in it.
func returnsOf(body *ast.BlockStmt) []*ast.ReturnStmt {
	var out []*ast.ReturnStmt
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			out = append(out, n)
		}
		return true
	})
	return out
}
