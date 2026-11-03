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
	"ergo.local/prototype/internal/sketch/syntax"
)

// The assert placement rule (tests.go, rule 4) over its domain: every
// statement position the parser produces × every lexical owner of that
// position. assertPositions has one statement form per syntax field that
// holds a statement, with `$` before the assert; assertOwners has one body
// per kind of owner (a declaration, a test, a law, and a function literal
// standing in each of them). TestAssertPlacementGuard derives the fields
// from the syntax package's source, so a new statement position or a new
// kind of body fails until it has a row in one table or the other.
//
// The verdict of each program is read twice, independently: from the
// owner's admits label, and from the parsed tree (assertOwnerAdmits, the
// innermost test, law, function declaration or function literal around
// each assert). The checker must agree with both.

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

// assertPosition is one statement position: the statements that place an
// assert there (`$` before each assert) and the parameters they read.
type assertPosition struct {
	params, stmts string
}

// assertPositions are the statement positions, by the syntax field that
// holds them. A key in parentheses is no field: the owner's own statement
// list, or a composite of two positions.
var assertPositions = map[string]assertPosition{
	"(direct)": {stmts: "$assert true\n"},
	"CallExpr.Trailing": {
		stmts: "with context { Store: Mem } {\n$assert Store.Get() == \"ok\"\n}\n",
	},
	"CatchArm.Body": {
		stmts: "let _ = risky(1) catch {\ncase Bang:\n$assert true\n0\n}\n",
	},
	"BlockStmt.List": {
		// A block stands only after a header; an else block is the one the
		// other rows do not already give.
		params: "c bool",
		stmts:  "if c {\n} else {\n$assert !c\n}\n",
	},
	"IfStmt.Body": {params: "c bool", stmts: "if c {\n$assert c\n}\n"},
	"IfStmt.Else": {
		params: "c bool",
		stmts:  "if c {\n} else if !c {\n$assert !c\n} else {\n}\n",
	},
	"ForStmt.Body":   {params: "c bool", stmts: "for c {\n$assert c\nbreak\n}\n"},
	"RangeStmt.Body": {params: "xs []int", stmts: "for x in xs {\n$assert x == x\n}\n"},
	"CaseClause.Body": {
		// A value match and a type match: two clause kinds, one field.
		params: "n int, s Shape",
		stmts:  "match n {\ncase 1:\n$assert n == 1\ncase _:\n}\nmatch s {\ncase Dot:\n$assert true\ncase Line:\n}\n",
	},
	"(catch arm in a with)": {
		stmts: "with context { Store: Mem } {\nlet _ = risky(1) catch {\ncase Bang:\n$assert Store.Get() == \"ok\"\n0\n}\n}\n",
	},
}

// assertOwner is one lexical owner: the body that holds a position's
// statements, by the syntax field of that body, and whether the rule admits
// an assert there. Only a test or a law body does; a function literal does
// not, wherever it stands, since its body runs when it is called.
type assertOwner struct {
	field  string
	admits bool
	// noParams: the owner cannot take the position's parameters.
	noParams bool
	program  func(params, stmts string) string
}

func assertTest(params, stmts string) string {
	if params == "" {
		return "test T {\n" + stmts + "}\n"
	}
	return "test T(" + params + ") {\n" + stmts + "}\n"
}

func assertLaw(params, stmts string) string {
	if params != "" {
		params = ", " + params
	}
	return "type C {\n\tGet() int,\n\n\tlaw L(v int" + params + ") {\n" + stmts + "Get() == v\n},\n}\n"
}

// assertLit binds a function literal holding stmts to name and calls it.
func assertLit(name, stmts string) string {
	return "let " + name + " = func() {\n" + stmts + "}\n" + name + "()\n"
}

