package syntax

// The AST follows go/ast: types are expressions, every node has Pos and End,
// and comments live in File.Comments in source order with a Doc group on the
// nodes that carry documentation. Ergo's constructs have dedicated nodes
// (ErrorRow, NeedsClause, ProvidesClause, TryExpr, CatchExpr,
// TypeSwitchStmt, MachineType, ContextDecl, ContextLit,
// CallExpr.Trailing). The parser resolves no names: with(…) and
// timeout.After(…) are ordinary calls.

// Node is any syntax node.
type Node interface {
	Pos() Pos // the first byte of the node
	End() Pos // the byte after the node
}

// Expr is an expression or a type.
type Expr interface {
	Node
	exprNode()
}

// Stmt is a statement.
type Stmt interface {
	Node
	stmtNode()
}

// Decl is a top-level declaration, or a local var declaration.
type Decl interface {
	Node
	declNode()
}

// ---------------------------------------------------------------- comments

// Comment is one // or /* */ comment. Text is its source text, with
// carriage returns removed.
type Comment struct {
	Slash  Pos
	Text   string
	EndPos Pos
}

func (c *Comment) Pos() Pos { return c.Slash }
func (c *Comment) End() Pos { return c.EndPos }

// CommentGroup is a run of comments with no blank line and no token between
// them.
type CommentGroup struct {
	List []*Comment
}

func (g *CommentGroup) Pos() Pos { return g.List[0].Pos() }
func (g *CommentGroup) End() Pos { return g.List[len(g.List)-1].End() }

// ---------------------------------------------------------------- file

// File is one parsed source file.
type File struct {
	// Name is the file's name in its package, set by a caller that read
	// the file from a directory (its package-relative path); the parser
	// leaves it empty. check.Check orders a package's files by it.
	Name     string
	Src      string
	Decls    []Decl // imports first, in source order
	Comments []*CommentGroup
	EOF      Pos
	lines    []int
}

// Pos is the first declaration or comment, or the end of an empty file: a
// decided source has no package clause (#196).
func (f *File) Pos() Pos {
	pos := f.EOF
	if len(f.Decls) > 0 {
		pos = f.Decls[0].Pos()
	}
	if len(f.Comments) > 0 && f.Comments[0].Pos() < pos {
		pos = f.Comments[0].Pos()
	}
	return pos
}
func (f *File) End() Pos { return f.EOF }

// Position returns the 1-based line and byte column of pos.
func (f *File) Position(pos Pos) (line, column int) { return lineColumn(f.lines, int(pos)) }

// Line returns the 1-based line of pos.
func (f *File) Line(pos Pos) int {
	line, _ := lineColumn(f.lines, int(pos))
	return line
}

// ---------------------------------------------------------------- declarations

// ImportKind is the form of an import spec.
type ImportKind int

const (
	ImportPlain ImportKind = iota // "path"
	ImportNamed                   // name "path"
	ImportDot                     // . "path"
	ImportGo                      // go "path": a Go package
)

// ImportDecl is `import p`: the decided parser builds one spec and no
// parentheses (#196).
type ImportDecl struct {
	Doc    *CommentGroup
	Import Pos
	Specs  []*ImportSpec
}

// ImportSpec is one imported path. Name is set for ImportNamed; NamePos is
// the position of the name, `.` or `go`.
type ImportSpec struct {
	Kind    ImportKind
	Name    *Ident
	NamePos Pos
	Path    *BasicLit
}

// VarDecl is `let [mut] patterns [Type] [= e]` (#196): one VarSpec, no
// parentheses, Var at `let`. Mut is the position of `mut`, or NoPos.
type VarDecl struct {
	Doc   *CommentGroup
	Var   Pos
	Mut   Pos
	Specs []*VarSpec
}

// VarSpec is the bound part of a let. Names holds the names of `let x` and
// `let q, r`; Pattern holds any other pattern (a record pattern, a tuple in
// parentheses, or several patterns as a TupleExpr without parentheses), and
// then Names is empty. Values holds the one value, if any.
type VarSpec struct {
	Names   []*Ident
	Pattern Expr
	Type    Expr
	Assign  Pos
	Values  []Expr
}

