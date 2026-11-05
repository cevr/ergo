package check

import (
	"fmt"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The host edge (check-v1 stage E4, 196x; D-E5). An `extern go path.F func
// Name(sig)` declaration binds a Go function. The checker trusts its
// written signature and does not resolve the Go target (D-E5): go build
// does.
//
// An extern function is call-only (P3; lower-v1 Q2). Any use of it other
// than a direct call is S108 with externCallOnlyText, at the reference:
// ident, qualified and explicit give it modeExtern, which only callOf
// accepts and needValue refuses. Two positions name a function without an
// operand, a machine's step (entry.go) and a context constructor entry
// (context.go); each reports the same S108 for an extern. A4's guard
// (extern_guard_test.go) places every Expr field of the syntax package. The
// rule keys on the declaration
// (isExtern), in every package, std included, so a host binding never
// escapes its call site and scope.OnExit always runs in its source frame.
//
// Prior art: Gleam's @external and ReScript's external trust a written
// type; cgo functions and Go's built-ins are call-only; Elixir makes a
// function value only by an explicit capture (&:math.sqrt/1), as P3's
// literal wrapper does.

// externCallOnlyText is the P3 message (check-v1 spec add-on), quoted
// exactly.
const externCallOnlyText = "an extern function is called directly; wrap it in a literal: func() { f() }"

// isExtern reports an extern go function, in any package.
func isExtern(f *Func) bool {
	return f != nil && f.Decl != nil && f.Decl.Extern != nil
}

// externRef is the operand of a reference to the extern f: call-only
// (modeExtern), so only callOf accepts it.
func externRef(f *Func, sig *Signature) operand {
	return operand{mode: modeExtern, typ: sig, fn: f}
}

// runtimeModule is the Go module path of the Ergo runtime. Only std binds
// it (stdlib.md, Hidden arguments).
const runtimeModule = "ergo.local/prototype/runtime"

// unsafeText is the S108 of a user extern of Go's unsafe package (check-v1
// spec, C26 target domain, amendment 2026-11-04). NORTH_STAR, Owner rules:
// unsafe stays at the FFI edge, and Ergo gets no unsafe. The refusal also
// keeps Go's qualified built-ins (unsafe.Sizeof, unsafe.Alignof, …) out of
// the domain, so every admitted target is a function or a function value
// (lower/calls.go hostStmt).
const unsafeText = "Ergo has no unsafe; unsafe stays at the FFI edge"

// checkExterns checks the declaration of each user extern (C26): its Go
// target first, then its signature. One declaration gets at most one
// diagnostic. Std's externs bind runtime/v1 with hidden arguments and are
// not in this rule.
func (c *checker) checkExterns() {
	c.checkExternParams()
	if c.pkg.std {
		return
	}
	for _, f := range c.funcs {
		if !isExtern(f) || f.Sig == nil {
			continue
		}
		c.cur = f.file
		if msg, code := externTargetRefusal(f); msg != "" {
			c.errorf(f.Decl, code, "%s", msg)
			continue
		}
		if part := c.externOutsideDomain(f); part != "" {
			c.errorf(f.Decl, CodeOutside, "%s of the extern %s is #202's (Go imports); it is not checked yet: a user extern takes and returns predeclared basic types only, with no type parameter and no rows", part, f.Name)
		}
	}
}

// checkExternParams gives every extern's parameter list, std's included,
// the binder rules of a function's (D-D3 names "parameter" at any scope):
// a prelude name and a name used twice are S108, with the diagnostics the
// same list gets on a function declaration (rows.go checkFuncBody). An
// extern has no body, so its parameters bind nothing.
func (c *checker) checkExternParams() {
	for _, f := range c.funcs {
		if !isExtern(f) || f.Sig == nil {
			continue
		}
		c.cur = f.file
		seen := map[string]bool{}
		for _, p := range f.Sig.Params {
			c.paramBinder(f, p, seen)
		}
	}
}

// externTargetRefusal is the refusal of a user extern's Go target, with its
// code, or "". The target is in the domain when its import path is in the
// pure allowlist (G9): a package of Go's standard library that does not
// reach the host. A missing function there is go build's error (D-E5).
// The Ergo runtime and Go's unsafe are S108 whatever the signature. A path
// whose first element has a dot is a module outside Go's standard library
// (Go's own rule for module paths) and keeps its #202 text; any other path
// outside the allowlist may reach the host and is S109 (hostText).
func externTargetRefusal(f *Func) (string, string) {
	path, _ := f.Decl.Extern.Split()
	if path == runtimeModule || strings.HasPrefix(path, runtimeModule+"/") {
		return fmt.Sprintf("only std binds the Ergo runtime; its functions take hidden arguments that std's bindings supply (here %s)", f.Decl.Extern.Target.Value), CodeType
	}
	if path == "unsafe" {
		return unsafeText, CodeType
	}
	first, _, _ := strings.Cut(path, "/")
	if strings.Contains(first, ".") {
		return fmt.Sprintf("the Go package %s of the extern %s is #202's (Go imports); it is not checked yet: a user extern binds a function of Go's standard library only", path, f.Name), CodeOutside
	}
	if !PureGoPackage(path) {
		return hostText(path, f.Decl.Extern.Target.Value), CodeOutside
	}
	return "", ""
}

// ---------------------------------------------------------------- the pure allowlist (G9, F7)

// pureGoPackages is the pure allowlist: the Go standard-library packages a
// user extern may bind, compared by exact import path. A function of these
// packages reads only its arguments and touches no host state (no files,
// no environment, no clock, no randomness, no process). Any other package
// may reach the host, and a user extern has no needs row (C26), so its
// call would be a hidden effect. NORTH_STAR: public effect contracts are
// explicit.
//
// The list has one owner: std's purity fact (G5b), runtime/v1's
// PureImports, the imports a pure Go body's file may have. This variable
// is the one reference to it in the checker; the batch merge with G5b
// points it at v1.PureImports. Until then it is a copy, and
// TestPureGoPackagesMatchRuntime fails when the two lists differ.
var pureGoPackages = []string{"math", "slices", "sort", "strconv", "strings", "unicode/utf8"}

// PureGoPackages returns a copy of the pure allowlist, sorted.
func PureGoPackages() []string {
	out := slices.Clone(pureGoPackages)
	slices.Sort(out)
	return out
}

// PureGoPackage reports whether path, a Go import path, is in the pure
// allowlist. The match is exact: a subpackage ("strings/x"), a longer name
// ("stringsx"), a vendored path ("vendor/strings") or a module path
// ("example.com/strings") is not in it.
func PureGoPackage(path string) bool {
	return slices.Contains(pureGoPackages, path)
}

// hostText is the S109 of a user extern whose Go package is outside the
// pure allowlist (G9). It names the std route for the package when std has
// one.
func hostText(path, target string) string {
	route := "a std capability"
	if path == "os" {
		route = "std os.Process"
	}
	return fmt.Sprintf("extern go %s reaches the host; use %s, or wait for #202 (a user extern binds only the pure Go packages %s)", target, route, strings.Join(PureGoPackages(), ", "))
}

// externOutsideDomain names the first part of f's signature outside the
// C26 domain, or "": a type parameter, a parameter or a result that is not
// a predeclared basic type, more than one result, a fails row or a needs
// row. Those types lower to the identical Go type, so the call needs no
// adapter.
func (c *checker) externOutsideDomain(f *Func) string {
	d, sig := f.Decl, f.Sig
	if len(sig.TParams) > 0 {
		return "the type parameter " + sig.TParams[0].Name
	}
	for i, p := range sig.Params {
		if !externBasic(p.Type) {
			name := p.Name
			if name == "" || name == "_" {
				name = fmt.Sprint(i + 1)
			}
			return fmt.Sprintf("the parameter %s of type %s", name, p.Type)
		}
	}
	switch {
	case len(sig.Results) > 1:
		return "the result " + (&Tuple{Types: sig.Results}).String()
	case len(sig.Results) == 1 && !externBasic(sig.Results[0]):
		return fmt.Sprintf("the result of type %s", sig.Results[0])
	}
	if d.Type.Results != nil {
		for _, r := range d.Type.Results.List {
			if _, ok := r.Type.(*syntax.ErrorRow); ok {
				return "the fails row"
			}
		}
	}
	if sig.Fallible || !c.types.resolve(sig.Errors).Empty() {
		return "the fails row"
	}
	if d.Type.Needs != nil || !c.types.resolve(sig.Uses).Empty() {
		return "the needs row"
	}
	return ""
}

// externBasic reports a predeclared basic type (types.go, the Basic kinds
// from bool to duration): not a defined type over one.
func externBasic(t Type) bool {
	b, ok := t.(*Basic)
	return ok && b.Kind > Invalid && b.Kind <= Duration
}

// ---------------------------------------------------------------- sealed std protocols (C28)

// sealedStd is t when t is a sealed std protocol: the prelude's Task, or
// an operation type a std package other than the prelude declares (the
// types the lowering represents by a runtime/v1 type, lower/std.go
// runtimeType). Only the runtime implements them, so no program value is
// converted into one (#206 lets a program implement a lawful protocol).
// The prelude's capabilities (Console, Scheduler, Clock, Random) are not
// sealed: programs provide them.
func sealedStd(t Type) *Named {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Pkg == nil || !n.Obj.Pkg.std || n.Obj.Decl == nil {
		return nil
	}
	spec, ok := n.Obj.Decl.Spec.(*syntax.InterfaceType)
	if !ok || len(spec.Union) > 0 {
		return nil
	}
	if n.Obj.Pkg.Path == std.PreludePath && n.Obj.Name != "Task" {
		return nil
	}
	return n
}

// sealedText is the S109 of a conversion into the sealed std protocol t.
func sealedText(v, t Type) string {
	return fmt.Sprintf("a value of %s does not convert to %s: it is a sealed std protocol; a program value implements it in #206", v, t)
}

// sealedRefused reports at x a refused assignment a whose target is a
// sealed std protocol, and whether it did: every site that admits by
// assignability reports that refusal with this one diagnostic (S109),
// instead of its own S108.
func (c *checker) sealedRefused(x syntax.Node, a assignment) bool {
	if a.sealed == nil {
		return false
	}
	c.errorf(x, CodeOutside, "%s", sealedText(a.from, a.sealed))
	return true
}
