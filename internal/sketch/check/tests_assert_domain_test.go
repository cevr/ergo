package check_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// The assert placement rule (tests.go, rule 4) over its domain: every
// statement position the parser produces. assertPositions has one program
// per syntax field that holds a statement, with `$` before the assert;
// TestAssertPlacementGuard derives the fields from the syntax package's
// source, so a new statement position fails until it has a row here.

const assertDecls = `type Store {
	Get() string,
}

type Mem {}

func Get(_ Mem) string {
	"ok"
}

type Bang {}

func risky(n int) int fails Bang {
	if n < 0 {
		return Bang
	}
	n
}

type Dot {}
type Line { N int }
type Shape = Dot | Line

`

// assertPosition places an assert in one statement position: outside is a
// program where that position stands outside every test and law, inside one
// where it stands in a test body.
type assertPosition struct {
	outside, inside string
}

// assertPositions are the statement positions, by the syntax field that
// holds them.
var assertPositions = map[string]assertPosition{
	"FuncDecl.Body": {
		outside: "func f() {\n\t$assert true\n}\n",
		inside:  "test T {\n\t$assert true\n}\n",
	},
	"TestDecl.Body": {
		outside: "func f() {\n\tlet g = func() {\n\t\t$assert true\n\t}\n\tg()\n}\n\ntest T {\n\tassert true\n}\n",
		inside:  "test T {\n\t$assert true\n}\n",
	},
	"LawDecl.Body": {
		// A law body admits assert; laws themselves are stage E3b's (S109),
		// so only the placement verdict is read.
		outside: "type C {\n\tGet() int,\n\n\tlaw L(v int) { Get() == v },\n}\n\nfunc f() {\n\t$assert true\n}\n",
		inside:  "type C {\n\tGet() int,\n\n\tlaw L(v int) {\n\t\t$assert true\n\t\tGet() == v\n\t},\n}\n",
	},
	"FuncLit.Body": {
		outside: "let g = func() {\n\t$assert true\n}\n",
		inside:  "test T {\n\tlet g = func() {\n\t\t$assert true\n\t}\n\tg()\n}\n",
	},
	"CallExpr.Trailing": {
		outside: "func f() {\n\twith context { Store: Mem } {\n\t\t$assert Store.Get() == \"ok\"\n\t}\n}\n",
		inside:  "test T {\n\twith context { Store: Mem } {\n\t\t$assert Store.Get() == \"ok\"\n\t}\n}\n",
	},
	"CatchArm.Body": {
		outside: "func f() int {\n\trisky(1) catch {\n\tcase Bang:\n\t\t$assert true\n\t\t0\n\t}\n}\n",
		inside:  "test T {\n\tlet n = risky(1) catch {\n\tcase Bang:\n\t\t$assert true\n\t\t0\n\t}\n\tassert n == 1\n}\n",
	},
	"BlockStmt.List": {
		// A block stands only after a header; an else block is the one the
		// other rows do not already give.
		outside: "func f(c bool) {\n\tif c {\n\t} else {\n\t\t$assert !c\n\t}\n}\n",
		inside:  "test T(c bool) {\n\tif c {\n\t} else {\n\t\t$assert !c\n\t}\n}\n",
	},
	"IfStmt.Body": {
		outside: "func f(c bool) {\n\tif c {\n\t\t$assert c\n\t}\n}\n",
		inside:  "test T(c bool) {\n\tif c {\n\t\t$assert c\n\t}\n}\n",
	},
	"IfStmt.Else": {
		outside: "func f(c bool) {\n\tif c {\n\t} else if !c {\n\t\t$assert !c\n\t} else {\n\t}\n}\n",
		inside:  "test T(c bool) {\n\tif c {\n\t} else if !c {\n\t\t$assert !c\n\t} else {\n\t}\n}\n",
	},
	"ForStmt.Body": {
		outside: "func f(c bool) {\n\tfor c {\n\t\t$assert c\n\t\tbreak\n\t}\n}\n",
		inside:  "test T(c bool) {\n\tfor c {\n\t\t$assert c\n\t\tbreak\n\t}\n}\n",
	},
	"RangeStmt.Body": {
		outside: "func f(xs []int) {\n\tfor x in xs {\n\t\t$assert x == x\n\t}\n}\n",
		inside:  "test T(xs []int) {\n\tfor x in xs {\n\t\t$assert x == x\n\t}\n}\n",
	},
	"CaseClause.Body": {
		// A value match and a type match: two clause kinds, one field.
		outside: "func f(n int, s Shape) {\n\tmatch n {\n\tcase 1:\n\t\t$assert n == 1\n\tcase _:\n\t}\n\tmatch s {\n\tcase Dot:\n\t\t$assert true\n\tcase Line:\n\t}\n}\n",
		inside:  "test T(n int, s Shape) {\n\tmatch n {\n\tcase 1:\n\t\t$assert n == 1\n\tcase _:\n\t}\n\tmatch s {\n\tcase Dot:\n\t\t$assert true\n\tcase Line:\n\t}\n}\n",
	},
}