// TypeDecl is `type Name[params] Spec`, where Spec is a StructType, an
// InterfaceType or a MachineType (grammar.md, Declarations 4).
type TypeDecl struct {
	Doc        *CommentGroup
	Type       Pos
	Name       *Ident
	TypeParams *FieldList // brackets; nil without a list
	Spec       Expr
}

// FuncDecl is a function, or with Extern set and Body nil, the Go binding
// `extern go path.F func Name(sig)`. A receiver is the first parameter
// (#196).
type FuncDecl struct {
	Doc        *CommentGroup
	Extern     *ExternSpec
	Func       Pos
	Name       *Ident
	TypeParams *FieldList
	Type       *FuncType
	Body       *BlockStmt
}

// ContextDecl is `context Name [provides A | B] [needs C | D] { entries }`.
// Provides lists the capabilities the context exposes; the entries it builds
// and does not list stay private to its build. Needs lists the capabilities
// its providers require from the with site. Either clause may be nil.
//
// The decided spelling is `let Name = context [provides …] [needs …] { … }` at
// package level (#196); Let is the position of `let`.
type ContextDecl struct {
	Doc      *CommentGroup
	Let      Pos
	Context  Pos
	Name     *Ident
	Provides *ProvidesClause
	Needs    *NeedsClause
	Body     *ContextBody
}

func (d *ImportDecl) Pos() Pos { return d.Import }
func (d *ImportDecl) End() Pos { return d.Specs[0].End() }
func (s *ImportSpec) Pos() Pos {
	if s.Kind != ImportPlain {
		return s.NamePos
	}
	return s.Path.Pos()
}
func (s *ImportSpec) End() Pos { return s.Path.End() }
func (d *VarDecl) Pos() Pos    { return d.Var }
func (d *VarDecl) End() Pos    { return d.Specs[0].End() }
func (s *VarSpec) Pos() Pos {
	if s.Pattern != nil {
		return s.Pattern.Pos()
	}
	return s.Names[0].Pos()
}
func (s *VarSpec) End() Pos {
	if len(s.Values) > 0 {
		return s.Values[len(s.Values)-1].End()
	}
	if s.Type != nil {
		return s.Type.End()
	}
	if s.Pattern != nil {
		return s.Pattern.End()
	}
	return s.Names[len(s.Names)-1].End()
}
func (d *TypeDecl) Pos() Pos { return d.Type }
func (d *TypeDecl) End() Pos { return d.Spec.End() }
func (d *FuncDecl) Pos() Pos {
	if d.Extern != nil {
		return d.Extern.Pos()
	}
	return d.Func
}
func (d *FuncDecl) End() Pos {
	if d.Body != nil {
		return d.Body.End()
	}
	return d.Type.End()
}
func (d *ContextDecl) Pos() Pos {
	if d.Let.IsValid() {
		return d.Let
	}
	return d.Context
}
func (d *ContextDecl) End() Pos { return d.Body.End() }

func (*ImportDecl) declNode()  {}
func (*VarDecl) declNode()     {}
func (*TypeDecl) declNode()    {}
func (*FuncDecl) declNode()    {}
func (*ContextDecl) declNode() {}

// ContextEntryKind is the form of a context entry.
type ContextEntryKind int

const (
	ContextBind    ContextEntryKind = iota // Key = Value
	ContextEmbed                           // Key: an embedded context
	ContextReplace                         // replace Key = Value
)

// ContextBody is the braced entry list of a context declaration or literal.
type ContextBody struct {
	Lbrace  Pos
	Entries []*ContextEntry
	Rbrace  Pos
}

// ContextEntry binds a capability (Key, a name or a qualified name) to a
// provider, embeds a context, or replaces an embedded entry. Keys holds the
// further capabilities of `A, B = provider`, one provider serving several
// capabilities.
type ContextEntry struct {
	Doc     *CommentGroup
	Kind    ContextEntryKind
	Replace Pos
	Key     Expr
	Keys    []Expr
	Assign  Pos
	Value   Expr
}

