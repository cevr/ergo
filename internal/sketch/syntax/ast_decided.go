package syntax

import "strconv"

// ---------------------------------------------------------------- decided-grammar nodes

// ExternSpec is the binding of an extern declaration: `extern go
// path.Func`. Target is the unquoted target token (Kind PATH); its function
// name is the part after the last `.` that follows the last `/`.
type ExternSpec struct {
	Extern Pos
	Go     Pos
	Target *BasicLit
}

func (s *ExternSpec) Pos() Pos { return s.Extern }
func (s *ExternSpec) End() Pos { return s.Target.End() }

// Split returns the Go import path and the function name of the target:
// `example.com/json.Valid` is ("example.com/json", "Valid").
func (s *ExternSpec) Split() (path, name string) { return splitTarget(s.Target.Value) }

// SplitMethod returns the Go import path, the type and the method name of
// a method target: `net/url.URL.Hostname` is ("net/url", "URL",
// "Hostname").
func (s *ExternSpec) SplitMethod() (path, typ, name string) {
	rest, name := splitTarget(s.Target.Value)
	path, typ = splitTarget(rest)
	return path, typ, name
}

// FuncTarget reports a method's target that names a Go function, not a
// method: path.F, which the call runs with the receiver as its first
// argument (S2-FILE-B): `extern go
// ergo.local/prototype/runtime/v1/hostfs.FileStat func (f *File) Stat()
// hostfs.Info` calls hostfs.FileStat(f). Its path has no type element.
func (s *ExternSpec) FuncTarget() bool {
	path, _, _ := s.SplitMethod()
	return path == ""
}

// RuntimeModule is the Go module path of the Ergo runtime. An extern type
// whose target is in it names the runtime Go type of an Ergo declaration
// (docs/spec/stdlib.md, The body contract), which the checker checks as
// Ergo, so it may refine its type with where; any other extern type is a
// binding's host type, which Go checks nothing on.
const RuntimeModule = "ergo.local/prototype/runtime"

// IsRuntimeTarget reports whether the import path is the runtime module or
// one of its packages.
func IsRuntimeTarget(path string) bool {
	return path == RuntimeModule || len(path) > len(RuntimeModule) && path[:len(RuntimeModule)+1] == RuntimeModule+"/"
}

// IsBindingDecl reports whether d is a declaration only a Go binding has
// (docs/spec/interop.md): an extern host type, an extern let, or an extern
// method with a receiver.
func IsBindingDecl(d Decl) bool {
	switch d := d.(type) {
	case *TypeDecl:
		return d.Extern != nil
	case *VarDecl:
		return d.Extern != nil
	case *FuncDecl:
		return d.Recv != nil
	}
	return false
}

func splitTarget(target string) (path, name string) {
	slash := -1
	for i := len(target) - 1; i >= 0; i-- {
		if target[i] == '/' {
			slash = i
			break
		}
	}
	for i := len(target) - 1; i > slash; i-- {
		if target[i] == '.' {
			return target[:i], target[i+1:]
		}
	}
	return "", target
}

// TestDecl is `test Name { … }`, an example, or `test Name(x T) { … }`, a
// property. Params is nil for an example.
type TestDecl struct {
	Doc    *CommentGroup
	Test   Pos
	Name   *Ident
	Params *FieldList
	Body   *BlockStmt
}

func (d *TestDecl) Pos() Pos { return d.Test }
func (d *TestDecl) End() Pos { return d.Body.End() }
func (*TestDecl) declNode()  {}

