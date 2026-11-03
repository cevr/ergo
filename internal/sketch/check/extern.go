package check

import (
	"fmt"
	"strings"

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
// accepts and needValue refuses. The rule keys on the declaration
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

// checkExterns checks the declaration of each user extern (C26): its Go
// target first, then its signature. One declaration gets at most one
// diagnostic. Std's externs bind runtime/v1 with hidden arguments and are
// not in this rule.
func (c *checker) checkExterns() {
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

// externTargetRefusal is the refusal of a user extern's Go target, with its
// code, or "". The target is in the domain when its import path's first
// element has no dot: Go's standard library, by Go's own rule for module
// paths. A missing package or function there is go build's error (D-E5).
func externTargetRefusal(f *Func) (string, string) {
	path, _ := f.Decl.Extern.Split()
	if path == runtimeModule || strings.HasPrefix(path, runtimeModule+"/") {
		return fmt.Sprintf("only std binds the Ergo runtime; its functions take hidden arguments that std's bindings supply (here %s)", f.Decl.Extern.Target.Value), CodeType
	}
	first, _, _ := strings.Cut(path, "/")
	if strings.Contains(first, ".") {
		return fmt.Sprintf("the Go package %s of the extern %s is #202's (Go imports); it is not checked yet: a user extern binds a function of Go's standard library only", path, f.Name), CodeOutside
	}
	return "", ""
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
