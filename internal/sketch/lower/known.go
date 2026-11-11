package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Known callbacks (OPT1, F3). A function value has one Go shape, the
// uniform func(*Env, …) (R, error) (typeCtx.funcType), and its call is
// opaque: the checker cannot see what it runs, so the frame that calls it
// has an opaque footprint and polls at entry (runtime.md, I1 2, Frame
// entry). Go cannot inline such a frame into its caller: the poll and the
// error test of the indirect call cost more than its budget.
//
// When a call names, for every callback parameter of its callee, a known
// pure function literal, or a callback parameter of a known variant that
// forwards it, the call goes to the callee's known variant: the same body
// lowered once more, where
//   - each callback parameter has the plain Go type func(…) R: no
//     environment and no error, since its row is empty;
//   - a call of one is a direct Go call of it, with no error test;
//   - a call that forwards one to another function goes to that
//     function's known variant;
//   - the entry poll is dropped. It exists because unknown code may
//     re-enter the frame (I1 2); a pure literal calls only pure code,
//     which cannot call the frame, so nothing re-enters it. Every other
//     point stays: a loop of the variant still polls at its back-edge,
//     since its footprint is the checker's, which counts the callback as
//     opaque. So a variant never polls less often in a loop than the
//     frame it stands for, and an infinite loop of it stays stoppable.
//
// The plain literal runs on the caller's task: it captures the caller's
// environment, as a host callback does (envClosure, capturedEnv), and it
// is called only while the call runs (no variant keeps a callback: its
// parameter is only called or forwarded).
//
// A literal is known pure when its checked signature has empty rows and
// no Task result, its footprint is empty (no capability, task start or
// opaque call: it calls no function value), and it opens no frame scope.
// A function has a known variant (knownVariant) when it has an Ergo body,
// takes the environment (a pure function calls no function value), has no
// capability, task start, with, Seq loop or context value in its body, no
// owned parameter (V5), no witness and no Task result, and every call in
// its body is a built-in, a pure std body, a function with an empty
// footprint, a call of one of its callback parameters, or a call of a
// function with a known variant that passes its callback parameters on.
// A callback parameter is used nowhere else. A cycle of variants is
// refused: recursion polls at entry for its own reason.
//
// Every other call keeps today's ABI and polls: a function value read from
// a binding, a field or a parameter that is not forwarded, a literal with
// a capability call or a failure, and a package function named as a
// value.
//
// The reference lowering is callbackLowering "opaque", set by a test or by
// a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.callbackLowering=opaque:
// every call takes the uniform ABI. TestKnownCallbacksAgree runs its
// programs under both and against a model.
//
// Prior art: GHC specializes a function on a known function argument
// (SpecConstr, and the inliner's worker/wrapper split); MLton
// defunctionalizes the whole program, so every call of a known lambda is
// direct; OCaml's flambda inlines and specializes a closure argument when
// it knows the closure; Rust monomorphizes a generic `impl Fn` parameter
// per closure type, so a callback inlines. Gleam, Elixir and ReScript pass
// a closure through the host's indirect call (BEAM's fun call, a
// JavaScript function) with no specialization; ReScript relies on the
// JavaScript engine's inline caches.
var callbackLowering string

// knownStats counts what the known-callback and result-alias rules did in
// one lowering, for the in-process evidence (TestKnownCallbacksAgree,
// TestResultAliasAgrees): never read by the lowering itself.
type knownStats struct {
	// Sites is the calls that go to a known variant.
	Sites int
	// Variants is the known variants emitted, each without its entry poll.
	Variants int
	// ClosedEntries is the closed frames emitted without their entry poll.
	ClosedEntries int
	// ArgsShared is the by-value arguments read from a root that the
	// result-alias rule passes uncopied (values.go, resultDeps).
	ArgsShared int
	// ResultCopies is the call results copied instead of their argument.
	ResultCopies int
	// BorrowCopies is the generic results through a borrow copied at the
	// caller (alias.go, borrowResult).
	BorrowCopies int
	// FlatOptions is the Option values the lowering puts into the flat
	// Option by their tag (flatunion.go, flatValue).
	FlatOptions int
	// OptionBails is the times a lowering of the program bailed on the
	// flat Option, so that it lowered the program again with Option an
	// interface (lowerAgain).
	OptionBails int
	// Inlined is the calls written at their site (inline.go), and
	// Substituted the literals written where an inlined body calls them.
	Inlined     int
	Substituted int
}

// statsHook is a test seam, nil outside tests: it receives the counts of
// each lowering when it ends.
var statsHook func(knownStats)

// knownState is a function's known-variant decision.
type knownState int8

const (
	knownDeciding knownState = iota + 1
	knownYes
	knownNo
)

