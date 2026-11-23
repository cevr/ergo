package lower

// IR builder: known-callback variants (FLIP F7 wave 2, "known-callback
// variant"; known.go, OPT1).
//
// The IR takes both halves of the emitter's rule:
//   - a call whose callbacks are all known (knownSite) goes to the
//     callee's known variant (knownName), its function literal operands
//     built plain (plainLit): func(params) R, with no environment
//     parameter, no error result and no poll; the literal runs on the
//     caller's environment, which it captures;
//   - the variant's body (emitBody with known) is an IR body under the
//     variant's Go name, with no entry poll; a call of one of its callback
//     parameters is a direct call of the plain value (vcall with the
//     operands alone, its result as it is), and a call that forwards them
//     goes to the next variant (the first half).
//
// Prior art: GHC's SpecConstr and MLton's defunctionalization make a call
// of a known function argument direct; Rust monomorphizes an `impl Fn`
// parameter per closure. Gleam, Elixir and ReScript pass a closure through
// the host's indirect call with no specialization.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// knownVariantName is the Go name of the body b builds when it is fd's
// known variant (fc.plain is set only there, decls.go emitBody), or "".
func (b *irBuilder) knownVariantName(fd *funcDecl) string {
	if b.fc.plain == nil {
		return ""
	}
	if fd.known == "" {
		irNo("known-callback variant without its name")
	}
	return fd.known
}

// knownCall is the Go name of the known variant the call x of c goes to,
// or "" for the function itself (calls.go callText: knownSite, then
// knownName). It runs before the operands are built: knownSite marks the
// literal operands plain, which closureLit then builds plain (plainLitIR).
func (b *irBuilder) knownCall(x *syntax.CallExpr, c *check.Call, fd *funcDecl) string {
	if !b.fc.knownSite(x, c) {
		return ""
	}
	name := b.l.knownName(c.Func, fd)
	irKnownTraced(name, "call: built at the known variant")
	return name
}

// plainCallee reports the call x of a callback parameter of the known
// variant b builds: a direct call of a plain Go function value.
func (b *irBuilder) plainCallee(x *syntax.CallExpr) bool {
	if b.fc.plain == nil {
		return false
	}
	id, ok := unparen(x.Fun).(*syntax.Ident)
	if !ok {
		return false
	}
	v, ok := b.l.info.Uses[id].(*check.Var)
	return ok && b.fc.plain[v]
}

// plainCall is the call x of a callback parameter (plainCallee): the value
// and the operands, no environment; its result is the callback's, with no
// error (calls.go: "a direct Go call, with no environment and no error").
func (b *irBuilder) plainCall(x *syntax.CallExpr, p irPos) ([]*irStmt, *irExpr) {
	pre, vals := b.operands(append([]syntax.Expr{x.Fun}, x.Args...))
	return pre, &irExpr{K: "vcall", Pos: p, X: vals[0], Args: vals[1:]}
}

// plainSigType is the IR spelling of the plain Go type of a known callback
// of signature s (plainFuncType): its parameter and result types admitted.
func (b *irBuilder) plainSigType(s *check.Signature, site syntax.Node) string {
	for _, p := range s.Params {
		b.goType(p.Type, site)
	}
	for _, r := range s.Results {
		b.goType(r, site)
	}
	return b.spelling(b.fc.plainFuncType(s))
}

// plainLitIR is the known pure literal x (knownSite marked it) built
// plain, as the emitter's plainLit: its parameters, its result, and its
// body as a frame of its own with no point (its footprint is empty), on
// the caller's environment (env, the maker's local, or none for a pure
// maker).
func (b *irBuilder) plainLitIR(x *syntax.FuncLit, sig *check.Signature, params []*check.Var, ids []*syntax.Ident, list []syntax.Stmt, end syntax.Pos) *irExpr {
	p := b.pos(x, x.Pos())
	var result check.Type
	if len(sig.Results) == 1 {
		result = sig.Results[0]
	}
	inner := b.fc.innerFuncCtx()
	inner.env = b.fc.env
	inner.frame = newFrame(result, false, false)
	fc, res, loops, tries, switches, label, law := b.fc, b.result, b.loops, b.tries, b.switches, b.label, b.law
	amort, sites := b.amort, b.pollSites
	b.amort, b.pollSites = nil, nil // a Go function of its own (ir_poll.go)
	defer func() {
		b.fc, b.result, b.loops, b.tries, b.switches, b.label, b.law = fc, res, loops, tries, switches, label, law
		b.amort, b.pollSites = amort, sites
	}()
	lit := &irExpr{K: "func", Pos: p, T: b.plainSigType(sig, x)}
	for i, v := range params {
		lit.Params = append(lit.Params, b.litParam(v, ids[i], sig.Params[i].Type, x, p)) // ir_closure.go
	}
	b.fc, b.result, b.loops, b.tries, b.switches, b.label, b.law = inner, result, 0, 0, 0, "", nil
	lit.Body = b.bodyStmts(list) // ir_fail.go
	e := b.pos(x.Body, end)
	lit.End = &e
	if result != nil {
		lit.Body = append(lit.Body, b.bodyEnd(lit.Body, e)...)
	}
	return lit
}
