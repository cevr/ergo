package check

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Derived providers (patterns.md, Provider plans; STD-NOPRIV NP3e). A type
// declared `type T[S] derive Provider(Of[S](), Of[F](), Of[N]())` is a test
// provider of the capability S that the checker derives from S's declared
// operations, as it derives a codec (196d): no name list, no reflection.
// testing.Script[S] is one (stdlib.md, Testing 7 to 12); any package may
// declare another, and the rules below read the plan, never a name.
//
//  1. S is a capability: a named operation type with operations, concrete
//     (a generic one at written arguments, Script[Store[Job]]). A type
//     parameter, a sealed std type, an operation that returns a Task is
//     S108 at the type argument (scriptWhy). The record of calls keeps
//     each argument past the call, so an operation that takes a function,
//     a borrow (scriptWhy) or a value that holds a resource (scriptKept) is
//     S108 there too: one rule for every parameter a call only lends (#197
//     L8 C4; q39). A generic record's Script field names a concrete
//     capability (Capabilities 7), so an instantiation adds no Script.
//  2. Fields. The instance's record has one field per operation of S, in
//     S's order and named like it, of type testing.Op[A, F]: A is the
//     operation's arguments (the one parameter's type, a tuple of several,
//     testing.NoArgs for none), F its signature as a function type
//     (scriptUnder). The fields are S's package's, so an operation hidden
//     from a package is a field hidden from it (grammar.md, Visibility 2).
//  3. Making one. The literal testing.Script[S]{} sets no field: each
//     operation starts with no answer and no calls (structLit).
//  4. Providing S. A Script[S] implements every operation type whose
//     operations S has, by S's signatures (implements): it converts to S
//     where a value of S is expected, a context entry included. The
//     conversion's table is empty; the lowering writes the derived methods.

// scriptArg is S of an instance of a derived provider T[S], and false for
// any other type.
func scriptArg(t Type) (Type, bool) {
	n, ok := t.(*Named)
	if !ok || n.Orig == nil || n.Obj == nil || n.Obj.Provider == nil || len(n.TArgs) != 1 {
		return nil, false
	}
	return n.TArgs[0], true
}

// resolveProviders resolves the plan types of each derived provider the
// package declares (patterns.md, Provider plans), before any instance is
// built: the field type, a generic type of two type parameters whose
// declaration names the runtime's recording Go type (providerFieldRefusal),
// and the type of no arguments, a record with no fields. A refusal is S108
// at the derive, and the type stays an empty record.
func (c *checker) resolveProviders() {
	for _, tn := range c.pkg.Types {
		if tn.Decl == nil || tn.Decl.Provider == nil {
			continue
		}
		c.cur = tn.file
		plan := tn.Decl.Provider
		c.usePlanPackage(plan.Field)
		c.usePlanPackage(plan.None)
		_, field := c.namedHead(plan.Field)
		_, none := c.namedHead(plan.None)
		why := ""
		switch {
		case field == nil:
			why = "its field type is not a declared type"
		case len(field.TParams) != 2:
			why = fmt.Sprintf("its field type %s has %d type parameters; a field type takes the arguments and the signature, two", field, len(field.TParams))
		case none == nil:
			why = "its type of no arguments is not a declared type"
		default:
			if st, ok := none.Underlying().(*Struct); !ok || len(st.Fields) > 0 || len(none.TParams) > 0 {
				why = fmt.Sprintf("its type of no arguments %s is not a record with no fields", none)
			} else {
				why = providerFieldRefusal(field)
			}
		}
		if why != "" {
			c.errorIn(tn.Decl, tn.Decl.DerivePos, tn.Decl.DerivePos+syntax.Pos(len("derive")), CodeType, "the Provider derive of %s: %s (patterns.md, Provider plans)", tn.Name, why)
			continue
		}
		tn.Provider = &ProviderTypes{Field: field, None: none}
	}
}

// providerName is how a diagnostic names the provider instance inst: its
// package and type, testing.Script for std's.
func providerName(inst *Named) string {
	return inst.Obj.Pkg.Name + "." + inst.Obj.Name
}

// ScriptOf is S of a testing.Script[S] instance whose S the checker
// admits (scriptWhy), and nil for any other type.
func ScriptOf(t Type) *Named {
	s, ok := scriptArg(t)
	if !ok || scriptWhy(s) != "" {
		return nil
	}
	return s.(*Named)
}

// returnsRunning reports an operation whose one result is a running task,
// a Task or a tasks.Set: a type whose Close is a runtime body that stops running code
// (OpBody.StopsRunning, the fact running.go reads).
func returnsRunning(sig *Signature) bool {
	if len(sig.Results) != 1 {
		return false
	}
	n, ok := sig.Results[0].(*Named)
	if !ok {
		return false
	}
	return OpBodyOf(n, "Close").StopsRunning
}

