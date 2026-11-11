package check

import (
	"fmt"
	"math/big"

	"ergo.local/prototype/internal/sketch/syntax"
)

// ---------------------------------------------------------------- substitution

// subst replaces type parameters by their bindings. A row parameter's
// binding is a RowArg, flattened into the row it occurs in; a result-position
// parameter bound to no value drops out of the result list (Q3).
func (c *checker) subst(t Type, m map[*TypeParam]Type) Type {
	if len(m) == 0 || t == nil {
		return t
	}
	switch t := t.(type) {
	case *TypeParam:
		if b, ok := m[t]; ok && b != nil {
			return b
		}
		return t
	case *Named:
		if len(t.TArgs) == 0 {
			return t
		}
		args := make([]Type, len(t.TArgs))
		changed := false
		for i, a := range t.TArgs {
			args[i] = c.subst(a, m)
			if args[i] != a {
				changed = true
			}
		}
		if !changed {
			return t
		}
		return c.instantiate(t.origin(), args)
	case *RowArg:
		return &RowArg{Row: c.substRow(t.Row, m)}
	case *Wired:
		// wired[T] at T's argument is the argument's wire row, which
		// flattens into the row it occurs in (Row.with).
		b, ok := m[t.T]
		if !ok || b == nil {
			return t
		}
		return &RowArg{Row: c.wireRow(b)}
	case *RowVar:
		// A use of a generic function's inferred row: the substitution is
		// recorded on the variable and applied to its solution when the row
		// is read (round 12 M1), composed with one recorded before.
		root := t.root()
		var tps []*TypeParam
		if root.Fn != nil && root.Fn.Sig != nil {
			tps = root.Fn.Sig.TParams
		}
		var nm map[*TypeParam]Type
		changed := false
		for _, tp := range tps {
			cur := Type(tp)
			if b, ok := t.m[tp]; ok {
				cur = b
			}
			nb := c.subst(cur, m)
			changed = changed || nb != cur
			if nb != Type(tp) {
				if nm == nil {
					nm = map[*TypeParam]Type{}
				}
				nm[tp] = nb
			}
		}
		if !changed {
			return t
		}
		if nm == nil {
			return root
		}
		return &RowVar{of: root, m: nm, ctx: c}
	case *Struct:
		s := &Struct{Fields: make([]*Field, len(t.Fields)), obj: t.obj}
		for i, f := range t.Fields {
			s.Fields[i] = &Field{Name: f.Name, Type: c.subst(f.Type, m), Pos: f.Pos, origin: f.Origin()}
		}
		return s
	case *Interface:
		it := &Interface{Set: t.Set, Methods: make([]*Method, len(t.Methods)), obj: t.obj}
		newOf := make(map[*Method]*Method, len(t.Methods))
		for i, mt := range t.Methods {
			it.Methods[i] = &Method{Name: mt.Name, Sig: c.subst(mt.Sig, m).(*Signature), Pos: mt.Pos, pkg: mt.pkg}
			newOf[mt] = it.Methods[i]
		}
		// The order sorted at resolution carries over (no sort here).
		for _, mt := range t.byName {
			it.byName = append(it.byName, newOf[mt])
		}
		return it
	case *Union:
		if t.Row != nil {
			// A row value's members are a row: a row parameter's binding
			// (a RowArg) flattens into it, as in substRow (q47).
			return c.rowValue(c.substRow(t.Row, m))
		}
		u := &Union{Variants: make([]Type, len(t.Variants)), Bad: t.Bad}
		for i, v := range t.Variants {
			u.Variants[i] = c.subst(v, m)
		}
		return u
	case *MachineType:
		return &MachineType{State: c.subst(t.State, m), Signal: c.subst(t.Signal, m)}
	case *Slice:
		return &Slice{Elem: c.subst(t.Elem, m)}
	case *Array:
		return &Array{Len: t.Len, Elem: c.subst(t.Elem, m)}
	case *Map:
		return &Map{Key: c.subst(t.Key, m), Elem: c.subst(t.Elem, m)}
	case *Pointer:
		return &Pointer{Elem: c.subst(t.Elem, m)}
	case *Signature:
		s := &Signature{Fallible: t.Fallible, NonEscaping: t.NonEscaping, Takes: t.Takes, TakesSelf: t.TakesSelf}
		for _, p := range t.Params {
			s.Params = append(s.Params, &Var{Name: p.Name, Type: c.subst(p.Type, m), Pos: p.Pos, id: p.id, param: true})
		}
		for _, r := range t.Results {
			r2 := c.subst(r, m)
			if r2 == NoValue {
				continue
			}
			s.Results = append(s.Results, r2)
		}
		s.Errors = c.substRow(t.Errors, m)
		s.Uses = c.substRow(t.Uses, m)
		// Type parameters that stay unbound remain the signature's own.
		for _, tp := range t.TParams {
			if _, bound := m[tp]; !bound {
				s.TParams = append(s.TParams, tp)
			}
		}
		return s
	case *Tuple:
		out := &Tuple{}
		for _, x := range t.Types {
			out.Types = append(out.Types, c.subst(x, m))
		}
		return out
	}
	return t
}

