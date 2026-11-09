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

// SplitMethod returns the Go import path, the type and the method name of
// a method target: `net/url.URL.Hostname` is ("net/url", "URL",
// "Hostname").
func (s *ExternSpec) SplitMethod() (path, typ, name string) {
	rest, name := splitTarget(s.Target.Value)
	path, typ = splitTarget(rest)
	return path, typ, name
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
// (law-top). A top-level law has no type parameters.
type LawDecl struct {
	Doc    *CommentGroup
	Law    Pos
	Name   *Ident
	Params *FieldList
	Body   *BlockStmt
}

func (d *LawDecl) Pos() Pos { return d.Law }
func (d *LawDecl) End() Pos { return d.Body.End() }
func (*LawDecl) declNode()  {}

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