// scriptWhy is why the type argument s makes no Script, or "" when it
// makes one (rule 1).
func scriptWhy(s Type) string {
	if _, ok := s.(*TypeParam); ok {
		return fmt.Sprintf("%s is a type parameter; a Script is derived from the operations of a written capability", s)
	}
	n, ok := s.(*Named)
	if !ok || n.foreign || !isCapability(s) {
		return fmt.Sprintf("%s is not a capability; a Script is derived from the operations of an operation type with written type arguments", s)
	}
	if Sealed(n) {
		return fmt.Sprintf("%s has an unexported operation, so a Script cannot implement it outside its package", s)
	}
	for _, m := range asInterface(s).Methods {
		if returnsRunning(m.Sig) {
			return fmt.Sprintf("its operation %s returns a Task, which a Script cannot answer", m.Name)
		}
		for _, p := range m.Sig.Params {
			switch p.Type.(type) {
			case *Signature:
				return fmt.Sprintf("its operation %s takes a function, which a Script's record of calls would keep past the call", m.Name)
			case *Pointer:
				return fmt.Sprintf("its operation %s takes the borrow %s %s, which a Script's record of calls would keep past the call (types.md, Borrows 1)", m.Name, p.Name, p.Type)
			}
		}
	}
	return ""
}

// scriptKept is why the record of calls of a Script of the capability s
// would keep a resource past the call, or "" (rule 1): an operation's
// argument that holds a resource is lent for the call, or taken by the
// answer, so a recorded copy would be a second owner (resources.md,
// Lending). A type resolves on demand and a Close is a declared function,
// so a Close declared after the Script is known at the type argument.
func (c *checker) scriptKept(s Type) string {
	for _, m := range asInterface(s).Methods {
		for _, p := range m.Sig.Params {
			if c.holdsResource(p.Type) {
				return fmt.Sprintf("its operation %s takes the resource %s %s, which a Script's record of calls would keep past the call (resources.md, Lending)", m.Name, p.Name, p.Type)
			}
		}
	}
	return ""
}

// scriptWritten reports the S108 of a Script instance whose type argument
// makes none, at the argument, and whether the instance is admitted.
func (c *checker) scriptWritten(inst *Named, at syntax.Node) bool {
	s, ok := scriptArg(inst)
	if !ok {
		return true
	}
	if why := scriptWhy(s); why != "" {
		c.errorf(at, CodeType, "%s[%s]: %s (stdlib.md, Testing 7)", providerName(inst), s, why)
		return false
	}
	if why := c.scriptKept(s); why != "" {
		c.errorf(at, CodeType, "%s[%s]: %s (stdlib.md, Testing 7)", providerName(inst), s, why)
		return false
	}
	return true
}

// scriptUnder is the record of the Script instance inst (rule 2). A type
// argument that makes no Script gives an empty record: its S108 is at the
// type argument.
func (c *checker) scriptUnder(inst *Named) Type {
	s, _ := scriptArg(inst)
	if scriptWhy(s) != "" {
		return &Struct{obj: inst.Obj}
	}
	op, none := inst.Obj.Provider.Field, inst.Obj.Provider.None
	n := s.(*Named)
	var fields []*Field
	for _, m := range asInterface(s).Methods {
		var args Type
		switch len(m.Sig.Params) {
		case 0:
			args = none
		case 1:
			args = m.Sig.Params[0].Type
		default:
			ts := make([]Type, len(m.Sig.Params))
			for i, p := range m.Sig.Params {
				ts[i] = p.Type
			}
			args = &Tuple{Types: ts}
		}
		f := *m.Sig
		f.NonEscaping = false
		fields = append(fields, &Field{Name: m.Name, Type: c.instantiate(op, []Type{args, &f}), Pos: m.Pos})
	}
	return &Struct{Fields: fields, obj: n.origin().Obj}
}

// scriptLit checks the literal x of the Script t: it sets no field (rule 3).
func (c *checker) scriptLit(x *syntax.CompositeLit, t Type) bool {
	if len(x.Elts) == 0 && x.Base == nil {
		return true
	}
	c.errorf(x, CodeType, "a %s is made with no fields, %s{}: each operation starts with no answer, and s.Op.Answer(f) sets one (stdlib.md, Testing 9)", t, strings.TrimSpace(c.localTypeText(t)))
	return false
}

// usePlanPackage marks the package that the plan type x qualifies, as
// qualifiedType does for a written type.
func (c *checker) usePlanPackage(x syntax.Expr) {
	sel, ok := x.(*syntax.SelectorExpr)
	if !ok {
		return
	}
	id, ok := sel.X.(*syntax.Ident)
	if !ok {
		return
	}
	if pn, ok := c.lookupType(id.Name, nil).(*PkgName); ok {
		pn.used = true
		c.recordUse(id, pn)
	}
}
