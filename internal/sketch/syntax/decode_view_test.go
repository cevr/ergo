package syntax

// The decoder property (capstone S2e): compiler/syntax's rows read two
// ways print the same canonical dump. One reading is the view
// (compiler/syntax/dump.Parse, linked through the IR as
// internal/linked/compiler and run in process); the other is this package's
// decoder (decode.go), whose Go tree the dumper below prints: one line per
// node in field order, with its Pos and End, its positions and its scalars.
// The declarations the decoder derives (refine.go, pair.go) and the
// expansion of or-patterns (orpattern.go) are left out, since the view
// holds the written tree only.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	linked "ergo.local/prototype/internal/linked/compiler"
)

type dumper struct {
	b      strings.Builder
	groups map[*CommentGroup]int
}

func (d *dumper) line(depth int, format string, args ...any) {
	fmt.Fprintf(&d.b, "%d ", depth)
	fmt.Fprintf(&d.b, format, args...)
	d.b.WriteByte('\n')
}

func (d *dumper) doc(g *CommentGroup) int {
	if g == nil {
		return -1
	}
	if i, ok := d.groups[g]; ok {
		return i
	}
	return -2
}

func b01(b bool) int {
	if b {
		return 1
	}
	return 0
}

// list prints a list's length, then its items.
func (d *dumper) list(depth int, xs []Node) {
	d.line(depth, "list %d", len(xs))
	for _, x := range xs {
		d.node(depth+1, x)
	}
}

