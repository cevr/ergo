package syntax

// The AST follows go/ast: types are expressions, every node has Pos and End,
// and comments live in File.Comments in source order with a Doc group on the
// nodes that carry documentation. Ergo's constructs have dedicated nodes
// (ErrorRow, NeedsClause, TryExpr, CatchExpr,
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
	// Expansion is the record the checker's derive expansion keeps for the
	// package of this file (internal/sketch/derive), or nil before it.
	Expansion any
}

// Pos is the first declaration or comment, or the end of an empty file: a
// decided source has no package clause.
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

// LineCount is the physical line count of f: blank and comment lines
// count, an empty file has 0 lines, and a trailing newline does not add
// an empty line. Uses the parser's line-start index when present.
func (f *File) LineCount() int {
	if len(f.Src) == 0 {
		return 0
	}
	n := len(f.lines)
	if n == 0 {
		for i := 0; i < len(f.Src); i++ {
			if f.Src[i] == '\n' {
				n++
			}
		}
		n++
	}
	if f.Src[len(f.Src)-1] == '\n' {
		n--
	}
	return n
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
// parentheses.
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

// VarDecl is `let [mut] patterns [Type] [= e]`: one VarSpec, no
// parentheses, Var at `let`. Mut is the position of `mut`, or NoPos.
type VarDecl struct {
	Doc    *CommentGroup
	Extern *ExternSpec // a Go constant or error variable of a binding: `extern go math.Pi let Pi float64`
	Var    Pos
	Mut    Pos
	Specs  []*VarSpec
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
	Extern     *ExternSpec // a host type of a binding: `extern go net/url.URL type URL { … }`
	Type       Pos
	Name       *Ident
	TypeParams *FieldList // brackets; nil without a list
	Spec       Expr
	// WherePos and Where are the refinement `where P` of a defined type
	// (types.md, Refinements), or NoPos and nil. The parser derives the
	// type's parse function and law from it (refine.go).
	WherePos Pos
	Where    Expr
	// DerivePos and Derive are `derive E` (patterns.md, Derivation;
	// derive.go), or NoPos and nil. Origin is the derived type a bundle
	// declaration was derived from, or nil for a written one.
	DerivePos Pos
	Derive    Expr
	Origin    *TypeDecl
	// ImplementsPos and Implements are the clause `implements A, B` after
	// the type's body (types.md, Interface assignability 1): the operation
	// types the type declares it implements, or NoPos and nil.
	ImplementsPos Pos
	Implements    []Expr
}

// FuncDecl is a function, or with Extern set and Body nil, the Go binding
// `extern go path.F func Name(sig)`. A receiver is the first parameter.
// Recv is set only on a Go method of a binding, `extern go
// net/url.URL.Hostname func (u *URL) Hostname() string`, whose target names
// the type and the method.
type FuncDecl struct {
	Doc           *CommentGroup
	Extern        *ExternSpec
	Func          Pos
	Recv          *FieldList
	Name          *Ident
	TypeParams    *FieldList
	Type          *FuncType
	Body          *BlockStmt
	ImplementsPos Pos
	Implements    Expr
	// Derived is the refined type whose `where` the parser derived this
	// function from (ParseX; refine.go), or nil for a written one. The
	// formatter does not print it.
	Derived *TypeDecl
}

// ContextDecl is `context Name [fails E | F] [needs C | D] { entries }`; it
// exposes every entry it binds. Fails is the failure row of its build, a
// signature's error row (CTX-ROWS-1b). Needs lists the capabilities its
// providers require from the with site. Either clause may be nil.
//
// The decided spelling is `let Name = context [fails …] [needs …] { … }` at
// package level; Let is the position of `let`.
type ContextDecl struct {
	Doc     *CommentGroup
	Let     Pos
	Context Pos
	Name    *Ident
	Fails   *ErrorRow
	Needs   *NeedsClause
	Body    *ContextBody
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
func (d *VarDecl) Pos() Pos {
	if d.Extern != nil {
		return d.Extern.Pos()
	}
	return d.Var
}
func (d *VarDecl) End() Pos { return d.Specs[0].End() }
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
func (d *TypeDecl) Pos() Pos {
	if d.Extern != nil {
		return d.Extern.Pos()
	}
	return d.Type
}
func (d *TypeDecl) End() Pos {
	if d.Derive != nil {
		return d.Derive.End() // derive.go: the bundle stands at the derive
	}
	if len(d.Implements) > 0 {
		return d.Implements[len(d.Implements)-1].End()
	}
	if d.Where != nil {
		return d.Where.End()
	}
	return d.Spec.End()
}
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
	if d.Implements != nil {
		return d.Implements.End()
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
// for type parameters written without a constraint (`type Attempt[T]`), and
// for a group of them that `any` ends before a constrained group (`[E any,
// K comparable]`), whose `any` is at Any (grammar.md, Declarations). Any is 0
// when there is none: an `any` never starts a file. For a method of a
// capability interface, Names holds the method name and Type is its
// *FuncType. A record member `..R` that includes the fields of the record R
// (types.md, Structs 7) has Include set, Dots at its `..`, no Names, and R
// as its Type.
type Field struct {
	Doc   *CommentGroup
	Names []*Ident
	Type  Expr
	Key   *BasicLit // optional record field key, independent of the codec format
	Any   Pos
	// Lent is the position of a type parameter's `lent` written before
	// its constraint, `[T lent Counter]`, or NoPos. lent is gone (MODES-4):
	// the parser keeps it for ergo fix, and the checker refuses it.
	Lent    Pos
	Include bool
	Dots    Pos
	// WherePos and Where are a law parameter's `where P` (types.md,
	// Refinements), or NoPos and nil.
	WherePos Pos
	Where    Expr
	// Pure marks a named parameter's clause `pure` before its type
	// (grammar.md, Declarations 16; rows.md, Footprints 6), written at
	// PurePos. PurePos means nothing without Pure: a Field built with
	// neither, or repositioned (derive), has no clause.
	Pure    bool
	PurePos Pos
	// Own is the mode word `own` (audit CSX §4.3, q168), or nil: between
	// a parameter's names and its type (`f own File`), before an unnamed
	// parameter's type (`func(own File)`), and before an operation's name
	// for its receiver (`own Close()`), and after a type parameter's
	// names (`[T own]`, `[T own C]`; types.md, Generics 9). It is a
	// word, not an expression.
	Own *Word
	// Mut is the mode word `mut` before an operation's name, `mut Bump()`:
	// the operation writes its receiver in place (audit CSX §4.3, planner
	// decision 8; MODES-4), or nil.
	Mut *Word
}

// Word is a mode word written in a field (Field.Own): its position.
type Word struct {
	At Pos
}

func (f *Field) Pos() Pos {
	if f.Include {
		return f.Dots
	}
	if f.Own != nil && (len(f.Names) == 0 || f.Own.At < f.Names[0].Pos()) {
		return f.Own.At
	}
	if f.Mut != nil {
		return f.Mut.At
	}
	if len(f.Names) > 0 {
		return f.Names[0].Pos()
	}
	return f.Type.Pos()
}
func (f *Field) End() Pos {
	if f.Key != nil {
		return f.Key.End()
	}
	if f.Any > 0 {
		return f.Any + Pos(len("any"))
	}
	if f.Where != nil {
		return f.Where.End()
	}
	if f.Type == nil {
		if f.Own != nil {
			return f.Own.At + Pos(len("own")) // [T own]
		}
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

// WritesTakes reports whether the signature t writes which parameters it
// takes: the mode word own on a parameter that is not written as a
// function type (own on a function type keeps the function and takes
// nothing; q168).
func WritesTakes(t *FuncType) bool {
	if t.Params != nil {
		for _, f := range t.Params.List {
			if _, fn := unparenType(f.Type).(*FuncType); f.Own != nil && !fn {
				return true
			}
		}
	}
	return false
}

// unparenType is x without its parentheses.
func unparenType(x Expr) Expr {
	for {
		p, ok := x.(*ParenExpr)
		if !ok {
			return x
		}
		x = p.X
	}
}

// FactKind distinguishes signature fact clauses.
type FactKind int

const (
	FactGiven FactKind = iota + 1 // given <expr>
	FactGives                     // gives <expr>
	FactKeeps                     // keeps <expr> [when <expr>]
)

// FactClause is a contextual signature fact clause (`given cond`,
// `gives cond`, or `keeps cond [when guard]`).
type FactClause struct {
	Kind    FactKind
	WordPos Pos  // position of `given`, `gives`, or `keeps`
	Cond    Expr // clause predicate expression
	WhenPos Pos  // position of `when`, or NoPos
	When    Expr // optional guard on `keeps`, or nil
}

func (x *FactClause) Pos() Pos { return x.WordPos }
func (x *FactClause) End() Pos {
	if x.When != nil {
		return x.When.End()
	}
	if x.Cond != nil {
		return x.Cond.End()
	}
	return x.WordPos + 5
}

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

	// TypeGuard is a subject of a TypeSwitchStmt: the subject of a match.
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

	// StarExpr is `*x`: a pointer type or an indirection. With Mut it
	// is the mode word `mut x`, the borrow spelled as a mode (audit CSX
	// §4.3, q169; SIMPLIFY U2): the same borrow type, Star at `mut`.
	StarExpr struct {
		Star Pos
		X    Expr
		Mut  bool
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

	// TryExpr is `try x`. At the start of an expression x is the whole
	// expression; in operand position x is the operand only, and the try
	// adds no grouping (q105): the calls it covers to its right are the
	// checker's rule (grammar.md, Expressions 1).
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

	// ContextLit is a context literal `context [fails …] [needs …] { … }`;
	// any clause may be nil.
	ContextLit struct {
		Context Pos
		Fails   *ErrorRow
		Needs   *NeedsClause
		Body    *ContextBody
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

	// MapType is `Map[K, V]` or `Map[K, V, H]`: Map is the position of
	// the name, Lbrack and Rbrack of its brackets (the v1 language, eighth round: a
	// prelude name cannot be redeclared, so the name always means the map
	// type). Hashing is the third type argument, the provider by which
	// the Map finds its keys, or nil (CONSTRAINTS C5).
	MapType struct {
		Map     Pos
		Lbrack  Pos
		Key     Expr
		Value   Expr
		Hashing Expr
		Rbrack  Pos
	}

	// FuncType is a signature. Func is NoPos in a declaration or method.
	// Results is nil without results; otherwise it holds the result type
	// and, after it, the failures as one Field of type *ErrorRow.
	FuncType struct {
		// Entry marks `entry func(…)`, the type of an entry's send
		// (grammar.md, Types; runtime.md, Machines and actors 14), written
		// at EntryPos. EntryPos means nothing without Entry, so a FuncType
		// built with neither has no clause.
		Entry    bool
		EntryPos Pos
		Func     Pos
		// TypeParams are an operation's own type parameters,
		// `Fold[A](z A, …) A` (types.md, Interface assignability,
		// Generic operations), or nil: only an operation member's
		// signature has them; a function's are its FuncDecl's.
		TypeParams *FieldList
		Params     *FieldList
		Results    *FieldList
		Needs      *NeedsClause
		// Defects marks a function signature written with `defects`
		// (NO-UNREACHABLE §3), at DefectsPos. DefectsPos means nothing
		// without Defects, so a zero-value FuncType has no clause.
		Defects    bool
		DefectsPos Pos
		// Facts holds contextual signature clauses (`given`, `gives`, `keeps ... [when ...]`)
		// after `fails`/`needs`/`takes`/`defects`.
		Facts []*FactClause
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
		Lbrace Pos
		// Includes are the members `..I` that include the operations of
		// the operation type I (types.md, Interface assignability 1):
		// each a Field with Include set.
		Includes []*Field
		Methods  []*Field
		Laws     []*LawDecl
		// Harnesses are the members `test H` (runtime.md, Tests: harnesses):
		// the function of this package a test whose body needs the type
		// runs under. The checker admits one.
		Harnesses []*Harness
		// Terms is the type-set member `~int | ~int64` (types.md,
		// Generics 2): one term per `|`-separated type. Nil when the
		// operation type has no type set; it then constrains by its
		// operations alone. A type with a type set is a constraint only.
		Terms []*TypeTerm
		Union []Expr
		// Rbrace closes the braces; NoPos for a union.
		Rbrace Pos
	}

	// Harness is the member `test H` of an operation type: Test is the
	// position of `test`, Name the harness function's name.
	Harness struct {
		Doc  *CommentGroup
		Test Pos
		Name *Ident
	}

	// TypeTerm is one term of a type-set member: `~T` (Tilde set) admits
	// every type whose underlying type is T, a bare `T` admits T alone,
	// and the term `comparable` admits the comparable types.
	TypeTerm struct {
		Tilde Pos // NoPos for a bare term
		Type  Expr
	}

	// ErrorRow is the failure row `fails A | B` of a signature: Error is the
	// position of `fails`.
	ErrorRow struct {
		Error Pos
		Types []Expr
	}

	// MachineType is `machine[S, E] { initial x; step f }`, with an
	// optional `after g` entry (the states' timers), an optional `work w`
	// entry (the states' work), and its laws after the entries.
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
		After   Expr       // nil when the machine has no after entry
		Work    Expr       // nil when the machine has no work entry
		Exit    Expr       // nil when the machine has no exit entry (FK9-g1)
		Laws    []*LawDecl // the machine's laws, in source order (MU6)
	}

	// TupleExpr is a tuple type or value `(A, B)`, or a tuple pattern; a
	// let with several patterns holds them in one without parentheses.
	TupleExpr struct {
		Lparen Pos
		Elts   []Expr
		Rparen Pos
	}
)

// MachineEntry is `initial: x`, `step: f`, `after: g`, `work: w` or
// `exit: x`.
type MachineEntry struct {
	Doc     *CommentGroup
	NamePos Pos
	Name    string // "initial", "step", "after", "work" or "exit"
	Colon   Pos
	Value   Expr
}

func (e *MachineEntry) Pos() Pos { return e.NamePos }
func (e *MachineEntry) End() Pos { return e.Value.End() }

// CatchArm is `case A, B: statements`.
type CatchArm struct {
	Case  Pos
	Types []Expr
	// Written is the patterns as written when one holds an OrPattern;
	// Types is then their expansion (orpattern.go). nil otherwise.
	Written []Expr
	Colon   Pos
	Body    []Stmt
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
	case x.Entry:
		return x.EntryPos
	case x.Func.IsValid():
		return x.Func
	case x.TypeParams != nil:
		return x.TypeParams.Pos()
	}
	return x.Params.Pos()
}
func (x *FuncType) End() Pos {
	switch {
	case len(x.Facts) > 0:
		return x.Facts[len(x.Facts)-1].End()
	case x.Defects:
		return x.DefectsPos + Pos(len("defects"))
	case x.Needs != nil:
		return x.Needs.End()
	case x.Results != nil:
		return x.Results.End()
	}
	return x.Params.End()
}
func (x *StructType) Pos() Pos { return x.Fields.Pos() }
func (x *StructType) End() Pos { return x.Fields.End() }
func (x *TypeTerm) Pos() Pos {
	if x.Tilde.IsValid() {
		return x.Tilde
	}
	return x.Type.Pos()
}
func (x *TypeTerm) End() Pos { return x.Type.End() }

func (x *Harness) Pos() Pos { return x.Test }
func (x *Harness) End() Pos { return x.Name.End() }

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
	Case Pos
	List []Expr
	// Written is the patterns as written when one holds an OrPattern;
	// List is then their expansion, one pattern per combination of
	// alternatives (orpattern.go). nil otherwise.
	Written []Expr
	Colon   Pos
	Body    []Stmt
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

// if and match are expressions in operand position (sixth round).
func (*IfStmt) exprNode()         {}
func (*SwitchStmt) exprNode()     {}
func (*TypeSwitchStmt) exprNode() {}
