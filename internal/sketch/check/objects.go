package check

import (
	"sort"

	"ergo.local/prototype/internal/sketch/syntax"
)

// requirementKeyword is the spelling of a capability (requirement) row in
// source. The keyword is the only place the checker names it, so a rename of
// the keyword (#217) touches this constant and the syntax nodes only.
const requirementKeyword = "needs"

// Object is a named entity: a type, a function, a variable, a constant, a
// package, a builtin or a wire.
type Object interface {
	name() string
	pos() syntax.Pos
}

// TypeName is a declared or predeclared type.
type TypeName struct {
	Name string
	Pkg  *Package
	Pos  syntax.Pos
	Type Type
	Decl *syntax.TypeDecl
	file *fileInfo
	id   int
}

// Func is a package function, extern included.
type Func struct {
	Name  string
	Pkg   *Package
	Pos   syntax.Pos
	Decl  *syntax.FuncDecl
	Ptr   bool // a 196m function whose first parameter is *owner
	Sig   *Signature
	file  *fileInfo
	state int // signature: 0 none, 1 resolving, 2 done

	// Inferred rows: true when the row is computed from the body
	// (rows.md, Capability rows 6; rows-and-constraints §7 amendment 1).
	inferErrors, inferUses bool
	errVar, useVar         *RowVar // the inferred rows' variables (class F)
	called                 bool

	// owner is set for a function x.f(…) reaches (196m): its first
	// parameter's type is owner, or *owner, a type declared in this
	// package. selfArgs are that parameter's type arguments, each a
	// distinct type parameter of the function, which a call on an instance
	// binds.
	owner    *Named
	selfArgs []syntax.Expr
	// same holds the later package functions of the same name (196m rule
	// 3), on the one the package scope holds; overloaded marks every
	// function of a valid group, which is called only as x.f(…).
	same       []*Func
	overloaded bool
	// size is the declaration's syntax nodes (bodySize), once known.
	size int
}

// Var is a variable: a parameter, a local or a package-level let.
type Var struct {
	Name    string
	Type    Type
	Pos     syntax.Pos
	used    bool
	param   bool
	pkg     bool
	group   *switchGroup // a type-switch or catch binding: used if any arm reads it
	bindRow bool         // a capability-row binding (needs X): an entry of the frame, not a value
	// let marks a name a local `let` binds (not a pattern's); mut a `let
	// mut` name, the only kind an assignment may target (D-D1). reassigned
	// records an assignment rooted at a mut name (Q3).
	let, mut, reassigned bool
	// undiagnosed: a local bound to a value typing treats as invalid with no
	// diagnostic of its own (provisional.go).
	undiagnosed bool
}

// Const is a predeclared constant (true, false).
type Const struct {
	Name string
	Type Type
	Val  bool
}

// PkgName is an imported package's name in a file.
type PkgName struct {
	Name string
	Pos  syntax.Pos
	Pkg  *Package
	used bool
	spec *syntax.ImportSpec
	// broken marks an import that failed (reported at the import): its
	// name is bound to an empty package, so a use of it adds nothing (D3).
	broken bool
}

// Builtin is a predeclared function such as len or append, or the with
// construct.
type Builtin struct{ Name string }

// capBinding is a written capability-row binding in a function's frame: the
// bare name of a capability the row lists (rows-and-constraints §7
// amendment 3).
type capBinding struct {
	tn  *TypeName
	at  syntax.Pos
	who string
}

func (o *capBinding) name() string    { return o.tn.Name }
func (o *capBinding) pos() syntax.Pos { return o.at }

