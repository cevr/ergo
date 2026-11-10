package check

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// Scripts (stdlib.md, Testing 7 to 12; #237 stage 5, unit L5b). The std
// record testing.Script[S] is a test provider of the capability S that the
// checker derives from S's declared operations, as it derives a codec
// (196d): no name list, no reflection.
//
//  1. S is a capability: a named operation type with operations, concrete
//     (a generic one at written arguments, Script[Store[Job]]). A type
//     parameter, a sealed std type, an operation that returns a Task is
//     S108 at the type argument (scriptWhy). The record of calls keeps
//     each argument past the call, so an operation that takes a function,
//     a borrow (scriptWhy) or a value that holds a resource (scriptKept, in
//     the late pass) is S108 there too: one rule for every parameter a call
//     only lends (#197 L8 C4; q39).
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

// scriptArg is S of a testing.Script[S] instance, and false for any other
// type.
func scriptArg(t Type) (Type, bool) {
	n, ok := t.(*Named)
	if !ok || n.Orig == nil || n.Obj == nil || n.Obj.Name != "Script" || n.Obj.Pkg == nil || !n.Obj.Pkg.std || n.Obj.Pkg.Path != "testing" || len(n.TArgs) != 1 {
		return nil, false
	}
	return n.TArgs[0], true
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
// a Task or a tasks.Set: a std type whose Close stops running code
// (v1.StopsRunning, the table running.go reads).
func returnsRunning(sig *Signature) bool {
	if len(sig.Results) != 1 {
		return false
	}
	n, ok := sig.Results[0].(*Named)
	if !ok {
		return false
	}
	o := n.origin().Obj
	return o != nil && o.Pkg != nil && o.Pkg.Std() && v1.StopsRunning(o.Name+".Close")
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
	if SealedStd(n.origin()) {
		return fmt.Sprintf("%s is a sealed std type, which only the runtime provides", s)
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
// Lending). It reads the late facts, every Close known (checkResourceTypes).
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
		c.errorf(at, CodeType, "testing.Script[%s]: %s (stdlib.md, Testing 7)", s, why)
		return false
	}
	if c.scriptsSeen == nil {
		c.scriptsSeen = map[syntax.Node]bool{}
	}
	if !c.scriptsSeen[at] {
		// A resource argument is decided once every Close is known.
		c.scriptsSeen[at] = true
		c.scriptSites = append(c.scriptSites, resourceSite{file: c.cur, at: at, t: s})
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
	op, okOp := c.stdTestingType(inst, "Op")
	none, okNone := c.stdTestingType(inst, "NoArgs")
	if !okOp || !okNone {
		return invalid
	}
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

// stdTestingType is the named type name of the std package that declares
// the Script inst.
func (c *checker) stdTestingType(inst *Named, name string) (*Named, bool) {
	o, _ := inst.Obj.Pkg.Scope.lookup(name)
	tn, ok := o.(*TypeName)
	if !ok {
		return nil, false
	}
	n, ok := tn.Type.(*Named)
	return n, ok
}

// scriptLit checks the literal x of the Script t: it sets no field (rule 3).
func (c *checker) scriptLit(x *syntax.CompositeLit, t Type) bool {
	if len(x.Elts) == 0 && x.Base == nil {
		return true
	}
	c.errorf(x, CodeType, "a %s is made with no fields, %s{}: each operation starts with no answer, and s.Op.Answer(f) sets one (stdlib.md, Testing 9)", t, strings.TrimSpace(c.localTypeText(t)))
	return false
}