func (c *checker) substRow(r *Row, m map[*TypeParam]Type) *Row {
	if r.Empty() {
		return newRow()
	}
	out := newRow()
	for _, e := range r.Elems() {
		out = out.with(c.types, c.subst(e, m))
	}
	return out
}

// ---------------------------------------------------------------- identity

// identical reports type identity: equal interned ids (intern.go). The ids
// follow Go's identity, minus what Ergo removes: a named type by its origin
// and identical type arguments; a struct by field names and types in order;
// an interface by method names and identical signatures; a signature by
// identical parameters and results and equal rows; a row argument by its
// set; a union and a machine only to themselves.
func (c *checker) identical(a, b Type) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return c.types.id(a) == c.types.id(b)
}

// sameShape reports whether two signatures have identical parameters and
// results, where an unknown row may be identical to another row
// (mayBeIdentical).
func (c *checker) sameShape(a, b *Signature) bool {
	if len(a.Params) != len(b.Params) || len(a.Results) != len(b.Results) {
		return false
	}
	for i := range a.Params {
		if !c.mayBeIdentical(a.Params[i].Type, b.Params[i].Type) {
			return false
		}
	}
	for i := range a.Results {
		if !c.mayBeIdentical(a.Results[i], b.Results[i]) {
			return false
		}
	}
	return true
}

// mayBeIdentical is identical, except that a function type nested in a
// parameter or a result may hold an unknown row (Row.unknown): such a type
// is identical to another when some content of the unknown row makes it so,
// so a nested unknown row gives no follow-on "the parameters or results
// differ" (stage D1 round 5). With no unknown row it is identical: a
// signature's identity is its parameters, results and rows (intern.go).
func (c *checker) mayBeIdentical(a, b Type) bool {
	if c.identical(a, b) {
		return true
	}
	as, ok := a.(*Signature)
	bs, ok2 := b.(*Signature)
	if !ok || !ok2 || len(as.TParams) > 0 || len(bs.TParams) > 0 || as.NonEscaping != bs.NonEscaping {
		return false
	}
	return c.sameShape(as, bs) && c.takesEffect(as, nil) == c.takesEffect(bs, nil) && c.mayBeEqualRow(as.Errors, bs.Errors) && c.mayBeEqualRow(as.Uses, bs.Uses)
}

// mayBeEqualRow reports whether two rows are equal for some content of their
// unknown parts: an unknown row holds its known part and may hold more.
func (c *checker) mayBeEqualRow(a, b *Row) bool {
	au, bu := a.unknown(c.types), b.unknown(c.types)
	switch {
	case au && bu:
		return true
	case au:
		return a.known(c.types).subsetOf(c.types, b)
	case bu:
		return b.known(c.types).subsetOf(c.types, a)
	}
	return c.identical(&RowArg{Row: a}, &RowArg{Row: b})
}

// ---------------------------------------------------------------- assignability

// assignClause names the clause of assignability that admits a value
// (types.md, Assignability).
type assignClause int

const (
	assignRefused    assignClause = iota
	assignIdentical               // 1: identical types
	assignUnderlying              // 2: identical underlying types, one side unnamed
	assignOperations              // 3: into an operation type the value implements
	assignUnion                   // 4: a variant or a row value into its union
	assignSignature               // 5: a function value into a function type with wider rows
	assignMachine                 // 6: a machine declaration's value into the prelude Machine
	assignRowValue                // 7: a row's member or a narrower row value into a row value
)

// assignment is the result of assignability: the clause that admitted the
// value and the implicit conversion the assignment makes, the Info.Implicit
// fact (Kind 0 when it makes none).
type assignment struct {
	clause assignClause
	conv   Conversion
	// sealed is set on a refusal whose target is a sealed std protocol
	// (C28, extern.go sealedStd), with from the source type: the admitting
	// site reports it through sealedRefused.
	sealed *Named
	from   Type
}

func (a assignment) ok() bool { return a.clause != assignRefused }

