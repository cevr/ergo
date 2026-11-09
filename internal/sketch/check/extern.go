package check

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// The host edge (check-v1 stage E4, 196x; D-E5). An `extern go path.F func
// Name(sig)` declaration binds a Go function. The checker trusts its
// written signature and does not resolve the Go target (D-E5): go build
// does.
//
// An extern function was call-only (P3; lower-v1 Q2). Since G1 an extern
// that externValue admits is also a function value; any other use of any
// other extern is S108 with externValueText (K4), at the reference: ident,
// qualified and explicit give it modeExtern, which callOf accepts and
// needValue refuses unless externValue admits it. Two positions name a function without an
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

// externValueText is the P3 message at a call-only extern (one externValue
// refuses) used as a value, an argument or a let's value (K4, G-ex-f-3): it
// names the extern and the literal to pass, its parameters and results
// written out, such as "f is an extern and is not a value; pass
// func(s string) string { f(s) }". A reference it cannot spell (a generic
// extern, several results) keeps externCallOnlyText.
func externValueText(op operand) string {
	sig, _ := op.typ.(*Signature)
	name := exprText(op.expr)
	if sig == nil || name == "this expression" || len(sig.Results) > 1 || len(sig.TParams) > 0 {
		// A generic extern's literal would name its type parameters,
		// which the caller instantiates: the plain P3 text.
		return externCallOnlyText
	}
	var params, args []string
	for i, p := range sig.Params {
		n := p.Name
		if n == "" || n == "_" {
			n = fmt.Sprintf("p%d", i)
		}
		params = append(params, n+" "+p.Type.String())
		args = append(args, n)
	}
	lit := "func(" + strings.Join(params, ", ") + ")"
	if len(sig.Results) == 1 {
		lit += " " + sig.Results[0].String()
	}
	if errs := sig.Errors.String(); errs != "" {
		lit += " fails " + errs
	}
	if uses := sig.Uses.String(); uses != "" {
		lit += " " + requirementKeyword + " " + uses
	}
	return fmt.Sprintf("%s is an extern and is not a value; pass %s { %s(%s) } (grammar.md, Extern)", name, lit, name, strings.Join(args, ", "))
}

// isExtern reports an extern go function, in any package.
func isExtern(f *Func) bool {
	return f != nil && f.Decl != nil && f.Decl.Extern != nil
}

// externValue reports whether a reference to the extern f may be a function
// value (G1, gx-extern-values): Gleam's @external and ReScript's external
// are values, and Go passes strings.ToUpper as one. A user extern always
// may: its signature is in the C26 domain, so the lowering wraps its Go
// target in the uniform function shape, and a callback parameter in an
// adapter. A std extern may when its runtime/v1 body takes no hidden
// argument (no frame, call site, task or scheduler: the call-only reason of
// P3, so scope.OnExit still runs in its source frame), no function and no
// type parameter, and does not return a deferred Seq. A generic extern and
// a prelude intrinsic stay call-only (externCallOnlyText).
func externValue(f *Func) bool {
	if !isExtern(f) || f.Sig == nil || len(f.Sig.TParams) > 0 {
		return false
	}
	if !f.Pkg.Std() {
		return true
	}
	if f.Pkg.Path == std.PreludePath {
		return false
	}
	path, name := f.Decl.Extern.Split()
	if path != runtimeModule+"/v1" {
		return false
	}
	body, err := v1.BodyOf(name)
	if err != nil || len(body.Hidden) > 0 || len(body.TypeParams) > 0 {
		return false
	}
	for _, p := range body.Params {
		if p.Func != nil {
			return false
		}
	}
	return !(len(body.Results) == 1 && strings.HasPrefix(body.Results[0], "iter.Seq["))
}

// externRef is the operand of a reference to the extern f: call-only
// (modeExtern), so callOf accepts it and needValue makes it a value only
// when externValue admits it.
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
			c.errorf(f.Decl, CodeOutside, "%s of the extern %s is #202's (Go imports); it is not checked yet: a user extern takes and returns predeclared basic types only, and callbacks over them, with no type parameter, no needs row and no fails row but go.Error", part, f.Name)
			continue
		}
		if msg := externErrorRefusal(f); msg != "" {
			c.errorf(f.Decl, CodeType, "%s", msg)
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
// outside the allowlist, and a member of v1.PureImportsExcept, may reach
// the host and is S109 (hostText), which names import go, the route that
// charges go.Foreign (IG2). The reviewed list is the one fact: no table
// says a package is pure but not listed (IG2 deleted v1.PureUnlisted).
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
	// A member of an allowed package that reaches the host (filepath.Abs
	// reads the working directory) has the same refusal as its package
	// would; the list's one owner is v1.PureImportsExcept.
	if _, name := f.Decl.Extern.Split(); slices.Contains(v1.PureImportsExcept[path], name) {
		return hostText(path, f.Decl.Extern.Target.Value), CodeOutside
	}
	return "", ""
}