// LawDecl is `law Name(params) { body }`: a top-level declaration in
// File.Decls, or a member of an operation type in InterfaceType.Laws
// (law-top). A top-level law has no type parameters. A member of a machine
// type (MachineType.Laws, MU6) is one of these too, or a reachability law:
// `law Name reaches V`, `law Name never reaches V` or `law Name path [V1,
// V2]`, with Reach set and no parameters and no body.
type LawDecl struct {
	Doc  *CommentGroup
	Law  Pos
	Name *Ident
	// Quant is the optional contextual quantifier after the law's name
	// (`law Name sometimes(...)` or `law Name always(...)`), or "".
	QuantPos Pos
	Quant    string
	// TypeParams are a top-level rewrite law's type parameters (`law
	// L[T, U](…) { lhs } == { rhs }`, owner q234), nil without a list.
	TypeParams *FieldList
	Params     *FieldList
	Body       *BlockStmt // nil for a reachability law
	Reach      *LawReach  // nil unless a reachability law
	// Rewrite is the rewrite form of a member law (types.md, Laws 14):
	// `law L(params) [when R() == C] { lhs } == { rhs }`, or nil.
	// Body then holds the guard, when there is one, as an ExprStmt, then
	// Rewrite.Lhs and Rewrite.Rhs, so a walk of Body sees every name.
	Rewrite *LawRewrite
	// Equation is the rewrite form of a top-level law over functions
	// (types.md, Laws 14; LAWFFI LF6a): `law L(params) { lhs } == { rhs
	// }`, each side one expression, or nil. Body then holds one statement,
	// `lhs == rhs` (a BinaryExpr at Equation.Eq), so the law is checked,
	// run and lowered as the property it states; EquationSides gives the
	// sides, and the formatter prints the two blocks.
	Equation *LawEquation
	// Derived is the refined type whose `where` the parser derived this
	// law from (refine.go), or nil for a written one. The formatter does
	// not print it.
	Derived *TypeDecl
	// Pair is the Wire function of the by-name codec pair whose round
	// trip the parser derived this law from (pair.go), or nil.
	Pair *FuncDecl
}

// IsSometimes reports whether d is an existential law (`law Name sometimes(...)`).
func (d *LawDecl) IsSometimes() bool { return d != nil && d.Quant == "sometimes" }

func (d *LawDecl) Pos() Pos { return d.Law }
func (d *LawDecl) End() Pos {
	if d.Reach != nil {
		return d.Reach.End()
	}
	return d.Body.End()
}
func (*LawDecl) declNode() {}

// LawRewrite is the rewrite form of a law (LawDecl.Rewrite). When is the
// position of `when`, else NoPos; Eq is the position of the == between
// the blocks (NoPos after a parse error). The guard and the blocks are
// statements of the law's Body (RewriteGuard, RewriteSides), so every
// walk of the law sees them once.
type LawRewrite struct {
	When Pos
	Eq   Pos
}

// LawEquation is the rewrite form of a top-level law (LawDecl.Equation):
// the braces of its two blocks. The == is the BinaryExpr of the law's
// Body.
type LawEquation struct {
	LhsLbrace, LhsRbrace Pos
	RhsLbrace, RhsRbrace Pos
}

// EquationSides are the left and right expressions of a top-level rewrite
// law (LawDecl.Equation), and the == between them.
func (d *LawDecl) EquationSides() (lhs, rhs Expr, eq *BinaryExpr) {
	b := d.Body.List[0].(*ExprStmt).X.(*BinaryExpr)
	return b.X, b.Y, b
}

// RewriteGuard is the `R() == C` of a rewrite law's `when`, nil without one.
func (d *LawDecl) RewriteGuard() Expr {
	if d.Rewrite == nil || d.Rewrite.When == NoPos {
		return nil
	}
	return d.Body.List[0].(*ExprStmt).X
}

// RewriteSides are the left and right blocks of a rewrite law.
func (d *LawDecl) RewriteSides() (lhs, rhs *BlockStmt) {
	n := len(d.Body.List)
	return d.Body.List[n-2].(*BlockStmt), d.Body.List[n-1].(*BlockStmt)
}

// LawReach is the graph form of a machine law (MU6). Never is the position
// of `never` in `never reaches V`, else NoPos; Word is the position of
// `reaches` or `path`; Targets are the variants of the machine's state the
// law names: one for reaches, one or more for a path, in order. An
// invariant law (X2), `always p` or `sometimes p`, has Invariant set to its
// word, Word at it, Pred the predicate's name and no Targets.
type LawReach struct {
	Never     Pos
	Word      Pos
	Path      bool
	Lbrack    Pos // a path's [, else NoPos
	Targets   []*Ident
	Rbrack    Pos    // a path's ], else NoPos
	Invariant string // "always" or "sometimes", else ""
	Pred      *Ident // the predicate of an invariant law, else nil
}

func (r *LawReach) Pos() Pos {
	if r.Never != NoPos {
		return r.Never
	}
	return r.Word
}

func (r *LawReach) End() Pos {
	if r.Path {
		return r.Rbrack + 1
	}
	if r.Pred != nil {
		return r.Pred.End()
	}
	if len(r.Targets) == 0 {
		return r.Word + 1
	}
	return r.Targets[len(r.Targets)-1].End()
}

// ReachKind names the form of a reachability law: "reaches", "never",
// "path", "always" or "sometimes".
func (r *LawReach) Kind() string {
	switch {
	case r.Invariant != "":
		return r.Invariant
	case r.Path:
		return "path"
	case r.Never != NoPos:
		return "never"
	}
	return "reaches"
}

