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

// loopCalled walks the call graph (direct calls of declared functions)
// from every function of the program's source packages and returns the
// functions on a loop path. A function found on a loop path after its
// body was walked is walked again, so the set is closed under calls. The
// body of a function literal is read as part of its function: a literal
// is called where its function is, or once per element of the loop that
// takes it. A call through a function value or an operation is not
// followed (its callee is unknown here); its callee is on a loop path
// only by a direct call.
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
		g := queue[0]
		queue = queue[1:]
		if g.Decl == nil || g.Decl.Body == nil || g.Pkg == nil || g.Pkg.Info == nil {
			continue
		}
		info := g.Pkg.Info
		var walk func(n syntax.Node, loop bool)
		walk = func(n syntax.Node, loop bool) {
			syntax.Inspect(n, func(m syntax.Node) bool {
				switch m := m.(type) {
				case *syntax.ForStmt, *syntax.RangeStmt:
					if !loop {
						walk(m, true)
						return false
					}
				case *syntax.CallExpr:
					c := info.Calls[m]
					if c == nil || c.Kind != check.CallFunc || c.Func == nil {
						break
					}
					h := c.Func
					if _, ok := cycle[h]; !ok {
						cycle[h] = l.inlineRecursive(h)
					}
					if (loop || hot[g] || cycle[h]) && !hot[h] {
						hot[h] = true
						seen[h] = false // walk it again: its callees are hot now
					}
					visit(h)
				}
				return true
			})
		}
		walk(g.Decl.Body, hot[g])
	}
	return hot
}