// ---------------------------------------------------------------- the pure allowlist (G9, F7)

// The pure allowlist is the set of Go standard-library packages a user
// extern may bind, compared by exact import path. A function of these
// packages reads only its arguments and touches no host state (no files,
// no environment, no clock, no randomness, no process). Any other package
// may reach the host, and a user extern has no needs row (C26), so its
// call would be a hidden effect. NORTH_STAR: public effect contracts are
// explicit.
//
// The list has one owner, std's purity fact (G5b): v1.PureImports, the
// imports a pure Go body's file may have. A user extern and a pure std
// body trust the same packages. The checker reads that list here and
// declares no table of its own (TestNoCheckerPackageTable guards it).

// PureGoPackages returns a copy of the pure allowlist, sorted.
func PureGoPackages() []string {
	out := slices.Clone(v1.PureImports)
	slices.Sort(out)
	return out
}

// PureGoPackage reports whether path, a Go import path, is in the pure
// allowlist. The match is exact: a subpackage ("strings/x"), a longer name
// ("stringsx"), a vendored path ("vendor/strings") or a module path
// ("example.com/strings") is not in it.
func PureGoPackage(path string) bool {
	return slices.Contains(v1.PureImports, path)
}

// hostText is the S109 of a user extern whose Go target is outside the
// pure allowlist (G9): an unreviewed function may reach the host, so it is
// called through import go, which charges go.Foreign (IG2).
func hostText(path, target string) string {
	return fmt.Sprintf("extern go %s is not on the reviewed pure list, so it may reach the host; call it through import go %s, which charges go.Foreign (a user extern binds only the pure Go packages %s)", target, path, strings.Join(PureGoPackages(), ", "))
}

