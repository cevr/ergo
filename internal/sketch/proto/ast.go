package proto

// File is one parsed source file.
type File struct {
	Name     string
	Package  string
	Imports  []string
	Types    []*TypeDecl
	Funcs    []*FuncDecl
	Contexts []*ContextDecl
}

type typeKind int

const (
	kindStruct typeKind = iota
	kindCapability
	kindUnion
)

// TypeDecl is `type X struct`, `type X interface { methods }` or `type X interface{ A | B }`.
type TypeDecl struct {
	Pos      Pos
	Name     string
	Kind     typeKind
	Fields   []Param
	Methods  []*Sig
	Variants []string
}

// QName is a possibly package-qualified name, such as timeout.Expired.
type QName struct {
	Pos  Pos
	Pkg  string
	Name string
}

func (q QName) String() string {
	if q.Pkg != "" {
		return q.Pkg + "." + q.Name
	}
	return q.Name
}

// Param is a named, typed parameter or field. Name may be empty for receivers.
type Param struct {
	Pos  Pos
	Name string
	Type string
}

// Sig is a function or method signature. Result is "" for no value.
type Sig struct {
	Pos      Pos
	Name     string
	Params   []Param
	Result   string
	Fallible bool
	Errors   []QName
	Needs    []QName
}

// FuncDecl is a function, or a method when Recv is set.
type FuncDecl struct {
	Sig
	Recv *Param
	Body *Block
}

// ContextDecl is `context Name { Cap = Provider ... }`.
type ContextDecl struct {
	Pos     Pos
	Name    string
	Entries []ContextEntry
}

// ContextEntry binds one capability to a provider literal.
type ContextEntry struct {
	Pos   Pos
	Cap   string
	Value Expr
}

// Block is a braced statement list. As an expression block, its value is its
// last statement, which must be an expression statement.
type Block struct {
	Pos   Pos
	Stmts []Stmt
}

type Stmt interface{ stmtPos() Pos }

type DefineStmt struct {
	Pos   Pos
	Name  string
	Value Expr
}

type ReturnStmt struct {
	Pos   Pos
	Value Expr // nil for a bare return
}

type IfStmt struct {
	Pos  Pos
	Cond Expr
	Then *Block
	Else *Block
}

// TypeSwitchStmt is `switch bind := subject.(type) { case T: ... }`.
type TypeSwitchStmt struct {
	Pos     Pos
	Bind    string
	Subject Expr
	Cases   []TypeCase
}

type TypeCase struct {
	Pos   Pos
	Types []QName
	Body  *Block
}

type ExprStmt struct {
	Pos Pos
	X   Expr
}

func (s *DefineStmt) stmtPos() Pos     { return s.Pos }
func (s *ReturnStmt) stmtPos() Pos     { return s.Pos }
func (s *IfStmt) stmtPos() Pos         { return s.Pos }
func (s *TypeSwitchStmt) stmtPos() Pos { return s.Pos }
func (s *ExprStmt) stmtPos() Pos       { return s.Pos }

type Expr interface{ exprPos() Pos }

type Ident struct {
	Pos  Pos
	Name string
}

type BasicLit struct {
	Pos  Pos
	Kind tokKind // tString, tInt or tDuration
	Text string
}

type Binary struct {
	Pos  Pos
	Op   string
	L, R Expr
}

type Selector struct {
	Pos Pos
	X   Expr
	Sel string
}

// Call is a call, with an optional trailing block (with and timeout.After).
type Call struct {
	Pos   Pos
	Fun   Expr
	Args  []Expr
	Block *Block
}

type KeyValue struct {
	Key   string
	Value Expr
}

// CompositeLit is `T{K: v}` or `T{v}`.
type CompositeLit struct {
	Pos        Pos
	Type       string
	Elems      []KeyValue
	Positional bool
}

type TryExpr struct {
	Pos Pos
	X   Expr
}

// CatchExpr is `x catch { case T: value }`.
type CatchExpr struct {
	Pos  Pos
	X    Expr
	Arms []CatchArm
}

type CatchArm struct {
	Pos   Pos
	Types []QName
	Value Expr
}

// ContextLit is an inline `context{ A = X{}; B = Y{} }`.
type ContextLit struct {
	Pos     Pos
	Entries []ContextEntry
}

func (e *Ident) exprPos() Pos        { return e.Pos }
func (e *BasicLit) exprPos() Pos     { return e.Pos }
func (e *Binary) exprPos() Pos       { return e.Pos }
func (e *Selector) exprPos() Pos     { return e.Pos }
func (e *Call) exprPos() Pos         { return e.Pos }
func (e *CompositeLit) exprPos() Pos { return e.Pos }
func (e *TryExpr) exprPos() Pos      { return e.Pos }
func (e *CatchExpr) exprPos() Pos    { return e.Pos }
func (e *ContextLit) exprPos() Pos   { return e.Pos }