// assignability decides whether a value of type v may be assigned to type
// t, and with which implicit conversion; when it may not, the reason, ""
// when there is no more to say than the two types. It is the one relation
// every admitting site reads, and the conversion it returns is the fact
// that site records for the lowering, so the lowering never derives it
// again. The clauses follow Go's order (types.md, Assignability):
//  1. identical types: no conversion;
//  2. identical underlying types, at least one side unnamed (a Named, a
//     basic type and a type parameter have a name), neither a type
//     parameter: no conversion;
//  3. t's underlying type is an operation type and v implements it:
//     ConvOperations with its table, none when v is an operation type;
//     refused (sealed) when t is a sealed std protocol and v is not one
//     (C28: only the runtime implements those; #206);
//  4. a variant or a row value into a union: ConvVariant or ConvRow;
//  5. a function value into a function type whose rows are supersets, at
//     least one side unnamed: no conversion (every function value has one
//     Go shape, spec D5);
//  6. a machine declaration's value into the prelude Machine instance of
//     its state, signal and step: ConvMachine (README Gap 46). It is tried
//     after clause 2, since neither side is an operation type, a union or
//     a function type.
//
// Prior art: go/types operand.assignableTo has clauses 1 to 3 in this
// order; cmd/compile typecheck.assignOp returns the lowering op (OCONVNOP,
// OCONVIFACE) with the reason from the same clauses, as this returns the
// Conversion.
func (c *checker) assignability(v, t Type) (assignment, string) {
	// A type with an invalid part was reported where it came from (D3).
	if containsInvalid(v) || containsInvalid(t) || c.identical(v, t) {
		return assignment{clause: assignIdentical}, ""
	}
	if v == NoValue {
		return assignment{}, "it has no value"
	}
	if _, borrow := v.(*Pointer); borrow && refElem(t) != nil {
		// A borrow lives no longer than its call (types.md, Borrows); a
		// go.Ref is a Go pointer a Go function returned (q38).
		return assignment{}, "a borrow is not kept as a go.Ref"
	}
	if c.sameUnderlying(v, t) {
		return assignment{clause: assignUnderlying}, ""
	}
	if view, step := c.machineValue(v); view != nil && isPreludeMachineValue(t) {
		// A machine declaration's value is a Machine of its state, signal
		// and step (README Gap 46), or an InputMachine when its initial is
		// a function of an input (V4): ConvMachine.
		input := !isPreludeMachine(view)
		if input != !isPreludeMachine(t) {
			if input {
				return assignment{}, fmt.Sprintf("its initial is a function of an input, so it is an InputMachine[%s, …]; machines.Start(value, input) gives a Machine", view.TArgs[0])
			}
			return assignment{}, "its initial is a value, so it is a Machine, not an InputMachine"
		}
		if !c.identical(view, t) {
			k := len(view.TArgs) - 4
			o, f, r, w := view.TArgs[k], view.TArgs[k+1].(*RowArg), view.TArgs[k+2].(*RowArg), view.TArgs[k+3].(*RowArg)
			if input {
				return assignment{}, fmt.Sprintf("its start takes %s and its step %s gives an InputMachine with output %s, error row %s, capability row %s and work row %s", view.TArgs[0], step.Name, o, rowText(f.Row), rowText(r.Row), rowText(w.Row))
			}
			return assignment{}, fmt.Sprintf("its step %s gives a Machine with output %s, error row %s, capability row %s and work row %s", step.Name, o, rowText(f.Row), rowText(r.Row), rowText(w.Row))
		}
		table := map[string]*Func{"Step": step}
		if input {
			table["Start"] = machineStart(v.(*Named).Obj.Pkg, v.(*Named).Obj.Decl.Spec.(*syntax.MachineType))
		}
		if after := c.machineAfter(v); after != nil {
			table["After"] = after // else every state has no timer
		}
		if work, _ := c.machineWork(v); work != nil {
			table["Work"] = work // else every state has no work
		}
		return assignment{clause: assignMachine, conv: Conversion{Kind: ConvMachine, From: v, To: t, Table: table}}, ""
	}
	if it := asInterface(t); it != nil {
		if _, isTuple := under(v).(*Tuple); isTuple {
			return assignment{}, "a tuple has no operations"
		}
		if it.Set != SetAll {
			return assignment{}, fmt.Sprintf("%s is a constraint only", it.Set)
		}
		if why := c.implements(v, it); why != "" {
			return assignment{}, why
		}
		// Only a value that implements the operation type can reach
		// the ownership guard. A missing operation is the more specific
		// failure (cmd/compile assignOp's diagnostic order).
		if !c.providing && !c.hostLendArg(t) && c.holdsResource(v) && !c.releasable(t) {
			// R22: the operation value would never be released.
			return assignment{}, resourceIntoCrossingText(v, t)
		}
		if s := sealedStd(t); s != nil && sealedStd(v) == nil {
			// A program value, or a program operation type's value, never
			// becomes a sealed std protocol's value (C28).
			return assignment{sealed: s, from: v}, sealedText(v, s)
		}
		if asInterface(v) != nil {
			if n := sealedStd(v); n != nil && sealedStd(t) == nil && !isHostNamed(t) {
				// The runtime's own type stands for a sealed std protocol,
				// with the runtime's Go shapes, not those of the target's
				// interface: the lowering builds the operations from the
				// std ones (lower/sealedops.go), for every pair this
				// member match admits.
				return assignment{clause: assignOperations, conv: Conversion{Kind: ConvOperations, From: v, To: t}}, ""
			}
			// An operation type's value is already an interface value.
			return assignment{clause: assignOperations}, ""
		}
		table := map[string]*Func{}
		instances := map[string]*Instance{}
		for _, m := range it.Methods {
			if _, fn := c.methodOf(v, m.Name); fn != nil {
				table[m.Name] = fn
				if len(fn.Sig.TParams) > 0 {
					inst, why := c.witnessInstance(fn, receiverTypeArgs(v, fn))
					if why != "" {
						return assignment{}, why
					}
					instances[m.Name] = inst
				}
			}
		}
		return assignment{clause: assignOperations, conv: Conversion{Kind: ConvOperations, From: v, To: t, Table: table, Instances: instances}}, ""
	}
	if rt, ok := t.(*Union); ok && rt.Row != nil {
		// Clause 7 (q47): into a row value goes a value of one of its
		// members, or a row value whose members it all holds, as a failure
		// of those types goes into the row (rows.md, Error rows 5). Both
		// sides are a Go error, so it converts nothing.
		if rv, isRow := v.(*Union); isRow && rv.Row != nil {
			if !rv.Row.subsetOf(c.types, rt.Row) {
				return assignment{}, fmt.Sprintf("it may hold an error type that %s does not list", t)
			}
			return assignment{clause: assignRowValue}, ""
		}
		if !rt.Row.has(c.types, v) {
			return assignment{}, fmt.Sprintf("%s is not one of its error types", v)
		}
		return assignment{clause: assignRowValue}, ""
	}
	if u := asUnion(t); u != nil {
		if _, isNamed := t.(*Named); isNamed {
			if rv, isRow := v.(*Union); isRow {
				// A row value goes into a declared union that lists each of
				// its error types (D-C3, ConvRow).
				for _, e := range rv.Variants {
					if !c.hasVariant(u, e) {
						return assignment{}, fmt.Sprintf("%s is not a variant of %s", e, t)
					}
				}
				return assignment{clause: assignUnion, conv: Conversion{Kind: ConvRow, From: v, To: t}}, ""
			}
			for _, variant := range u.Variants {
				if c.identical(variant, v) {
					return assignment{clause: assignUnion, conv: Conversion{Kind: ConvVariant, From: v, To: t}}, ""
				}
			}
			return assignment{}, fmt.Sprintf("%s is not a variant of %s", v, t)
		}
	}
	vs, ok := under(v).(*Signature)
	ts, ok2 := under(t).(*Signature)
	if !ok || !ok2 {
		return assignment{}, ""
	}
	if hasName(v) && hasName(t) {
		// Two named function types cross only by conversion (D-B7).
		return assignment{}, ""
	}
	if !c.sameShape(vs, ts) {
		return assignment{}, "the parameters or results differ"
	}
	if c.takesEffect(vs, nil) != c.takesEffect(ts, nil) {
		// Gap 39: a caller of t gives up exactly what t's clause names.
		return assignment{}, fmt.Sprintf("its takes clause (%s) is not %s's (%s) (resources R19, Gap 39)", takesText(vs), t, takesText(ts))
	}
	// An unknown source row is checked by its known part, and an unknown
	// destination row accepts any row, as an invalid type does both ways
	// (Row.unknown; stage D1 round 4 review M2). An inferred receiver keeps
	// the source's known part (widenUnknown).
	if ve := vs.Errors.known(c.types); !ts.Errors.unknown(c.types) && !ve.subsetOf(c.types, ts.Errors) {
		return assignment{}, fmt.Sprintf("its error row %s is wider than %s", rowText(ve), rowText(ts.Errors))
	}
	if vu := vs.Uses.known(c.types); !ts.Uses.unknown(c.types) && !vu.subsetOf(c.types, ts.Uses) {
		return assignment{}, fmt.Sprintf("its capability row %s is wider than %s", rowText(vu), rowText(ts.Uses))
	}
	return assignment{clause: assignSignature}, ""
}

