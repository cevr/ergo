package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// Every new function frame goes through a constructor. In particular an
// inner frame must inherit the enclosing generic signature's facts.
func TestE1FunctionContextConstruction(t *testing.T) {
	files, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	innerCalls := 0
	for _, entry := range files {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, _ := decl.(*ast.FuncDecl)
			constructor := fn != nil && (fn.Name.Name == "newFuncCtx" || fn.Name.Name == "innerFuncCtx")
			inner := fn != nil && fn.Recv != nil && strings.Contains(astTextType(fn.Recv.List[0].Type), "funcCtx")
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CompositeLit:
					if astTextType(n.Type) == "funcCtx" && !constructor {
						t.Errorf("%s: funcCtx literal bypasses its constructor", fset.Position(n.Pos()))
					}
				case *ast.ValueSpec:
					if astTextType(n.Type) == "funcCtx" {
						t.Errorf("%s: zero funcCtx bypasses its constructor", fset.Position(n.Pos()))
					}
				case *ast.CallExpr:
					if id, ok := n.Fun.(*ast.Ident); ok {
						if id.Name == "new" && len(n.Args) == 1 && astTextType(n.Args[0]) == "funcCtx" {
							t.Errorf("%s: new(funcCtx) bypasses its constructor", fset.Position(n.Pos()))
						}
						if id.Name == "newFuncCtx" && inner {
							t.Errorf("%s: inner frame must use innerFuncCtx", fset.Position(n.Pos()))
						}
					}
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "innerFuncCtx" {
						innerCalls++
					}
				}
				return true
			})
		}
	}
	if innerCalls == 0 {
		t.Fatal("no inner frame uses innerFuncCtx")
	}
}

func astTextType(x ast.Expr) string {
	switch x := x.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return "*" + astTextType(x.X)
	}
	return ""
}

func TestErgoRunE1ClosureWitnesses(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"generic-call", `All(x)`},
		{"operation-call", `x.Check()`},
		{"conversion", `let v Valid = Box[T]{V: x}; v.Check()`},
		{"method-value", `let f = x.Check; f()`},
		{"nested-closure", `let f = func() bool { All(x) }; f()`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := nestedWitnessDecls + `func Mk[T Valid](x T) func() bool { func() bool { ` + tc.body + ` } }
func main() needs Console {
	let yes = Mk(Card{N: 1})
	let no = Mk(Card{N: 0})
	Console.Println("\(yes()) \(no())")
}`
			wantRun(t, src, "true false\n")
		})
	}
}
