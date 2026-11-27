package lint

import (
	"fmt"
	"reflect"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// shapeOf is the meaning-level text of a syntax tree, for the rules that
// ask whether two pieces of code are the same (identical-arms,
// duplicate-test, duplicate-shape): the tree's node kinds and their
// operators, nested; a name as the object it resolves to, so two
// same-spelled names of different objects differ; a name the tree itself
// declares as the order of its declaration, so bodies that differ only in
// their locals' names are the same; a literal as its constant value. No
// position, comment or layout enters it, so a reformatted tree keeps its
// shape. Two trees with equal shapes do the same thing. Several nodes
// (a parameter list and a body, a list of statements) share one
// numbering of their declarations.
func shapeOf[N syntax.Node](info *check.Info, nodes ...N) string {
	var s shaper
	s.info = info
	s.b.buf = s.stack[:0]
	for _, n := range nodes {
		s.node(n)
		s.b.putByte(';')
	}
	return string(s.b.buf)
}

// shapeOfEdit is shapeOf of a piece of a body (an added hunk), whose
// variables may be declared outside it: a variable it uses but does not
// declare (a parameter, a local of the enclosing body) is numbered by its
// first use, as a declared one is, so one edit made in two bodies has one
// shape.
func shapeOfEdit(info *check.Info, nodes ...syntax.Node) string {
	var s shaper
	s.info = info
	s.free = true
	s.b.buf = s.stack[:0]
	for _, n := range nodes {
		s.node(n)
		s.b.putByte(';')
	}
	return string(s.b.buf)
}

type byteBuf struct {
	buf []byte
}

func (w *byteBuf) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *byteBuf) WriteString(s string) {
	w.buf = append(w.buf, s...)
}

func (w *byteBuf) putByte(c byte) {
	w.buf = append(w.buf, c)
}

type shaper struct {
	info   *check.Info
	locals map[check.Object]int
	b      byteBuf
	stack  [128]byte
	// free numbers the variables the tree uses and does not declare.
	free bool
}

func (s *shaper) writeInt(v int64) {
	s.b.buf = strconv.AppendInt(s.b.buf, v, 10)
}

func (s *shaper) writeBool(v bool) {
	if v {
		s.b.WriteString("true")
	} else {
		s.b.WriteString("false")
	}
}

func (s *shaper) writePtr(o check.Object) {
	v := reflect.ValueOf(o)
	if v.Kind() != reflect.Pointer {
		s.b.buf = fmt.Appendf(s.b.buf, "%p", o)
		return
	}
	s.b.WriteString("0x")
	s.b.buf = strconv.AppendUint(s.b.buf, uint64(v.Pointer()), 16)
}

func (s *shaper) child(c syntax.Node) {
	if c == nil || syntax.IsNil(c) {
		return
	}
	s.b.putByte(' ')
	s.node(c)
}

func (s *shaper) childExprs(list []syntax.Expr) {
	for _, x := range list {
		s.child(x)
	}
}

func (s *shaper) childStmts(list []syntax.Stmt) {
	for _, st := range list {
		s.child(st)
	}
}

func (s *shaper) childIdents(list []*syntax.Ident) {
	for _, id := range list {
		if id != nil {
			s.b.putByte(' ')
			s.ident(id)
		}
	}
}

func (s *shaper) childFields(l *syntax.FieldList) {
	if l == nil {
		return
	}
	for _, fl := range l.List {
		if fl != nil {
			s.child(fl)
		}
	}
}

