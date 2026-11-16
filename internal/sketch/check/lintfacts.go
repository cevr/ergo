package check

import (
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Facts for lint (internal/sketch/lint). Each reads what the checker has
// already decided; none decides anything new, so a lint rule can match a
// resolved object or a fact and never an identifier's spelling.

// CallAddsFootprint reports whether the call x adds to its frame's
// footprint (rows.md, Footprints): a capability operation, a callee whose
// footprint is not empty or is unknown, a task start or a with. It follows
// the footprint rule the checker's own purity diagnostics name
// (impureWhy).
func CallAddsFootprint(info *Info, x *syntax.CallExpr) bool {
	return impureWhy(info, x) != ""
}

// Exported reports whether f is visible to importers of its package
// (grammar.md, Visibility).
func (f *Func) Exported() bool { return isExported(f.Name) }

// WritesErrors reports whether f's error row is written in its
// declaration, not inferred from its body (rows.md, Error rows 6).
func (f *Func) WritesErrors() bool { return !f.inferErrors }

// ImplementationTypes are the types of the providers the with converts
// into the capabilities it discharges (Entries), its later operands
// included: a provider whose type is the capability itself (a capability
// value passed down) converts nothing and is not listed.
func (w *With) ImplementationTypes() []Type {
	var out []Type
	for ; w != nil; w = w.Next {
		for _, e := range w.Entries {
			for _, conv := range e.Conversions {
				if conv.Kind != 0 && conv.From != nil {
					out = append(out, conv.From)
				}
			}
		}
	}
	return out
}

// ProgramType reports a type declared in a program package: not std's,
// not a host binding's, not predeclared.
func ProgramType(t Type) bool {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*Named)
	if !ok || n.origin().Obj == nil || n.origin().Obj.Pkg == nil {
		return false
	}
	pkg := n.origin().Obj.Pkg
	return !pkg.Std() && !pkg.Host() && pkg != universeOnce.goPkg
}

// Import is one import of a file: its spec, the name the file binds, and
// the package it resolved to (a std, program or host package).
type Import struct {
	Spec *syntax.ImportSpec
	Name string
	Pkg  *Package
}

// ImportsOf is the resolved imports of f, a file of p, in source order. A
// broken import is left out.
func (p *Package) ImportsOf(f *syntax.File) []Import {
	var out []Import
	for _, fi := range p.files {
		if fi.syntax != f {
			continue
		}
		for _, pn := range fi.imports {
			if !pn.broken && pn.Pkg != nil {
				out = append(out, Import{Spec: pn.spec, Name: pn.Name, Pkg: pn.Pkg})
			}
		}
	}
	return out
}

// FileBinds reports whether name is bound at the top of f, a file of p: an
// import name of the file, a declaration of the package, or a prelude
// name. A fix that adds an import of that name would collide with it.
func (p *Package) FileBinds(f *syntax.File, name string) bool {
	for _, fi := range p.files {
		if fi.syntax == f && fi.scope != nil {
			if o, _ := fi.scope.lookup(name); o != nil {
				return true
			}
		}
	}
	if p.Scope != nil {
		if o, _ := p.Scope.lookup(name); o != nil {
			return true
		}
	}
	return false
}

// DocOf is the doc comment of o's declaration and the package that
// declares it: a function, a type or a named context. Every other object
// (a local, a package let, a builtin) has none.
func DocOf(o Object) (*syntax.CommentGroup, *Package) {
	switch o := o.(type) {
	case *Func:
		if o.Decl != nil {
			return o.Decl.Doc, o.Pkg
		}
	case *TypeName:
		if o.Decl != nil {
			return o.Decl.Doc, o.Pkg
		}
	case *Context:
		if o.Decl != nil {
			return o.Decl.Doc, o.pkg
		}
	}
	return nil, nil
}