func (b *ContextBody) Pos() Pos { return b.Lbrace }
func (b *ContextBody) End() Pos { return b.Rbrace + 1 }
func (e *ContextEntry) Pos() Pos {
	if e.Kind == ContextReplace {
		return e.Replace
	}
	return e.Key.Pos()
}
func (e *ContextEntry) End() Pos {
	if e.Value != nil {
		return e.Value.End()
	}
	return e.Key.End()
}

// ---------------------------------------------------------------- fields and signatures

// Field is a parameter, result, receiver, struct field, type parameter or
// interface method. Names is empty for a type-only parameter. Type is nil
// for type parameters written without a constraint (`type Attempt[T]`). For
// a method of a capability interface, Names holds the method name and Type
// is its *FuncType.
type Field struct {
	Doc   *CommentGroup
	Names []*Ident
	Type  Expr
}

func (f *Field) Pos() Pos {
	if len(f.Names) > 0 {
		return f.Names[0].Pos()
	}
	return f.Type.Pos()
}
func (f *Field) End() Pos {
	if f.Type == nil {
		return f.Names[len(f.Names)-1].End()
	}
	return f.Type.End()
}

// FieldList is a parenthesised, bracketed or braced field list. Opening and
// Closing are NoPos for an unparenthesised single result.
type FieldList struct {
	Opening Pos
	List    []*Field
	Closing Pos
}

func (l *FieldList) Pos() Pos {
	if l.Opening.IsValid() {
		return l.Opening
	}
	return l.List[0].Pos()
}
func (l *FieldList) End() Pos {
	if l.Closing.IsValid() {
		return l.Closing + 1
	}
	return l.List[len(l.List)-1].End()
}

// NeedsClause is a capability row: `needs A | B`, a union as an error row
// is (196n). It is never parenthesised: `|` does not end a parameter, so
// the row reads the same inside a list.
type NeedsClause struct {
	Needs Pos
	Names []Expr
}

func (u *NeedsClause) Pos() Pos { return u.Needs }
func (u *NeedsClause) End() Pos { return u.Names[len(u.Names)-1].End() }

// ProvidesClause is a context's exposure: `provides A | B`, the same kind of
// row as a needs clause (196n).
type ProvidesClause struct {
	Provides Pos
	Names    []Expr
}

func (u *ProvidesClause) Pos() Pos { return u.Provides }
func (u *ProvidesClause) End() Pos { return u.Names[len(u.Names)-1].End() }

// ---------------------------------------------------------------- expressions and types