func (s *shaper) node(n syntax.Node) {
	if n == nil || syntax.IsNil(n) {
		return
	}
	switch x := n.(type) {
	case *syntax.Ident:
		s.ident(x)
		return
	case *syntax.BasicLit:
		if v, ok := s.info.Values[x]; ok && v != nil {
			fmt.Fprintf(&s.b, "(lit %s)", v.ExactString())
		} else {
			s.b.WriteString("(lit ")
			s.writeInt(int64(x.Kind))
			s.b.putByte(' ')
			s.b.buf = strconv.AppendQuote(s.b.buf, x.Value)
			s.b.putByte(')')
		}
		return
	case *syntax.BinaryExpr:
		s.b.WriteString("(BinaryExpr Op=")
		s.b.WriteString(x.Op.String())
		s.child(x.X)
		s.child(x.Y)
		s.b.putByte(')')
	case *syntax.UnaryExpr:
		s.b.WriteString("(UnaryExpr Op=")
		s.b.WriteString(x.Op.String())
		s.child(x.X)
		s.b.putByte(')')
	case *syntax.CallExpr:
		s.b.WriteString("(CallExpr TrailingFunc=")
		s.writeBool(x.TrailingFunc)
		s.child(x.Fun)
		s.childExprs(x.Args)
		if x.Trailing != nil {
			s.child(x.Trailing)
		}
		s.b.putByte(')')
	case *syntax.SelectorExpr:
		s.b.WriteString("(SelectorExpr")
		s.child(x.X)
		if x.Sel != nil {
			s.b.putByte(' ')
			s.ident(x.Sel)
		}
		s.b.putByte(')')
	case *syntax.IndexExpr:
		s.b.WriteString("(IndexExpr")
		s.child(x.X)
		s.childExprs(x.Indices)
		s.b.putByte(')')
	case *syntax.SliceExpr:
		s.b.WriteString("(SliceExpr Slice3=")
		s.writeBool(x.Slice3)
		s.child(x.X)
		s.child(x.Low)
		s.child(x.High)
		s.child(x.Max)
		s.b.putByte(')')
	case *syntax.ParenExpr:
		s.b.WriteString("(ParenExpr")
		s.child(x.X)
		s.b.putByte(')')
	case *syntax.StarExpr:
		s.b.WriteString("(StarExpr Mut=")
		s.writeBool(x.Mut)
		s.child(x.X)
		s.b.putByte(')')
	case *syntax.TryExpr:
		s.b.WriteString("(TryExpr")
		s.child(x.X)
		s.b.putByte(')')
	case *syntax.CompositeLit:
		s.b.WriteString("(CompositeLit")
		s.child(x.Type)
		s.child(x.Base)
		s.childExprs(x.Elts)
		s.b.putByte(')')
	case *syntax.KeyValueExpr:
		s.b.WriteString("(KeyValueExpr")
		s.child(x.Key)
		s.child(x.Value)
		s.b.putByte(')')
	case *syntax.FuncLit:
		s.b.WriteString("(FuncLit")
		if x.Type != nil {
			s.child(x.Type)
		}
		if x.Body != nil {
			s.child(x.Body)
		}
		s.b.putByte(')')
	case *syntax.CatchExpr:
		s.b.WriteString("(CatchExpr")
		s.child(x.X)
		for _, a := range x.Arms {
			s.child(a)
		}
		s.b.putByte(')')
	case *syntax.CatchArm:
		s.b.WriteString("(CatchArm")
		s.childExprs(x.Types)
		s.childStmts(x.Body)
		s.b.putByte(')')
	case *syntax.ExprStmt:
		s.b.WriteString("(ExprStmt")
		s.child(x.X)
		s.b.putByte(')')
	case *syntax.AssignStmt:
		s.b.WriteString("(AssignStmt Tok=")
		s.b.WriteString(x.Tok.String())
		s.childExprs(x.Lhs)
		s.childExprs(x.Rhs)
		s.b.putByte(')')
	case *syntax.DeclStmt:
		s.b.WriteString("(DeclStmt")
		if x.Decl != nil {
			s.child(x.Decl)
		}
		s.b.putByte(')')
	case *syntax.ReturnStmt:
		s.b.WriteString("(ReturnStmt")
		s.childExprs(x.Results)
		s.b.putByte(')')
	case *syntax.BranchStmt:
		s.b.WriteString("(BranchStmt Tok=")
		s.b.WriteString(x.Tok.String())
		s.b.putByte(')')
	case *syntax.BlockStmt:
		s.b.WriteString("(BlockStmt")
		s.childStmts(x.List)
		s.b.putByte(')')
	case *syntax.IfStmt:
		s.b.WriteString("(IfStmt")
		s.child(x.Cond)
		if x.Body != nil {
			s.child(x.Body)
		}
		s.child(x.Else)
		s.b.putByte(')')
	case *syntax.SwitchStmt:
		s.b.WriteString("(SwitchStmt")
		s.child(x.Tag)
		for _, c := range x.Clauses {
			s.child(c)
		}
		s.b.putByte(')')
	case *syntax.TypeSwitchStmt:
		s.b.WriteString("(TypeSwitchStmt")
		for _, g := range x.Subjects {
			s.child(g)
		}
		for _, c := range x.Clauses {
			s.child(c)
		}
		s.b.putByte(')')
	case *syntax.CaseClause:
		s.b.WriteString("(CaseClause")
		s.childExprs(x.List)
		s.childStmts(x.Body)
		s.b.putByte(')')
	case *syntax.TypeGuard:
		s.b.WriteString("(TypeGuard")
		s.child(x.X)
		s.b.putByte(')')
	case *syntax.ForStmt:
		s.b.WriteString("(ForStmt")
		s.child(x.Cond)
		if x.Body != nil {
			s.child(x.Body)
		}
		s.b.putByte(')')
	case *syntax.RangeStmt:
		s.b.WriteString("(RangeStmt Tok=")
		s.b.WriteString(x.Tok.String())
		s.child(x.Key)
		s.child(x.Value)
		s.child(x.X)
		if x.Body != nil {
			s.child(x.Body)
		}
		s.b.putByte(')')
	case *syntax.AssertStmt:
		s.b.WriteString("(AssertStmt")
		s.child(x.X)
		s.b.putByte(')')
	case *syntax.DieStmt:
		s.b.WriteString("(DieStmt")
		s.child(x.X)
		s.b.putByte(')')
	case *syntax.AlwaysStmt:
		s.b.WriteString("(AlwaysStmt")
		s.child(x.X)
		s.b.putByte(')')
	case *syntax.SometimesStmt:
		s.b.WriteString("(SometimesStmt")
		s.child(x.X)
		s.b.putByte(')')
	case *syntax.SkipStmt:
		s.b.WriteString("(SkipStmt Reason=")
		s.b.WriteString(x.Reason)
		s.b.putByte(')')
	case *syntax.VarDecl:
		s.b.WriteString("(VarDecl")
		if x.Extern != nil {
			s.child(x.Extern)
		}
		for _, sp := range x.Specs {
			s.child(sp)
		}
		s.b.putByte(')')
	case *syntax.VarSpec:
		s.b.WriteString("(VarSpec")
		s.childIdents(x.Names)
		s.child(x.Pattern)
		s.child(x.Type)
		s.childExprs(x.Values)
		s.b.putByte(')')
	case *syntax.TupleExpr:
		s.b.WriteString("(TupleExpr")
		s.childExprs(x.Elts)
		s.b.putByte(')')
	case *syntax.InterpolatedString:
		s.b.WriteString("(InterpolatedString")
		for i, t := range x.Texts {
			s.child(t)
			if i < len(x.Exprs) {
				s.child(x.Exprs[i])
			}
		}
		s.b.putByte(')')
	case *syntax.AsPattern:
		s.b.WriteString("(AsPattern")
		s.child(x.X)
		if x.Name != nil {
			s.b.putByte(' ')
			s.ident(x.Name)
		}
		s.b.putByte(')')
	case *syntax.OrPattern:
		s.b.WriteString("(OrPattern")
		s.childExprs(x.Alts)
		s.b.putByte(')')
	case *syntax.ArrayType:
		s.b.WriteString("(ArrayType Len=")
		s.b.WriteString(x.Len)
		s.child(x.Elt)
		s.b.putByte(')')
	case *syntax.MapType:
		s.b.WriteString("(MapType")
		s.child(x.Key)
		s.child(x.Value)
		if x.Hashing != nil {
			s.child(x.Hashing)
		}
		s.b.putByte(')')
	case *syntax.FuncType:
		s.b.WriteString("(FuncType Entry=")
		s.writeBool(x.Entry)
		s.b.WriteString(" Defects=")
		s.writeBool(x.Defects)
		if x.TypeParams != nil {
			s.child(x.TypeParams)
		}
		if x.Params != nil {
			s.child(x.Params)
		}
		if x.Results != nil {
			s.child(x.Results)
		}
		if x.Needs != nil {
			s.child(x.Needs)
		}
		for _, fc := range x.Facts {
			s.child(fc)
		}
		s.b.putByte(')')
	case *syntax.FieldList:
		s.b.WriteString("(FieldList")
		s.childFields(x)
		s.b.putByte(')')
	case *syntax.Field:
		s.b.WriteString("(Field Include=")
		s.writeBool(x.Include)
		s.b.WriteString(" Pure=")
		s.writeBool(x.Pure)
		s.childIdents(x.Names)
		s.child(x.Type)
		if x.Key != nil {
			s.child(x.Key)
		}
		s.child(x.Where)
		s.b.putByte(')')
	case *syntax.ContextLit:
		s.b.WriteString("(ContextLit")
		if x.Provides != nil {
			s.child(x.Provides)
		}
		if x.Fails != nil {
			s.child(x.Fails)
		}
		if x.Needs != nil {
			s.child(x.Needs)
		}
		if x.Body != nil {
			s.child(x.Body)
		}
		s.b.putByte(')')
	case *syntax.ContextBody:
		s.b.WriteString("(ContextBody")
		for _, e := range x.Entries {
			s.child(e)
		}
		s.b.putByte(')')
	case *syntax.ContextEntry:
		s.b.WriteString("(ContextEntry Kind=")
		s.writeInt(int64(x.Kind))
		s.child(x.Key)
		s.childExprs(x.Keys)
		s.child(x.Value)
		s.b.putByte(')')
	case *syntax.ProvidesClause:
		s.b.WriteString("(ProvidesClause")
		s.childExprs(x.Names)
		s.b.putByte(')')
	case *syntax.NeedsClause:
		s.b.WriteString("(NeedsClause")
		s.childExprs(x.Names)
		s.b.putByte(')')
	case *syntax.ErrorRow:
		s.b.WriteString("(ErrorRow")
		s.childExprs(x.Types)
		s.b.putByte(')')
	case *syntax.FactClause:
		s.b.WriteString("(FactClause Kind=")
		s.writeInt(int64(x.Kind))
		s.child(x.Cond)
		if x.When != nil {
			s.child(x.When)
		}
		s.b.putByte(')')
	case *syntax.StructType:
		s.b.WriteString("(StructType")
		if x.Fields != nil {
			s.child(x.Fields)
		}
		s.b.putByte(')')
	case *syntax.InterfaceType:
		s.b.WriteString("(InterfaceType")
		for _, m := range x.Includes {
			s.child(m)
		}
		for _, m := range x.Methods {
			s.child(m)
		}
		for _, l := range x.Laws {
			s.child(l)
		}
		for _, h := range x.Harnesses {
			s.child(h)
		}
		for _, t := range x.Terms {
			if t != nil {
				s.child(t.Type)
			}
		}
		s.childExprs(x.Union)
		s.b.putByte(')')
	case *syntax.VariantSpec:
		s.b.WriteString("(VariantSpec")
		if x.Name != nil {
			s.b.putByte(' ')
			s.ident(x.Name)
		}
		if x.TypeArgs != nil {
			s.child(x.TypeArgs)
		}
		if x.Fields != nil {
			s.child(x.Fields)
		}
		s.b.putByte(')')
	case *syntax.Harness:
		s.b.WriteString("(Harness")
		if x.Name != nil {
			s.b.putByte(' ')
			s.ident(x.Name)
		}
		s.b.putByte(')')
	case *syntax.MachineType:
		s.b.WriteString("(MachineType")
		s.child(x.State)
		s.child(x.Signal)
		for _, e := range x.Entries {
			s.child(e)
		}
		for _, l := range x.Laws {
			s.child(l)
		}
		s.b.putByte(')')
	case *syntax.MachineEntry:
		s.b.WriteString("(MachineEntry Name=")
		s.b.WriteString(x.Name)
		s.child(x.Value)
		s.b.putByte(')')
	case *syntax.FuncDecl:
		s.b.WriteString("(FuncDecl")
		if x.Extern != nil {
			s.child(x.Extern)
		}
		if x.Recv != nil {
			s.child(x.Recv)
		}
		if x.Name != nil {
			s.b.putByte(' ')
			s.ident(x.Name)
		}
		if x.TypeParams != nil {
			s.child(x.TypeParams)
		}
		if x.Type != nil {
			s.child(x.Type)
		}
		s.child(x.Implements)
		if x.Body != nil {
			s.child(x.Body)
		}
		s.b.putByte(')')
	case *syntax.TypeDecl:
		s.b.WriteString("(TypeDecl")
		if x.Extern != nil {
			s.child(x.Extern)
		}
		if x.Name != nil {
			s.b.putByte(' ')
			s.ident(x.Name)
		}
		if x.TypeParams != nil {
			s.child(x.TypeParams)
		}
		s.child(x.Spec)
		s.child(x.Where)
		s.child(x.Derive)
		s.childExprs(x.Implements)
		s.b.putByte(')')
	case *syntax.TestDecl:
		s.b.WriteString("(TestDecl")
		if x.Name != nil {
			s.b.putByte(' ')
			s.ident(x.Name)
		}
		if x.Params != nil {
			s.child(x.Params)
		}
		if x.Body != nil {
			s.child(x.Body)
		}
		s.b.putByte(')')
	case *syntax.LawDecl:
		s.b.WriteString("(LawDecl Quant=")
		s.b.WriteString(x.Quant)
		if x.Name != nil {
			s.b.putByte(' ')
			s.ident(x.Name)
		}
		if x.TypeParams != nil {
			s.child(x.TypeParams)
		}
		if x.Params != nil {
			s.child(x.Params)
		}
		if x.Body != nil {
			s.child(x.Body)
		}
		if x.Reach != nil {
			s.childIdents(x.Reach.Targets)
			if x.Reach.Pred != nil {
				s.b.putByte(' ')
				s.ident(x.Reach.Pred)
			}
		}
		s.b.putByte(')')
	case *syntax.ContextDecl:
		s.b.WriteString("(ContextDecl")
		if x.Name != nil {
			s.b.putByte(' ')
			s.ident(x.Name)
		}
		if x.Provides != nil {
			s.child(x.Provides)
		}
		if x.Fails != nil {
			s.child(x.Fails)
		}
		if x.Needs != nil {
			s.child(x.Needs)
		}
		if x.Body != nil {
			s.child(x.Body)
		}
		s.b.putByte(')')
	case *syntax.ImportDecl:
		s.b.WriteString("(ImportDecl")
		for _, sp := range x.Specs {
			s.child(sp)
		}
		s.b.putByte(')')
	case *syntax.ImportSpec:
		s.b.WriteString("(ImportSpec Kind=")
		s.writeInt(int64(x.Kind))
		if x.Name != nil {
			s.b.putByte(' ')
			s.ident(x.Name)
		}
		if x.Path != nil {
			s.child(x.Path)
		}
		s.b.putByte(')')
	case *syntax.ExternSpec:
		s.b.WriteString("(ExternSpec")
		if x.Target != nil {
			s.child(x.Target)
		}
		s.b.putByte(')')
	case *syntax.File:
		s.b.WriteString("(File Name=")
		s.b.WriteString(x.Name)
		s.b.WriteString(" Src=")
		s.b.WriteString(x.Src)
		for _, d := range x.Decls {
			s.child(d)
		}
		s.b.putByte(')')
	}
}

