package bindc

import (
	"go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestNameScopeOneWriter: a name enters a nameScope only through claim
// (names.go). The test reads the package's source (not its tests) and
// fails when:
//   - nameScope stores names in a map other than owner (a second store
//     would be a second way in);
//   - a function other than (*nameScope).claim writes owner: an assignment
//     to it or to one of its elements, a composite literal that sets it,
//     delete, clear, range, or any use that is not a read of one element
//     (x.owner[k] on the right of an assignment or in an expression);
//   - claim does not write owner (a control: the scan sees the writer).
//
// So the Ergo import name, the generated names and every C name pass the
// same check of reserved and taken names, and no owner is replaced.
func TestNameScopeOneWriter(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := gotoken.NewFileSet()
	var stores, bad []string
	claimWrites := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := goparser.ParseFile(fset, name, nil, goparser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if ts, ok := n.(*ast.TypeSpec); ok && ts.Name.Name == "nameScope" {
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					t.Fatalf("%s: nameScope is not a struct", fset.Position(ts.Pos()))
				}
				for _, fl := range st.Fields.List {
					if _, isMap := fl.Type.(*ast.MapType); isMap {
						for _, id := range fl.Names {
							stores = append(stores, id.Name)
						}
					}
				}
			}
			return true
		})
		for _, d := range f.Decls {
			fn := declName(d)
			var stack []ast.Node
			ast.Inspect(d, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				mention := false
				switch n := n.(type) {
				case *ast.SelectorExpr:
					mention = n.Sel.Name == "owner"
				case *ast.KeyValueExpr:
					id, ok := n.Key.(*ast.Ident)
					mention = ok && id.Name == "owner"
				}
				if mention {
					read := isElementRead(n, stack)
					switch {
					case fn == "(*nameScope).claim":
						if !read {
							claimWrites++
						}
					case !read:
						bad = append(bad, fset.Position(n.Pos()).String()+" in "+fn)
					}
				}
				stack = append(stack, n)
				return true
			})
		}
	}
	sort.Strings(stores)
	if strings.Join(stores, ",") != "owner" {
		t.Errorf("nameScope stores names in the maps %v, want only owner, which claim writes", stores)
	}
	if claimWrites == 0 {
		t.Errorf("control: the scan finds no write of owner in (*nameScope).claim; the scan is broken")
	}
	for _, b := range bad {
		t.Errorf("%s: writes or exposes nameScope.owner; only claim may add a name to a scope", b)
	}
}

// declName is the name of a top-level declaration: "(*T).m", "f", or
// "package-level" for a var, const or type declaration.
func declName(d ast.Decl) string {
	fd, ok := d.(*ast.FuncDecl)
	if !ok {
		return "package-level"
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	switch rt := fd.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := rt.X.(*ast.Ident); ok {
			return "(*" + id.Name + ")." + fd.Name.Name
		}
	case *ast.Ident:
		return "(" + rt.Name + ")." + fd.Name.Name
	}
	return fd.Name.Name
}

// isElementRead reports whether the selector n, under the nodes of stack
// (outermost first), is x.owner[k] read as a value: the X of an index
// expression that is not assigned, incremented, ranged into or addressed.
func isElementRead(n ast.Node, stack []ast.Node) bool {
	if len(stack) == 0 {
		return false
	}
	ix, ok := stack[len(stack)-1].(*ast.IndexExpr)
	if !ok || ix.X != n {
		return false
	}
	if len(stack) < 2 {
		return true
	}
	switch p := stack[len(stack)-2].(type) {
	case *ast.AssignStmt:
		for _, l := range p.Lhs {
			if l == ix {
				return false
			}
		}
	case *ast.IncDecStmt:
		return false
	case *ast.RangeStmt:
		return p.Key != ix && p.Value != ix
	case *ast.UnaryExpr:
		return p.Op != gotoken.AND
	}
	return true
}
