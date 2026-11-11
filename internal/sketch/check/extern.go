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
	if !isExtern(f) || f.Sig == nil || len(f.Sig.TParams) > 0 || GoBuiltin(f) != "" {
		return false
	}
	if !f.Pkg.Std() {
		// IG4: an extern outside the C26 domain (a Level 2 adapter, a
		// host type) is call-only, as an import go function is.
		return f.goExtern == nil || f.goExtern.Basic
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
	for _, f := range c.funcs {
		if b := GoBuiltin(f); b != "" && f.Sig != nil {
			c.cur = f.file
			if msg := builtinRefusal(f, b); msg != "" {
				c.errorf(f.Decl, CodeType, "%s", msg)
			}
		}
	}
	if c.pkg.std {
		return
	}
	for _, f := range c.funcs {
		if !isExtern(f) || f.Sig == nil || GoBuiltin(f) != "" {
			continue
		}
		c.cur = f.file
		if msg, code := externTargetRefusal(f); msg != "" {
			c.errorf(f.Decl, code, "%s", msg)
			continue
		}
		if msg, code := c.externAdapter(f); msg != "" {
			c.errorf(f.Decl, code, "%s", msg)
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
// code, or "", before its binding is read (externAdapter). The Ergo
// runtime and Go's unsafe are S108 whatever the signature. A path whose
// first element has a dot is a module outside Go's standard library (Go's
// own rule for module paths) and keeps its #202 text: IG7 binds module
// packages through import go; a Level 2 adapter of one is a follow-up.
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
// The empty path is Go's universe: an extern of a Go builtin (builtin.go),
// which calls no Ergo code and no host.
func PureGoPackage(path string) bool {
	return path == "" || slices.Contains(v1.PureImports, path)
}

// ---------------------------------------------------------------- Level 2 adapters (IG4)

// GoAdapter is how a user extern's written signature adapts the Level 1
// signature that its Go target's binding gives (docs/spec/interop.md,
// Level 2 adapters; #9). The checker verifies the relation from the
// binding and runs no Go tool.
type GoAdapter int

const (
	// AdaptSame: the written signature is the Level 1 one; only the names
	// differ.
	AdaptSame GoAdapter = iota + 1
	// AdaptNonNil: Level 1's Option[T] result (a Go pointer or interface
	// that may be nil) is written T; a nil result is a defect.
	AdaptNonNil
	// AdaptOption: Level 1's (T, bool) result is written Option[T]: Some
	// when Go's bool is true.
	AdaptOption
	// AdaptPartial: Level 1's results R… with fails go.Error are written
	// (R…, Option[go.Error]) with no fails row, so the value Go returns
	// beside an error (io.Reader.Read's n) is kept.
	AdaptPartial
)

// GoExtern is an admitted user extern: its adapter; whether its Go target
// is variadic (the lowering spreads the last argument); and whether it is
// in the C26 domain (AdaptSame over basic types and G1's callbacks), where
// it lowers as a direct call that go build also checks (lower hostCall)
// and may be a function value. Any other user extern is call-only and
// lowers by the binding's boundary rules (lower, goExternCall).
type GoExtern struct {
	Adapter  GoAdapter
	Variadic bool
	// Context: the Go target takes a context.Context first, which the
	// call passes (IG10); such an extern is never Basic.
	Context bool
	Basic   bool
	// Binding is the Go target's declaration in its binding (Level 1),
	// which ergo inspect reports (IG8).
	Binding *Func
}

// GoExternOf is the admitted adapter of the user extern f, or nil.
func GoExternOf(f *Func) *GoExtern {
	if f == nil {
		return nil
	}
	return f.goExtern
}

// externAdapter admits the user extern f when its written signature is an
// adapter of its Go target's Level 1 signature, read from the target's
// binding (IG4), and records it (f.goExtern); else it returns the refusal
// and its code. The relation:
//
//   - the target is a function the binding imports: a refused one is S109
//     with the binding's reason, as at a use of import go;
//   - as many parameters as Level 1's, each of the identical type; names
//     are free. A callback stays in G1's domain (externCallback) until IG6;
//   - the needs row is the binding's: needs go.Foreign for the foreign
//     tier, none for the reviewed pure tier;
//   - the results and the fails row are one GoAdapter.
//
// A generic Go target is called at the types the written parameters give:
// its type parameters bind from the parameters, as Go infers them at the
// call, each satisfies its constraint, and Level 1 is instantiated with
// them. A written type parameter is refused.
func (c *checker) externAdapter(f *Func) (string, string) {
	target := f.Decl.Extern.Target.Value
	path, name := f.Decl.Extern.Split()
	pkg, ok := c.goBindings().Package(path)
	if !ok {
		return fmt.Sprintf("extern go %s: no binding for Go package %q; run ergo bind go %s (docs/spec/interop.md)", target, path, path), CodeUnknown
	}
	o, found := pkg.member(name)
	bf, isFunc := o.(*Func)
	switch {
	case !found:
		if why := hostOf(pkg).refusal(name); why != "" {
			return fmt.Sprintf("extern go %s is not imported from Go: %s (the binding of %s refuses it)", target, why, path), CodeOutside
		}
		return fmt.Sprintf("extern go %s: Go package %s has no exported function %s", target, path, name), CodeUnknown
	case !isFunc || bf.Sig == nil:
		return fmt.Sprintf("extern go %s names a Go type, constant or variable; a user extern binds a function", target), CodeType
	}
	w, level1 := f.Sig, bf.Sig
	if sigHasInvalid(w) {
		return "", "" // the signature's own diagnostic stands
	}
	if len(w.TParams) > 0 {
		return fmt.Sprintf("the extern %s has the type parameter %s; a user extern writes the types it calls its Go target at, and Go infers the type arguments", f.Name, w.TParams[0].Name), CodeType
	}
	if len(level1.Params) != len(w.Params) {
		return fmt.Sprintf("extern go %s takes %d parameters (Level 1: %s); the extern writes %d", target, len(level1.Params), level1, len(w.Params)), CodeType
	}
	if len(level1.TParams) > 0 {
		inst, msg := c.externInstance(f, level1)
		if msg != "" {
			return fmt.Sprintf("extern go %s: %s", target, msg), CodeType
		}
		if inst == nil {
			return "", "" // typeArgsAdmitted reported the Task rule
		}
		level1 = inst
	}
	for i, p := range w.Params {
		if !c.identical(level1.Params[i].Type, p.Type) {
			return fmt.Sprintf("extern go %s: the parameter %s is %s; Go's is %s (Level 1: %s)", target, paramName(p, i), p.Type, level1.Params[i].Type, level1), CodeType
		}
		if _, ok := p.Type.(*Signature); ok && !c.externCallback(p.Type) {
			return fmt.Sprintf("the callback parameter %s of the extern %s is #202's (Go imports; IG6): a user extern takes callbacks over basic types with no rows", paramName(p, i), f.Name), CodeOutside
		}
	}
	foreign := goForeignRow(level1.Uses)
	wrote := c.types.resolve(w.Uses)
	switch {
	case foreign && !goForeignRow(wrote):
		return fmt.Sprintf("extern go %s runs Go that is not on the reviewed pure list; declare needs go.Foreign (only main provides it) (docs/spec/interop.md)", target), CodeType
	case !foreign && !wrote.Empty():
		return fmt.Sprintf("extern go %s is on the reviewed pure list and needs nothing; write no needs row", target), CodeType
	}
	adapter, msg := c.externResults(level1, w, target)
	if msg != "" {
		return msg, CodeType
	}
	f.goExtern = &GoExtern{Adapter: adapter, Variadic: GoVariadic(bf), Context: GoContext(bf), Basic: adapter == AdaptSame && !GoContext(bf) && c.externBasicSig(w), Binding: bf}
	return "", ""
}

// externResults is the adapter of the written results and fails row of w
// to Level 1's (g), or the refusal.
func (c *checker) externResults(g, w *Signature, target string) (GoAdapter, string) {
	if w.Fallible && !externGoErrorRow(c.types.resolve(w.Errors)) {
		return 0, fmt.Sprintf("extern go %s: the fails row of a user extern is go.Error, the error its Go target returns", target)
	}
	gv, wv := resultList(g), resultList(w)
	same := c.identicalList(gv, wv)
	switch {
	case same && g.Fallible == w.Fallible:
		return AdaptSame, ""
	case g.Fallible == w.Fallible && len(gv) == 1 && len(wv) == 1 && optionElem(gv[0]) != nil && c.identical(optionElem(gv[0]), wv[0]):
		return AdaptNonNil, ""
	case g.Fallible == w.Fallible && len(gv) == 2 && isBasicKind(gv[1], Bool) && len(wv) == 1 && optionElem(wv[0]) != nil && c.identical(optionElem(wv[0]), gv[0]):
		return AdaptOption, ""
	case g.Fallible && !w.Fallible && len(wv) == len(gv)+1 && c.identicalList(gv, wv[:len(gv)]) && isOptionGoError(wv[len(gv)]):
		for _, t := range gv {
			if refElem(t) != nil {
				return 0, fmt.Sprintf("extern go %s: a Go pointer returned beside an error may be nil, and a go.Ref never is; keep fails go.Error", target)
			}
		}
		return AdaptPartial, ""
	case same && g.Fallible:
		return 0, fmt.Sprintf("extern go %s returns an error; declare it in the row: fails go.Error, or keep the value Go returns beside it with the result (…, Option[go.Error])", target)
	case same && w.Fallible:
		return 0, fmt.Sprintf("extern go %s returns no error; a user extern fails go.Error only when its Go function's last result is error", target)
	}
	return 0, fmt.Sprintf("extern go %s: the signature %s is no adapter of Go's Level 1 signature %s (docs/spec/interop.md, Level 2 adapters)", target, w, g)
}

// externInstance instantiates the generic Level 1 signature g at the types
// the written parameters of the extern f give its type parameters. The
// instance passes the Task rule (typeArgsAdmitted), which reports its own
// diagnostic: then the result is nil with no text.
func (c *checker) externInstance(f *Func, g *Signature) (*Signature, string) {
	m := map[*TypeParam]Type{}
	for i := range g.Params {
		bindTypeParams(g.Params[i].Type, f.Sig.Params[i].Type, m)
	}
	targs := make([]Type, len(g.TParams))
	for i, tp := range g.TParams {
		t, ok := m[tp]
		if !ok && tp.Kind != KindType {
			// IG6: a row parameter of a binding (a sync callback's CE,
			// CR) is Ergo's alone, and a user extern's callback has no
			// rows (S109): it is empty.
			t, ok = &RowArg{Row: newRow()}, true
			m[tp] = t
		}
		if !ok {
			return nil, fmt.Sprintf("Go cannot infer its type parameter %s from the parameters", tp.Name)
		}
		if why := c.satisfies(t, c.subst(tp.Constraint, m)); why != "" {
			return nil, fmt.Sprintf("its type parameter %s cannot be %s: %s", tp.Name, t, why)
		}
		targs[i] = t
	}
	if !c.typeArgsAdmitted(f.Decl.Extern.Target.Value, g.TParams, targs, f.Decl) {
		return nil, ""
	}
	return c.subst(g, m).(*Signature), ""
}

// bindTypeParams binds, in m, each type parameter that g holds to the type
// at the same place in w, the first binding winning (Go's inference from
// typed arguments); identity is checked after substitution.
func bindTypeParams(g, w Type, m map[*TypeParam]Type) {
	switch g := g.(type) {
	case *TypeParam:
		if _, ok := m[g]; !ok {
			m[g] = w
		}
	case *Named:
		if wn, ok := w.(*Named); ok && wn.origin() == g.origin() && len(wn.TArgs) == len(g.TArgs) {
			for i := range g.TArgs {
				bindTypeParams(g.TArgs[i], wn.TArgs[i], m)
			}
		}
	case *Slice:
		if ws, ok := w.(*Slice); ok {
			bindTypeParams(g.Elem, ws.Elem, m)
		}
	case *Map:
		if wm, ok := w.(*Map); ok {
			bindTypeParams(g.Key, wm.Key, m)
			bindTypeParams(g.Elem, wm.Elem, m)
		}
	case *Pointer:
		if wp, ok := w.(*Pointer); ok {
			bindTypeParams(g.Elem, wp.Elem, m)
		}
	case *Signature:
		if ws, ok := w.(*Signature); ok && len(ws.Params) == len(g.Params) && len(ws.Results) == len(g.Results) {
			for i := range g.Params {
				bindTypeParams(g.Params[i].Type, ws.Params[i].Type, m)
			}
			for i := range g.Results {
				bindTypeParams(g.Results[i], ws.Results[i], m)
			}
		}
	}
}

// externBasicSig reports a signature in the C26 domain: basic parameters
// or G1's callbacks, and at most one basic result.
func (c *checker) externBasicSig(w *Signature) bool {
	for _, p := range w.Params {
		if !externBasic(p.Type) && !c.externCallback(p.Type) {
			return false
		}
	}
	return len(w.Results) == 0 || len(w.Results) == 1 && externBasic(w.Results[0])
}

// resultList is a signature's results, a tuple result spread.
func resultList(s *Signature) []Type {
	if len(s.Results) == 1 {
		if t, ok := s.Results[0].(*Tuple); ok {
			return t.Types
		}
	}
	return s.Results
}

func (c *checker) identicalList(a, b []Type) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !c.identical(a[i], b[i]) {
			return false
		}
	}
	return true
}

// optionElem is T for the prelude's Option[T], else nil.
func optionElem(t Type) Type {
	n, ok := t.(*Named)
	if !ok || len(n.TArgs) != 1 || n.origin() != preludeType("Option") {
		return nil
	}
	return n.TArgs[0]
}

func isOptionGoError(t Type) bool {
	n, ok := optionElem(t).(*Named)
	return ok && IsGoError(n)
}

func isBasicKind(t Type, k BasicKind) bool {
	b, ok := t.(*Basic)
	return ok && b.Kind == k
}

// goForeignRow reports the needs row go.Foreign, exactly.
func goForeignRow(r *Row) bool {
	elems := r.Elems()
	if len(elems) != 1 {
		return false
	}
	n, ok := elems[0].(*Named)
	return ok && IsGoForeign(n)
}

func paramName(p *Var, i int) string {
	if p.Name == "" || p.Name == "_" {
		return fmt.Sprint(i + 1)
	}
	return p.Name
}

// externGoErrorRow reports the one fails row a user extern may write (G4,
// decision O14): exactly go.Error. Its Go target's last result is error,
// and the lowering turns a non-nil error into the go.Error that keeps it (IG5).
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

// IsGoForeign reports the predeclared go.Foreign, the authority to run
// unreviewed Go (import go), which only the entry provides.
func IsGoForeign(n *Named) bool {
	return n != nil && n.Obj != nil && n.Obj.Pkg == universeOnce.goPkg && n.Obj.Name == "Foreign"
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