// hasName reports a type with a name for assignability (go/types hasName):
// a declared type, a predeclared basic type, or a type parameter.
func hasName(t Type) bool {
	switch t.(type) {
	case *Named, *Basic, *TypeParam:
		return true
	}
	return false
}

// sameUnderlying is assignability's clause 2: v and t have identical
// underlying types, at least one of them is unnamed, and neither is a type
// parameter. A row value is never the unnamed side: it lowers to a Go error,
// not to its union's interface, so it goes in by clause 4 (ConvRow).
func (c *checker) sameUnderlying(v, t Type) bool {
	if hasName(v) && hasName(t) || isRowValue(v) || isRowValue(t) {
		return false
	}
	_, vp := v.(*TypeParam)
	_, tp := t.(*TypeParam)
	if vp || tp {
		return false
	}
	return c.identical(under(v), under(t))
}

// widenUnknown returns dst, the type an inferred receiver holds (the first
// branch of a join, the binding of a type parameter), with the known part of
// src's rows joined into each of dst's unknown rows, in dst and in its
// results. An unknown row accepts any row (assignable), so without the join
// the failures src is known to have would be lost downstream (stage D1
// round 4 review M2). Koka and OCaml check the known labels of an open row
// and let the tail absorb the rest; Elixir checks the static part of
// dynamic() and t. It is dst itself when no row changes.
func (c *checker) widenUnknown(dst, src Type) Type {
	d, ok := dst.(*Signature)
	s, ok2 := src.(*Signature)
	if !ok || !ok2 || len(d.TParams) > 0 || len(s.TParams) > 0 {
		return dst
	}
	out, changed := *d, false
	if d.Errors.unknown(c.types) {
		if e := d.Errors.union(c.types, s.Errors.known(c.types)); !e.equal(c.types, d.Errors) {
			out.Errors, changed = e, true
		}
	}
	if d.Uses.unknown(c.types) {
		if u := d.Uses.union(c.types, s.Uses.known(c.types)); !u.equal(c.types, d.Uses) {
			out.Uses, changed = u, true
		}
	}
	if len(d.Results) == len(s.Results) {
		var rs []Type
		for i, r := range d.Results {
			if w := c.widenUnknown(r, s.Results[i]); w != r {
				if rs == nil {
					rs = append([]Type(nil), d.Results...)
				}
				rs[i] = w
			}
		}
		if rs != nil {
			out.Results, changed = rs, true
		}
	}
	if !changed {
		return dst
	}
	out.Fallible = d.Fallible || s.Fallible
	return &out
}