// Wire is a named wire.
type Wire struct {
	Name string
	Pos  syntax.Pos
	Decl *syntax.WireDecl
	// Lit is the syntax of an inline wire, nil for a named wire. Both
	// share the binding summary and ownership facts.
	Lit   *syntax.WireLit
	file  *fileInfo
	binds *capSet // capabilities the wire binds; nil until resolved
	own   *capSet // the wire's own entries (wireSummaries)
	// unknown marks a wire that embeds, directly or not, a wire that does
	// not exist (reported at the embedding): what it binds is unknown, so
	// a with of it discharges everything (D3; M18).
	unknown bool
	// budget marks the wire whose summary or body passed the package's
	// wire budget (wiresum.go): it gets the budget S111.
	budget bool
	pkg    *Package
	// Entries are the wire's own entries as written, in source order, made
	// when its body is checked: its provider entries, and an entry per
	// embedding that points to the embedded wire. Provider reads the
	// resolved bindings, shared with the wires that embed it.
	Entries []*WireEntry
	// Overrides are this wire's own replace overrides, so a replaced
	// provider stays traceable: in its replace entries' source order, and
	// for one capability each overridden entry once, in the order the wire
	// pass meets them (wires by name, then entries in source order). An
	// embedded wire's own overrides are on it (Entries[i].Embedded): a
	// replace of a replace is one override on each wire (wiresum.go,
	// finishBody).
	Overrides []Override
}

func (o *TypeName) name() string { return o.Name }
func (o *Func) name() string     { return o.Name }
func (o *Var) name() string      { return o.Name }
func (o *Const) name() string    { return o.Name }
func (o *PkgName) name() string  { return o.Name }
func (o *Builtin) name() string  { return o.Name }
func (o *Wire) name() string     { return o.Name }

// A record field and an operation are Objects, so Defs records their
// declaring names (spec D-A4, invariant 1).
func (o *Field) name() string     { return o.Name }
func (o *Field) pos() syntax.Pos  { return o.Pos }
func (o *Method) name() string    { return o.Name }
func (o *Method) pos() syntax.Pos { return o.Pos }

func (o *TypeName) pos() syntax.Pos { return o.Pos }
func (o *Func) pos() syntax.Pos     { return o.Pos }
func (o *Var) pos() syntax.Pos      { return o.Pos }
func (o *Const) pos() syntax.Pos    { return syntax.NoPos }
func (o *PkgName) pos() syntax.Pos  { return o.Pos }
func (o *Builtin) pos() syntax.Pos  { return syntax.NoPos }
func (o *Wire) pos() syntax.Pos     { return o.Pos }

// switchGroup ties the per-arm variables of one type-switch or catch
// binding: the binding is read when any arm reads it (rows.md, Totality 1).
type switchGroup struct{ used bool }

// Scope is a lexical frame.
type Scope struct {
	parent *Scope
	names  map[string]Object
	// frame marks a function body's frame: its parameters, its written
	// capability-row bindings and its top-level locals
	// (rows-and-constraints §7 amendment 3).
	frame bool
	// barrier marks a with block, a trailing block or a bare block: return,
	// break and continue may not cross it (grammar.md, Statements 5).
	barrier bool
}

func newScope(parent *Scope) *Scope { return &Scope{parent: parent, names: map[string]Object{}} }

func (s *Scope) lookup(name string) (Object, *Scope) {
	for sc := s; sc != nil; sc = sc.parent {
		if o, ok := sc.names[name]; ok {
			return o, sc
		}
	}
	return nil, nil
}

func (s *Scope) insert(name string, o Object) {
	if name == "_" {
		return
	}
	s.names[name] = o
}

// Exported returns the package's exported objects, sorted by name: its
// summary for importers (grammar.md, Visibility 2).
func (p *Package) Exported() []Object {
	var out []Object
	for name, o := range p.Scope.names {
		if isExported(name) {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name() < out[j].name() })
	return out
}

type fileInfo struct {
	syntax  *syntax.File
	index   int
	scope   *Scope // the file's import names
	imports []*PkgName
}

// methods returns the method set of the origin of n, keyed by name.
func (n *Named) method(name string) *Func {
	o := n.origin()
	if o.methods == nil {
		return nil
	}
	return o.methods[name]
}

func (n *Named) addMethod(f *Func) bool {
	if n.methods == nil {
		n.methods = map[string]*Func{}
	}
	if _, dup := n.methods[f.Name]; dup {
		return false
	}
	n.methods[f.Name] = f
	n.mlist = append(n.mlist, f)
	return true
}
