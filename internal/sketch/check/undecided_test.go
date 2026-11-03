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

// undecidedRows lists every node kind and field the decided parser never
// builds (spec D-A5), each with an edit that puts one into undecidedBase.
// The checker keeps no branch for any of them: each reaches the one
// default of its node switch, S109 "not produced by the decided parser",
// located at the node, with no panic and no other diagnostic.
var undecidedRows = []struct {
	name string
	edit func(f *syntax.File, fn *syntax.FuncDecl, stmt *syntax.ExprStmt, call *syntax.CallExpr)
}{
	{"GoExpr", func(_ *syntax.File, _ *syntax.FuncDecl, s *syntax.ExprStmt, call *syntax.CallExpr) {
		s.X = &syntax.GoExpr{Go: call.Pos(), Call: call}
	}},
	{"SelectStmt", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, _ *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.SelectStmt{Select: s.Pos(), Lbrace: s.Pos(), Rbrace: s.End()}
	}},
	{"SendStmt", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, call *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.SendStmt{Chan: call.Fun, Arrow: call.Lparen, Value: call.Args[0]}
	}},
	{"DeferStmt", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, _ *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.DeferStmt{Defer: s.Pos(), Stmt: s}
	}},
	{"LabeledStmt", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, call *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.LabeledStmt{Label: call.Fun.(*syntax.Ident), Colon: call.Lparen, Stmt: s}
	}},
	{"IncDecStmt", func(_ *syntax.File, fn *syntax.FuncDecl, _ *syntax.ExprStmt, call *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.IncDecStmt{X: call.Args[0], TokPos: call.Rparen, Tok: syntax.INC}
	}},
	{"EmptyStmt", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, _ *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.EmptyStmt{Semicolon: s.Pos()}
	}},
	{"ChanType", func(_ *syntax.File, fn *syntax.FuncDecl, _ *syntax.ExprStmt, _ *syntax.CallExpr) {
		p := fn.Type.Params.List[0]
		p.Type = &syntax.ChanType{Begin: p.Type.Pos(), Value: p.Type}
	}},
	{"Ellipsis", func(_ *syntax.File, fn *syntax.FuncDecl, _ *syntax.ExprStmt, _ *syntax.CallExpr) {
		p := fn.Type.Params.List[0]
		p.Type = &syntax.Ellipsis{Ellipsis: p.Type.Pos(), Elt: p.Type}
	}},
	{"ArrayType.Len", func(_ *syntax.File, fn *syntax.FuncDecl, _ *syntax.ExprStmt, call *syntax.CallExpr) {
		p := fn.Type.Params.List[0]
		p.Type = &syntax.ArrayType{Lbrack: p.Type.Pos(), Len: &syntax.BasicLit{ValuePos: p.Type.Pos(), Kind: syntax.INT, Value: "3", ValueEnd: p.Type.Pos() + 1}, Elt: p.Type}
	}},
	{"UnaryExpr.ARROW", func(_ *syntax.File, _ *syntax.FuncDecl, _ *syntax.ExprStmt, call *syntax.CallExpr) {
		call.Args[0] = &syntax.UnaryExpr{OpPos: call.Args[0].Pos(), Op: syntax.ARROW, X: call.Args[0]}
	}},
	{"CallExpr.Ellipsis", func(_ *syntax.File, _ *syntax.FuncDecl, _ *syntax.ExprStmt, call *syntax.CallExpr) {
		call.Ellipsis = call.Rparen
	}},
	{"BranchStmt.Label", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, call *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.ForStmt{For: s.Pos(), Body: &syntax.BlockStmt{Lbrace: s.Pos(), List: []syntax.Stmt{
			&syntax.BranchStmt{TokPos: s.Pos(), Tok: syntax.BREAK, Label: call.Fun.(*syntax.Ident)},
		}, Rbrace: s.End()}}
	}},
	{"ForStmt.Init", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, _ *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.ForStmt{For: s.Pos(), Init: s, Body: &syntax.BlockStmt{Lbrace: s.Pos(), Rbrace: s.End()}}
	}},
	{"ForStmt.Post", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, _ *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.ForStmt{For: s.Pos(), Post: s, Body: &syntax.BlockStmt{Lbrace: s.Pos(), Rbrace: s.End()}}
	}},
	{"TypeSwitchStmt.Init", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, _ *syntax.CallExpr) {
		u := &syntax.Ident{NamePos: s.Pos(), Name: "u"}
		fn.Body.List[0] = &syntax.TypeSwitchStmt{Switch: s.Pos(), Init: s, Subjects: []*syntax.TypeGuard{{X: u}}, Lbrace: s.Pos(), Rbrace: s.End()}
	}},
	{"IfStmt.Init", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, _ *syntax.CallExpr) {
		cond := &syntax.Ident{NamePos: s.Pos(), Name: "true"}
		fn.Body.List[0] = &syntax.IfStmt{If: s.Pos(), Init: s, Cond: cond, Body: &syntax.BlockStmt{Lbrace: s.Pos(), Rbrace: s.End()}}
	}},
	{"SwitchStmt.Init", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, call *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.SwitchStmt{Switch: s.Pos(), Init: s, Tag: call.Args[0], Lbrace: s.Pos(), Rbrace: s.End()}
	}},
	{"TypeSwitchStmt.Binds", func(_ *syntax.File, fn *syntax.FuncDecl, s *syntax.ExprStmt, call *syntax.CallExpr) {
		u := &syntax.Ident{NamePos: s.Pos(), Name: "u"}
		fn.Body.List[0] = &syntax.TypeSwitchStmt{Switch: s.Pos(), Binds: []*syntax.Ident{call.Fun.(*syntax.Ident)}, Define: s.Pos(),
			Subjects: []*syntax.TypeGuard{{X: u}}, Lbrace: s.Pos(), Rbrace: s.End()}
	}},
	{"CatchExpr.Bind", func(_ *syntax.File, _ *syntax.FuncDecl, s *syntax.ExprStmt, call *syntax.CallExpr) {
		fail := &syntax.CallExpr{Fun: &syntax.Ident{NamePos: s.Pos(), Name: "Fail"}, Lparen: call.Lparen, Rparen: call.Rparen}
		s.X = &syntax.CatchExpr{X: fail, Catch: call.Rparen, Bind: call.Fun.(*syntax.Ident), Lbrace: call.Rparen, Rbrace: call.Rparen}
	}},
	{"AssignStmt.DEFINE", func(_ *syntax.File, fn *syntax.FuncDecl, _ *syntax.ExprStmt, call *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.AssignStmt{Lhs: []syntax.Expr{call.Fun}, TokPos: call.Lparen, Tok: syntax.DEFINE, Rhs: []syntax.Expr{call.Args[0]}}
	}},
	{"AssignStmt.sides", func(_ *syntax.File, fn *syntax.FuncDecl, _ *syntax.ExprStmt, call *syntax.CallExpr) {
		fn.Body.List[0] = &syntax.AssignStmt{Lhs: []syntax.Expr{call.Args[0], call.Args[0]}, TokPos: call.Lparen, Tok: syntax.ASSIGN, Rhs: []syntax.Expr{call.Args[0]}}
	}},
	{"FuncDecl.Recv", func(_ *syntax.File, fn *syntax.FuncDecl, _ *syntax.ExprStmt, _ *syntax.CallExpr) {
		fn.Recv = fn.Type.Params
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
		d.Lparen = d.Var
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