// rowText prints a row in a message as a set, its names spelled as the
// source spells a row: `{A | B}` (196n).
func rowText(r *Row) string {
	if r.Empty() {
		return "{}"
	}
	return "{" + r.String() + "}"
}

// methodSet returns the method of t named name and its signature, for a
// value of type t: a named type's declared methods, or an interface's.
func (c *checker) methodOf(t Type, name string) (*Signature, *Func) {
	switch t := t.(type) {
	case *Named:
		if f := t.method(name); f != nil {
			if f.owner != nil {
				return c.selfSig(t, f), f
			}
			sig := c.funcSig(f)
			if t.Orig != nil {
				m := map[*TypeParam]Type{}
				for i, tp := range t.Orig.TParams {
					if i < len(t.TArgs) {
						m[tp] = t.TArgs[i]
					}
				}
				sig = c.subst(sig, m).(*Signature)
			}
			return sig, f
		}
		if it, ok := t.underlying().(*Interface); ok {
			if m := it.method(name); m != nil {
				return m.Sig, nil
			}
		}
	case *Interface:
		if m := t.method(name); m != nil {
			return m.Sig, nil
		}
	case *Pointer:
		return c.methodOf(t.Elem, name)
	case *TypeParam:
		if it := asInterface(t.Constraint); it != nil {
			if m := it.method(name); m != nil {
				return m.Sig, nil
			}
		}
	}
	return nil, nil
}

// selfSig is the signature x.f(…) calls for a value of t (196m): f's
// without its first parameter, which x fills. On an instance of a generic
// t, the type parameters the first parameter names take t's type
// arguments; f's other type parameters stay the signature's own. A
// prelude function on a slice or a Map binds them from the element, key
// and value types (D-D8).
func (c *checker) selfSig(t Type, f *Func) *Signature {
	sig := c.funcSig(f)
	if len(sig.Params) == 0 {
		return sig
	}
	m := selfBinding(t, f)
	out := dropSelf(sig)
	if len(m) == 0 {
		return out
	}
	nb := 0
	for i, tp := range sig.TParams {
		if _, bound := m[tp]; !bound && i < sig.NBracket {
			nb++
		}
	}
	s := c.subst(out, m).(*Signature)
	s.NBracket = nb
	return s
}

// ownTypeParam returns the first type parameter of the 196m function f
// that its first parameter does not bind from t's type arguments, or nil.
func ownTypeParam(t *Named, f *Func) *TypeParam {
	if f.Sig == nil {
		return nil
	}
	bound := selfBinding(t, f)
	for _, tp := range f.Sig.TParams {
		if _, ok := bound[tp]; !ok {
			return tp
		}
	}
	return nil
}

