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
		if t.Uses {
			return &RowArg{Row: c.wireNeedsRow(b)}
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
		it := &Interface{Set: t.Set, Methods: make([]*Method, len(t.Methods)), obj: t.obj, Generic: t.Generic}
		if t.Terms != nil {
			it.Terms = make([]*Term, len(t.Terms))
			for i, term := range t.Terms {
				it.Terms[i] = &Term{Tilde: term.Tilde, Type: c.subst(term.Type, m)}
			}
		}
		newOf := make(map[*Method]*Method, len(t.Methods))
		for i, mt := range t.Methods {
			it.Methods[i] = &Method{Name: mt.Name, Sig: c.subst(mt.Sig, m).(*Signature), Pos: mt.Pos, pkg: mt.pkg}
			newOf[mt] = it.Methods[i]
		}
		// The order sorted at resolution carries over (no sort here).
		for _, mt := range t.byName {
			it.byName = append(it.byName, newOf[mt])
		}
		for _, inc := range t.includes {
			if n, ok := c.subst(inc, m).(*Named); ok {
				it.includes = append(it.includes, n)
			}
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
		s := &Signature{Fallible: t.Fallible, NonEscaping: t.NonEscaping, Entry: t.Entry, Takes: t.Takes, TakesSelf: t.TakesSelf, MutSelf: t.MutSelf}
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
		if len(s.TParams) == len(t.TParams) {
			// None bound, as a generic operation's own at an instance of
			// its type: they stay written ones (GENERIC-OP).
			s.NBracket = t.NBracket
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
	if !ok || !ok2 || len(as.TParams) > 0 || len(bs.TParams) > 0 || as.NonEscaping != bs.NonEscaping || as.Entry != bs.Entry {
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
	assignGoAny                   // 8: a value with a Go form into go.Any (goany.go)
)

// assignment is the result of assignability: the clause that admitted the
// value and the implicit conversion the assignment makes, the Info.Implicit
// fact (Kind 0 when it makes none).
type assignment struct {
	clause assignClause
	conv   Conversion
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
//  4. a variant or a row value into a union: ConvVariant or ConvRow; a
//     union whose variants it all holds: ConvWiden;
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
	capture := c.capture // the caller's go.Foreign capture, this call's only
	c.capture = nil
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
	if view, step, stepSig := c.machineValue(v); view != nil && isPreludeMachineValue(t) {
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
				return assignment{}, fmt.Sprintf("its start takes %s and its step %sgives an InputMachine with output %s, error row %s, capability row %s and work row %s", view.TArgs[0], entryLabel(step.Expr), o, rowText(f.Row), rowText(r.Row), rowText(w.Row))
			}
			return assignment{}, fmt.Sprintf("its step %sgives a Machine with output %s, error row %s, capability row %s and work row %s", entryLabel(step.Expr), o, rowText(f.Row), rowText(r.Row), rowText(w.Row))
		}
		conv, why := c.machineConversion(v.(*Named), t, step, stepSig, input)
		if why != "" {
			return assignment{}, why
		}
		return assignment{clause: assignMachine, conv: conv}, ""
	}
	if IsGoAny(t) {
		// Clause 8 (GB2): Go's own conversion to its empty interface.
		if why := c.goAnyForm(v); why != "" {
			return assignment{}, why
		}
		return assignment{clause: assignGoAny}, ""
	}
	if IsGoAny(v) {
		return assignment{}, "only go.As[T](a) takes a value out of a go.Any"
	}
	if it := asInterface(t); it != nil {
		if _, isTuple := under(v).(*Tuple); isTuple {
			return assignment{}, "a tuple has no operations"
		}
		if it.Set != SetAll {
			return assignment{}, fmt.Sprintf("%s is a constraint only", it.Set)
		}
		if it.Terms != nil {
			return assignment{}, fmt.Sprintf("%s has a type set, so it is a constraint only", t)
		}
		if why := c.undeclared(v, t, "conversion"); why != "" {
			return assignment{}, why
		}
		cell := false
		if why := c.implementsCapturing(v, it, !c.hostLendArg(t), capture); why != "" {
			if _, named := t.(*Named); named && !isHostNamed(t) && c.clauseUnmet(v, it) {
				// The clause failed at the type (S106 there).
				return assignment{clause: assignIdentical}, ""
			}
			// A value provider whose functions take *v moves into a cell
			// (cells.go): *v implements t where v does not.
			n, named := v.(*Named)
			if !named || ScriptOf(v) != nil || n.Obj == nil || n.Obj.Pkg.Host() || c.implementsCapturing(&Pointer{Elem: v}, it, !c.hostLendArg(t), capture) != "" {
				return assignment{}, why
			}
			if refusal := c.cellRefusal(v, t, it); refusal != "" {
				return assignment{}, why + "; " + refusal
			}
			cell = true
		}
		if h := c.holdsCell(v); h != nil && !c.cellType(t) && !(c.encodeLend && !cell) {
			// A cell stays where its confinement rules see it. A confined
			// go.Ref may go into a Go interface (rand.New(src)): only a
			// go.Foreign call reaches it there (goconfined.go).
			if !goConfinedRef(h) {
				return assignment{}, fmt.Sprintf(cellHideText, v, h, t)
			}
			if !isHostNamed(t) {
				return assignment{}, fmt.Sprintf(goConfinedHideText, v, h, t)
			}
		}
		if tn, ok := t.(*Named); ok && c.keeps() != nil && tn.origin() == c.keeps() && !c.identical(v, t) {
			// Keeping is the release's contract (R13): a type includes it,
			// and its one operation, sealed, is the runtime's; no value
			// is converted to it.
			return assignment{}, fmt.Sprintf("%s is the release's contract for a type that keeps values (resources R13): a type includes it with ..%s, and no value converts to it", t, t)
		}
		// Only a value that implements the operation type can reach
		// the ownership guard. A missing operation is the more specific
		// failure (cmd/compile assignOp's diagnostic order).
		if !c.providing && !c.hostLendArg(t) && c.holdsResource(v) && !c.releasable(t) {
			// R22: the operation value would never be released.
			return assignment{}, c.resourceIntoCrossingText(v, t)
		}
		if isHostNamed(t) && len(it.Methods) > 0 && ergoOperations(v) {
			// The operations of an Ergo operation type, or of a type
			// parameter (its witness's), take Ergo's calling convention
			// (the task they run on), not the Go method shapes of a Go
			// interface, and no function of the program provides them for
			// an adapter (lower/gointerface.go), so the value does not
			// convert (batch-16b r3, r4): check and build agree.
			return assignment{}, fmt.Sprintf("the operations of %s take Ergo's calling convention, not the Go methods of %s", v, t)
		}
		if asInterface(v) != nil {
			if sealedStdOps(v) && !sealedStdOps(t) && !isHostNamed(t) {
				// The runtime's own type may stand for a sealed std
				// operation type, with the runtime's Go shapes, not those
				// of the target's interface: the lowering builds the
				// operations from the std ones (lower/sealedops.go), for
				// every pair this member match admits. The checker
				// records the conversion; the lowering decides the Go
				// representation (lower/std.go runtimeType).
				return assignment{clause: assignOperations, conv: Conversion{Kind: ConvOperations, From: v, To: t}}, ""
			}
			if isHostNamed(t) && len(it.Methods) > 0 && !isHostNamed(v) && !sealedStdOps(v) {
				// An Ergo operation type's operations take Ergo's calling
				// convention (the task they run on), not the Go method
				// shapes of a Go interface, so Go would refuse the
				// assignment (batch-16b r3): check and build agree.
				return assignment{}, fmt.Sprintf("the operations of %s take Ergo's calling convention, not the Go methods of %s", v, t)
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
		conv := Conversion{Kind: ConvOperations, From: v, To: t, Table: table, Instances: instances, Cell: cell}
		if n, ok := t.(*Named); ok && cell && len(instances) == 0 && n.Obj.Pkg == c.pkg {
			if c.pkg.cellConvs == nil {
				c.pkg.cellConvs = map[*Named]Conversion{}
			}
			c.pkg.cellConvs[n.origin()] = conv
		}
		return assignment{clause: assignOperations, conv: conv}, ""
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
			if vu := asUnion(v); vu != nil && vu.Row == nil && len(vu.Variants) > 0 {
				// A declared union goes into one that holds each of its
				// variants: a subset (ConvWiden).
				for _, e := range vu.Variants {
					if !c.hasVariant(u, e) {
						return assignment{}, fmt.Sprintf("%s may hold %s, which %s does not list", v, e, t)
					}
				}
				return assignment{clause: assignUnion, conv: Conversion{Kind: ConvWiden, From: v, To: t}}, ""
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
		if n, isNamed := t.(*Named); isNamed && IsGoError(n) {
			// Go's error is go.Error, an error value, not an interface an
			// Ergo type's Error function would provide: interop.md, go.Error.
			return assignment{}, "go.Error holds a Go error, which a Go call or go.Error{Text: s} makes"
		}
		return assignment{}, ""
	}
	if hasName(v) && hasName(t) {
		// Two named function types cross only by conversion (D-B7).
		return assignment{}, ""
	}
	if ts.Entry && !vs.Entry {
		// Only the runner makes an entry's send (Signature.Entry).
		return assignment{}, "only an entry's send is one: a parameter written entry func(…) that the runner or a caller gave (runtime.md, Machines and actors 14)"
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

// ownTypeParams are the type parameters of f its first parameter's type
// arguments do not bind from t, in declaration order.
func ownTypeParams(t *Named, f *Func) []*TypeParam {
	if f.Sig == nil {
		return nil
	}
	bound := selfBinding(t, f)
	var out []*TypeParam
	for _, tp := range f.Sig.TParams {
		if _, ok := bound[tp]; !ok {
			out = append(out, tp)
		}
	}
	return out
}

// opTypeName is the declared name of the operation type it, for a
// diagnostic; "the operation type" for a literal one.
func opTypeName(it *Interface) string {
	if it.obj != nil {
		return it.obj.Name
	}
	return "the operation type"
}

// substOrNil is subst of a constraint that may be absent.
func (c *checker) substOrNil(t Type, m map[*TypeParam]Type) Type {
	if t == nil {
		return nil
	}
	return c.subst(t, m)
}

// sameConstraint reports whether two type parameters' constraints are
// the same: both absent (any), or identical.
func (c *checker) sameConstraint(a, b Type) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return c.identical(a, b)
}

// constraintText is a type parameter's constraint as a diagnostic
// writes it: "unconstrained" for none, else its kind's word or the type.
func constraintText(tp *TypeParam) string {
	switch {
	case tp.Kind != KindType:
		return tp.Kind.String()
	case tp.Constraint == nil:
		return "unconstrained"
	}
	return tp.Constraint.String()
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

// ergoOperations reports a value, or a borrow of one, whose operations
// are an Ergo operation type's: a value of a program or std operation type
// (a generic one's instance included) or of a type parameter, whose
// operations its witness gives. A sealed std protocol's value is the
// runtime's own Go type and a Go value has Go's methods, so neither is.
func ergoOperations(v Type) bool {
	if p, ok := v.(*Pointer); ok {
		v = p.Elem
	}
	if isHostNamed(v) || sealedStd(v) != nil {
		return false
	}
	_, param := v.(*TypeParam)
	return param || asInterface(v) != nil
}

// implements checks method-row compatibility: for every method of it, v has
// a method with identical parameters and results, the same fallibility, and
// error and capability rows that are subsets (types.md, Interface
// assignability 1; rows.md, Context 2). Each lookup is a keyed map access, so
// the check is linear in the method count.
func (c *checker) implements(v Type, it *Interface, checkReceiver bool) string {
	return c.implementsCapturing(v, it, checkReceiver, nil)
}

// implementsCapturing is implements where, with capture not nil, a method
// whose capability row exceeds the operation's by go.Foreign alone is
// admitted, and *capture is set when v implements it with such a method:
// the site that builds the provider holds go.Foreign for it, as a provider
// built by a constructor captures what its constructor needed (README Gap
// 17; context construction for every other capability). go.Foreign has no provider value
// (rows.md, Entry 4), so the operation's Go call needs nothing at run time
// that the build site did not have.
func (c *checker) implementsCapturing(v Type, it *Interface, checkReceiver bool, capture *captured) string {
	return c.implementsAt(v, it, checkReceiver, capture, nil)
}

// implementsAt is implementsCapturing, at a type parameter's constraint
// or a type's implements clause when declared (the operation type) is
// set: there a function on mut T implements only an operation marked mut
// (mut Bump()), and a generic body calls it on a mut T only (types.md,
// Generics 2; Interface assignability 1; MODES-4). A conversion
// (declared nil) reads the clause's verdict: a function on mut T
// provides its operation for a value with a place, &x.
func (c *checker) implementsAt(v Type, it *Interface, checkReceiver bool, capture *captured, declared Type) string {
	var held captured
	// A Go interface spells no mut: a function on mut T provides its
	// operation as Go's pointer-receiver method does, by the conversion
	// rule (interop.md, Checking).
	if declared != nil && isHostNamed(declared) {
		declared = nil
	}
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
		if why := c.implementsCapturing(s, it, checkReceiver, capture); why != "" {
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
		switch {
		case fn != nil && fn.Ptr && declared != nil && !m.Sig.MutSelf:
			if c.recordMutOp(declared, m.Name) {
				break // ergo fix: the old rule admitted it, and the fix marks the operation mut
			}
			return fmt.Sprintf("%s.%s writes its receiver (mut %s), and the operation %s does not: declare it mut %s(…)", v, m.Name, v, m.Name, m.Name)
		case fn != nil && fn.Ptr && !ptr && declared == nil:
			return fmt.Sprintf("%s.%s has a mut %s receiver", v, m.Name, v)
		case fn == nil && got.MutSelf && !m.Sig.MutSelf:
			return fmt.Sprintf("%s.%s writes its receiver (mut %s()), and the operation it implements does not", v, m.Name, m.Name)
		}
		if why := sealedBy(v, it, m, fn); why != "" {
			return why
		}
		// A function's type parameters beyond those its first parameter
		// binds from v's type match the operation's own, in order, in
		// kind and in constraint; the rest of the signature is compared
		// with them renamed to the operation's (types.md, Interface
		// assignability, Generic operations; round 6b1 ruling for an
		// operation with none).
		if n, ok := v.(*Named); ok && fn != nil && fn.owner != nil {
			extra := ownTypeParams(n, fn)
			if len(extra) > 0 && len(m.Sig.TParams) == 0 {
				return fmt.Sprintf("%s.%s has a type parameter of its own, %s; the operation takes none", v, m.Name, extra[0].Name)
			}
			if len(extra) != len(m.Sig.TParams) {
				return fmt.Sprintf("%s.%s has %d type parameters beyond its first parameter's, and %s.%s has %d", n.Obj.Name, m.Name, len(extra), opTypeName(it), m.Name, len(m.Sig.TParams))
			}
			if len(extra) > 0 {
				rename := make(map[*TypeParam]Type, len(extra))
				for i, tp := range extra {
					rename[tp] = m.Sig.TParams[i]
				}
				for i, tp := range extra {
					want := m.Sig.TParams[i]
					if tp.Kind != want.Kind || !c.sameConstraint(c.substOrNil(tp.Constraint, rename), want.Constraint) {
						return fmt.Sprintf("%s.%s's type parameter %s is %s, and %s.%s's %s is %s", n.Obj.Name, m.Name, tp.Name, constraintText(tp), opTypeName(it), m.Name, want.Name, constraintText(want))
					}
				}
				got = c.subst(got, rename).(*Signature)
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
			extra := gu.minus(c.types, m.Sig.Uses)
			if capture == nil || !held.add(extra) {
				return fmt.Sprintf("%s.%s requires %s, which the interface's method does not (README Gap 17: a provider's build holds go.Foreign or Ambient for it; any other capability stays with context construction)", v, m.Name, rowText(extra))
			}
		}
		if why := c.releaseWider(v, m); why != "" {
			return why
		}
		if got.Fallible != m.Sig.Fallible {
			return fmt.Sprintf("%s.%s differs in fallibility from the interface's method", v, m.Name)
		}
		// At a direct Go argument, the enclosing Go function's clause
		// owns the receiver lifetime (io.ReadAll lends it; bufio keeps
		// it). The Go interface supplies its method shape there, not a
		// second receiver hand-off. Explicit parameter takes still match.
		recv := v
		if !checkReceiver {
			recv = nil
		}
		if c.takesEffect(got, recv) != c.takesEffect(m.Sig, recv) {
			return fmt.Sprintf("%s.%s %s, but the interface's method %s (resources R19, Gap 39)", v, m.Name, takesText(got), takesText(m.Sig))
		}
	}
	if held.foreign || held.ambient != nil {
		*capture = held
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
		// A type parameter compares when its set holds comparable types
		// only (the predeclared comparable, or terms that all compare).
		terms := paramTerms(u)
		if terms == nil {
			return false
		}
		for _, term := range terms {
			if !term.isComparableTerm() && !comparable(term.Type) {
				return false
			}
		}
		return true
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

// typeSet is the predeclared constraint a type parameter's constraint
// names (lent, comparable or the codec one), SetAll for any other type or
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

// inSet reports whether t lies in the predeclared constraint s (types.md,
// Generics 2). A type-set member is read by inTypeSet (typeset.go).
func inSet(t Type, s TypeSet) bool {
	switch s {
	case SetAll:
		return true
	case SetComparable:
		return comparable(t)
	}
	// The codec constraint is read by satisfies before it reaches here.
	return false
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
	if it.Terms != nil {
		// A type set is satisfied by membership, with no clause; a
		// constraint with operations too needs both (types.md, Generics 2).
		if !c.inTypeSet(t, it.Terms) {
			return notInSetText(t, constraint, it)
		}
		if len(it.Methods) == 0 {
			return ""
		}
	}
	if why := c.undeclared(t, constraint, "constraint"); why != "" {
		return why
	}
	if why := c.implementsAt(t, it, true, nil, constraint); why != "" {
		if c.clauseUnmet(t, it) {
			return "" // the clause failed at the type (S106 there)
		}
		return why
	}
	return ""
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
	if r := RefinementOf(t); r != nil && (!r.Holds(big.NewInt(0)) || r.Pattern != nil && !r.Pattern.Match("")) {
		// The base's zero (0, or length 0) fails the where (Refinements 8).
		return false
	}
	if _, h, _, ok := providerParts(t); ok {
		// A Map's default is its empty Map, with its provider's default
		// (mapprovider.go).
		return defaultable(h)
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
