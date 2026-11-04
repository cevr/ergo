package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// undecidedBase is the decided program each undecided row edits by hand:
// the decided parser never builds the node or field the row puts in it
// (syntax's TestDecidedOnly), so only a hand-built tree reaches the
// checker with one.
const undecidedBase = `import timeout

type A {}

func G(x int) {}

func Fail() fails A { return A }

func F(x int, u A) {
	G(x)
}

let V = 1
`

// undecidedRows lists every shape the decided parser never builds and the
// tree can still express (spec D-A5; lower-v1 stage 5 deleted the nodes
// and fields of retired forms, D10), each with an edit that puts one into
// undecidedBase.
// The checker keeps no branch for any of them: each reaches the one
// default of its node switch, S109 "not produced by the decided parser",
// located at the node, with no panic and no other diagnostic.
var undecidedRows = []struct {
	name string
	edit func(f *syntax.File, fn *syntax.FuncDecl, stmt *syntax.ExprStmt, call *syntax.CallExpr)
}{
	{"UnaryExpr.ARROW", func(_ *syntax.File, _ *syntax.FuncDecl, _ *syntax.ExprStmt, call *syntax.CallExpr) {
		call.Args[0] = &syntax.UnaryExpr{OpPos: call.Args[0].Pos(), Op: syntax.ARROW, X: call.Args[0]}
	}},
	{"AssignStmt.DEFINE", func(_ *syntax.File, fn *syntax.FuncDecl, _ *syntax.ExprStmt, call *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.AssignStmt{Lhs: []syntax.Expr{call.Fun}, TokPos: call.Lparen, Tok: syntax.DEFINE, Rhs: []syntax.Expr{call.Args[0]}}
	}},
	{"AssignStmt.sides", func(_ *syntax.File, fn *syntax.FuncDecl, _ *syntax.ExprStmt, call *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.AssignStmt{Lhs: []syntax.Expr{call.Args[0], call.Args[0]}, TokPos: call.Lparen, Tok: syntax.ASSIGN, Rhs: []syntax.Expr{call.Args[0]}}
	}},
	{"ImportSpec.ImportDot", func(f *syntax.File, _ *syntax.FuncDecl, _ *syntax.ExprStmt, _ *syntax.CallExpr) {
		f.Decls[0].(*syntax.ImportDecl).Specs[0].Kind = syntax.ImportDot
	}},
	{"ImportDecl.grouped", func(f *syntax.File, _ *syntax.FuncDecl, _ *syntax.ExprStmt, _ *syntax.CallExpr) {
		d := f.Decls[0].(*syntax.ImportDecl)
		d.Specs = append(d.Specs, d.Specs[0])
	}},
	{"VarDecl.grouped", func(f *syntax.File, _ *syntax.FuncDecl, _ *syntax.ExprStmt, _ *syntax.CallExpr) {
		d := f.Decls[len(f.Decls)-1].(*syntax.VarDecl)
		d.Specs = append(d.Specs, d.Specs[0])
	}},
}

// TestUndecidedNodes (spec D-A5): a node or field the decided parser never
// builds, put into a tree by hand, is S109 "not produced by the decided
// parser" at that node, with no other diagnostic and no panic.
func TestUndecidedNodes(t *testing.T) {
	for _, row := range undecidedRows {
		t.Run(row.name, func(t *testing.T) {
			f, err := syntax.Parse(undecidedBase)
			if err != nil {
				t.Fatal(err)
			}
			var fn *syntax.FuncDecl
			for _, d := range f.Decls {
				if d, ok := d.(*syntax.FuncDecl); ok && d.Name.Name == "F" {
					fn = d
				}
			}
			stmt := fn.Body.List[0].(*syntax.ExprStmt)
			row.edit(f, fn, stmt, stmt.X.(*syntax.CallExpr))
			_, diags := check.Check("main", []*syntax.File{f}, nil)
			var hits, other []check.Diagnostic
			for _, d := range diags {
				if d.Code == check.CodeOutside && strings.Contains(d.Message, "not produced by the decided parser") {
					hits = append(hits, d)
				} else if !strings.Contains(d.Message, "imported and not used") {
					other = append(other, d)
				}
			}
			if len(hits) != 1 || len(other) > 0 {
				t.Fatalf("want one S109 not produced by the decided parser, got %s", describe(diags))
			}
			if hits[0].Line == 0 {
				t.Fatalf("the S109 is not located: %s", describe(diags))
			}
		})
	}
}