// selfBinding maps the type parameters of f that its first parameter's
// type arguments name to t's type arguments, by position.
func selfBinding(t Type, f *Func) map[*TypeParam]Type {
	m := map[*TypeParam]Type{}
	targs := selfTArgs(t)
	for i, a := range f.selfArgs {
		id, _ := a.(*syntax.Ident)
		if id == nil || i >= len(targs) || f.Sig == nil {
			continue
		}
		for _, tp := range f.Sig.TParams {
			if tp.Name == id.Name {
				m[tp] = targs[i]
			}
		}
	}
	return m
}

// selfTArgs are the type arguments x's type t gives the first parameter of
// a function x.f(…) reaches: a named type's, a slice's element type, or a
// Map's key and value types (D-D8).
func selfTArgs(t Type) []Type {
	switch t := t.(type) {
	case *Named:
		return t.TArgs
	case *Slice:
		return []Type{t.Elem}
	case *Map:
		return []Type{t.Key, t.Elem}
	}
	return nil
}

// fullTypeArgs are the type arguments of every type parameter of f, in
// f's order: those x of x.f(…) binds (self) and those the call wrote or
// inferred (rest), the Info.Calls TypeArgs fact (D-D8).
func fullTypeArgs(f *Func, self, rest map[*TypeParam]Type) []Type {
	targs := make([]Type, len(f.Sig.TParams))
	for i, tp := range f.Sig.TParams {
		if b, ok := self[tp]; ok {
			targs[i] = b
		} else {
			targs[i] = rest[tp]
		}
	}
	return targs
}

// sigHasInvalid reports whether a parameter or result type of sig is
// invalid or holds an invalid part.
func sigHasInvalid(sig *Signature) bool {
	for _, p := range sig.Params {
		if containsInvalid(p.Type) {
			return true
		}
	}
	for _, r := range sig.Results {
		if containsInvalid(r) {
			return true
		}
	}
	return false
}

// shapeOf is sig's parameters and results without its rows.
func shapeOf(sig *Signature) *Signature {
	return &Signature{Params: sig.Params, Results: sig.Results, Errors: newRow(), Uses: newRow()}
}

// dropSelf is sig without its first parameter, which x of x.f(…) fills.
func dropSelf(sig *Signature) *Signature {
	out := *sig
	out.Params = sig.Params[1:]
	if len(sig.Takes) > 0 {
		// The first parameter is the value the call is on: a takes of it
		// is the bare takes of an operation.
		out.Takes = nil
		for _, i := range sig.Takes {
			if i == 0 {
				out.TakesSelf = true
			} else {
				out.Takes = append(out.Takes, i-1)
			}
		}
	}
	return &out
}

// fnPkg is fn's package, or nil for no function.
func fnPkg(fn *Func) *Package {
	if fn == nil {
		return nil
	}
	return fn.Pkg
}

// implements checks method-row compatibility: for every method of it, v has
// a method with identical parameters and results, the same fallibility, and
// error and capability rows that are subsets (types.md, Interface
// assignability 1; rows.md, Context 2). Each lookup is a keyed map access, so
// the check is linear in the method count (#122).
func (c *checker) implements(v Type, it *Interface) string {
	// The method set of *T holds the *T-receiver methods too, as in Go
	// (N1); T's holds only the value-receiver ones.
	ptr := false
	if p, ok := v.(*Pointer); ok {
		v, ptr = p.Elem, true
	}
	// go.Ref[T] is Go's *T: a Go value implements a Go interface by Go's
	// method sets (docs/spec/interop.md, Checking).
	if e := refElem(v); e != nil {
		v, ptr = e, true
	}
	if s := ScriptOf(v); s != nil && !ptr {
		// A Script has the operations of its capability (script.go, rule 4).
		if why := c.implements(s, it); why != "" {
			return fmt.Sprintf("%s: %s", v, why)
		}
		return ""
	}
	for _, m := range it.Methods {
		got, fn := c.methodOf(v, m.Name)
		if got == nil {
			return fmt.Sprintf("%s has no method %s", v, m.Name)
		}
		if h := hostOf(fnPkg(fn)); h != nil && h.recvPtr[fn] && !ptr {
			return fmt.Sprintf("the Go method %s.%s has a pointer receiver; pass a go.Ref[%s]", v, m.Name, v)
		}
		if fn != nil && fn.Ptr && !ptr {
			return fmt.Sprintf("%s.%s has a *T receiver", v, m.Name)
		}
		if why := sealedBy(v, it, m, fn); why != "" {
			return why
		}
		// An operation takes no type parameters, so a 196m function with
		// type parameters beyond those its first parameter binds from v's
		// type cannot satisfy one (round 6b1 ruling; round 6 review m1).
		if n, ok := v.(*Named); ok && fn != nil && fn.owner != nil {
			if extra := ownTypeParam(n, fn); extra != nil {
				return fmt.Sprintf("%s.%s has a type parameter of its own, %s; an operation takes none", v, m.Name, extra.Name)
			}
		}
		if sigHasInvalid(got) {
			// The invalid part has its own diagnostic; comparing it would
			// cascade (D3; stage D1 fuzz triage, fuzz-check-C-r4-f8314ece).
			continue
		}
		if !c.sameShape(got, m.Sig) {
			// The shape is the mismatch, so the message prints shapes: an
			// inferred row may still be open here, and its text would
			// depend on declaration order (D0; fuzz-check-C-r7rev-dbdbb39c).
			return fmt.Sprintf("%s.%s has signature %s, want %s", v, m.Name, shapeOf(got), shapeOf(m.Sig))
		}
		// An unknown row is checked by its known part (Row.unknown); an
		// unknown method row (a refused entry, rowExpr) accepts any row, as
		// clause 5's unknown destination does.
		if ge := got.Errors.known(c.types); !m.Sig.Errors.unknown(c.types) && !ge.subsetOf(c.types, m.Sig.Errors) {
			return fmt.Sprintf("%s.%s can fail with %s, wider than %s", v, m.Name, rowText(ge.minus(c.types, m.Sig.Errors)), rowText(m.Sig.Errors))
		}
		if gu := got.Uses.known(c.types); !gu.subsetOf(c.types, m.Sig.Uses) {
			return fmt.Sprintf("%s.%s requires %s, which the interface's method does not (README Gap 17 stays with #37)", v, m.Name, rowText(gu.minus(c.types, m.Sig.Uses)))
		}
		if got.Fallible != m.Sig.Fallible {
			return fmt.Sprintf("%s.%s differs in fallibility from the interface's method", v, m.Name)
		}
		if c.takesEffect(got, v) != c.takesEffect(m.Sig, v) {
			return fmt.Sprintf("%s.%s %s, but the interface's method %s (resources R19, Gap 39)", v, m.Name, takesText(got), takesText(m.Sig))
		}
	}
	return ""
}

