package check

import (
	"fmt"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
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
	// FrameRelease: the body registers its callback as an exit action of
	// the caller's frame (v1.Body.Release "frame"; scope.OnExit).
	FrameRelease bool
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
		b.FrameRelease = body.Release == "frame"
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

// runtimeTypeDecl reports a type declaration that names a Go type of the
// Ergo runtime as its representation (`extern go
// ergo.local/prototype/runtime/v1.Closed type Closed {}`; docs/spec/stdlib.md,
// The body contract; STD-NOPRIV NP4c). It is an Ergo declaration, checked as
// one, and checkRuntimeTypes holds it to its Go type's shape. Any other
// extern type is a binding's host type (#202).
func runtimeTypeDecl(d syntax.Decl) bool {
	td, ok := d.(*syntax.TypeDecl)
	if !ok || td.Extern == nil || td.Extern.Target == nil {
		return false
	}
	path, _ := td.Extern.Split()
	return syntax.IsRuntimeTarget(path)
}

// checkRuntimeTypes refuses each type declaration of the package that names
// a runtime Go type it may not (runtimeTypeRefusal).
func (c *checker) checkRuntimeTypes() {
	for _, tn := range c.pkg.Types {
		if tn.Decl == nil || !runtimeTypeDecl(tn.Decl) {
			continue
		}
		c.cur = tn.file
		if msg := c.runtimeTypeRefusal(tn); msg != "" {
			c.errorf(tn.Decl, CodeType, "%s", msg)
		}
	}
}

// runtimeTypeRefusal is why the declaration of tn may not name its Go type,
// or "". The package's module is trusted, as for a runtime body (the Go is
// unchecked), and the Go type is an exported type of runtime/v1 of the
// declaration's arity: a struct with the record's fields, in order, for a
// record; for an operation type, any type, whose methods are its operations
// (OpBodyOf), and the operation type is sealed, so only the runtime's value
// implements it.
func (c *checker) runtimeTypeRefusal(tn *TypeName) string {
	target := tn.Decl.Extern.Target.Value
	if !c.pkg.trusted {
		if c.pkg.module == "" {
			return fmt.Sprintf("extern go %s names a Go type of the Ergo runtime, which a package names only when the root module trusts its Go; this package is in no module, so no ergo.mod can trust it (docs/spec/interop.md, Trusted Go)", target)
		}
		return fmt.Sprintf("extern go %s names a Go type of the Ergo runtime, which a package names only when the root module trusts its Go; write the line `trust %s` in the root module's ergo.mod (docs/spec/interop.md, Trusted Go)", target, c.pkg.module)
	}
	path, name := tn.Decl.Extern.Split()
	if path != runtimeModule+"/v1" {
		return fmt.Sprintf("extern go %s: a type names a Go type of %s/v1 (docs/spec/stdlib.md, The body contract)", target, runtimeModule)
	}
	gt, ok := v1.TypeOf(name)
	if !ok {
		return fmt.Sprintf("extern go %s: runtime/v1 has no exported type %s (docs/spec/stdlib.md, The body contract)", target, name)
	}
	n, ok := tn.Type.(*Named)
	if !ok {
		return fmt.Sprintf("extern go %s: %s is not a declared type", target, tn.Name)
	}
	// A row or failure parameter has no Go form (types.go in the
	// lowering), so only the type parameters count.
	arity := 0
	for _, p := range n.TParams {
		if p.Kind == KindType {
			arity++
		}
	}
	if gt.Arity != arity {
		return fmt.Sprintf("extern go %s: %s has %d type parameters and v1.%s has %d", target, tn.Name, arity, name, gt.Arity)
	}
	switch spec := tn.Decl.Spec.(type) {
	case *syntax.StructType:
		st, _ := n.Underlying().(*Struct)
		var fields []string
		if st != nil {
			for _, f := range st.Fields {
				fields = append(fields, f.Name)
			}
		}
		if st == nil || !gt.Struct || !slices.Equal(fields, gt.Fields) {
			return fmt.Sprintf("extern go %s: the record %s has fields %v, and v1.%s is not a struct with those fields in that order (docs/spec/stdlib.md, The body contract)", target, tn.Name, fields, name)
		}
	case *syntax.InterfaceType:
		if len(spec.Union) > 0 || !Sealed(n) {
			return fmt.Sprintf("extern go %s: an operation type that names a Go type declares an unexported operation, so no other package's value implements it (docs/spec/stdlib.md, The body contract)", target)
		}
	case *syntax.Ident:
		// A defined type over a basic type, a refinement too (os.Code):
		// the Go type is a defined type over the same Go basic type. The
		// refinement's predicate stays the checker's, as for any
		// refinement.
		b, _ := n.Underlying().(*Basic)
		if b == nil || b.Kind == Duration || gt.Basic != b.Name {
			return fmt.Sprintf("extern go %s: %s is over %s, and v1.%s is not a defined type over the Go type %s (docs/spec/stdlib.md, The body contract)", target, tn.Name, n.Underlying(), name, n.Underlying())
		}
	default:
		return fmt.Sprintf("extern go %s: only a record, an operation type or a defined basic type names a Go type (docs/spec/stdlib.md, The body contract)", target)
	}
	return ""
}

// GoTypeOf is the runtime/v1 Go type that the declaration of the named
// type n (or of its origin) names, and whether it names one: the type is
// that Go type in the lowered program (lower/std.go runtimeType). It reads
// the declaration only (STD-NOPRIV NP4c).
func GoTypeOf(n *Named) (string, bool) {
	if n == nil || n.origin().Obj == nil || n.origin().Obj.Decl == nil || !runtimeTypeDecl(n.origin().Obj.Decl) {
		return "", false
	}
	path, name := n.origin().Obj.Decl.Extern.Split()
	return name, path == runtimeModule+"/v1"
}

// embedTreeBody is the runtime's embedded-tree constructor, which the
// lowering calls for embedded[T] of a tree (lower/embed.go).
const embedTreeBody = "EmbeddedFS"

// embedTree reports a type embedded[T] builds as a tree of assets: its
// declaration names the Go type the runtime's embedded-tree constructor
// returns (embed.FS is `extern go …/runtime/v1.FS type FS`). It reads the
// declaration and the constructor's body, never a std name, so a trusted
// package's type that names the same Go type is a tree too (STD-NOPRIV
// NP3d; docs/spec/stdlib.md, The body contract).
func embedTree(n *Named) bool {
	goName, ok := GoTypeOf(n)
	if !ok {
		return false
	}
	body, err := v1.BodyOf(embedTreeBody)
	return err == nil && len(body.Results) == 1 && body.Results[0] == "*"+goName
}

// providerFieldRefusal is why the type field cannot be a derived
// provider's field type, or "" (patterns.md, Provider plans 3): the
// lowering makes each field with the Go constructor NewG and answers a
// call with G.Call, so the field type's declaration names a runtime Go
// type G of two type parameters that has both bodies (testing.Op names
// v1.Op). Any module may use testing.Op, or name G with trust.
func providerFieldRefusal(field *Named) string {
	g, ok := GoTypeOf(field)
	if !ok {
		return fmt.Sprintf("its field type %s names no runtime Go type, whose constructor and Call answer the operations", field)
	}
	if gt, ok := v1.TypeOf(g); !ok || gt.Arity != 2 {
		return fmt.Sprintf("its field type's Go type v1.%s is not a type of two type parameters", g)
	}
	for _, b := range []string{ProviderFieldNew(g), g + ".Call"} {
		if _, err := v1.BodyOf(b); err != nil {
			return fmt.Sprintf("its field type's Go type v1.%s has no body %s", g, b)
		}
	}
	return ""
}

// ProviderFieldNew is the runtime constructor of a provider field whose Go
// type is g (v1.NewOp for v1.Op).
func ProviderFieldNew(g string) string { return "New" + g }