func exprs[T Node](xs []T) []Node {
	out := make([]Node, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

func idents(xs []*Ident) []Node { return exprs(xs) }

func isNil(n Node) bool { return n == nil || IsNil(n) }

func (d *dumper) node(depth int, n Node) {
	if isNil(n) {
		d.line(depth, "nil")
		return
	}
	at := fmt.Sprintf("%d %d", n.Pos(), n.End())
	c := depth + 1
	switch x := n.(type) {
	case *File:
		d.line(depth, "File %s EOF=%d", at, x.EOF)
		var decls []Node
		for _, dc := range x.Decls {
			if !IsDerived(dc) {
				decls = append(decls, dc)
			}
		}
		d.list(c, decls)
	case *ImportDecl:
		d.line(depth, "ImportDecl %s doc=%d Import=%d", at, d.doc(x.Doc), x.Import)
		d.node(c, x.Specs[0])
	case *ImportSpec:
		d.line(depth, "ImportSpec %s Kind=%d NamePos=%d", at, x.Kind, x.NamePos)
		d.node(c, x.Name)
		d.node(c, x.Path)
	case *VarDecl:
		d.line(depth, "VarDecl %s doc=%d Var=%d Mut=%d", at, d.doc(x.Doc), x.Var, x.Mut)
		d.node(c, x.Extern)
		d.node(c, x.Specs[0])
	case *VarSpec:
		d.line(depth, "VarSpec %s Assign=%d", at, x.Assign)
		d.list(c, idents(x.Names))
		d.node(c, x.Pattern)
		d.node(c, x.Type)
		if len(x.Values) > 0 {
			d.node(c, x.Values[0])
		} else {
			d.node(c, nil)
		}
	case *TypeDecl:
		d.line(depth, "TypeDecl %s doc=%d Type=%d WherePos=%d DerivePos=%d ImplementsPos=%d", at, d.doc(x.Doc), x.Type, x.WherePos, x.DerivePos, x.ImplementsPos)
		d.node(c, x.Extern)
		d.node(c, x.Name)
		d.node(c, x.TypeParams)
		d.node(c, x.Spec)
		d.node(c, x.Where)
		d.node(c, x.Derive)
		d.list(c, exprs(x.Implements))
	case *FuncDecl:
		d.line(depth, "FuncDecl %s doc=%d Func=%d", at, d.doc(x.Doc), x.Func)
		d.node(c, x.Extern)
		d.node(c, x.Recv)
		d.node(c, x.Name)
		d.node(c, x.TypeParams)
		d.node(c, x.Type)
		d.node(c, x.Body)
	case *ContextDecl:
		d.line(depth, "ContextDecl %s doc=%d Let=%d Context=%d", at, d.doc(x.Doc), x.Let, x.Context)
		d.node(c, x.Name)
		d.node(c, x.Provides)
		d.node(c, x.Fails)
		d.node(c, x.Needs)
		d.node(c, x.Body)
	case *TestDecl:
		d.line(depth, "TestDecl %s doc=%d Test=%d", at, d.doc(x.Doc), x.Test)
		d.node(c, x.Name)
		d.node(c, x.Params)
		d.node(c, x.Body)
	case *LawDecl:
		when, eq, rewrite, body := NoPos, NoPos, b01(x.Rewrite != nil), x.Body
		if x.Rewrite != nil {
			when, eq = x.Rewrite.When, x.Rewrite.Eq
		}
		if e := x.Equation; e != nil {
			// The view holds a top-level rewrite law (word 2) as its two
			// blocks; the Go tree's Body is lhs == rhs (LawDecl.Equation).
			lhs, rhs, b := x.EquationSides()
			rewrite, eq = 2, b.OpPos
			side := func(l, r Pos, x Expr) *BlockStmt {
				return &BlockStmt{Lbrace: l, List: []Stmt{&ExprStmt{X: x}}, Rbrace: r}
			}
			body = &BlockStmt{Lbrace: e.LhsLbrace, List: []Stmt{side(e.LhsLbrace, e.LhsRbrace, lhs), side(e.RhsLbrace, e.RhsRbrace, rhs)}, Rbrace: e.RhsRbrace}
		}
		d.line(depth, "LawDecl %s doc=%d Law=%d Rewrite=%d When=%d Eq=%d", at, d.doc(x.Doc), x.Law, rewrite, when, eq)
		d.node(c, x.Name)
		d.node(c, x.Params)
		d.node(c, body)
		d.node(c, x.Reach)
	case *LawReach:
		inv := x.Invariant
		if inv == "" {
			inv = "-"
		}
		d.line(depth, "LawReach %s Never=%d Word=%d Path=%d Lbrack=%d Rbrack=%d Invariant=%s", at, x.Never, x.Word, b01(x.Path), x.Lbrack, x.Rbrack, inv)
		d.list(c, idents(x.Targets))
		d.node(c, x.Pred)
	case *ExternSpec:
		d.line(depth, "ExternSpec %s Extern=%d Go=%d", at, x.Extern, x.Go)
		d.node(c, x.Target)
	case *ContextBody:
		d.line(depth, "ContextBody %s Lbrace=%d Rbrace=%d", at, x.Lbrace, x.Rbrace)
		d.list(c, exprs(x.Entries))
	case *ContextEntry:
		d.line(depth, "ContextEntry %s doc=%d Kind=%d Replace=%d Assign=%d", at, d.doc(x.Doc), x.Kind, x.Replace, x.Assign)
		d.node(c, x.Key)
		d.list(c, exprs(x.Keys))
		d.node(c, x.Value)
	case *Field:
		own := NoPos
		if x.Own != nil {
			own = x.Own.At
		}
		purePos := NoPos
		if x.Pure {
			purePos = x.PurePos
		}
		dots := NoPos
		if x.Include {
			dots = x.Dots
		}
		anyPos := NoPos
		if x.Any > 0 {
			anyPos = x.Any
		}
		lent := NoPos
		if x.Lent > 0 {
			lent = x.Lent
		}
		mut := NoPos
		if x.Mut != nil {
			mut = x.Mut.At
		}
		d.line(depth, "Field %s doc=%d Any=%d Lent=%d Include=%d Dots=%d WherePos=%d Pure=%d PurePos=%d Own=%d Mut=%d", at, d.doc(x.Doc), anyPos, lent, b01(x.Include), dots, x.WherePos, b01(x.Pure), purePos, own, mut)
		d.list(c, idents(x.Names))
		d.node(c, x.Type)
		d.node(c, x.Key)
		d.node(c, x.Where)
	case *FieldList:
		d.line(depth, "FieldList %s Opening=%d Closing=%d", at, x.Opening, x.Closing)
		d.list(c, exprs(x.List))
	case *NeedsClause:
		d.line(depth, "NeedsClause %s Needs=%d", at, x.Needs)
		d.list(c, exprs(x.Names))
	case *TakesClause:
		d.line(depth, "TakesClause %s Takes=%d", at, x.Takes)
		d.list(c, idents(x.Names))
	case *ProvidesClause:
		d.line(depth, "ProvidesClause %s Provides=%d", at, x.Provides)
		d.list(c, exprs(x.Names))
	case *MachineEntry:
		d.line(depth, "MachineEntry %s doc=%d NamePos=%d Name=%s Colon=%d", at, d.doc(x.Doc), x.NamePos, x.Name, x.Colon)
		d.node(c, x.Value)
	case *CatchArm:
		types := x.Types
		if x.Written != nil {
			types = x.Written
		}
		d.line(depth, "CatchArm %s Case=%d Colon=%d", at, x.Case, x.Colon)
		d.list(c, exprs(types))
		d.list(c, exprs(x.Body))
	case *CaseClause:
		list := x.List
		if x.Written != nil {
			list = x.Written
		}
		d.line(depth, "CaseClause %s Case=%d Colon=%d", at, x.Case, x.Colon)
		d.list(c, exprs(list))
		d.list(c, exprs(x.Body))
	case *VariantSpec:
		d.line(depth, "VariantSpec %s", at)
		d.node(c, x.Name)
		d.node(c, x.TypeArgs)
		d.node(c, x.Fields)
	case *Ident:
		d.line(depth, "Ident %s Name=%s", at, x.Name)
	case *BasicLit:
		d.line(depth, "BasicLit %s Kind=%d Value=%s", at, x.Kind, strconv.Quote(x.Value))
	case *CompositeLit:
		dotdot := NoPos
		if x.Base != nil {
			dotdot = x.DotDot
		}
		d.line(depth, "CompositeLit %s Lbrace=%d DotDot=%d Rbrace=%d", at, x.Lbrace, dotdot, x.Rbrace)
		d.node(c, x.Type)
		d.node(c, x.Base)
		d.list(c, exprs(x.Elts))
	case *KeyValueExpr:
		d.line(depth, "KeyValueExpr %s Colon=%d", at, x.Colon)
		d.node(c, x.Key)
		d.node(c, x.Value)
	case *FuncLit:
		d.line(depth, "FuncLit %s", at)
		d.node(c, x.Type)
		d.node(c, x.Body)
	case *ParenExpr:
		d.line(depth, "ParenExpr %s Lparen=%d Rparen=%d", at, x.Lparen, x.Rparen)
		d.node(c, x.X)
	case *SelectorExpr:
		d.line(depth, "SelectorExpr %s", at)
		d.node(c, x.X)
		d.node(c, x.Sel)
	case *IndexExpr:
		d.line(depth, "IndexExpr %s Lbrack=%d Rbrack=%d", at, x.Lbrack, x.Rbrack)
		d.node(c, x.X)
		d.list(c, exprs(x.Indices))
	case *SliceExpr:
		d.line(depth, "SliceExpr %s Lbrack=%d Slice3=%d Rbrack=%d", at, x.Lbrack, b01(x.Slice3), x.Rbrack)
		d.node(c, x.X)
		d.node(c, x.Low)
		d.node(c, x.High)
		d.node(c, x.Max)
	case *TypeGuard:
		d.line(depth, "TypeGuard %s", at)
		d.node(c, x.X)
	case *CallExpr:
		d.line(depth, "CallExpr %s Lparen=%d Rparen=%d TrailingFunc=%d", at, x.Lparen, x.Rparen, b01(x.TrailingFunc))
		d.node(c, x.Fun)
		d.list(c, exprs(x.Args))
		d.node(c, x.Trailing)
	case *StarExpr:
		d.line(depth, "StarExpr %s Star=%d Mut=%d", at, x.Star, b01(x.Mut))
		d.node(c, x.X)
	case *UnaryExpr:
		d.line(depth, "UnaryExpr %s OpPos=%d Op=%d", at, x.OpPos, x.Op)
		d.node(c, x.X)
	case *BinaryExpr:
		d.line(depth, "BinaryExpr %s OpPos=%d Op=%d", at, x.OpPos, x.Op)
		d.node(c, x.X)
		d.node(c, x.Y)
	case *TryExpr:
		d.line(depth, "TryExpr %s Try=%d", at, x.Try)
		d.node(c, x.X)
	case *CatchExpr:
		d.line(depth, "CatchExpr %s Catch=%d Lbrace=%d Rbrace=%d", at, x.Catch, x.Lbrace, x.Rbrace)
		d.node(c, x.X)
		d.list(c, exprs(x.Arms))
	case *ContextLit:
		d.line(depth, "ContextLit %s Context=%d", at, x.Context)
		d.node(c, x.Provides)
		d.node(c, x.Fails)
		d.node(c, x.Needs)
		d.node(c, x.Body)
	case *ArrayType:
		ln := x.Len
		if ln == "" {
			ln = "-"
		}
		d.line(depth, "ArrayType %s Lbrack=%d Len=%s LenPos=%d", at, x.Lbrack, ln, x.LenPos)
		d.node(c, x.Elt)
	case *MapType:
		d.line(depth, "MapType %s Map=%d Lbrack=%d Rbrack=%d", at, x.Map, x.Lbrack, x.Rbrack)
		d.node(c, x.Key)
		d.node(c, x.Value)
		d.node(c, x.Hashing)
	case *FuncType:
		entry := NoPos
		if x.Entry {
			entry = x.EntryPos
		}
		defects := NoPos
		if x.Defects {
			defects = x.DefectsPos
		}
		d.line(depth, "FuncType %s Escaping=%d Entry=%d Func=%d Defects=%d", at, x.Escaping, entry, x.Func, defects)
		d.node(c, x.TypeParams)
		d.node(c, x.Params)
		d.node(c, x.Results)
		d.node(c, x.Needs)
		d.node(c, x.Takes)
	case *StructType:
		d.line(depth, "StructType %s", at)
		d.node(c, x.Fields)
	case *InterfaceType:
		d.line(depth, "InterfaceType %s Lbrace=%d Rbrace=%d", at, x.Lbrace, x.Rbrace)
		d.list(c, exprs(x.Includes))
		d.list(c, exprs(x.Methods))
		d.list(c, exprs(x.Laws))
		d.list(c, exprs(x.Union))
		d.line(c, "list %d", len(x.Terms))
		for _, t := range x.Terms {
			d.line(c+1, "TypeTerm %d %d Tilde=%d", t.Pos(), t.End(), t.Tilde)
			d.node(c+2, t.Type)
		}
		// `test H`, the harness member: the view keeps its TestDecl row.
		d.line(c, "list %d", len(x.Harnesses))
		for _, h := range x.Harnesses {
			d.line(c+1, "TestDecl %d %d doc=%d Test=%d", h.Pos(), h.End(), d.doc(h.Doc), h.Test)
			d.node(c+2, h.Name)
			d.line(c+2, "nil")
			d.line(c+2, "nil")
		}
	case *ErrorRow:
		d.line(depth, "ErrorRow %s Error=%d", at, x.Error)
		d.list(c, exprs(x.Types))
	case *MachineType:
		d.line(depth, "MachineType %s Machine=%d Lbrack=%d Rbrack=%d Lbrace=%d Rbrace=%d", at, x.Machine, x.Lbrack, x.Rbrack, x.Lbrace, x.Rbrace)
		d.node(c, x.State)
		d.node(c, x.Signal)
		d.list(c, exprs(x.Entries))
		d.list(c, exprs(x.Laws))
	case *TupleExpr:
		d.line(depth, "TupleExpr %s Lparen=%d Rparen=%d", at, x.Lparen, x.Rparen)
		d.list(c, exprs(x.Elts))
	case *AsPattern:
		d.line(depth, "AsPattern %s As=%d", at, x.As)
		d.node(c, x.X)
		d.node(c, x.Name)
	case *OrPattern:
		var bars []string
		for _, b := range x.Bars {
			bars = append(bars, strconv.Itoa(int(b)))
		}
		d.line(depth, "OrPattern %s Bars=%s", at, strings.Join(bars, ","))
		d.list(c, exprs(x.Alts))
	case *InterpolatedString:
		d.line(depth, "InterpolatedString %s", at)
		d.list(c, exprs(x.Texts))
		d.list(c, exprs(x.Exprs))
	case *DeclStmt:
		d.line(depth, "DeclStmt %s", at)
		d.node(c, x.Decl)
	case *ExprStmt:
		d.line(depth, "ExprStmt %s", at)
		d.node(c, x.X)
	case *AssignStmt:
		d.line(depth, "AssignStmt %s TokPos=%d Tok=%d", at, x.TokPos, x.Tok)
		d.node(c, x.Lhs[0])
		d.node(c, x.Rhs[0])
	case *ReturnStmt:
		d.line(depth, "ReturnStmt %s Return=%d", at, x.Return)
		if len(x.Results) > 0 {
			d.node(c, x.Results[0])
		} else {
			d.node(c, nil)
		}
	case *BranchStmt:
		d.line(depth, "BranchStmt %s TokPos=%d Tok=%d", at, x.TokPos, x.Tok)
	case *BlockStmt:
		d.line(depth, "BlockStmt %s Lbrace=%d Rbrace=%d", at, x.Lbrace, x.Rbrace)
		d.list(c, exprs(x.List))
	case *IfStmt:
		d.line(depth, "IfStmt %s If=%d ElsePos=%d", at, x.If, x.ElsePos)
		d.node(c, x.Cond)
		d.node(c, x.Body)
		d.node(c, x.Else)
	case *SwitchStmt:
		d.line(depth, "SwitchStmt %s Switch=%d Lbrace=%d Rbrace=%d", at, x.Switch, x.Lbrace, x.Rbrace)
		d.node(c, x.Tag)
		d.list(c, exprs(x.Clauses))
	case *TypeSwitchStmt:
		d.line(depth, "TypeSwitchStmt %s Switch=%d Lbrace=%d Rbrace=%d", at, x.Switch, x.Lbrace, x.Rbrace)
		d.list(c, exprs(x.Subjects))
		d.list(c, exprs(x.Clauses))
	case *ForStmt:
		d.line(depth, "ForStmt %s For=%d", at, x.For)
		d.node(c, x.Cond)
		d.node(c, x.Body)
	case *RangeStmt:
		d.line(depth, "RangeStmt %s For=%d Tok=%d Range=%d", at, x.For, x.Tok, x.Range)
		d.node(c, x.Key)
		d.node(c, x.Value)
		d.node(c, x.X)
		d.node(c, x.Body)
	case *AssertStmt:
		d.line(depth, "AssertStmt %s Assert=%d", at, x.Assert)
		d.node(c, x.X)
	case *SkipStmt:
		d.line(depth, "SkipStmt %s Skip=%d ReasonPos=%d Reason=%s", at, x.Skip, x.ReasonPos, strconv.Quote(x.Reason))
	case *DieStmt:
		d.line(depth, "DieStmt %s Die=%d", at, x.Die)
		d.node(c, x.X)
	default:
		d.line(depth, "unknown %T", n)
	}
}

// decodedDump is the canonical dump of the decoded tree of src, or of its
// diagnostic.
func decodedDump(src string) string {
	f, err := ParseRows(src)
	if err != nil {
		e, ok := err.(*Error)
		if !ok {
			return fmt.Sprintf("decoder failure %v\n", err)
		}
		return fmt.Sprintf("E %s %d %d %d %d %s\n", e.Code, e.Offset, e.Length, e.Line, e.Column, strconv.Quote(e.Message))
	}
	d := &dumper{groups: map[*CommentGroup]int{}}
	for i, g := range f.Comments {
		d.groups[g] = i
		d.line(0, "G %d %d %d", g.Pos(), g.End(), len(g.List))
	}
	d.node(0, f)
	return d.b.String()
}

// TestDecodeAgreesWithView (capstone S2e): on every .ergo file of the
// repository, the positive and superseded corpora (the files that do not
// parse give the same diagnostic, every field) and seeded sources
// (generated programs and perturbed corpus files, a third of them with
// CRLF line ends), the dump of the parse through the view equals the dump
// of the decoded tree. A failing seed prints the source, both dumps and the
// replay command. It kills the decode rows of testdata/mutants.json and the
// view rows of compiler/syntax/testdata/mutants.json that name it.
func TestDecodeAgreesWithView(t *testing.T) {
	corpus := corpusInputs(t)
	inputs := agreeSeeds(t, func(s int64) string { return pickDecode(s, corpus) })
	if os.Getenv("ERGO_AGREE_SEED") == "" {
		inputs = append(corpus, inputs...)
	}
	failures := 0
	for _, in := range inputs {
		got, err := linked.Parse(context.Background(), in.src)
		if err != nil {
			t.Fatalf("%s: the Ergo parser failed: %v\nsource %q", in.name, err, in.src)
		}
		want := decodedDump(in.src)
		if got == want {
			continue
		}
		failures++
		if failures <= 3 {
			src := in.src
			if len(src) > 600 && in.seed < 0 {
				src = src[:600] + "…"
			}
			t.Errorf("%s: the decoded tree and the view differ\nsource %q\n--- decoded\n%s--- view\n%s%s", in.name, src, firstDiff(want, got), firstDiff(got, want), replay(in, "TestDecodeAgreesWithView"))
		}
	}
	if failures > 0 {
		t.Errorf("%d of %d inputs differ", failures, len(inputs))
	}
}

// pickParse is seed's source: a generated program for an even seed, a
// perturbed corpus file for an odd one.
func pickParse(seed int64, corpus []agreeInput) string {
	if seed%2 == 0 {
		return generatedProgram(seed)
	}
	return perturbed(seed, corpus)
}
