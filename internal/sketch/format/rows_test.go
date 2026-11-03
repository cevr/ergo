package format

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// rowPosition is one syntax position of a capability row (196n): src holds
// ROW where the row goes, field names the syntax field that holds the
// clause, and list marks a clause inside a comma-separated list of types,
// where a comma ends the element (grammar.md, Parser rule 9).
type rowPosition struct {
	name  string
	field string
	kw    string
	src   string
	list  bool
}

var rowPositions = []rowPosition{
	{"function", "FuncType.Needs", "needs", "func F() needs ROW {}\n", false},
	{"method", "FuncType.Needs", "needs", "func Fee(o Order) int needs ROW { 0 }\n", false},
	{"function type declaration", "FuncType.Needs", "needs", "type F func() needs ROW\n", false},
	{"function type of a let", "FuncType.Needs", "needs", "let f func() needs ROW = g\n", false},
	{"extern", "FuncType.Needs", "needs", "extern go strings.ToUpper func Upper(s string) string needs ROW\n", false},
	{"operation member", "FuncType.Needs", "needs", "type S { Get() int needs ROW }\n", false},
	{"operation member before another", "FuncType.Needs", "needs", "type S { Get() int needs ROW, Put() }\n", false},
	{"record field of function type", "FuncType.Needs", "needs", "type R { F func() needs ROW }\n", false},
	{"function literal", "FuncType.Needs", "needs", "let f = func() needs ROW {}\n", false},
	{"function literal argument", "FuncType.Needs", "needs", "func F() { g(func() needs ROW {}) }\n", false},
	{"named parameter", "FuncType.Needs", "needs", "func F(f func() needs ROW) {}\n", false},
	{"named parameter's result", "FuncType.Needs", "needs", "func F(f func() func() needs ROW) {}\n", false},
	{"result", "FuncType.Needs", "needs", "func F() func() needs ROW {}\n", false},
	{"unnamed parameter", "FuncType.Needs", "needs", "func F(func() needs ROW) {}\n", true},
	{"type argument", "FuncType.Needs", "needs", "let b Box[func() needs ROW] = x\n", true},
	{"context literal provides", "ContextLit.Provides", "provides", "func F() { let w = context provides ROW { A: a } }\n", false},
	{"context literal needs", "ContextLit.Needs", "needs", "func F() { let w = context needs ROW { A: a } }\n", false},
	{"named context provides", "ContextDecl.Provides", "provides", "let Live = context provides ROW { A: a }\n", false},
	{"named context provides before needs", "ContextDecl.Provides", "provides", "let Live = context provides ROW needs D { A: a }\n", false},
	{"named context needs", "ContextDecl.Needs", "needs", "let Live = context needs ROW { A: a }\n", false},
}

// rowNames are the names a row is built from: a qualified name stands among
// them, since a capability name may be `pkg.Name`.
var rowNames = []string{"A", "pkg.B", "C"}

// TestCapabilityRowPlacement is the placement property of 196n: at every
// position that holds a needs or provides clause, cross one, two and three
// names. The pipe form parses and is canonical. A comma between the names
// is S001 at the comma with the fix; inside a list of types the comma ends
// the element instead, so the clause holds one name (rule 3: parameters and
// type arguments keep their commas). A parenthesised row is S001 at the
// parenthesis with the fix. Each verdict comes from the rule, not from
// running the parser.
func TestCapabilityRowPlacement(t *testing.T) {
	for _, pos := range rowPositions {
		for n := 1; n <= len(rowNames); n++ {
			names := rowNames[:n]
			pipe := strings.Replace(pos.src, "ROW", strings.Join(names, " | "), 1)
			if out, err := Source(pipe); err != nil {
				t.Errorf("%s, %d names: the pipe form does not parse: %v\n%s", pos.name, n, err, pipe)
			} else if out != pipe {
				t.Errorf("%s, %d names: the pipe form is not canonical\n--- source\n%s--- formatted\n%s", pos.name, n, pipe, out)
			} else {
				check(t, pos.name, pipe, 4)
				if got := clauseNames(t, pipe); got != n {
					t.Errorf("%s, %d names: the clause holds %d names\n%s", pos.name, n, got, pipe)
				}
			}
			fix := "write " + pos.kw + " A | B"
			at := strings.Index(pos.src, "ROW")
			if n > 1 {
				comma := strings.Replace(pos.src, "ROW", strings.Join(names, ", "), 1)
				if pos.list {
					if _, err := syntax.Parse(comma); err != nil {
						t.Errorf("%s, %d names: a comma in a list ends the element, so the program parses: %v\n%s", pos.name, n, err, comma)
					} else if got := clauseNames(t, comma); got != 1 {
						t.Errorf("%s, %d names: the clause holds %d names; a comma in a list ends it after one\n%s", pos.name, n, got, comma)
					}
				} else {
					wantSyntax(t, pos.name+", comma", comma, at+len(names[0]), fix)
				}
			}
			for _, sep := range []string{", ", " | "} {
				paren := strings.Replace(pos.src, "ROW", "("+strings.Join(names, sep)+")", 1)
				wantSyntax(t, pos.name+", parenthesised", paren, at, fix)
			}
		}
	}
}

// wantSyntax asserts S001 at byte offset at, with a message that holds fix.
func wantSyntax(t *testing.T, name, src string, at int, fix string) {
	t.Helper()
	_, err := syntax.Parse(src)
	var se *syntax.Error
	if !errors.As(err, &se) {
		t.Errorf("%s: want S001 %q at offset %d, got %v\n%s", name, fix, at, err, src)
		return
	}
	if se.Code != syntax.CodeSyntax || se.Offset != at || !strings.Contains(se.Message, fix) {
		t.Errorf("%s: want S001 %q at offset %d, got %s at offset %d: %s\n%s", name, fix, at, se.Code, se.Offset, se.Message, src)
	}
}

// clauseNames returns the number of names in the one needs or provides
// clause of src.
func clauseNames(t *testing.T, src string) int {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	got, clauses := 0, 0
	syntax.Inspect(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.NeedsClause:
			if n != nil && !(len(n.Names) == 1 && isName(n.Names[0], "D")) {
				got, clauses = len(n.Names), clauses+1
			}
		case *syntax.ProvidesClause:
			if n != nil {
				got, clauses = len(n.Names), clauses+1
			}
		}
		return true
	})
	if clauses != 1 {
		t.Fatalf("%d row clauses in\n%s", clauses, src)
	}
	return got
}

func isName(x syntax.Expr, name string) bool {
	id, ok := x.(*syntax.Ident)
	return ok && id.Name == name
}

// TestCapabilityRowPositionsDerived derives every field of the syntax tree
// that holds a needs or provides clause, and asserts that rowPositions
// exercises each one and names no other.
func TestCapabilityRowPositionsDerived(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "syntax", "ast*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no syntax sources: %v", err)
	}
	derived := map[string]bool{}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return true
			}
			for _, fld := range st.Fields.List {
				star, ok := fld.Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				if id, ok := star.X.(*ast.Ident); !ok || (id.Name != "NeedsClause" && id.Name != "ProvidesClause") {
					continue
				}
				for _, name := range fld.Names {
					derived[ts.Name.Name+"."+name.Name] = true
				}
			}
			return true
		})
	}
	rows := map[string]bool{}
	for _, pos := range rowPositions {
		rows[pos.field] = true
	}
	var missing, extra []string
	for k := range derived {
		if !rows[k] {
			missing = append(missing, k)
		}
	}
	for k := range rows {
		if !derived[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("row fields with no placement position: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("positions that name no row field of the syntax package: %v", extra)
	}
}
