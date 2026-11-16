package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// hotFunc reports whether f is on a loop path (inline.go, Size): a call
// of f sits inside a loop, or in the body of a function on a loop path,
// or f is recursive (a chain of calls from its body reaches it: the
// recursion is the loop), so f's whole body runs once per iteration.
func (l *lowerer) hotFunc(f *check.Func) bool {
	if l.hot == nil {
		l.hot = l.loopCalled()
	}
	return l.hot[f]
}

// hotLowering "direct" is the reference of the loop-path mark: only a
// direct call puts its callee on a loop path, as before OPT9. It is set by
// a test, or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.hotLowering=direct.
// TestInlinePays holds the decisions of both.
var hotLowering string

// loopCalled walks the call graph from every function of the program's
// source packages and returns the functions on a loop path. A function
// found on a loop path after its body was walked is walked again, so the
// set is closed under calls. The body of a function literal is read as
// part of its function: a literal is called where its function is, or
// once per element of the loop that takes it.
//
// A call through a function value or an operation names no callee here,
// so it marks every function that can be its callee (OPT9: under the
// direct mark, a loop through either kept its callee's large callees as
// Go calls, TestInlinePays): a call of a function value in a loop marks
// each function the program takes as a value (a reference that is not a
// call's callee), and a call of an operation in a loop marks each
// function that implements an operation of its name (the tables of the
// walked packages' operations conversions, check.ConvOperations). Both
// over-approximate the callees, as a points-to set without flow does: a
// function there may be inlined where it does not run in a loop, which
// costs size, never behaviour. They are marked each time the queue runs
// empty, and the walk goes on until nothing new is marked, so the order
// in which a value, a table and a call are found does not matter.
func (l *lowerer) loopCalled() map[*check.Func]bool {
	hot := map[*check.Func]bool{}
	cycle := map[*check.Func]bool{} // a call of f is a loop when f's body reaches f
	seen := map[*check.Func]bool{}
	var queue []*check.Func
	visit := func(f *check.Func) {
		if !seen[f] {
			seen[f] = true
			queue = append(queue, f)
		}
	}
	mark := func(h *check.Func) {
		if !hot[h] {
			hot[h] = true
			seen[h] = false // walk it again: its callees are hot now
		}
		visit(h)
	}
	direct := hotLowering == "direct"
	// values are the functions taken as values, valueHot whether a
	// function value is called in a loop; impls are the functions that
	// implement each operation name, opHot the names called in a loop.
	var values []*check.Func
	valueHot := false
	impls := map[string][]*check.Func{}
	opHot := map[string]bool{}
	tables := map[*check.Info]bool{}
	pkgs := map[*check.Package]bool{}
	for _, s := range l.srcs {
		if s.pkg != nil && !pkgs[s.pkg] {
			pkgs[s.pkg] = true
			for _, f := range s.pkg.Funcs {
				visit(f)
			}
		}
	}
	for len(queue) > 0 {
		for len(queue) > 0 {
			g := queue[0]
			queue = queue[1:]
			if g.Decl == nil || g.Decl.Body == nil || g.Pkg == nil || g.Pkg.Info == nil {
				continue
			}
			info := g.Pkg.Info
			if !tables[info] {
				tables[info] = true
				for _, conv := range operationsConversions(info) {
					for name, h := range conv.Table {
						if h != nil {
							impls[name] = append(impls[name], h)
						}
					}
				}
			}
			callee := map[*syntax.Ident]bool{}
			var walk func(n syntax.Node, loop bool)
			walk = func(n syntax.Node, loop bool) {
				syntax.Inspect(n, func(m syntax.Node) bool {
					switch m := m.(type) {
					case *syntax.ForStmt, *syntax.RangeStmt:
						if !loop {
							walk(m, true)
							return false
						}
					case *syntax.Ident:
						if h, ok := info.Uses[m].(*check.Func); ok && !callee[m] {
							values = append(values, h)
						}
					case *syntax.CallExpr:
						if id := calleeIdent(m.Fun); id != nil {
							callee[id] = true
						}
						c := info.Calls[m]
						if c == nil {
							break
						}
						if c.Kind == check.CallFuncValue && loop {
							valueHot = true
						}
						if (c.Kind == check.CallOp || c.Kind == check.CallProviderOp) && c.Op != nil && loop {
							opHot[c.Op.Name] = true
						}
						if c.Kind != check.CallFunc || c.Func == nil {
							break
						}
						h := c.Func
						if _, ok := cycle[h]; !ok {
							cycle[h] = l.inlineRecursive(h)
						}
						if loop || cycle[h] {
							mark(h)
						}
						visit(h)
					}
					return true
				})
			}
			walk(g.Decl.Body, hot[g])
		}
		if direct {
			break
		}
		// The queue ran empty: mark the callees of the calls through
		// values and operations, which queues those that are new.
		for _, h := range values {
			if valueHot && !hot[h] {
				mark(h)
			}
		}
		for name := range opHot {
			for _, h := range impls[name] {
				if !hot[h] {
					mark(h)
				}
			}
		}
	}
	return hot
}

// calleeIdent is the name a call's callee expression ends in (f, p.f,
// f[T]), nil for any other callee.
func calleeIdent(x syntax.Expr) *syntax.Ident {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		return x
	case *syntax.SelectorExpr:
		return x.Sel
	case *syntax.IndexExpr:
		return calleeIdent(x.X)
	}
	return nil
}

// operationsConversions are the operations conversions of info's
// assignment sites (Implicit), a with's provider entries included.
func operationsConversions(info *check.Info) []check.Conversion {
	var out []check.Conversion
	for _, conv := range info.Implicit {
		if conv.Kind == check.ConvOperations {
			out = append(out, conv)
		}
	}
	return out
}
