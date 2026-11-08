package check

import "ergo.local/prototype/internal/sketch/syntax"

// Mints and the mint rule (fu-ambient-ctor r6; stdlib.md, Host packages
// 6). Authority is made where a value is built, not where a signature
// names it: a value can hide behind a type no signature names (an open
// operation type it implements, a closure that captures it, a call's
// argument). So the std host guard asks, for each place where a host
// provider's package builds a value of the provider or of a record it
// needs, whether that place holds the authority.
//
// Mints are every place where package code makes a new value, with the
// type made and the place that makes it. A value that a call returns, a
// variable holds or a pattern takes apart is not made there: the function
// that made it is asked where it made it. The form of each kind of
// expression is decided in one switch, mintForm, with no default case;
// TestMintFormNamesEveryKind fails when a kind of expression or of
// declaration is added and the switches do not name it.

// MintForm is how a mint makes its value.
type MintForm int

const (
	// MintLiteral is a composite literal: a record, a slice or a Map
	// literal, with its type written or elided.
	MintLiteral MintForm = iota + 1
	// MintUpdate is a record update `T{..base, F: v}`: a new record.
	MintUpdate
	// MintZeroField is the zero value of a field a record literal omits.
	MintZeroField
	// MintZeroLet is the zero value of a let with a type and no value.
	MintZeroLet
	// MintZeroRecord is a record with no fields written bare as a value
	// (spec D-B2): the record is its own value.
	MintZeroRecord
	// MintConversion is a conversion `T(x)`.
	MintConversion
	// MintConstant is a literal or a constant expression of its type.
	MintConstant
	// MintFunc is a function literal or a trailing block.
	MintFunc
	// MintTuple is a tuple value `(a, b)`.
	MintTuple
	// MintString is an interpolated string.
	MintString
	// MintGenerated is a value a test or a law generates for a parameter:
	// each declaration the parameter's type is made of (Mentions).
	MintGenerated
)

// MintPlace is the kind of declaration a mint is in.
type MintPlace int

const (
	// MintInFunc is a function body, a closure in it included.
	MintInFunc MintPlace = iota + 1
	// MintInLet is a package-level let's value.
	MintInLet
	// MintInContext is a named context's entries.
	MintInContext
	// MintInType is a type declaration: a machine's initial state, an
	// operation type's law.
	MintInType
	// MintInTest is a test block: its body and its generated parameters.
	MintInTest
	// MintInLaw is a top-level law: its body and its parameters.
	MintInLaw
)

// MintFrame is a function around a mint in a function body: the declared
// function, or a function literal or trailing block in it (Lit), with its
// signature. Written reports that its needs row is written in the source.
type MintFrame struct {
	Func    *Func
	Lit     syntax.Node
	Sig     *Signature
	Written bool
}

// Mint is one place where package code makes a new value of Type.
type Mint struct {
	Form  MintForm
	Type  Type
	Node  syntax.Node
	Place MintPlace
	// Name is the name of the declaration the mint is in.
	Name string
	// Frames are the functions around a MintInFunc mint, the declared
	// function first, then each closure inward.
	Frames []MintFrame
}

// Mints are every place in pkg's files where its code makes a new value,
// in source order.
func Mints(pkg *Package) []Mint {
	m := &minter{pkg: pkg}
	for _, f := range pkg.files {
		for _, d := range f.syntax.Decls {
			m.decl(d)
		}
	}
	return m.out
}

// minter is Mints's walk: out holds the mints found so far.
type minter struct {
	pkg *Package
	out []Mint
}

// at is the place a walk is in.
type at struct {
	place  MintPlace
	name   string
	frames []MintFrame
}

// decl walks one declaration. Each case names one kind of declaration.
func (m *minter) decl(d syntax.Decl) {
	switch d := d.(type) {
	case *syntax.ImportDecl:
		// An import makes no value.
	case *syntax.VarDecl:
		for _, s := range d.Specs {
			name := ""
			if len(s.Names) > 0 {
				name = s.Names[0].Name
			}
			m.walk(s, at{place: MintInLet, name: name})
		}
	case *syntax.TypeDecl:
		m.walk(d.Spec, at{place: MintInType, name: d.Name.Name})
	case *syntax.FuncDecl:
		if d.Body == nil {
			return // an extern: its Go body is not Ergo code
		}
		f, _ := m.pkg.Info.Defs[d.Name].(*Func)
		frame := MintFrame{Func: f, Written: d.Type != nil && d.Type.Needs != nil}
		if f != nil {
			frame.Sig = f.Sig
		}
		m.walk(d.Body, at{place: MintInFunc, name: d.Name.Name, frames: []MintFrame{frame}})
	case *syntax.ContextDecl:
		m.walk(d.Body, at{place: MintInContext, name: d.Name.Name})
	case *syntax.TestDecl:
		where := at{place: MintInTest, name: d.Name.Name}
		m.params(d.Params, where)
		m.walk(d.Body, where)
	case *syntax.LawDecl:
		where := at{place: MintInLaw, name: d.Name.Name}
		m.params(d.Params, where)
		m.walk(d.Body, where)
	}
}