// AssertStmt is `assert e`.
type AssertStmt struct {
	Assert Pos
	X      Expr
}

func (s *AssertStmt) Pos() Pos { return s.Assert }
func (s *AssertStmt) End() Pos { return s.X.End() }
func (*AssertStmt) stmtNode()  {}

// DieStmt is `die e`: a terminating deliberate-defect statement.
type DieStmt struct {
	Die Pos
	X   Expr
}

func (s *DieStmt) Pos() Pos { return s.Die }
func (s *DieStmt) End() Pos { return s.X.End() }
func (*DieStmt) stmtNode()  {}

// AlwaysStmt is `always e`: an invariant check statement inside a law body.
type AlwaysStmt struct {
	Always Pos
	X      Expr
}

func (s *AlwaysStmt) Pos() Pos { return s.Always }
func (s *AlwaysStmt) End() Pos { return s.X.End() }
func (*AlwaysStmt) stmtNode()  {}

// SometimesStmt is `sometimes e`: an existential witness check statement
// inside a law body, statically registered and required to evaluate true
// at least once across the law's cases.
type SometimesStmt struct {
	Sometimes Pos
	X         Expr
}

func (s *SometimesStmt) Pos() Pos { return s.Sometimes }
func (s *SometimesStmt) End() Pos { return s.X.End() }
func (*SometimesStmt) stmtNode()  {}

// SkipStmt is `skip "reason"` (runtime.md, Tests 4): it ends the test or
// law run it stands in as skipped, with the reason, a string literal.
// Reason is the literal's source text, quoted, from ReasonPos to
// ReasonEnd: a literal, not an expression, so no name or value stands in
// it.
type SkipStmt struct {
	Skip      Pos
	ReasonPos Pos
	Reason    string
	ReasonEnd Pos
}

func (s *SkipStmt) Pos() Pos { return s.Skip }
func (s *SkipStmt) End() Pos { return s.ReasonEnd }

func (*SkipStmt) stmtNode() {}

// Text is the skip's reason, unquoted; false when the literal does not
// unquote.
func (s *SkipStmt) Text() (string, bool) {
	text, err := strconv.Unquote(s.Reason)
	return text, err == nil
}

// VariantSpec is a union variant declared in place: `Authorized{AuthID
// string}` or `Pending{}`. A variant without members is a bare type name
// in InterfaceType.Union, never a VariantSpec.
type VariantSpec struct {
	Name     *Ident
	TypeArgs *FieldList // brackets; nil without type arguments
	Fields   *FieldList // braces
}

func (v *VariantSpec) Pos() Pos { return v.Name.Pos() }
func (v *VariantSpec) End() Pos { return v.Fields.End() }
func (*VariantSpec) exprNode()  {}

// AsPattern is `P as name`: it matches P and binds the matched value,
// narrowed to P's type, to name (ninth round).
type AsPattern struct {
	X    Expr
	As   Pos
	Name *Ident
}

func (x *AsPattern) Pos() Pos { return x.X.Pos() }
func (x *AsPattern) End() Pos { return x.Name.End() }
func (*AsPattern) exprNode()  {}

// OrPattern is `P | Q` nested in a case's pattern: a tuple element, a
// record pattern's field or a parenthesised pattern (FK7a; orpattern.go).
// It matches when one alternative matches. The parser expands a case
// whose patterns hold one into the case's list of patterns, so only the
// formatter reads it (CaseClause.Written, CatchArm.Written).
type OrPattern struct {
	Alts []Expr
	Bars []Pos // the `|` before each alternative after the first
}

func (x *OrPattern) Pos() Pos { return x.Alts[0].Pos() }
func (x *OrPattern) End() Pos { return x.Alts[len(x.Alts)-1].End() }
func (*OrPattern) exprNode()  {}

// InterpolatedString is `"a \(x) b"`. Texts holds the raw source of each
// literal segment, delimiters included: the head runs from the opening
// quote through the first `\(` (Kind STRING_HEAD), each middle from a `)`
// through the next `\(` (STRING_MID), and the tail from the last `)`
// through the closing quote (STRING_TAIL). Exprs holds the interpolated
// expressions; len(Texts) == len(Exprs)+1.
type InterpolatedString struct {
	Texts []*BasicLit
	Exprs []Expr
}

func (x *InterpolatedString) Pos() Pos { return x.Texts[0].Pos() }
func (x *InterpolatedString) End() Pos { return x.Texts[len(x.Texts)-1].End() }
func (*InterpolatedString) exprNode()  {}