// callbackParams are the indices of g's parameters of a function type
// (not a named one, such as Seq), counting a method's receiver first.
func callbackParams(g *check.Func) []int {
	var out []int
	for i, p := range g.Sig.Params {
		if _, ok := p.Type.(*check.Signature); ok {
			out = append(out, i)
		}
	}
	return out
}

// plainSig reports a callback signature the plain Go type can carry: empty
// rows, no type parameter, at most one result and no Task.
func plainSig(s *check.Signature) bool {
	return s != nil && len(rowElems(s.Errors)) == 0 && len(rowElems(s.Uses)) == 0 && len(s.TParams) == 0 && len(s.Results) <= 1 && !s.ResultIsTask()
}

// paramVars are g's parameter objects in order, nil for an unnamed one.
func paramVars(g *check.Func) []*check.Var {
	var out []*check.Var
	if g.Decl == nil || g.Decl.Type == nil || g.Decl.Type.Params == nil {
		return nil
	}
	for _, field := range g.Decl.Type.Params.List {
		if len(field.Names) == 0 {
			out = append(out, nil)
			continue
		}
		for _, id := range field.Names {
			v, _ := g.Pkg.Info.Defs[id].(*check.Var)
			out = append(out, v)
		}
	}
	return out
}

// knownVariant reports whether g has a known variant (above).
func (l *lowerer) knownVariant(g *check.Func) bool {
	if callbackLowering == "opaque" || g == nil {
		return false
	}
	if l.known == nil {
		l.known = map[*check.Func]knownState{}
	}
	switch l.known[g] {
	case knownDeciding, knownNo:
		return false
	case knownYes:
		return true
	}
	l.known[g] = knownDeciding
	ok := l.knownBody(g, false)
	l.known[g] = knownNo
	if ok {
		l.known[g] = knownYes
	}
	return ok
}

// knownClosed reports whether every function value g's frame calls, at
// any depth, is a known pure literal: its footprint is opaque only
// through known sites. Then g's own entry poll guards nothing (no unknown
// code runs to re-enter it) and is dropped as a variant's is; its other
// points stay. A cycle is refused, so g is not recursive.
func (l *lowerer) knownClosed(g *check.Func) bool {
	if callbackLowering == "opaque" || g == nil {
		return false
	}
	if l.closed == nil {
		l.closed = map[*check.Func]knownState{}
	}
	switch l.closed[g] {
	case knownDeciding, knownNo:
		return false
	case knownYes:
		return true
	}
	l.closed[g] = knownDeciding
	ok := l.knownBody(g, true)
	l.closed[g] = knownNo
	if ok {
		l.closed[g] = knownYes
	}
	return ok
}

// knownBody decides knownVariant (closed false) or knownClosed (closed
// true) for an undecided g: the rules of the comment above. A closed
// frame may make literals (each a frame of its own, judged where a known
// site passes it); a variant makes none, so no literal holds a callback.
func (l *lowerer) knownBody(g *check.Func, closed bool) bool {
	d := g.Decl
	if d == nil || d.Body == nil || d.Extern != nil || g.Pkg == nil || g.Pkg.Info == nil || g.Name == "main" || isClose(g) {
		return false
	}
	info := g.Pkg.Info
	s := g.Sig
	if s.ResultIsTask() || info.TaskResults[g] || len(witnessParams(s)) > 0 || len(rowElems(s.Uses)) > 0 {
		return false
	}
	fp, ok := info.Footprints[d]
	if !ok || fp.Empty() || len(fp.Caps) > 0 || fp.Tasks {
		return false
	}
	if l.isPure(g) || len(l.ownedParams(g)) > 0 {
		return false
	}
	vars := paramVars(g)
	if len(vars) != len(s.Params) {
		return false
	}
	plain := map[*check.Var]bool{}
	if !closed {
		for _, i := range callbackParams(g) {
			// A non-escaping parameter lives no longer than the call
			// (types.md, Function values 2): the checker's own guarantee
			// that the plain literal is only called while the call runs.
			if cb := s.Params[i].Type.(*check.Signature); !plainSig(cb) || !cb.NonEscaping {
				return false
			}
			if vars[i] != nil {
				plain[vars[i]] = true
			}
		}
		if len(plain) == 0 {
			return false
		}
	}
	allowed := map[*syntax.Ident]bool{}
	ok = true
	syntax.Inspect(d.Body, func(n syntax.Node) bool {
		if !ok {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit:
			if closed {
				return false // a frame of its own: making it runs nothing
			}
			ok = false
		case *syntax.RangeStmt:
			if lp := info.Loops[n]; lp == nil || lp.Domain == check.LoopSeq {
				ok = false
			}
		case *syntax.Ident:
			if v, isVar := info.Uses[n].(*check.Var); isVar && plain[v] && !allowed[n] {
				ok = false
			}
		case *syntax.CallExpr:
			ok = l.knownBodyCall(info, n, plain, allowed, closed)
		}
		if x, isExpr := n.(syntax.Expr); isExpr && ok && info.ContextValues[x] != nil {
			ok = false
		}
		return ok
	})
	return ok
}

