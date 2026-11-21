package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A part of a borrow returned to the caller (OWN1, own-borrow-return; lane
// C's S3d6 typer: `func tree(k *Pkg, t *Typer) syntax.Tree {
// k.Parsed[t.file].Tree }` copied the whole syntax tree at every call,
// 84% of the pointer form's cost). A read through a borrow kept past the
// read is copied (V2) because the borrow's owner may write after the
// call; a return keeps it, so the callee copied at its return, whatever
// the caller did with the result.
//
// The copy moves to the caller, as alias.go's borrowResult already does
// for a generic result: the callee returns the part shared, and each call
// copies the result unless its use cannot see a write to the borrowed
// storage:
//   - a read that runs at once (readonly.go, readOnlyUse); or
//   - the whole initializer of a plain let that keeps nothing (storeread.go,
//     letKeepsNothing), when no use of the root each borrowed operand names
//     can write storage the result reaches (storageWritten, as keptRead
//     judges a let of a read through the borrow).
//
// Only a function whose every use is a direct call of its own package
// moves its copy: one that is unexported, has no receiver, one result
// that holds a Map or slice and declares no type parameter in it (those
// are borrowResult's), and whose result-alias summary marks each borrow
// parameter a return reads (so each call sees the dependence). A function
// value or a method value would call it with no caller to copy.
//
// The reference is valueLowering "copy"; a test build copies at the
// return, as before (takes). TestHandoverAgrees holds both to the model
// (peekRet, peekRetPoke).
//
// Prior art: Swift returns a stored property +0 under a _read coroutine
// (a yielded borrow) and lets the caller copy when it keeps the value;
// Rust returns `&T` tied to the borrow's lifetime, so a caller that keeps
// it past a write is refused. Gleam, Elixir and ReScript return the shared
// value: their data is immutable.

// borrowReturns reports whether f returns parts of its borrows shared,
// the caller copying (above): it has a site to share (borrowRetSites).
func (l *lowerer) borrowReturns(f *check.Func) bool {
	return len(l.borrowRetSites(f)) > 0
}

// borrowRetSites are the values f returns shared: each value its body
// yields (a tail through if and match arms, or a return's, outside any
// function literal) that is a path rooted at a borrow parameter the
// result-alias summary marks. They are read from the syntax and the
// summary alone, so every caller and f's own analysis see one set,
// whatever order the bodies are analyzed in. Empty when f does not move
// its copy (above), in a test build and under the reference.
func (l *lowerer) borrowRetSites(f *check.Func) map[syntax.Expr]bool {
	vf := l.valueFacts()
	if vf.borrowRet == nil {
		vf.borrowRet = map[*check.Func]map[syntax.Expr]bool{}
	}
	if s, ok := vf.borrowRet[f]; ok {
		return s
	}
	vf.borrowRet[f] = nil
	s := l.borrowRetSitesAt(f)
	if len(s) > 0 && !l.takes("own-borrow-return") {
		s = nil
	}
	if len(s) > 0 {
		l.took("own-borrow-return")
	}
	vf.borrowRet[f] = s
	return s
}

func (l *lowerer) borrowRetSitesAt(f *check.Func) map[syntax.Expr]bool {
	if !valueOwned() || f == nil || f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Decl.Recv != nil || f.Decl.Name == nil {
		return nil
	}
	if exported(f.Name) || f.Pkg == nil || f.Pkg.Info == nil || len(f.Sig.Results) != 1 {
		return nil
	}
	t := f.Sig.Results[0]
	if len(freeTypeParams(t)) != 0 || !l.holdsValue(t) || !onlyCalled(f.Pkg.Info, f) {
		return nil
	}
	values := map[syntax.Expr]bool{}
	tailsOf(f.Decl.Body.List, values)
	syntax.Inspect(f.Decl.Body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.ReturnStmt:
			for _, r := range n.Results {
				values[r] = true
			}
		}
		return true
	})
	params := map[*check.Var]int{}
	for j := range f.Sig.Params {
		if v := declParam(f, j); v != nil && isPointer(v) {
			params[v] = j
		}
	}
	deps := l.resultDeps(f)
	sites := map[syntax.Expr]bool{}
	for e := range values {
		id := pathRoot(e)
		if id == nil {
			continue
		}
		v, ok := f.Pkg.Info.Uses[id].(*check.Var)
		if !ok {
			continue
		}
		j, ok := params[v]
		if !ok {
			continue
		}
		if j >= len(deps) || !deps[j] {
			return nil
		}
		sites[unparen(e)] = true
	}
	return sites
}

// onlyCalled reports whether every use of f in info is the callee of a
// call: f(…), or k.f(…) with k its first operand.
func onlyCalled(info *check.Info, f *check.Func) bool {
	callees := map[*syntax.Ident]bool{}
	for call, c := range info.Calls {
		if c == nil || c.Func != f {
			continue
		}
		switch fun := unparen(call.Fun).(type) {
		case *syntax.Ident:
			callees[fun] = true
		case *syntax.SelectorExpr:
			callees[fun.Sel] = true
		}
	}
	for id, obj := range info.Uses {
		if obj == check.Object(f) && !callees[id] {
			return false
		}
	}
	return true
}

// sharedReturn reports whether o, a use of the borrow parameter vv, is a
// return of the body's function that borrowReturns lets go uncopied.
func (w *valueWalk) sharedReturn(vv *vvar, o *vocc) bool {
	if o.kind != occReturn || !vv.ptr || vv.local || o.frame != w.root {
		return false
	}
	d, ok := w.root.node.(*syntax.FuncDecl)
	if !ok || d.Name == nil {
		return false
	}
	f, ok := w.info.Defs[d.Name].(*check.Func)
	return ok && w.l.borrowRetSites(f)[unparen(o.path)]
}

// keptCall reports whether the result of call, a function that returns a
// part of a borrow shared, initializes a plain let that keeps nothing,
// and no use of a root a borrowed operand names writes storage the result
// reaches (above).
func (w *valueWalk) keptCall(call *syntax.CallExpr) bool {
	c := w.info.Calls[call]
	if c == nil || c.Func == nil || !w.l.borrowReturns(c.Func) {
		return false
	}
	var local *vvar
	for _, cand := range w.order {
		if cand.init != nil && unparen(cand.init) == syntax.Expr(call) {
			local = cand
			break
		}
	}
	if local == nil || !w.letKeepsNothing(local, 0) {
		return false
	}
	read := map[string]bool{}
	if w.l.reaches(w.typeOf(call), read, map[check.Type]bool{}) {
		return false
	}
	written := map[string]bool{}
	deps := w.l.resultDeps(c.Func)
	for i, a := range callOperands(call, c) {
		if i >= len(deps) || !deps[i] {
			continue
		}
		if _, ptr := w.typeOf(a).(*check.Pointer); !ptr {
			if u, ok := unparen(a).(*syntax.UnaryExpr); !ok || u.Op != syntax.AND {
				continue // a value operand: the callee's copy rules hold for it
			}
		}
		id := pathRoot(a)
		if id == nil {
			return false
		}
		v, ok := w.info.Uses[id].(*check.Var)
		if !ok {
			return false
		}
		vv := w.vars[v]
		if vv == nil || !vv.root() {
			return false
		}
		for _, u := range vv.occs {
			if u.frame != local.frame {
				return false
			}
			if !w.writesNothing(u) && w.storageWritten(u, written) {
				return false
			}
		}
	}
	for k := range read {
		if written[k] {
			return false
		}
	}
	return true
}