// params records the values a test or law generates for its parameters.
func (m *minter) params(l *syntax.FieldList, where at) {
	if l == nil {
		return
	}
	for _, f := range l.List {
		for _, n := range f.Names {
			v, ok := m.pkg.Info.Defs[n].(*Var)
			if !ok || v.Type == nil {
				continue
			}
			for _, tn := range Mentions(v.Type, m.pkg) {
				m.add(MintGenerated, tn.Type, n, where)
			}
		}
	}
}

// walk visits every node under root, a closure under a new frame.
func (m *minter) walk(root syntax.Node, where at) {
	syntax.Inspect(root, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.VarSpec:
			m.zeroLet(n, where)
		case *syntax.LawDecl:
			// An operation type's law: its parameters are generated.
			law := at{place: MintInType, name: where.name}
			m.params(n.Params, law)
			m.walk(n.Body, law)
			return false
		case syntax.Expr:
			if _, ok := m.pkg.Info.Patterns[n]; ok {
				return false // a pattern takes a value apart
			}
			return m.expr(n, where)
		}
		return true
	})
}

// zeroLet records the zero value of a let with a type and no value.
func (m *minter) zeroLet(s *syntax.VarSpec, where at) {
	if len(s.Values) > 0 || s.Type == nil {
		return
	}
	for _, n := range s.Names {
		if v, ok := m.pkg.Info.Defs[n].(*Var); ok && v.Type != nil {
			for _, t := range zeroMade(v.Type) {
				m.add(MintZeroLet, t, n, where)
			}
		}
	}
}

func (m *minter) add(form MintForm, t Type, n syntax.Node, where at) {
	if t == nil || isInvalid(t) {
		return
	}
	m.out = append(m.out, Mint{Form: form, Type: t, Node: n, Place: where.place, Name: where.name, Frames: where.frames})
}

// expr records the mint x is, if any, and reports whether the walk goes on
// into x's children (a closure's are walked here, under its frame).
func (m *minter) expr(x syntax.Expr, where at) bool {
	t := m.pkg.Info.Types[x]
	form, more := mintForm(x, m.pkg.Info)
	switch form {
	case 0:
	case MintLiteral, MintUpdate:
		m.add(form, t, x, where)
		m.zeroFields(x.(*syntax.CompositeLit), t, where)
	case MintConversion:
		m.add(form, m.pkg.Info.Calls[x.(*syntax.CallExpr)].Type, x, where)
	default:
		m.add(form, t, x, where)
	}
	switch x := x.(type) {
	case *syntax.FuncLit:
		m.walk(x.Body, m.inside(where, x, x.Type != nil && x.Type.Needs != nil))
		return false
	case *syntax.CallExpr:
		if form != MintConversion {
			m.walk(x.Fun, where)
		}
		for _, a := range x.Args {
			m.walk(a, where)
		}
		if x.Trailing != nil {
			if sig := m.pkg.Info.Literals[x.Trailing]; sig != nil {
				m.add(MintFunc, sig, x.Trailing, where)
			}
			m.walk(x.Trailing, m.inside(where, x.Trailing, false))
		}
		return false
	}
	return more
}

// inside is where with one more closure frame, lit.
func (m *minter) inside(where at, lit syntax.Node, written bool) at {
	frames := append(append([]MintFrame(nil), where.frames...), MintFrame{Lit: lit, Sig: m.pkg.Info.Literals[lit], Written: written})
	return at{place: where.place, name: where.name, frames: frames}
}

// zeroFields records the zero values of the fields a record literal (not
// an update, which takes them from its base) omits.
func (m *minter) zeroFields(x *syntax.CompositeLit, t Type, where at) {
	s, ok := under(t).(*Struct)
	if !ok || x.Base != nil {
		return
	}
	set := map[string]bool{}
	for i, e := range x.Elts {
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			if id, ok := kv.Key.(*syntax.Ident); ok {
				set[id.Name] = true
			}
		} else if i < len(s.Fields) {
			set[s.Fields[i].Name] = true
		}
	}
	for _, f := range s.Fields {
		if !set[f.Name] {
			for _, z := range zeroMade(f.Type) {
				m.add(MintZeroField, z, x, where)
			}
		}
	}
}

