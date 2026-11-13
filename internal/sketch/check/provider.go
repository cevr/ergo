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
type ProviderCalls struct {
	New, Call       *Func
	TypeArgs        []Type
	NewSig, CallSig *Signature
}

// ProviderCalls is the Ergo NewF and Call of f, a field of a derived
// provider instance, or nil when its field type is a runtime Go type.
func (f *Field) ProviderCalls() *ProviderCalls { return f.provider }

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
