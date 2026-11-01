package check

import (
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Types by member shape (#196, fifth round; spec D-B1): fields make a
// record, operations make an operation type, `=` makes a union and
// `type N T` a defined type. An inline variant `Name{…}` of a union
// declares a record in the package scope, usable as a type anywhere in the
// package; a bare-name variant names a record declared in this package.
//
// Prior art: Gleam custom types and ReScript variants declare constructors
// inside the type, but those constructors are not types; Ergo's variants
// are record types so rows can share them (Koka, Effekt). MoonBit's enum
// constructors are not types either; Go has no unions.

// declareVariant declares the record an inline variant of the union d
// declares. Its declaration is one the checker builds: the variant's name,
// the union's type parameters its fields mention in the union's order
// (Some[T], None; Enter[S], Done[O]), and a record over the variant's
// fields. The record is a TypeName of the package like any other, listed
// after its union.
func (c *checker) declareVariant(fi *fileInfo, d *syntax.TypeDecl, vs *syntax.VariantSpec) {
	params := c.variantParams(d, vs)
	if vs.TypeArgs != nil && !sameParamNames(vs.TypeArgs, params) {
		want := vs.Name.Name + "{…}"
		if params != nil {
			want = vs.Name.Name + "[" + strings.Join(paramNames(params), ", ") + "]{…}"
		}
		c.errorf(vs.TypeArgs, CodeType, "an inline variant takes the union's type parameters its fields mention, in the union's order: write %s (types.md, Unions 1)", want)
	}
	decl := &syntax.TypeDecl{Type: vs.Pos(), Name: vs.Name, TypeParams: params, Spec: &syntax.StructType{Struct: syntax.NoPos, Fields: vs.Fields}}
	tn := &TypeName{Name: vs.Name.Name, Pkg: c.pkg, Pos: vs.Name.Pos(), Decl: decl, file: fi, id: newID()}
	tn.Type = &Named{Obj: tn}
	if c.declare(vs.Name, tn) {
		c.pkg.Types = append(c.pkg.Types, tn)
		c.variants[vs] = tn
	}
}

// variantParams returns the bracket list of an inline variant: the union's
// type parameters that a field type of the variant mentions, in the
// union's order, each a new identifier at the union's position (not in the
// tree, so Info.Defs does not list it), with the union's constraint.
func (c *checker) variantParams(d *syntax.TypeDecl, vs *syntax.VariantSpec) *syntax.FieldList {
	if d.TypeParams == nil || vs.Fields == nil {
		return nil
	}
	used := map[string]bool{}
	for _, f := range vs.Fields.List {
		if f.Type != nil {
			walkTypePositions(f.Type, KindType, func(id *syntax.Ident, _ Kind) { used[id.Name] = true })
		}
	}
	var list []*syntax.Field
	for _, f := range d.TypeParams.List {
		var names []*syntax.Ident
		for _, n := range f.Names {
			if used[n.Name] {
				id := &syntax.Ident{NamePos: n.NamePos, Name: n.Name}
				c.synthetic[id] = true
				names = append(names, id)
			}
		}
		if len(names) > 0 {
			list = append(list, &syntax.Field{Names: names, Type: f.Type})
		}
	}
	if len(list) == 0 {
		return nil
	}
	return &syntax.FieldList{Opening: d.TypeParams.Opening, List: list, Closing: d.TypeParams.Closing}
}

func paramNames(l *syntax.FieldList) []string {
	var out []string
	if l == nil {
		return nil
	}
	for _, f := range l.List {
		for _, n := range f.Names {
			out = append(out, n.Name)
		}
	}
	return out
}

// sameParamNames reports whether written type arguments `Name[A, B]{…}`
// are exactly the parameters the variant takes, in order.
func sameParamNames(written, params *syntax.FieldList) bool {
	want := paramNames(params)
	if len(written.List) != len(want) {
		return false
	}
	for i, f := range written.List {
		id, ok := f.Type.(*syntax.Ident)
		if !ok || id.Name != want[i] {
			return false
		}
	}
	return true
}

// variantType is the type an inline variant stands for in its union's
// member list: the variant's record, instantiated with the union's own
// type parameters of the same names.
func (c *checker) variantType(vs *syntax.VariantSpec, ctx *typeCtx) Type {
	tn := c.variants[vs]
	if tn == nil {
		return invalid // its name was declared twice (reported)
	}
	n := tn.Type.(*Named)
	c.resolveNamed(n)
	if len(n.TParams) == 0 {
		return n
	}
	targs := make([]Type, len(n.TParams))
	for i, tp := range n.TParams {
		var arg *TypeParam
		if ctx != nil {
			arg = ctx.params[tp.Name]
		}
		if arg == nil {
			return invalid
		}
		targs[i] = arg
	}
	return c.instantiate(n, targs)
}

// definedType resolves the body of a defined type `type N T` (spec D-B1):
// N is a new nominal type with T's body, so T's operators and literals
// apply to it, and N converts to and from T (D-B7). T is a basic, slice,
// Map, function, tuple or record type (types.md, Defined types 1); a
// defined type over a type parameter, over itself, or over any other type
// (an operation type, a union, a machine, a pointer) is S108, and the type
// is invalid, so its uses add no diagnostic.
//
// Prior art: Gleam, ReScript and MoonBit have no newtype over a trait or a
// variant; Go has defined interface types, which Ergo leaves out so an
// operation type keeps one name (spec, ruling of the D3a rewrite, item 2).
func (c *checker) definedType(n *Named, spec syntax.Expr, ctx *typeCtx) Type {
	base := c.typExpr(spec, ctx)
	if isInvalid(base) {
		return invalid
	}
	switch b := base.(type) {
	case *TypeParam:
		c.errorf(spec, CodeType, "a defined type is defined over a type, not the type parameter %s (types.md, Defined types)", b.Name)
		return invalid
	case *Named:
		if o := b.origin(); o.state == 1 {
			c.errorf(spec, CodeType, "%s is defined over itself (types.md, Defined types)", n.Obj.Name)
			return invalid
		}
	}
	if noun := baseNoun(under(base)); noun != "" {
		c.errorf(spec, CodeType, "a defined type is over a basic, slice, Map, function, tuple or record type, and %s is %s; use %s directly (types.md, Defined types)", c.localTypeText(base), noun, c.localTypeText(base))
		return invalid
	}
	if c.taskRefused(spec, base) {
		// A record instance over a Task (type B Box[Task[int, F]]).
		return invalid
	}
	n.def = base
	return under(base)
}

// baseNoun names the kind of u, the underlying type of a defined type's
// base, when a defined type may not be over it, and is "" when it may.
func baseNoun(u Type) string {
	switch u.(type) {
	case *Basic, *Slice, *Map, *Signature, *Tuple, *Struct:
		return ""
	case *Interface:
		return "an operation type"
	case *Union:
		return "a union"
	case *MachineType:
		return "a machine"
	case *Pointer:
		return "a pointer type"
	}
	return "not one of them"
}

// shapeNoun names a declaration's kind by its members, for a message.
func shapeNoun(spec syntax.Expr) string {
	switch s := spec.(type) {
	case *syntax.StructType:
		return "a record"
	case *syntax.InterfaceType:
		if len(s.Union) > 0 {
			return "a union"
		}
		return "an operation type"
	case *syntax.MachineType:
		return "a machine"
	}
	return "a defined type"
}

// Underlying returns a declared type's body: a record (*Struct), an
// operation type (*Interface), a union (*Union) or a machine; for a
// defined type, the body of the type it is defined over (a *Basic, a
// *Tuple, a *Slice, …). An instance's body has its type arguments
// substituted.
func (n *Named) Underlying() Type { return n.underlying() }

// Defined returns the type a defined type `type N T` is defined over, T,
// with an instance's type arguments substituted; nil for a record, an
// operation type, a union or a machine.
func (n *Named) Defined() Type {
	if n.Orig == nil {
		return n.def
	}
	if n.Orig.def == nil || n.ctx == nil {
		return nil
	}
	return n.ctx.subst(n.Orig.def, paramMap(n.Orig.TParams, n.TArgs))
}

// isZeroRecord reports whether t is a record with no fields (D-B2): a
// value of it is written bare, `Pending`, never `Pending{}`.
func isZeroRecord(t Type) bool {
	n, ok := t.(*Named)
	if !ok || n.Orig == nil && len(n.TParams) > 0 {
		return false
	}
	if n.origin().def != nil {
		return false // a defined type over a record keeps its braces
	}
	s, ok := n.underlying().(*Struct)
	return ok && len(s.Fields) == 0
}

// isRecordName reports whether t is a declared record (not a defined type
// over one).
func isRecordName(t Type) bool {
	n, ok := t.(*Named)
	if !ok || n.origin().def != nil {
		return false
	}
	_, ok = n.underlying().(*Struct)
	return ok
}