// knownBodyCall reports whether the call x in the body of a known variant
// or a closed frame is one it may make (knownBody), and marks the callback
// parameters it uses in allowed.
func (l *lowerer) knownBodyCall(info *check.Info, x *syntax.CallExpr, plain map[*check.Var]bool, allowed map[*syntax.Ident]bool, closed bool) bool {
	c := info.Calls[x]
	if c == nil || x.Trailing != nil {
		return false
	}
	switch c.Kind {
	case check.CallConversion, check.CallLen, check.CallGoError, check.CallEmbed:
		return true
	case check.CallFuncValue:
		id, ok := unparen(x.Fun).(*syntax.Ident)
		if !ok {
			return false
		}
		v, ok := info.Uses[id].(*check.Var)
		if !ok || !plain[v] {
			return false
		}
		allowed[id] = true
		return true
	case check.CallFunc:
	default:
		return false
	}
	f := c.Func
	if f == nil || c.TaskResult {
		return false
	}
	if check.IsUnreachable(c) || pureStdBody(f) {
		return true
	}
	if f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil {
		return false
	}
	if fp, ok := f.Pkg.Info.Footprints[f.Decl]; ok && fp.Empty() && !f.Pkg.Info.TaskResults[f] {
		return true
	}
	var lit func(*syntax.FuncLit) bool
	if closed {
		// A closed frame is impure, so it binds an environment.
		lit = func(n *syntax.FuncLit) bool { return l.pureLit(info, n, true) }
	}
	list := callOperands(x, c)
	ids, ok := knownOperands(info, f, list, func(v *check.Var) bool { return plain[v] }, lit)
	if ok && l.knownVariant(f) {
		for _, id := range ids {
			allowed[id] = true
		}
		return true
	}
	return l.knownClosed(f)
}

// callOperands are the operands of the call x, a dot call's receiver
// first, or nil when the call has a trailing block.
func callOperands(x *syntax.CallExpr, c *check.Call) []syntax.Expr {
	if x.Trailing != nil {
		return nil
	}
	if c.Recv != nil {
		return append([]syntax.Expr{c.Recv}, x.Args...)
	}
	return x.Args
}

// knownOperands reports whether each callback operand of a call of f in
// list is known: a callback parameter of the variant being lowered
// (plain) or a known pure literal (lit, nil inside a variant's body). It
// returns the parameter identifiers it passes on.
func knownOperands(info *check.Info, f *check.Func, list []syntax.Expr, plain func(*check.Var) bool, lit func(*syntax.FuncLit) bool) ([]*syntax.Ident, bool) {
	cbs := callbackParams(f)
	if len(cbs) == 0 || len(list) != len(f.Sig.Params) {
		return nil, false
	}
	var ids []*syntax.Ident
	for _, i := range cbs {
		switch a := unparen(list[i]).(type) {
		case *syntax.Ident:
			v, ok := info.Uses[a].(*check.Var)
			if !ok || !plain(v) {
				return nil, false
			}
			ids = append(ids, a)
		case *syntax.FuncLit:
			if lit == nil || !lit(a) {
				return nil, false
			}
		default:
			return nil, false
		}
	}
	return ids, true
}

// pureLit reports whether lit is a known pure literal (above) where it is
// written, in a frame that binds an environment when env is set. Without
// one, its body may call only functions that take none.
func (l *lowerer) pureLit(info *check.Info, lit *syntax.FuncLit, env bool) bool {
	if !plainSig(info.Literals[lit]) {
		return false
	}
	fp, ok := info.Footprints[lit]
	if !ok || !fp.Empty() || fp.EntryPoll || lit.Body == nil || l.bodyNeedsFrame(lit.Body.List) {
		return false
	}
	if env {
		return true
	}
	ok = true
	syntax.Inspect(lit.Body, func(n syntax.Node) bool {
		if !ok {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit:
			ok = n == lit
		case *syntax.CallExpr:
			c := info.Calls[n]
			switch {
			case c == nil || n.Trailing != nil:
				ok = false
			case c.Kind == check.CallLen || c.Kind == check.CallConversion:
			case c.Kind == check.CallFunc && c.Func != nil && !c.TaskResult && (pureStdBody(c.Func) || l.isPure(c.Func)):
			default:
				ok = false
			}
		}
		return ok
	})
	return ok
}

