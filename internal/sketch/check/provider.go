package check

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Ergo field types (patterns.md, Provider plans 3; STD-NOPRIV NP3e-2). A
// derived provider's field type F is either a type whose declaration names
// a runtime Go type with the bodies NewV and V.Call (testing.Op), or a type
// declared in Ergo whose package declares, as Ergo code, the same two
// parts:
//
//	func NewF[A, G]() F[A, G]
//	func Call[A, G](r F[A, G], a A) Option[G]
//
// NewF makes the field of one operation, as builders.go rule 1 names a
// generic type's builder; Call records the arguments a of one call and
// returns the answer, if the test set one. Neither has a row: the provider
// answers on the caller's task with no capability of its own. So a module
// writes its own recorder entirely in Ergo, and the std testing.Op is one
// field type among others.

// ProviderCalls are the Ergo functions that make and answer one field of a
// derived provider instance whose field type is declared in Ergo, and
// their signatures at the field's arguments A and signature G (TypeArgs).
// For a wrapping provider (Wrapping == true), New takes (op, faults),
// Wrap runs an operation with inner and failWith closures, and Draw draws
// tape values for failure payloads.
type ProviderCalls struct {
	New, Call, Wrap, Draw *Func
	TypeArgs              []Type
	NewSig, CallSig       *Signature
	Wrapping              bool
}

// ProviderCalls is the Ergo NewF and Call (or Wrap/Draw) of f, a field of a
// derived provider instance, or nil when its field type is a runtime Go type.
func (f *Field) ProviderCalls() *ProviderCalls { return f.provider }

// ErrorRowTypes returns the concrete declared failure types of r in canonical
// order, flattening row variables and row arguments.
func ErrorRowTypes(r *Row) []Type {
	var out []Type
	var walk func(r *Row)
	walk = func(r *Row) {
		if r == nil {
			return
		}
		for _, e := range r.Elems() {
			switch e := e.(type) {
			case *RowVar:
				walk(e.Solution())
			case *RowArg:
				walk(e.Row)
			default:
				out = append(out, e)
			}
		}
	}
	walk(r)
	return out
}

// wrappingProviderFuncs checks whether field's package declares the wrapping
// provider trio:
//
//	func New<Field>[A, G](op string, faults []string) F[A, G] needs R
//	func Wrap[A, G, T, E](r F[A, G], inner func() T fails E, failWith func(idx int) T fails E) T fails E
//	func Draw[A, G](r F[A, G], n uint64) uint64
func (c *checker) wrappingProviderFuncs(field *Named) (nf, wf, df *Func, why string) {
	newName := "New" + field.Obj.Name
	look := func(name string) *Func {
		o, ok := field.Obj.Pkg.Scope.names[name]
		if !ok {
			return nil
		}
		f, _ := o.(*Func)
		return f
	}
	nf, wf, df = look(newName), look("Wrap"), look("Draw")
	if nf == nil || wf == nil || df == nil {
		return nil, nil, nil, "missing wrapping provider functions"
	}
	ns, ws, ds := c.funcSig(nf), c.funcSig(wf), c.funcSig(df)
	isStr := func(t Type) bool { b, ok := t.(*Basic); return ok && b.Kind == String }
	isStrSlice := func(t Type) bool { s, ok := t.(*Slice); return ok && isStr(s.Elem) }
	isU64 := func(t Type) bool { b, ok := t.(*Basic); return ok && b.Kind == Uint64 }
	isInt := func(t Type) bool { b, ok := t.(*Basic); return ok && b.Kind == Int }
	if !plainPair(ns.TParams) || len(ns.Params) != 2 || !isStr(ns.Params[0].Type) || !isStrSlice(ns.Params[1].Type) || len(ns.Results) != 1 || !ownInstance(ns.Results[0], field, ns.TParams) {
		return nil, nil, nil, fmt.Sprintf("its field type's %s is not func %s[A, G](op string, faults []string) %s[A, G]", newName, newName, field.Obj.Name)
	}
	if len(ws.TParams) != 4 || ws.TParams[0].Kind != KindType || ws.TParams[1].Kind != KindType || ws.TParams[2].Kind != KindType || ws.TParams[3].Kind != KindErrors ||
		ws.TParams[0].Constraint != nil || ws.TParams[1].Constraint != nil || ws.TParams[2].Constraint != nil ||
		len(ws.Params) != 3 || len(ws.Results) != 1 ||
		!ownInstance(ws.Params[0].Type, field, ws.TParams[:2]) ||
		ws.Results[0] != Type(ws.TParams[2]) {
		return nil, nil, nil, fmt.Sprintf("its field type's Wrap is not func Wrap[A, G, T, E](r %s[A, G], inner func() T fails E, failWith func(idx int) T fails E) T fails E", field.Obj.Name)
	}
	innerSig, ok1 := ws.Params[1].Type.(*Signature)
	failSig, ok2 := ws.Params[2].Type.(*Signature)
	if !ok1 || !ok2 || len(innerSig.Params) != 0 || len(innerSig.Results) != 1 || innerSig.Results[0] != Type(ws.TParams[2]) ||
		len(failSig.Params) != 1 || !isInt(failSig.Params[0].Type) || len(failSig.Results) != 1 || failSig.Results[0] != Type(ws.TParams[2]) {
		return nil, nil, nil, fmt.Sprintf("its field type's Wrap is not func Wrap[A, G, T, E](r %s[A, G], inner func() T fails E, failWith func(idx int) T fails E) T fails E", field.Obj.Name)
	}
	if !plainPair(ds.TParams) || len(ds.Params) != 2 || len(ds.Results) != 1 || !ownInstance(ds.Params[0].Type, field, ds.TParams) || !isU64(ds.Params[1].Type) || !isU64(ds.Results[0]) {
		return nil, nil, nil, fmt.Sprintf("its field type's Draw is not func Draw[A, G](r %s[A, G], n uint64) uint64", field.Obj.Name)
	}
	return nf, wf, df, ""
}