// comparable reports whether == applies to values of t. Comparability is
// structural (types.md, Operators 1): a record or tuple compares when its
// fields do, and a union only when every variant does. A row value (a
// `case err:` binding, D-C3) never compares, and neither does an operation
// type's value, whose provider type is not known here: Go would compare
// either as an interface and panic on a variant that does not compare. Every
// use of comparability reads this one rule: ==, tuples, record fields,
// comparable constraints and map keys.
func comparable(t Type) bool {
	return comparableSeen(t, map[Type]bool{})
}

func comparableSeen(t Type, seen map[Type]bool) bool {
	if n, ok := t.(*Named); ok && IsGoError(n) {
		// go.Error keeps Go's native error (IG5): Go's == would compare
		// the error's identity, and panic on an error type Go cannot
		// compare. Its == compares Text (equatable, not comparable), so
		// it is no Map key.
		return false
	}
	switch u := under(t).(type) {
	case *Basic:
		return u.Kind != Invalid
	case *Struct:
		// A recursive struct reaches itself through a named type, the same
		// type object (instances are cached by id).
		if seen[t] {
			return true
		}
		seen[t] = true
		for _, f := range u.Fields {
			if !comparableSeen(f.Type, seen) {
				return false
			}
		}
		return true
	case *Tuple:
		// A tuple compares when its elements do (196t).
		for _, e := range u.Types {
			if !comparableSeen(e, seen) {
				return false
			}
		}
		return true
	case *Union:
		// A row value compares as a union of its members does (q47): an
		// error row parameter among them has no comparable constraint.
		// A recursive union reaches itself through its named type.
		if seen[t] {
			return true
		}
		seen[t] = true
		for _, v := range u.Variants {
			if !comparableSeen(v, seen) {
				return false
			}
		}
		return true
	case *Array:
		// An array compares when its element does, as in Go (196y).
		return comparableSeen(u.Elem, seen)
	case *Interface:
		return false
	case *Pointer:
		return true
	case *TypeParam:
		return typeSet(u) >= SetComparable
	}
	return false
}

// Comparable reports Go's comparability of t (comparable): what a map key,
// a comparable constraint and Go's own == take.
func Comparable(t Type) bool { return comparable(t) }

// Equatable reports whether == and != apply to values of t (types.md,
// Operators 1; eq-structural). Every comparable type is equatable, and so
// is a slice of an equatable element, a Map whose values are equatable
// (its keys are comparable already), and a record, tuple or union whose
// fields, elements and variants are equatable: their equality is
// structural, element by element, and the lowering generates it where Go
// has none. A function, an operation type's value and a row value are not
// equatable, as they are not comparable.
func Equatable(t Type) bool { return equatableSeen(t, map[Type]bool{}) }