type (
	// Ident is a name.
	Ident struct {
		NamePos Pos
		Name    string
	}

	// BasicLit is an INT, DURATION, STRING or RUNE literal. Value is its
	// source text.
	BasicLit struct {
		ValuePos Pos
		Kind     Token
		Value    string
		ValueEnd Pos
	}

	// CompositeLit is `T{…}`, or `{…}` with an elided type inside another
	// composite literal (Type is nil). A record update `T{..Base, F: v}`
	// has a Base, written first after `..` at DotDot; Elts are then its
	// keyed fields (grammar.md, Expressions 11). Base is nil in every other
	// literal.
	CompositeLit struct {
		Type   Expr
		Lbrace Pos
		DotDot Pos
		Base   Expr
		Elts   []Expr
		Rbrace Pos
	}

	// KeyValueExpr is `key: value` in a composite literal.
	KeyValueExpr struct {
		Key   Expr
		Colon Pos
		Value Expr
	}

	// FuncLit is a function literal.
	FuncLit struct {
		Type *FuncType
		Body *BlockStmt
	}

	// ParenExpr is a parenthesised expression or type.
	ParenExpr struct {
		Lparen Pos
		X      Expr
		Rparen Pos
	}

	// SelectorExpr is `x.Sel`.
	SelectorExpr struct {
		X   Expr
		Sel *Ident
	}

	// IndexExpr is `x[i]`, or an instantiation `x[A, B]`.
	IndexExpr struct {
		X       Expr
		Lbrack  Pos
		Indices []Expr
		Rbrack  Pos
	}

	// SliceExpr is `x[low:high]` or `x[low:high:max]`.
	SliceExpr struct {
		X      Expr
		Lbrack Pos
		Low    Expr
		High   Expr
		Max    Expr
		Slice3 bool
		Rbrack Pos
	}

	// TypeGuard is a subject of a TypeSwitchStmt: the subject of a match
	// (#196).
	TypeGuard struct {
		X Expr
	}

	// CallExpr is a call or a conversion, with an optional trailing block
	// (grammar.md, Expressions 3).
	CallExpr struct {
		Fun      Expr
		Lparen   Pos
		Args     []Expr
		Rparen   Pos
		Trailing *BlockStmt
		// TrailingFunc is true when the last of Args is a function
		// literal written after the `)` (grammar.md, Expressions 9):
		// it is an argument like any other, and the formatter writes
		// it back after the call.
		TrailingFunc bool
	}

	// StarExpr is `*x`: a pointer type or an indirection.
	StarExpr struct {
		Star Pos
		X    Expr
	}

	// UnaryExpr is `op x` for + - ! ^ & <-.
	UnaryExpr struct {
		OpPos Pos
		Op    Token
		X     Expr
	}

	// BinaryExpr is `x op y`.
	BinaryExpr struct {
		X     Expr
		OpPos Pos
		Op    Token
		Y     Expr
	}

	// TryExpr is `try x`. It binds looser than every binary operator, also
	// in operand position, where it extends over the rest of the binary
	// expression to its right (grammar.md D8).
	TryExpr struct {
		Try Pos
		X   Expr
	}

	// CatchExpr is `x catch { arms }`. Arms hold statements.
	CatchExpr struct {
		X      Expr
		Catch  Pos
		Lbrace Pos
		Arms   []*CatchArm
		Rbrace Pos
	}

	// ContextLit is a context literal `context [provides …] [needs …] { … }`; either
	// clause may be nil.
	ContextLit struct {
		Context  Pos
		Provides *ProvidesClause
		Needs    *NeedsClause
		Body     *ContextBody
	}

	// ArrayType is the slice type `[]T`, or the fixed array type `[N]T`
	// when Len is set: the text of an integer literal at LenPos (196y).
	// The length is a literal, never an expression, so it holds no name
	// and no use.
	ArrayType struct {
		Lbrack Pos
		Len    string
		LenPos Pos
		Elt    Expr
	}

	// MapType is `Map[K, V]`: Map is the position of the name, Lbrack and
	// Rbrack of its brackets (#196, eighth round: a prelude name cannot be
	// redeclared, so the name always means the map type).
	MapType struct {
		Map    Pos
		Lbrack Pos
		Key    Expr
		Value  Expr
		Rbrack Pos
	}

	// FuncType is a signature. Func is NoPos in a declaration or method.
	// Results is nil without results; otherwise it holds the result type
	// and, after it, the failures as one Field of type *ErrorRow. Escaping
	// is the position of `escaping`, or NoPos.
	FuncType struct {
		Escaping Pos
		Func     Pos
		Params   *FieldList
		Results  *FieldList
		Needs    *NeedsClause
	}

	// StructType is the record of `type T { fields }`: Fields holds the
	// braces.
	StructType struct {
		Fields *FieldList
	}

	// InterfaceType is an operation type `type T { operations, laws }`
	// (Methods and Laws, braces set) or a union `type T = A | B{…}` (Union,
	// each a type name or a *VariantSpec; Lbrace and Rbrace NoPos).
	InterfaceType struct {
		Lbrace  Pos
		Methods []*Field
		Laws    []*LawDecl
		Union   []Expr
		Rbrace  Pos
	}

	// ErrorRow is the failure row `fails A | B` of a signature: Error is the
	// position of `fails`.
	ErrorRow struct {
		Error Pos
		Types []Expr
	}

	// MachineType is `machine[S, E] { initial x; step f }`, with an
	// optional `after g` entry (the states' timers).
	MachineType struct {
		Machine Pos
		Lbrack  Pos
		State   Expr
		Signal  Expr
		Rbrack  Pos
		Lbrace  Pos
		Entries []*MachineEntry // in source order
		Rbrace  Pos
		Initial Expr
		Step    Expr
		After   Expr // nil when the machine has no after entry
	}

	// TupleExpr is a tuple type or value `(A, B)`, or a tuple pattern; a
	// let with several patterns holds them in one without parentheses.
	TupleExpr struct {
		Lparen Pos
		Elts   []Expr
		Rparen Pos
	}
)