// providerFuncs are NewF and Call of the Ergo field type field, the generic
// declaration, or why its package declares no such pair. It resolves their
// signatures; it does not read their rows, which the late check does
// (checkProviderFields) once the bodies infer them.
func (c *checker) providerFuncs(field *Named) (nf, cf *Func, why string) {
	newName := "New" + field.Obj.Name
	look := func(name string) *Func {
		o, ok := field.Obj.Pkg.Scope.names[name]
		if !ok {
			return nil
		}
		f, _ := o.(*Func)
		return f
	}
	nf, cf = look(newName), look("Call")
	if nf == nil {
		return nil, nil, fmt.Sprintf("its field type %s names no runtime Go type, and its package declares no func %s[A, G]() %s[A, G]", field.Obj.Name, newName, field.Obj.Name)
	}
	if cf == nil {
		return nil, nil, fmt.Sprintf("its field type %s names no runtime Go type, and its package declares no func Call[A, G](r %s[A, G], a A) Option[G]", field.Obj.Name, field.Obj.Name)
	}
	ns, cs := c.funcSig(nf), c.funcSig(cf)
	if !plainPair(ns.TParams) || len(ns.Params) != 0 || len(ns.Results) != 1 || !ownInstance(ns.Results[0], field, ns.TParams) {
		return nil, nil, fmt.Sprintf("its field type's %s is not func %s[A, G]() %s[A, G]", newName, newName, field.Obj.Name)
	}
	if !plainPair(cs.TParams) || len(cs.Params) != 2 || len(cs.Results) != 1 || !ownInstance(cs.Params[0].Type, field, cs.TParams) || cs.Params[1].Type != Type(cs.TParams[0]) || optionElem(cs.Results[0]) != Type(cs.TParams[1]) {
		return nil, nil, fmt.Sprintf("its field type's Call is not func Call[A, G](r %s[A, G], a A) Option[G]", field.Obj.Name)
	}
	return nf, cf, ""
}

// plainPair reports two type parameters of kind type with no constraint:
// a field's arguments and signature may be any types.
func plainPair(tps []*TypeParam) bool {
	return len(tps) == 2 && tps[0].Kind == KindType && tps[1].Kind == KindType && tps[0].Constraint == nil && tps[1].Constraint == nil
}

// ownInstance reports t as field[tps[0], tps[1]], the generic field type
// at the function's own type parameters, in their order.
func ownInstance(t Type, field *Named, tps []*TypeParam) bool {
	n, ok := t.(*Named)
	if !ok || n.Obj != field.Obj || len(n.TArgs) != 2 {
		return false
	}
	return n.TArgs[0] == Type(tps[0]) && n.TArgs[1] == Type(tps[1])
}

