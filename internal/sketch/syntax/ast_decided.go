package syntax

// ---------------------------------------------------------------- decided-grammar nodes (#196)

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
	Doc    *CommentGroup
	Law    Pos
	Name   *Ident
	Params *FieldList
	Body   *BlockStmt // nil for a reachability law
	Reach  *LawReach  // nil unless a reachability law
}

func (d *LawDecl) Pos() Pos { return d.Law }
func (d *LawDecl) End() Pos {
	if d.Reach != nil {
		return d.Reach.End()
	}
	return d.Body.End()
}
func (*LawDecl) declNode() {}

// LawReach is the graph form of a machine law (MU6). Never is the position
// of `never` in `never reaches V`, else NoPos; Word is the position of
// `reaches` or `path`; Targets are the variants of the machine's state the
// law names: one for reaches, one or more for a path, in order.
type LawReach struct {
	Never   Pos
	Word    Pos
	Path    bool
	Lbrack  Pos // a path's [, else NoPos
	Targets []*Ident
	Rbrack  Pos // a path's ], else NoPos
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
	if len(r.Targets) == 0 {
		return r.Word + 1
	}
	return r.Targets[len(r.Targets)-1].End()
}

// ReachKind names the form of a reachability law: "reaches", "never" or
// "path".
func (r *LawReach) Kind() string {
	switch {
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
// narrowed to P's type, to name (#196, ninth round).
type AsPattern struct {
	X    Expr
	As   Pos
	Name *Ident
}

func (x *AsPattern) Pos() Pos { return x.X.Pos() }
func (x *AsPattern) End() Pos { return x.Name.End() }
func (*AsPattern) exprNode()  {}

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