// MachineEntry is `initial: x`, `step: f` or `after: g`.
type MachineEntry struct {
	Doc     *CommentGroup
	NamePos Pos
	Name    string // "initial", "step" or "after"
	Colon   Pos
	Value   Expr
}

func (e *MachineEntry) Pos() Pos { return e.NamePos }
func (e *MachineEntry) End() Pos { return e.Value.End() }

// CatchArm is `case A, B: statements`.
type CatchArm struct {
	Case  Pos
	Types []Expr
	Colon Pos
	Body  []Stmt
}

func (a *CatchArm) Pos() Pos { return a.Case }
func (a *CatchArm) End() Pos {
	if len(a.Body) > 0 {
		return a.Body[len(a.Body)-1].End()
	}
	return a.Colon + 1
}

func (x *Ident) Pos() Pos    { return x.NamePos }
func (x *Ident) End() Pos    { return x.NamePos + Pos(len(x.Name)) }
func (x *BasicLit) Pos() Pos { return x.ValuePos }
func (x *BasicLit) End() Pos { return x.ValueEnd }
func (x *CompositeLit) Pos() Pos {
	if x.Type != nil {
		return x.Type.Pos()
	}
	return x.Lbrace
}
func (x *CompositeLit) End() Pos { return x.Rbrace + 1 }
func (x *KeyValueExpr) Pos() Pos { return x.Key.Pos() }
func (x *KeyValueExpr) End() Pos { return x.Value.End() }
func (x *FuncLit) Pos() Pos      { return x.Type.Pos() }
func (x *FuncLit) End() Pos      { return x.Body.End() }
func (x *ParenExpr) Pos() Pos    { return x.Lparen }
func (x *ParenExpr) End() Pos    { return x.Rparen + 1 }
func (x *SelectorExpr) Pos() Pos { return x.X.Pos() }
func (x *SelectorExpr) End() Pos { return x.Sel.End() }
func (x *IndexExpr) Pos() Pos    { return x.X.Pos() }
func (x *IndexExpr) End() Pos    { return x.Rbrack + 1 }
func (x *SliceExpr) Pos() Pos    { return x.X.Pos() }
func (x *SliceExpr) End() Pos    { return x.Rbrack + 1 }
func (x *TypeGuard) Pos() Pos    { return x.X.Pos() }
func (x *TypeGuard) End() Pos    { return x.X.End() }
func (x *CallExpr) Pos() Pos     { return x.Fun.Pos() }
func (x *CallExpr) End() Pos {
	if x.Trailing != nil {
		return x.Trailing.End()
	}
	if x.TrailingFunc {
		return x.Args[len(x.Args)-1].End()
	}
	return x.Rparen + 1
}
func (x *StarExpr) Pos() Pos   { return x.Star }
func (x *StarExpr) End() Pos   { return x.X.End() }
func (x *UnaryExpr) Pos() Pos  { return x.OpPos }
func (x *UnaryExpr) End() Pos  { return x.X.End() }
func (x *BinaryExpr) Pos() Pos { return x.X.Pos() }
func (x *BinaryExpr) End() Pos { return x.Y.End() }
func (x *TryExpr) Pos() Pos    { return x.Try }
func (x *TryExpr) End() Pos    { return x.X.End() }
func (x *CatchExpr) Pos() Pos  { return x.X.Pos() }
func (x *CatchExpr) End() Pos  { return x.Rbrace + 1 }
func (x *ContextLit) Pos() Pos { return x.Context }
func (x *ContextLit) End() Pos { return x.Body.End() }
func (x *ArrayType) Pos() Pos  { return x.Lbrack }
func (x *ArrayType) End() Pos  { return x.Elt.End() }
func (x *MapType) Pos() Pos    { return x.Map }
func (x *MapType) End() Pos {
	if x.Rbrack.IsValid() {
		return x.Rbrack + 1
	}
	return x.Value.End()
}
func (x *FuncType) Pos() Pos {
	switch {
	case x.Escaping.IsValid():
		return x.Escaping
	case x.Func.IsValid():
		return x.Func
	}
	return x.Params.Pos()
}
func (x *FuncType) End() Pos {
	switch {
	case x.Needs != nil:
		return x.Needs.End()
	case x.Results != nil:
		return x.Results.End()
	}
	return x.Params.End()
}
func (x *StructType) Pos() Pos { return x.Fields.Pos() }
func (x *StructType) End() Pos { return x.Fields.End() }
func (x *InterfaceType) Pos() Pos {
	if x.Lbrace.IsValid() {
		return x.Lbrace
	}
	return x.Union[0].Pos()
}
func (x *InterfaceType) End() Pos {
	if x.Rbrace.IsValid() {
		return x.Rbrace + 1
	}
	return x.Union[len(x.Union)-1].End()
}
func (x *ErrorRow) Pos() Pos    { return x.Error }
func (x *ErrorRow) End() Pos    { return x.Types[len(x.Types)-1].End() }
func (x *MachineType) Pos() Pos { return x.Machine }
func (x *MachineType) End() Pos { return x.Rbrace + 1 }
func (x *TupleExpr) Pos() Pos {
	if x.Lparen.IsValid() {
		return x.Lparen
	}
	return x.Elts[0].Pos()
}
func (x *TupleExpr) End() Pos {
	if x.Rparen.IsValid() {
		return x.Rparen + 1
	}
	return x.Elts[len(x.Elts)-1].End()
}

