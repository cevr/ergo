package check

import (
	"strings"

	v1 "ergo.local/prototype/runtime/v1"
)

// The body contract (GLOSSARY.md, Body contract; docs/spec/stdlib.md, Std
// is ordinary): the reviewed facts of the Go body an extern declaration
// binds. This file is the one place the checker reads them from
// runtime/v1 (TestCompilerReadsNoStdMembership exempts it from the rule
// on runtime/v1's fact tables, and holds it to every other rule). A fact
// follows the body the declaration names, so an extern of any package
// that binds the same body gets the same facts: the declaring package
// is never read (STD-NOPRIV NP4b). Whether a package may bind the
// runtime at all is checkExterns' rule, not this file's.
//
// Prior art: OCaml's `external … = "%primitive"` gives a primitive's
// meaning to any module that writes the declaration; GHC's primops are
// keyed on the primitive, not on the module that imports it.

// Body is the body contract of one extern: the facts of its bound Go
// body. The zero Body is a body the checker knows nothing about (a
// target outside runtime/v1, or a function that is no extern).
type Body struct {
	// Runtime is the runtime/v1 name of the bound body ("Go",
	// "Queue.Push"), or "" when the target is not a runtime/v1 body.
	Runtime string
	// Known reports that runtime/v1 has the body and its shape (v1.BodyOf).
	Known bool
	// Pure: the body is marked //ergo:pure and passed the reviewed rules
	// (v1.Body.Pure): a call runs no capability, starts no task and calls
	// no unknown code.
	Pure bool
	// Host: the body is marked //ergo:host (v1.Body.Host).
	Host bool
	// Inert: the body reads its hidden arguments but acts on nothing
	// outside the value it makes (v1.Inert), so a durable step may call it.
	Inert bool
	// Hidden are the body's hidden parameters, in their order.
	Hidden []v1.Hidden
	// Value: the body takes no hidden argument, no type parameter and no
	// function, and its result is no deferred Seq, so the extern may be a
	// function value (externValue; G1, P3).
	Value bool
	// MovesOnly: a generic body that only moves its type parameters'
	// values (v1.MovesOnly).
	MovesOnly bool
	// Moves: the body runs the callback parameter MovingParam on a child
	// task, whose captures move to its root (v1.MovingBody); Repeated when
	// the callback can run again.
	Moves       bool
	MovingParam int
	Repeated    bool
	// Keeps: the owner of the moving callback (v1.CallbackKeeper): -1 is
	// the result, any other index a parameter.
	Keeps  bool
	Keeper int
}

// BodyOf returns the body contract of the extern f, the zero Body for
// anything else. It reads the declaration's target only.
func BodyOf(f *Func) Body {
	if f == nil || f.Decl == nil || f.Decl.Extern == nil {
		return Body{}
	}
	path, name := f.Decl.Extern.Split()
	if path != runtimeModule+"/v1" {
		if body, ok := ModuleBody(f); ok {
			// A module's own body: its shape and policy, call-only. Its
			// purity is not read yet (a follow-up of NP4d): no call of it
			// is pure.
			return Body{Known: true, Host: body.Host, Hidden: body.Hidden}
		}
		return Body{}
	}
	b := Body{Runtime: name, MovesOnly: v1.MovesOnly(name), Inert: v1.Inert(name)}
	if body, err := v1.BodyOf(name); err == nil {
		b.Known = true
		b.Pure, b.Host, b.Hidden = body.Pure, body.Host, body.Hidden
		b.Value = valueBody(body)
	}
	b.MovingParam, b.Repeated, b.Moves = v1.MovingBody(name)
	b.Keeper, b.Keeps = v1.CallbackKeeper(name)
	return b
}

// ModuleBody is the Go body that the extern f of a trusted package binds
// in its module's own Go package (docs/spec/interop.md, Trusted Go 3), and
// whether f binds one. The checker read it from the package's Go source
// when it checked f's package (moduleDoor), so the lowering calls it with
// its hidden arguments and by its waiting policy, as it calls a runtime/v1
// body.
func ModuleBody(f *Func) (v1.Body, bool) {
	if f == nil || f.Pkg == nil || f.Decl == nil || f.Decl.Extern == nil {
		return v1.Body{}, false
	}
	path, name := f.Decl.Extern.Split()
	b, ok := f.Pkg.goBodies[path+"."+name]
	return b, ok
}

// valueBody reports a body a function value may stand for: no hidden
// argument (the call-only reason of P3, so scope.OnExit still runs in its
// source frame), no type parameter, no function parameter, and no deferred
// Seq result.
func valueBody(body v1.Body) bool {
	if len(body.Hidden) > 0 || len(body.TypeParams) > 0 {
		return false
	}
	for _, p := range body.Params {
		if p.Func != nil {
			return false
		}
	}
	return !(len(body.Results) == 1 && strings.HasPrefix(body.Results[0], "iter.Seq["))
}

// releasesResult reports an extern whose bound body takes the hidden
// ResultRelease (v1.Releaser; L8 B4, Gap 47): it releases a successful
// result it drops, so its call records the result's Drop.
func releasesResult(f *Func) bool {
	for _, h := range BodyOf(f).Hidden {
		if h == v1.ResultRelease {
			return true
		}
	}
	return false
}

// OpBody is the body contract of an operation of a named type whose
// operations are runtime/v1 methods.
type OpBody struct {
	// Runtime is the bound method's runtime/v1 name ("Task.Close"), or "".
	Runtime string
	// StopsRunning: the operation is a release that stops code still
	// running (v1.StopsRunning).
	StopsRunning bool
	// DrainsLends: the operation does not return while its keeper's
	// callback still runs (v1.DrainsLends).
	DrainsLends bool
}

// OpBodyOf returns the body contract of the operation op of the named type
// t. The operations of a type are runtime/v1 methods when the type's
// package binds runtime/v1 (an extern of it targets a runtime/v1 body) and
// the runtime has a method of the type's name and op's name, the
// convention the lowering's runtime calls follow (docs/spec/stdlib.md,
// Hidden arguments). It reads declarations only, never the package's std
// mark; the type's Go type joins its declaration with NP4c.
func OpBodyOf(t Type, op string) OpBody {
	n, ok := t.(*Named)
	if !ok || n.origin().Obj == nil || n.origin().Obj.Pkg == nil {
		return OpBody{}
	}
	o := n.origin().Obj
	name := o.Name + "." + op
	if _, err := v1.BodyOf(name); err != nil || !bindsRuntime(o.Pkg) {
		return OpBody{}
	}
	return OpBody{Runtime: name, StopsRunning: v1.StopsRunning(name), DrainsLends: v1.DrainsLends(name)}
}

// bindsRuntime reports whether an extern of pkg targets a runtime/v1 body.
func bindsRuntime(pkg *Package) bool {
	for _, f := range pkg.Funcs {
		if f.Decl != nil && f.Decl.Extern != nil {
			if path, _ := f.Decl.Extern.Split(); path == runtimeModule+"/v1" {
				return true
			}
		}
	}
	return false
}
