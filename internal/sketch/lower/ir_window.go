package lower

// IR pass: operation-law windows on the IR (types.md, Laws 14 Windows;
// LF6w). See rewrite.go for the rule and the engine (rewriter.run), which
// this pass shares with the text emitter's rewriteWindow.
//
// A window is a run of consecutive call statements on one provider local
// x, each a call of one of the provider's operation functions with no
// result and no failure, each argument a constant or a local (windowAt,
// the same test as windowCall, read from the call's facts: irCallFacts).
// The engine rewrites the window's calls; the pass writes its result as
// call statements built from the window's own call nodes (windowCall's
// receiver and environment, the law's arguments). The arguments are
// locals and constants, which the window's calls cannot assign, so the
// result reads them where the window read them: no user expression moves,
// drops or repeats.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// rwWindowsOn reports a run build whose evidence may admit a window.
func (b *irBuilder) rwWindowsOn() bool {
	return b.l.rewrite != nil && rewriteLowering != "reference" && len(b.l.rewrite.rules) > 0
}

// irWindowCall is a window's call statement: its receiver local, the
// provider, the provider function, the source call and the call node.
type irWindowCall struct {
	recv *check.Var
	prov *check.Named
	fn   *check.Func
	call *syntax.CallExpr
	node *irExpr
}

// windowAt is s as a window's call, or ok false.
func (b *irBuilder) windowAt(s *irStmt) (w irWindowCall, ok bool) {
	if s.K != "expr" || s.X == nil || s.X.CF == nil || s.X.CF.Site == nil || s.X.CF.Keep != "" {
		return w, false
	}
	call := s.X.CF.Site
	if call.Trailing != nil {
		return w, false
	}
	c := b.l.info.Calls[call]
	if c == nil || c.Recv == nil || c.Kind != check.CallFunc {
		// A cell's operation (cells.go cellWindow) keeps its text form.
		return w, false
	}
	v := b.localVar(c.Recv)
	if v == nil || !rewritable(c.Func) {
		return w, false
	}
	typ := v.Type
	if ptr, isPtr := typ.(*check.Pointer); isPtr {
		typ = ptr.Elem
	}
	prov, isNamed := typ.(*check.Named)
	if !isNamed {
		return w, false
	}
	known := false
	for _, r := range b.l.rewrite.rules[prov] {
		if r.conv.Table[c.Func.Name] == c.Func {
			known = true
		}
	}
	if !known {
		return w, false
	}
	for _, a := range call.Args {
		if _, isConst := b.l.info.Values[a]; !isConst && b.localVar(a) == nil {
			return w, false
		}
	}
	return irWindowCall{recv: v, prov: prov, fn: c.Func, call: call, node: s.X}, true
}

// localVar is the local x names, or nil.
func (b *irBuilder) localVar(x syntax.Expr) *check.Var {
	id, ok := unparen(x).(*syntax.Ident)
	if !ok {
		return nil
	}
	v, ok := b.l.info.Uses[id].(*check.Var)
	if !ok {
		return nil
	}
	if _, local := b.locals[v]; !local {
		return nil
	}
	return v
}

// windows rewrites every window of list and of the lists inside it.
func (b *irBuilder) windows(list []*irStmt) []*irStmt {
	for _, s := range list {
		s.Then, s.Else, s.Body = b.windows(s.Then), b.windows(s.Else), b.windows(s.Body)
		for _, c := range s.Cases {
			c.Body = b.windows(c.Body)
		}
		irLiterals(s, func(e *irExpr) { e.Body = b.windows(e.Body) })
	}
	var out []*irStmt
	for i := 0; i < len(list); {
		first, ok := b.windowAt(list[i])
		if !ok {
			out = append(out, list[i])
			i++
			continue
		}
		win := []irWindowCall{first}
		for _, s := range list[i+1:] {
			w, ok := b.windowAt(s)
			// One local has one type, so one provider.
			if !ok || w.recv != first.recv {
				break
			}
			win = append(win, w)
		}
		n := len(win)
		if rewritten, ok := b.window(list[i:i+n], win); ok {
			out = append(out, rewritten...)
		} else {
			out = append(out, list[i:i+n]...)
		}
		i += n
	}
	return out
}