// knownSite reports whether the call x of c.Func goes to its known variant
// from the code fc writes, and marks its literal operands to be lowered
// plain (plainLit).
func (fc *funcCtx) knownSite(x *syntax.CallExpr, c *check.Call) bool {
	if callbackLowering == "opaque" || c.Kind != check.CallFunc || c.Func == nil || c.TaskResult {
		return false
	}
	if fc.l.vals != nil && fc.l.vals.ownCall[x] {
		return false
	}
	info := fc.l.info
	list := callOperands(x, c)
	var lits []*syntax.FuncLit
	_, ok := knownOperands(info, c.Func, list, func(v *check.Var) bool { return fc.plain[v] }, func(lit *syntax.FuncLit) bool {
		if !fc.l.pureLit(info, lit, fc.env != "") {
			return false
		}
		lits = append(lits, lit)
		return true
	})
	if !ok || !fc.l.knownVariant(c.Func) {
		return false
	}
	if fc.plainLits == nil {
		fc.plainLits = map[*syntax.FuncLit]bool{}
	}
	for _, lit := range lits {
		fc.plainLits[lit] = true
	}
	fc.l.stats.Sites++
	return true
}

// knownKey is the package-scope key of a function's known variant.
func knownKey(g *check.Func) pkgKey { return pkgKey{"known", g} }

// reserveKnown gives, in the naming pass, the name of the known variant of
// each package-scope function of pkgs that has one; a method's variant is a
// method name (knownName).
func (l *lowerer) reserveKnown(pkgs []*check.Package) {
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			if l.knownVariant(f) {
				if td, _ := l.methodReceiver(f); td == nil {
					l.pkgName(knownKey(f), f.Name+"Known")
				}
			}
		}
	}
}

// knownName is the Go name of g's known variant, queued for emission once.
// A Go method's variant is a method of the same receiver, under a name no
// field or other method of the receiver has.
func (l *lowerer) knownName(g *check.Func, gd *funcDecl) string {
	if gd.known != "" {
		return gd.known
	}
	if gd.method != nil {
		taken := map[string]bool{}
		for _, n := range gd.method.fieldName {
			taken[n] = true
		}
		for _, f := range l.methodsOf(gd.method.tn) {
			taken[l.funcDecl(f).name] = true
		}
		name := gd.name + "Known"
		for taken[name] {
			name += "_"
		}
		gd.known = name
	} else {
		gd.known = l.pkgName(knownKey(g), g.Name+"Known")
	}
	l.stats.Variants++
	l.pending = append(l.pending, func() { l.emitBody(gd, false, nil, true) })
	return gd.known
}

// plainFuncType is the plain Go type of a known callback: func(params)
// result, with no environment and no error.
func (tc *typeCtx) plainFuncType(s *check.Signature) string {
	params := make([]string, len(s.Params))
	for i, p := range s.Params {
		params[i] = tc.goType(p.Type)
	}
	out := "func(" + strings.Join(params, ", ") + ")"
	if len(s.Results) == 1 {
		out += " " + tc.goType(s.Results[0])
	}
	return out
}

// plainLit lowers the known pure literal lit (knownSite) to a plain Go
// closure on the caller's environment: its parameters, its result, and
// its body as its own frame, with no point (its footprint is empty).
func (fc *funcCtx) plainLit(lit *syntax.FuncLit) string {
	sig := fc.l.info.Literals[lit]
	return fc.snapshot(lit, func() string {
		inner := fc.innerFuncCtx()
		return fc.envClosure(plainEnv, "", lit, func(env string) (parts []string, results, body string) {
			inner.env = env
			var result check.Type
			if len(sig.Results) == 1 {
				result = sig.Results[0]
			}
			inner.frame = newFrame(result, false, false)
			var params []*check.Var
			if lit.Type != nil && lit.Type.Params != nil {
				for _, field := range lit.Type.Params.List {
					for _, id := range field.Names {
						v, _ := fc.l.info.Defs[id].(*check.Var)
						params = append(params, v)
					}
				}
			}
			if len(params) != len(sig.Params) {
				ice(lit, "a literal declares %d parameters and its signature has %d", len(params), len(sig.Params))
			}
			for i, p := range sig.Params {
				name := fc.scope.alloc("_p")
				if params[i] != nil {
					name = inner.declare(params[i])
				}
				parts = append(parts, name+" "+fc.goType(p.Type))
			}
			if result != nil {
				results = " " + fc.goType(result)
			}
			inner.depth = fc.depth + 1
			inner.body(lit.Body.List)
			return parts, results, inner.out.String()
		})
	})
}