// zeroMade are the types whose values the zero value of t makes: t itself
// when it is a defined type, and what its record fields and tuple elements
// hold by value, at any depth. A slice, a Map, a borrow and a function are
// empty or absent at zero, and a union and an operation type have no zero
// value (types.md, Zero values).
func zeroMade(t Type) []Type {
	var out []Type
	seen := map[Type]bool{}
	var walk func(Type)
	walk = func(t Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch u := t.(type) {
		case *Named:
			switch under(u).(type) {
			case *Union, *Interface:
				return
			}
			out = append(out, u)
			walk(under(u))
		case *Struct:
			for _, f := range u.Fields {
				walk(f.Type)
			}
		case *Tuple:
			for _, e := range u.Types {
				walk(e)
			}
		}
	}
	walk(t)
	return out
}

// mintForm is the form of mint the expression x is, or 0 when x makes no
// new value, and whether the walk goes on into x's children. Each case
// names one kind of expression; there is no default case.
func mintForm(x syntax.Expr, info *Info) (MintForm, bool) {
	t, typed := info.Types[x]
	_, constant := info.Values[x]
	switch x := x.(type) {
	case *syntax.Ident:
		// A record with no fields written bare is its own value; a
		// variable, function or parameter moves a value made elsewhere.
		if _, ok := info.Uses[x].(*TypeName); ok && typed && isZeroRecord(t) {
			return MintZeroRecord, true
		}
		if constant {
			return MintConstant, true
		}
		return 0, true
	case *syntax.SelectorExpr:
		// pkg.R: a record of another package with no fields; a field or
		// an operation moves a value.
		if typed && isZeroRecord(t) && namesType(x, info) {
			return MintZeroRecord, true
		}
		return 0, true
	case *syntax.IndexExpr:
		// R[A]: an instance of a generic record with no fields; an index
		// moves an element.
		if typed && isZeroRecord(t) && namesType(x.X, info) {
			return MintZeroRecord, true
		}
		return 0, true
	case *syntax.BasicLit:
		return MintConstant, true
	case *syntax.CompositeLit:
		if x.Base != nil {
			return MintUpdate, true
		}
		return MintLiteral, true
	case *syntax.FuncLit:
		return MintFunc, true
	case *syntax.CallExpr:
		// A conversion makes a value of its type; a call moves the value
		// its callee made.
		if c := info.Calls[x]; c != nil && c.Kind == CallConversion {
			return MintConversion, true
		}
		return 0, true
	case *syntax.TupleExpr:
		if typed {
			return MintTuple, true
		}
		return 0, true // a tuple type
	case *syntax.InterpolatedString:
		return MintString, true
	case *syntax.ParenExpr, *syntax.UnaryExpr, *syntax.BinaryExpr:
		// Arithmetic makes a value from its operands, which hold one
		// already; a constant expression is a literal.
		if constant {
			return MintConstant, true
		}
		return 0, true
	case *syntax.KeyValueExpr:
		return 0, true // a part of a composite literal
	case *syntax.SliceExpr:
		return 0, true // a part of a slice it moves
	case *syntax.StarExpr:
		return 0, true // a borrow's element or a pointer type
	case *syntax.TypeGuard:
		return 0, true // a match's subject
	case *syntax.TryExpr:
		return 0, true // moves its operand's value
	case *syntax.CatchExpr:
		return 0, true // moves its operand's or an arm's value
	case *syntax.ContextLit:
		return 0, true // a context: its entries' values are walked
	case *syntax.IfStmt:
		return 0, true // moves a branch's value
	case *syntax.SwitchStmt:
		return 0, true // moves an arm's value
	case *syntax.TypeSwitchStmt:
		return 0, true // moves an arm's value
	case *syntax.AsPattern:
		return 0, false // a pattern
	case *syntax.ArrayType:
		return 0, true // a type: an element type is walked for nothing
	case *syntax.MapType:
		return 0, true // a type
	case *syntax.FuncType:
		return 0, false // a type
	case *syntax.StructType:
		return 0, false // a type
	case *syntax.InterfaceType:
		return 0, true // a type; its laws are walked
	case *syntax.ErrorRow:
		return 0, false // a type
	case *syntax.MachineType:
		return 0, true // a type; its initial state is walked
	case *syntax.VariantSpec:
		return 0, false // a type
	}
	return 0, true // unreachable while every kind has a case (TestMintFormNamesEveryKind)
}

// namesType reports whether x names a type: a name, or a name qualified
// by a package (Info records no use of the selected name, so the package
// decides: a field or an operation is selected from a value).
func namesType(x syntax.Expr, info *Info) bool {
	switch x := x.(type) {
	case *syntax.Ident:
		_, ok := info.Uses[x].(*TypeName)
		return ok
	case *syntax.SelectorExpr:
		id, ok := x.X.(*syntax.Ident)
		if !ok {
			return false
		}
		_, ok = info.Uses[id].(*PkgName)
		return ok
	}
	return false
}