// QualifiedMember is the package member a qualified name pkg.Name names,
// which Info.Uses does not hold; nil when x is not one.
func QualifiedMember(info *Info, x *syntax.SelectorExpr) Object {
	id, ok := x.X.(*syntax.Ident)
	if !ok {
		return nil
	}
	pn, ok := info.Uses[id].(*PkgName)
	if !ok || pn.Pkg == nil {
		return nil
	}
	o, _ := pn.Pkg.member(x.Sel.Name)
	return o
}

// OpDoc is the doc comment of the operation an operation call x calls
// (CallOp or CallProviderOp) and the package of its operation type.
func OpDoc(info *Info, x *syntax.CallExpr) (*syntax.CommentGroup, *Package) {
	call := info.Calls[x]
	if call == nil || call.Op == nil {
		return nil, nil
	}
	recv := call.Recv
	if call.Kind == CallProviderOp {
		if sel, ok := unparen(x.Fun).(*syntax.SelectorExpr); ok {
			recv = sel.X
		}
	}
	if call.Kind != CallOp && call.Kind != CallProviderOp || recv == nil {
		return nil, nil
	}
	t := info.Types[recv]
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*Named)
	if !ok || n.origin().Obj == nil || n.origin().Obj.Decl == nil {
		return nil, nil
	}
	obj := n.origin().Obj
	it, ok := obj.Decl.Spec.(*syntax.InterfaceType)
	if !ok {
		return nil, nil
	}
	for _, f := range it.Methods {
		for _, id := range f.Names {
			if id.Pos() == call.Op.Pos {
				return f.Doc, obj.Pkg
			}
		}
	}
	return nil, nil
}

// StdRole is a std declaration a lint rule reads by its identity: the
// package and the name std declares it with, never a call's spelling.
type StdRole int

const (
	RoleNone    StdRole = iota
	RoleRace            // tasks.Race
	RoleCollect         // slices.Collect, slices.Sorted: read a Seq to its end
	RoleAppend          // the prelude's Append
	RoleAfter           // timeout.After
)

// stdRoles names each role's declarations: the std package path and the
// declared name. TestStdRolesResolve (lint) checks that each resolves.
var stdRoles = []struct {
	path, name string
	role       StdRole
}{
	{"tasks", "Race", RoleRace},
	{"slices", "Collect", RoleCollect},
	{"slices", "Sorted", RoleCollect},
	{std.PreludePath, "Append", RoleAppend},
	{"timeout", "After", RoleAfter},
}

// RoleOf is the std role of the function f, RoleNone for any function
// that is not one of std's role declarations.
func RoleOf(f *Func) StdRole {
	if f == nil || !f.Pkg.Std() {
		return RoleNone
	}
	for _, r := range stdRoles {
		if f.Pkg.Path == r.path && f.Name == r.name {
			return r.role
		}
	}
	return RoleNone
}

// IsSchedulerSleep reports whether x calls the Sleep operation of the
// prelude's Scheduler capability, on the provider a with binds.
func IsSchedulerSleep(info *Info, x *syntax.CallExpr) bool {
	call := info.Calls[x]
	if call == nil || call.Kind != CallProviderOp || call.Op == nil {
		return false
	}
	sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
	if !ok {
		return false
	}
	n, ok := info.Types[sel.X].(*Named)
	return ok && isStdObj(n.origin().Obj, std.PreludePath, "Scheduler") && call.Op.Name == "Sleep"
}

// IsTimeoutExpired reports std timeout.Expired, the failure of a deadline.
func IsTimeoutExpired(t Type) bool {
	n, ok := t.(*Named)
	return ok && isStdObj(n.origin().Obj, "timeout", "Expired")
}

// IsTimeoutPackage reports std's timeout package.
func IsTimeoutPackage(p *Package) bool { return p.Std() && p.Path == "timeout" }

func isStdObj(o *TypeName, path, name string) bool {
	return o != nil && o.Pkg.Std() && o.Pkg.Path == path && o.Name == name
}