func (s *shaper) ident(x *syntax.Ident) {
	if o := s.info.Defs[x]; o != nil {
		if s.locals == nil {
			s.locals = map[check.Object]int{}
		}
		if _, seen := s.locals[o]; !seen {
			s.locals[o] = len(s.locals)
		}
		fmt.Fprintf(&s.b, "(def %d)", s.locals[o])
		return
	}
	if o := s.info.Uses[x]; o != nil {
		if i, local := s.locals[o]; local {
			s.b.WriteString("(local ")
			s.writeInt(int64(i))
			s.b.putByte(')')
		} else if _, isVar := o.(*check.Var); isVar && s.free {
			if s.locals == nil {
				s.locals = map[check.Object]int{}
			}
			i := len(s.locals)
			s.locals[o] = i
			s.b.WriteString("(local ")
			s.writeInt(int64(i))
			s.b.putByte(')')
		} else {
			s.b.WriteString("(obj ")
			s.writePtr(o)
			s.b.putByte(')')
		}
		return
	}
	// A name the checker resolves by its context, such as a field of a
	// selector or a composite literal's key: its spelling, read only
	// beside the resolved operand it belongs to.
	s.b.WriteString("(name ")
	s.b.buf = strconv.AppendQuote(s.b.buf, x.Name)
	s.b.putByte(')')
}