func equatableSeen(t Type, seen map[Type]bool) bool {
	switch u := under(t).(type) {
	case *Slice:
		return equatableSeen(u.Elem, seen)
	case *Array:
		return equatableSeen(u.Elem, seen)
	case *Map:
		return equatableSeen(u.Elem, seen)
	case *Struct:
		if seen[t] {
			return true
		}
		seen[t] = true
		for _, f := range u.Fields {
			if !equatableSeen(f.Type, seen) {
				return false
			}
		}
		return true
	case *Tuple:
		for _, e := range u.Types {
			if !equatableSeen(e, seen) {
				return false
			}
		}
		return true
	case *Union:
		// A row value compares as a union of its members does (q47): an
		// error row parameter among them has no comparable constraint.
		if seen[t] {
			return true
		}
		seen[t] = true
		for _, v := range u.Variants {
			if !equatableSeen(v, seen) {
				return false
			}
		}
		return true
	}
	return comparable(t)
}

// typeSet is the predeclared type set a type parameter's constraint names
// (comparable, ordered or number), SetAll for any other type or
// constraint.
func typeSet(t Type) TypeSet {
	tp, ok := t.(*TypeParam)
	if !ok {
		return SetAll
	}
	if it := asInterface(tp.Constraint); it != nil {
		return it.Set
	}
	return SetAll
}

// inSet reports whether t lies in the type set s (types.md, Generics 2):
// a type parameter by its own constraint's set, any other type by its
// underlying type.
func inSet(t Type, s TypeSet) bool {
	switch s {
	case SetAll:
		return true
	case SetComparable:
		return comparable(t)
	}
	if _, ok := t.(*TypeParam); ok {
		return typeSet(t) >= s
	}
	if RefinementOf(t) != nil {
		// ordered and number give + to a generic body, which could leave
		// the where (Refinements 7).
		return false
	}
	if s == SetOrdered && (isString(t) || isDuration(under(t))) {
		return true
	}
	return isInteger(t) || isFloat(t)
}

// satisfies checks a type argument against its constraint.
func (c *checker) satisfies(t Type, constraint Type) string {
	if constraint == nil {
		return ""
	}
	it := asInterface(constraint)
	if it == nil {
		return ""
	}
	if it.Set == SetWire {
		// An instance of a generic codec: the caller's package lowers
		// the argument's codec, so it records the argument's pairs.
		if r := c.wireReason(t); r != "" {
			return r
		}
		if p := c.firstInferredParse(t); p != nil {
			return fmt.Sprintf("%s reaches %s, whose fails row is inferred: write it (types.md, Codecs 2)", t, p.Name)
		}
		c.recordWire(t)
		return ""
	}
	if it.Set != SetAll {
		if !inSet(t, it.Set) {
			return fmt.Sprintf("%s is not %s", t, it.Set)
		}
		return ""
	}
	return c.implements(t, it)
}

// letZero checks `let name T` with no value (types.md, Zero values 3): T
// must have a zero value. Another package's record that only its package
// builds is S114 first, since its message names the function to call.
func (c *checker) letZero(name *syntax.Ident, declared Type) {
	if declared == nil || isInvalid(declared) {
		return
	}
	if a := zeroAuthority(declared, c.pkg); a != nil {
		c.errorf(name, CodeAuthority, "let %s of type %s needs a value: its zero value builds a new value of %s, and %s (types.md, Zero values 6)", name.Name, c.localTypeText(declared), qualifiedName(a), authorityText(a))
		return
	}
	if !defaultable(declared) {
		c.errorf(name, CodeType, "let %s of type %s needs a value: %s has no zero value (types.md, Zero values 3)", name.Name, declared, declared)
		return
	}
	if c.zeroHoldsResource(declared) {
		// A resource comes only from its acquisition (resources R1).
		c.errorf(name, CodeType, "let %s of type %s needs a value: %s is a resource, and a resource has no zero value (resources R1)", name.Name, declared, declared)
	}
}

// defaultable reports whether t has a valid zero value (types.md, Zero
// values 1): a basic type, a slice or Map, a record with no fields, or an
// array or a tuple of these. A record with fields has none: it is built only by a
// literal that names every field or by a record update (Zero values 2,
// owner q44). A union, an interface, a function type, a pointer and a tuple
// that holds a record with fields have none either.
func defaultable(t Type) bool {
	if r := RefinementOf(t); r != nil && !r.Holds(big.NewInt(0)) {
		// The base's zero (0, or length 0) fails the where (Refinements 8).
		return false
	}
	switch u := under(t).(type) {
	case *Basic:
		return true
	case *Slice, *Map:
		return true
	case *Array:
		return defaultable(u.Elem)
	case *Tuple:
		for _, e := range u.Types {
			if !defaultable(e) {
				return false
			}
		}
		return true
	case *Struct:
		return len(u.Fields) == 0
	}
	return false
}

// hasVariant reports whether the union u lists v.
func (c *checker) hasVariant(u *Union, v Type) bool {
	for _, variant := range u.Variants {
		if c.identical(variant, v) {
			return true
		}
	}
	return false
}