// providerCalls are the Ergo calls of the field instance fieldInst, a
// field type at one operation's arguments and signature, or nil for a
// runtime field type or a refused pair (checkProviderFields reports it).
func (c *checker) providerCalls(p *ProviderTypes, fieldInst *Named) *ProviderCalls {
	if _, runtime := GoTypeOf(p.Field); runtime {
		return nil
	}
	if p.Wrapping {
		nf, wf, df, why := c.wrappingProviderFuncs(p.Field)
		if why != "" {
			return nil
		}
		at := map[*TypeParam]Type{nf.Sig.TParams[0]: fieldInst.TArgs[0], nf.Sig.TParams[1]: fieldInst.TArgs[1]}
		ns, _ := c.subst(nf.Sig, at).(*Signature)
		if ns == nil {
			return nil
		}
		return &ProviderCalls{New: nf, Wrap: wf, Draw: df, TypeArgs: fieldInst.TArgs, NewSig: ns, Wrapping: true}
	}
	nf, cf, why := c.providerFuncs(p.Field)
	if why != "" {
		return nil
	}
	at := func(f *Func) map[*TypeParam]Type {
		return map[*TypeParam]Type{f.Sig.TParams[0]: fieldInst.TArgs[0], f.Sig.TParams[1]: fieldInst.TArgs[1]}
	}
	ns, _ := c.subst(nf.Sig, at(nf)).(*Signature)
	cs, _ := c.subst(cf.Sig, at(cf)).(*Signature)
	if ns == nil || cs == nil {
		return nil
	}
	return &ProviderCalls{New: nf, Call: cf, TypeArgs: fieldInst.TArgs, NewSig: ns, CallSig: cs}
}

// checkProviderFields refuses, at the derive, a provider the package
// declares whose Ergo field type lacks its pair or whose pair has a row
// (Provider plans 3). It runs after the bodies, so an inferred row is
// known.
func (c *checker) checkProviderFields() {
	for _, tn := range c.pkg.Types {
		p := tn.Provider
		if p == nil {
			continue
		}
		if _, runtime := GoTypeOf(p.Field); runtime {
			continue
		}
		isWrapCandidate := p.Wrapping
		if !isWrapCandidate {
			if wf, ok := p.Field.Obj.Pkg.Scope.names["Wrap"].(*Func); ok {
				if ws := c.funcSig(wf); len(ws.Params) > 0 {
					if rn, ok := ws.Params[0].Type.(*Named); ok && rn.Obj == p.Field.Obj {
						isWrapCandidate = true
					}
				}
			}
		}
		if isWrapCandidate {
			nf, wf, df, why := c.wrappingProviderFuncs(p.Field)
			if why == "" {
				if !nf.Sig.Errors.Empty() {
					why = fmt.Sprintf("its field type's %s has a failure row; a wrapping provider constructor fails nothing", nf.Name)
				} else if !wf.Sig.Uses.Empty() {
					why = fmt.Sprintf("its field type's %s has a capability row; Wrap answers on the caller's task", wf.Name)
				} else if !df.Sig.Errors.Empty() || !df.Sig.Uses.Empty() {
					why = fmt.Sprintf("its field type's %s has a row; Draw has no capability or failure of its own", df.Name)
				}
			}
			if why != "" {
				c.cur = tn.file
				c.errorIn(tn.Decl, tn.Decl.DerivePos, tn.Decl.DerivePos+syntax.Pos(len("derive")), CodeType, "the Provider derive of %s: %s (patterns.md, Provider plans)", tn.Name, why)
			}
			continue
		}
		nf, cf, why := c.providerFuncs(p.Field)
		if why == "" {
			for _, f := range []*Func{nf, cf} {
				if !f.Sig.Errors.Empty() || !f.Sig.Uses.Empty() {
					why = fmt.Sprintf("its field type's %s has a row; a provider answers on the caller's task with no capability or failure of its own", f.Name)
					break
				}
			}
		}
		if why != "" {
			c.cur = tn.file
			c.errorIn(tn.Decl, tn.Decl.DerivePos, tn.Decl.DerivePos+syntax.Pos(len("derive")), CodeType, "the Provider derive of %s: %s (patterns.md, Provider plans)", tn.Name, why)
		}
	}
}