func (*Ident) exprNode()         {}
func (*BasicLit) exprNode()      {}
func (*CompositeLit) exprNode()  {}
func (*KeyValueExpr) exprNode()  {}
func (*FuncLit) exprNode()       {}
func (*ParenExpr) exprNode()     {}
func (*SelectorExpr) exprNode()  {}
func (*IndexExpr) exprNode()     {}
func (*SliceExpr) exprNode()     {}
func (*TypeGuard) exprNode()     {}
func (*CallExpr) exprNode()      {}
func (*StarExpr) exprNode()      {}
func (*UnaryExpr) exprNode()     {}
func (*BinaryExpr) exprNode()    {}
func (*TryExpr) exprNode()       {}
func (*CatchExpr) exprNode()     {}
func (*ContextLit) exprNode()    {}
func (*ArrayType) exprNode()     {}
func (*MapType) exprNode()       {}
func (*FuncType) exprNode()      {}
func (*StructType) exprNode()    {}
func (*InterfaceType) exprNode() {}
func (*ErrorRow) exprNode()      {}
func (*MachineType) exprNode()   {}
func (*TupleExpr) exprNode()     {}

// ---------------------------------------------------------------- statements

type (
	// DeclStmt is a local var declaration.
	DeclStmt struct {
		Decl *VarDecl
	}

	// ExprStmt is an expression used as a statement.
	ExprStmt struct {
		X Expr
	}

	// AssignStmt is `lhs := rhs`, `lhs = rhs` or `lhs op= rhs`.
	AssignStmt struct {
		Lhs    []Expr
		TokPos Pos
		Tok    Token
		Rhs    []Expr
	}

	// ReturnStmt is `return [results]`.
	ReturnStmt struct {
		Return  Pos
		Results []Expr
	}

	// BranchStmt is `break` or `continue`.
	BranchStmt struct {
		TokPos Pos
		Tok    Token
	}

	// BlockStmt is a braced statement list.
	BlockStmt struct {
		Lbrace Pos
		List   []Stmt
		Rbrace Pos
	}

	// IfStmt is `if cond { } [else …]`; Else is an *IfStmt or a
	// *BlockStmt.
	IfStmt struct {
		If      Pos
		Cond    Expr
		Body    *BlockStmt
		ElsePos Pos // NoPos without else
		Else    Stmt
	}

	// SwitchStmt is a match on a literal subject.
	SwitchStmt struct {
		Switch  Pos
		Tag     Expr
		Lbrace  Pos
		Clauses []*CaseClause
		Rbrace  Pos
	}

	// TypeSwitchStmt is any other match, with one subject per tuple
	// element. Each case item of a multi-subject match is a *TupleExpr.
	TypeSwitchStmt struct {
		Switch   Pos
		Subjects []*TypeGuard
		Lbrace   Pos
		Clauses  []*CaseClause
		Rbrace   Pos
	}

	// ForStmt is `for { }` or `for cond { }`.
	ForStmt struct {
		For  Pos
		Cond Expr
		Body *BlockStmt
	}

	// RangeStmt is `for [key[, value]] in x { }`. Tok is ILLEGAL without
	// key and value.
	RangeStmt struct {
		For   Pos
		Key   Expr
		Value Expr
		Tok   Token
		Range Pos
		X     Expr
		Body  *BlockStmt
	}
)