// externOutsideDomain names the first part of f's signature outside the
// C26 domain, or "": a type parameter, a parameter that is neither a
// predeclared basic type nor a callback (externCallback, G1), a result
// that is not basic, more than one result, a fails row other than go.Error
// (G4) or a needs row.
// Basic types lower to the identical Go type, so only a callback needs an
// adapter.
func (c *checker) externOutsideDomain(f *Func) string {
	d, sig := f.Decl, f.Sig
	if len(sig.TParams) > 0 {
		return "the type parameter " + sig.TParams[0].Name
	}
	for i, p := range sig.Params {
		if !externBasic(p.Type) && !c.externCallback(p.Type) {
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
	if errs := c.types.resolve(sig.Errors); sig.Fallible && !externGoErrorRow(errs) || !sig.Fallible && !errs.Empty() {
		return "the fails row"
	}
	if d.Type.Needs != nil || !c.types.resolve(sig.Uses).Empty() {
		return "the needs row"
	}
	return ""
}

// externGoErrorRow reports the one fails row a user extern may write (G4,
// decision O14): exactly go.Error. Its Go target's last result is error,
// and the lowering turns a non-nil error into go.Error{Text: err.Error()}.
func externGoErrorRow(errs *Row) bool {
	elems := errs.Elems()
	if len(elems) != 1 {
		return false
	}
	n, ok := elems[0].(*Named)
	return ok && IsGoError(n)
}

// IsGoError reports the predeclared go.Error.
func IsGoError(n *Named) bool {
	return n != nil && n.Obj != nil && n.Obj.Pkg == universeOnce.goPkg && n.Obj.Name == "Error"
}

// externErrorRefusal is the refusal of a user extern whose fails row does
// not match its Go target (G4), or "": a target whose last result is error
// must declare fails go.Error, and a target with no error result must not.
// The checker does not read Go's sources (D-E5): it reads the target's
// embedded binding, whose row is fails go.Error exactly when Go's last
// result is error (IG1; TestGoStdBindingsMatchToolchain holds the bindings
// to the toolchain). A name the binding lacks returns no error; a name Go
// lacks is go build's error.
func externErrorRefusal(f *Func) string {
	path, name := f.Decl.Extern.Split()
	returnsError := stdGoReturnsError(path, name)
	switch {
	case returnsError && !f.Sig.Fallible:
		return fmt.Sprintf("extern go %s returns an error; declare it in the row: fails go.Error", f.Decl.Extern.Target.Value)
	case !returnsError && f.Sig.Fallible:
		return fmt.Sprintf("extern go %s returns no error; a user extern fails go.Error only when its Go function's last result is error", f.Decl.Extern.Target.Value)
	}
	return ""
}

// externCallback reports a parameter type a user extern may take besides a
// basic type (G1): an unnamed function type whose parameters are basic,
// with at most one basic result and no rows, as strings.Map's
// func(rune) rune. The lowering passes an adapter that calls the Ergo
// function value with the caller's environment (lower, hostCallback), so
// the callback polls its caller's task. A callback with a needs or fails
// row would run a capability or a failure through Go code that cannot
// carry it, so it stays #202's.
func (c *checker) externCallback(t Type) bool {
	s, ok := t.(*Signature)
	if !ok || len(s.TParams) > 0 || len(s.Results) > 1 {
		return false
	}
	for _, p := range s.Params {
		if !externBasic(p.Type) {
			return false
		}
	}
	if len(s.Results) == 1 && !externBasic(s.Results[0]) {
		return false
	}
	return !s.Fallible && c.types.resolve(s.Errors).Empty() && c.types.resolve(s.Uses).Empty()
}

// externBasic reports a predeclared basic type (types.go, the Basic kinds
// from bool to duration): not a defined type over one.
func externBasic(t Type) bool {
	b, ok := t.(*Basic)
	return ok && b.Kind > Invalid && b.Kind <= Duration
}

// ---------------------------------------------------------------- sealed std protocols (C28)

// sealedStd is t when t is a sealed std protocol, else nil (SealedStd).
func sealedStd(t Type) *Named {
	n, ok := t.(*Named)
	if !ok || !SealedStd(n) {
		return nil
	}
	return n
}

// SealedStd reports a sealed std protocol: an operation type of the
// prelude or a std package that the result of a std extern names, such as
// the prelude's Task (task.Go) and queue.Queue (queue.New). A std extern
// gives a runtime value whose identity a program must not forge, so only
// the runtime implements the type: no program value converts into it
// (S109; #206), and the lowering represents it by its runtime/v1 type
// (lower/std.go runtimeType). Any other std operation type is open, as
// the prelude's Console and std os.Process are: a program value implements
// it (stdlib.md, os 5; std-1 plan 2.2).
func SealedStd(n *Named) bool {
	if n == nil || n.Obj == nil || n.Obj.Pkg == nil || !n.Obj.Pkg.std || n.Obj.Decl == nil {
		return false
	}
	spec, ok := n.Obj.Decl.Spec.(*syntax.InterfaceType)
	if !ok || len(spec.Union) > 0 {
		return false
	}
	return stdExternResults()[n.Obj.Pkg.Path+"."+n.Obj.Name]
}

// stdExternResults is stdExternResultSet, computed once per process.
var stdExternResults = sync.OnceValue(stdExternResultSet)

// stdExternResultSet is the set, as path.Name, of the std types the results
// of std extern functions name, at any depth of the result and its fails
// row (not its needs row), read from the std sources with the parser. An
// unqualified name is the declaring package's type when it declares one,
// else the prelude's. It is a named function, not a closure, so it is a
// Parse site of its own in the provenance manifest; it returns only names,
// so no node it parses leaves it.
func stdExternResultSet() map[string]bool {
	sources := map[string]map[string]string{std.PreludePath: std.Prelude()}
	for _, path := range std.Packages() {
		if files, ok := std.Package(path); ok {
			sources[path] = files
		}
	}
	set := map[string]bool{}
	for path, files := range sources {
		var parsed []*syntax.File
		declared := map[string]bool{}
		for _, src := range files {
			f, err := syntax.Parse(src)
			if err != nil {
				// Std always parses; a file that did not would unseal
				// what its externs return.
				panic(fmt.Sprintf("std package %s does not parse: %v", path, err))
			}
			parsed = append(parsed, f)
			for _, d := range f.Decls {
				if td, ok := d.(*syntax.TypeDecl); ok {
					declared[td.Name.Name] = true
				}
			}
		}
		for _, f := range parsed {
			imports := map[string]string{}
			for _, d := range f.Decls {
				id, ok := d.(*syntax.ImportDecl)
				if !ok {
					continue
				}
				for _, s := range id.Specs {
					p := strings.Trim(s.Path.Value, "\"`")
					name := p[strings.LastIndex(p, "/")+1:]
					if s.Name != nil {
						name = s.Name.Name
					}
					imports[name] = p
				}
			}
			for _, d := range f.Decls {
				fd, ok := d.(*syntax.FuncDecl)
				if !ok || fd.Extern == nil || fd.Type == nil || fd.Type.Results == nil {
					continue
				}
				for _, r := range fd.Type.Results.List {
					syntax.Inspect(r.Type, func(x syntax.Node) bool {
						switch x := x.(type) {
						case *syntax.SelectorExpr:
							if q, ok := x.X.(*syntax.Ident); ok {
								if p, ok := imports[q.Name]; ok {
									set[p+"."+x.Sel.Name] = true
								}
							}
							return false
						case *syntax.Ident:
							if declared[x.Name] {
								set[path+"."+x.Name] = true
							} else {
								set[std.PreludePath+"."+x.Name] = true
							}
						}
						return true
					})
				}
			}
		}
	}
	return set
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