// window is the statements of win rewritten by the engine, or ok false to
// keep them as written.
func (b *irBuilder) window(stmts []*irStmt, win []irWindowCall) ([]*irStmt, bool) {
	prov := win[0].prov
	// Every function a rule of the provider may call must be written from
	// a call node of the window: same kind of call, environment and
	// receiver. Checked before the engine runs, which records the site.
	for _, r := range b.l.rewrite.rules[prov] {
		if r.post != nil {
			continue
		}
		for _, rc := range r.law.Rewrite.Rhs {
			if fn := r.conv.Table[rc.Op.Name]; fn != nil && rewritable(fn) && !b.windowShape(fn, win[0]) {
				return nil, false
			}
		}
	}
	calls := make([]rwCall, len(win))
	for k, w := range win {
		calls[k] = rwCall{fn: w.fn}
		for _, a := range w.call.Args {
			arg := rwArg{}
			if c, ok := b.l.info.Values[a]; ok {
				arg.c = c
			}
			calls[k].args = append(calls[k].args, arg)
		}
	}
	if ev, ok := b.l.rewrite.ev.(*LedgerEvidence); ok {
		// The sites the evidence records are this window's (inspect).
		ev.file, ev.line, ev.col = b.l.locate(win[0].call, win[0].call.Pos())
	}
	out, changed := b.l.rewrite.run(prov, calls)
	if !changed {
		return nil, false
	}
	var rewritten []*irStmt
	for k, c := range out {
		at := stmts[min(k, len(stmts)-1)]
		node := b.windowNode(c, win)
		rewritten = append(rewritten, &irStmt{K: "expr", Pos: at.Pos, X: node})
	}
	return rewritten, true
}

// windowShape reports fn, a provider function, as one a call node of the
// window w writes: the same Go call kind, environment and receiver.
func (b *irBuilder) windowShape(fn *check.Func, w irWindowCall) bool {
	fd, wd := b.l.useFunc(fn, w.call), b.l.useFunc(w.fn, w.call)
	if (fd.method != nil) != (wd.method != nil) || fd.envFree != wd.envFree || fn.Ptr != w.fn.Ptr {
		return false
	}
	// An owned variant's call (ir_own.go ownedCallName) is the window's
	// own: another function has no such name to take.
	return w.node.Name == wd.name || fn == w.fn
}

// windowNode is the call node of the engine's call c: a copy of a window
// call node of c's function (or of the first's, renamed), its arguments
// the law's, a pass-through one the window call's own operand node.
func (b *irBuilder) windowNode(c rwCall, win []irWindowCall) *irExpr {
	tmpl := win[0]
	for _, w := range win {
		if w.fn == c.fn {
			tmpl = w
			break
		}
	}
	t := tmpl.node
	node := *t
	node.CF = nil
	node.C = nil // LF3's pure-call facts name the template's call, not this one (I-FLIP)
	if tmpl.fn != c.fn {
		node.Name = b.l.useFunc(c.fn, tmpl.call).name
	}
	off := len(t.Args) - (len(tmpl.fn.Sig.Params) - 1)
	node.Args = append([]*irExpr(nil), t.Args[:off]...)
	for j, a := range c.args {
		if a.c != nil {
			node.Args = append(node.Args, b.constant(c.fn.Sig.Params[j+1].Type, a.c, tmpl.call, t.Pos))
			continue
		}
		k, arg := rwPlace(a.text)
		src := win[k].node
		srcOff := len(src.Args) - (len(win[k].fn.Sig.Params) - 1)
		cp := *src.Args[srcOff+arg]
		node.Args = append(node.Args, &cp)
	}
	return &node
}