// CaseClause is `case list:` or `default:` (List is nil) of an expression
// or type switch.
type CaseClause struct {
	Case  Pos
	List  []Expr
	Colon Pos
	Body  []Stmt
}

func (c *CaseClause) Pos() Pos { return c.Case }
func (c *CaseClause) End() Pos {
	if len(c.Body) > 0 {
		return c.Body[len(c.Body)-1].End()
	}
	return c.Colon + 1
}

func (s *DeclStmt) Pos() Pos   { return s.Decl.Pos() }
func (s *DeclStmt) End() Pos   { return s.Decl.End() }
func (s *ExprStmt) Pos() Pos   { return s.X.Pos() }
func (s *ExprStmt) End() Pos   { return s.X.End() }
func (s *AssignStmt) Pos() Pos { return s.Lhs[0].Pos() }
func (s *AssignStmt) End() Pos { return s.Rhs[len(s.Rhs)-1].End() }
func (s *ReturnStmt) Pos() Pos { return s.Return }
func (s *ReturnStmt) End() Pos {
	if len(s.Results) > 0 {
		return s.Results[len(s.Results)-1].End()
	}
	return s.Return + 6
}
func (s *BranchStmt) Pos() Pos { return s.TokPos }
func (s *BranchStmt) End() Pos { return s.TokPos + Pos(len(s.Tok.String())) }
func (s *BlockStmt) Pos() Pos  { return s.Lbrace }
func (s *BlockStmt) End() Pos  { return s.Rbrace + 1 }
func (s *IfStmt) Pos() Pos     { return s.If }
func (s *IfStmt) End() Pos {
	if s.Else != nil {
		return s.Else.End()
	}
	return s.Body.End()
}
func (s *SwitchStmt) Pos() Pos     { return s.Switch }
func (s *SwitchStmt) End() Pos     { return s.Rbrace + 1 }
func (s *TypeSwitchStmt) Pos() Pos { return s.Switch }
func (s *TypeSwitchStmt) End() Pos { return s.Rbrace + 1 }
func (s *ForStmt) Pos() Pos        { return s.For }
func (s *ForStmt) End() Pos        { return s.Body.End() }
func (s *RangeStmt) Pos() Pos      { return s.For }
func (s *RangeStmt) End() Pos      { return s.Body.End() }

func (*DeclStmt) stmtNode()       {}
func (*ExprStmt) stmtNode()       {}
func (*AssignStmt) stmtNode()     {}
func (*ReturnStmt) stmtNode()     {}
func (*BranchStmt) stmtNode()     {}
func (*BlockStmt) stmtNode()      {}
func (*IfStmt) stmtNode()         {}
func (*SwitchStmt) stmtNode()     {}
func (*TypeSwitchStmt) stmtNode() {}
func (*ForStmt) stmtNode()        {}
func (*RangeStmt) stmtNode()      {}

// if and match are expressions in operand position (#196, sixth round).
func (*IfStmt) exprNode()         {}
func (*SwitchStmt) exprNode()     {}
func (*TypeSwitchStmt) exprNode() {}