// assertExtra are positions inside the rows above that the rule must see
// through: a generic body, a nested literal, a context's provider and a
// package let.
var assertExtra = map[string]assertPosition{
	"generic body": {
		outside: "func f[T](x T) {\n\t$assert true\n}\n",
	},
	"literal in a literal": {
		outside: "func f() {\n\tlet g = func() {\n\t\tlet h = func() {\n\t\t\t$assert true\n\t\t}\n\t\th()\n\t}\n\tg()\n}\n",
		inside:  "test T {\n\tlet g = func() {\n\t\tlet h = func() {\n\t\t\t$assert true\n\t\t}\n\t\th()\n\t}\n\tg()\n}\n",
	},
	"catch arm in a with in a test": {
		inside: "test T {\n\twith context { Store: Mem } {\n\t\tlet n = risky(1) catch {\n\t\tcase Bang:\n\t\t\t$assert Store.Get() == \"ok\"\n\t\t\t0\n\t\t}\n\t\tassert n == 1\n\t}\n}\n",
	},
	"context provider": {
		outside: "let W = context {\n\tStore: func() Mem {\n\t\t$assert true\n\t\tMem\n\t}(),\n}\n",
	},
}

// assertNotBuilt are statement fields the decided parser never sets
// (node_inventory_test.go, notBuiltFields and the not-built family).
var assertNotBuilt = map[string]bool{
	"GoExpr.Block": true, "CommClause.Body": true, "CommClause.Comm": true,
	"DeferStmt.Stmt": true, "LabeledStmt.Stmt": true,
	"IfStmt.Init": true, "SwitchStmt.Init": true, "TypeSwitchStmt.Init": true,
	"ForStmt.Init": true, "ForStmt.Post": true,
}

const strayMessage = "assert stands only in a test or law body"

func TestAssertPlacement(t *testing.T) {
	// Each `$` marks an assert; outside, each marked line has exactly one
	// stray report and no other line has one.
	run := func(name, prog string, stray bool) {
		full := assertDecls + prog
		var want []string
		for i := strings.Index(full, "$"); i >= 0; i = strings.Index(full, "$") {
			want = append(want, fmt.Sprintf("S108:%d", strings.Count(full[:i], "\n")+1))
			full = full[:i] + full[i+1:]
		}
		_, diags := check.CheckSource(full)
		var got []string
		for _, d := range diags {
			if strings.Contains(d.Message, strayMessage) {
				got = append(got, fmt.Sprintf("%s:%d", d.Code, d.Line))
			}
		}
		found := len(got) > 0
		switch {
		case stray && strings.Join(got, " ") != strings.Join(want, " "):
			t.Errorf("%s: want stray asserts %v, got %v", name, want, diags)
		case !stray && found:
			t.Errorf("%s: an assert in a test or law body is admitted, got %v", name, diags)
		case !stray && !strings.Contains(name, "LawDecl") && len(diags) > 0:
			t.Errorf("%s: want a clean check, got %v", name, diags)
		}
	}
	for _, rows := range []map[string]assertPosition{assertPositions, assertExtra} {
		for name, p := range rows {
			if p.outside != "" {
				run(name+" outside", p.outside, true)
			}
			if p.inside != "" {
				run(name+" inside", p.inside, false)
			}
		}
	}
}

// TestAssertPlacementGuard: assertPositions names exactly the syntax fields
// that hold a statement, a statement list or a block, less the ones the
// decided parser never sets.
func TestAssertPlacementGuard(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "syntax", "ast*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no syntax sources: %v", err)
	}
	holds := func(x ast.Expr) bool {
		switch x := x.(type) {
		case *ast.Ident:
			return x.Name == "Stmt"
		case *ast.ArrayType:
			id, ok := x.Elt.(*ast.Ident)
			return ok && id.Name == "Stmt"
		case *ast.StarExpr:
			id, ok := x.X.(*ast.Ident)
			return ok && id.Name == "BlockStmt"
		}
		return false
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
				if !holds(fld.Type) {
					continue
				}
				for _, name := range fld.Names {
					derived[ts.Name.Name+"."+name.Name] = true
				}
			}
			return true
		})
	}
	var missing, extra []string
	for k := range derived {
		if _, ok := assertPositions[k]; !ok && !assertNotBuilt[k] {
			missing = append(missing, k)
		}
	}
	for k := range assertPositions {
		if !derived[k] {
			extra = append(extra, k)
		}
	}
	for k := range assertNotBuilt {
		if !derived[k] {
			extra = append(extra, k+" (not built)")
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("statement positions with no assert placement row: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("rows that name no statement field of the syntax package: %v", extra)
	}
}