var assertOwners = map[string]assertOwner{
	"function": {field: "FuncDecl.Body", program: func(p, s string) string {
		return "func f(" + p + ") {\n" + s + "}\n"
	}},
	"generic function": {field: "FuncDecl.Body", program: func(p, s string) string {
		if p != "" {
			p = ", " + p
		}
		return "func f[T](_ T" + p + ") {\n" + s + "}\n"
	}},
	"test": {field: "TestDecl.Body", admits: true, program: assertTest},
	"law":  {field: "LawDecl.Body", admits: true, program: assertLaw},
	"package literal": {field: "FuncLit.Body", program: func(p, s string) string {
		return "let g = func(" + p + ") {\n" + s + "}\n"
	}},
	"literal in a function": {field: "FuncLit.Body", program: func(p, s string) string {
		return "func f(" + p + ") {\n" + assertLit("g", s) + "}\n"
	}},
	"literal in a literal in a function": {field: "FuncLit.Body", program: func(p, s string) string {
		return "func f(" + p + ") {\n" + assertLit("g", assertLit("h", s)) + "}\n"
	}},
	"literal in a test": {field: "FuncLit.Body", program: func(p, s string) string {
		return assertTest(p, assertLit("g", s))
	}},
	"literal in a literal in a test": {field: "FuncLit.Body", program: func(p, s string) string {
		return assertTest(p, assertLit("g", assertLit("h", s)))
	}},
	"literal in a with in a test": {field: "FuncLit.Body", program: func(p, s string) string {
		return assertTest(p, "with context { Store: Mem } {\n"+assertLit("g", s)+"}\n")
	}},
	"literal in a law": {field: "FuncLit.Body", program: func(p, s string) string {
		return assertLaw(p, assertLit("g", s))
	}},
	"literal in a context provider": {field: "FuncLit.Body", noParams: true, program: func(_, s string) string {
		return "let W = context {\n\tStore: func() Mem {\n" + s + "Mem\n}(),\n}\n"
	}},
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

// assertOwnerAdmits reads the placement verdict of every assert from the
// parsed tree, apart from the checker: an assert's lexical owner is the
// innermost test, law, function declaration or function literal around
// it, and only a test or a law admits it. It gives the line of each assert
// whose owner does not admit it.
func assertOwnerAdmits(t *testing.T, src string) []string {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	var stray []string
	var owners []syntax.Node
	var visit func(n syntax.Node)
	visit = func(root syntax.Node) {
		syntax.Inspect(root, func(n syntax.Node) bool {
			if n == root {
				return true
			}
			switch n := n.(type) {
			case *syntax.FuncDecl, *syntax.TestDecl, *syntax.LawDecl, *syntax.FuncLit:
				owners = append(owners, n)
				visit(n)
				owners = owners[:len(owners)-1]
				return false
			case *syntax.AssertStmt:
				admitted := false
				if len(owners) > 0 {
					switch owners[len(owners)-1].(type) {
					case *syntax.TestDecl, *syntax.LawDecl:
						admitted = true
					}
				}
				if !admitted {
					stray = append(stray, fmt.Sprintf("S108:%d", strings.Count(src[:int(n.Pos())], "\n")+1))
				}
			}
			return true
		})
	}
	visit(f)
	return stray
}

func TestAssertPlacement(t *testing.T) {
	programs := 0
	for oname, owner := range assertOwners {
		for pname, pos := range assertPositions {
			if owner.noParams && pos.params != "" {
				continue
			}
			programs++
			name := pname + " in " + oname
			full := assertDecls + owner.program(pos.params, pos.stmts)
			var marked []string
			for i := strings.Index(full, "$"); i >= 0; i = strings.Index(full, "$") {
				marked = append(marked, fmt.Sprintf("S108:%d", strings.Count(full[:i], "\n")+1))
				full = full[:i] + full[i+1:]
			}
			// The owner's label and the tree must agree on the verdict.
			var want []string
			if !owner.admits {
				want = marked
			}
			tree := assertOwnerAdmits(t, full)
			if strings.Join(tree, " ") != strings.Join(want, " ") {
				t.Errorf("%s: the owner says %v, the parsed tree %v\n%s", name, want, tree, full)
				continue
			}
			_, diags := check.CheckSource(full)
			var got []string
			for _, d := range diags {
				if strings.Contains(d.Message, strayMessage) {
					got = append(got, fmt.Sprintf("%s:%d", d.Code, d.Line))
				}
			}
			switch {
			case strings.Join(got, " ") != strings.Join(want, " "):
				t.Errorf("%s: want stray asserts %v, got %v\n%s", name, want, diags, full)
			case owner.admits && len(diags) > 0:
				t.Errorf("%s: want a clean check, got %v\n%s", name, diags, full)
			}
		}
	}
	// Every owner meets every position it can take, and every position
	// meets an owner that admits it and one that does not.
	admits, refuses := map[string]bool{}, map[string]bool{}
	for _, owner := range assertOwners {
		for pname, pos := range assertPositions {
			if owner.noParams && pos.params != "" {
				continue
			}
			if owner.admits {
				admits[pname] = true
			} else {
				refuses[pname] = true
			}
		}
	}
	for pname := range assertPositions {
		if !admits[pname] || !refuses[pname] {
			t.Errorf("position %s: admitted by an owner %v, refused by one %v", pname, admits[pname], refuses[pname])
		}
	}
	t.Logf("%d programs: %d positions × %d owners", programs, len(assertPositions), len(assertOwners))
}

// TestAssertPlacementGuard: assertPositions and the owners' fields name
// exactly the syntax fields that hold a statement, a statement list or a
// block, less the ones the decided parser never sets.
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
	rows := map[string]bool{}
	for k := range assertPositions {
		if !strings.HasPrefix(k, "(") {
			rows[k] = true
		}
	}
	for _, o := range assertOwners {
		rows[o.field] = true
	}
	var missing, extra []string
	for k := range derived {
		if !rows[k] && !assertNotBuilt[k] {
			missing = append(missing, k)
		}
	}
	for k := range rows {
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
