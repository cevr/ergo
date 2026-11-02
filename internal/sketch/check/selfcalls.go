package check

import (
	"maps"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Typed edges of x.f(…) calls (196m; round 6 review M1, round 7 review M1).
//
// A call x.f(…) of a package function on x's type reaches one function,
// which x's type picks. The reference walk resolves the call when x's
// declaration fixes its type (selfCallee), and retires it when x names
// nothing (an S104 whatever the rows); any other call waits here for
// typing, as a method use did in 196a: the round-15 rule, references that
// need types come from typing. No edge is ever taken by name.
//
// Typing a body needs its callees' rows only for the types of function
// values and for the rows of calls (a catch on a call that cannot fail is
// an error), and a call's callee depends only on the head of the
// receiver's type. So each body with a waiting call is typed once,
// silently, from empty inferred rows (the rows of a body typed earlier are
// reset, so the result does not depend on the order of the bodies), and
// every call typing reaches is resolved to its function: one edge per
// call, linear in the source.
//
// A type does not depend on a row: a catch keeps its value's type even on
// a call that cannot fail (handlers.go catch), so the reviewer's
// `let b = w() catch { case E: B{} }; b.m()` resolves from empty rows. One
// form still waits: an arm `case G as g` of a generic error type written
// without type arguments takes G's instance from the caught row, so g.m()
// resolves only once that row holds it. settleWaiting resolves such calls
// over the bodies the waiting bodies reach, the only bodies whose rows
// their typing reads, in the order converge uses (worklist.go).
//
// Prior art: go/types records a method's use when it resolves the selector
// (Info.Uses, LookupFieldOrMethod) and orders initialization by those
// dependencies; MoonBit and Koka resolve x.f by x's inferred type, never
// lexically; rustc resolves methods during type checking, after name
// resolution has fixed every plain path.

// resolveSelfCalls resolves the pending calls x.f(…) of each body by typing
// and adds their edges and class G value uses to the body's references.
func (c *checker) resolveSelfCalls(nodes []*bodyNode, byFunc map[*Func]*bodyNode, byVar map[*pkgVar]*bodyNode, pending map[any][]pendingSelf) {
	if len(pending) == 0 {
		return
	}
	c.selfRes = map[*syntax.SelectorExpr]*Func{}
	defer func() { c.selfRes = nil }()
	var all []*syntax.SelectorExpr
	if selfTrace != nil {
		for _, n := range nodes {
			for _, p := range pending[n.owner()] {
				all = append(all, p.sel)
			}
		}
	}
	if !selfSettleOnly && !c.settleOnly {
		c.silent++
		for _, n := range nodes {
			if len(pending[n.owner()]) == 0 {
				continue
			}
			c.checkNode(n)
			if n.fn != nil {
				n.fn.resetRows()
			}
		}
		c.silent--
		c.newSolutions()
		for _, n := range nodes {
			c.applySelfCalls(n.owner(), pending)
		}
	}
	if len(pending) > 0 {
		c.settleWaiting(nodes, byFunc, byVar, pending)
	}
	if selfTrace != nil {
		selfTrace(c, all, maps.Clone(c.selfRes))
	}
}

// selfTrace, when set by a test, receives the pending calls of a check and
// what typing resolved them to before any body is checked with
// diagnostics: a 196m function, or nil for another member (a field, an
// interface or capability method). A call typing never reached is absent.
var selfTrace func(c *checker, pending []*syntax.SelectorExpr, resolved map[*syntax.SelectorExpr]*Func)

// selfSettleOnly (or the checker's settleOnly), set by a test, skips the
// typing from empty rows, so the worklist alone resolves the calls.
var selfSettleOnly bool

// applySelfCalls moves each pending call of owner that typing resolved
// into owner's references: an edge to its function and, when the
// function's rows are inferred, the value uses its frames give (the
// earliest site of each row is kept, as the walk keeps the first). It
// leaves the unresolved calls and returns the functions with inferred rows
// it added an edge to.
func (c *checker) applySelfCalls(owner any, pending map[any][]pendingSelf) []*Func {
	list := pending[owner]
	if len(list) == 0 {
		return nil
	}
	var left []pendingSelf
	var added []*Func
	for _, p := range list {
		f, seen := c.selfRes[p.sel]
		if !seen {
			left = append(left, p)
			continue
		}
		if f == nil {
			continue
		}
		if !c.refs[owner][f] && (f.inferErrors || f.inferUses) {
			added = append(added, f)
		}
		c.refs[owner][f] = true
		if !f.inferErrors && !f.inferUses {
			continue
		}
		if p.guard != nil {
			// Inside callback frames: a use unless typing records one invalid-diagnosed
			// (liveUses).
			c.guardedRefs[owner] = append(c.guardedRefs[owner], guardedUse{f: f, site: p.site, guard: p.guard})
			continue
		}
		uses := c.valueRefs[owner][f]
		for k := range uses {
			if p.site[k].ok && (!uses[k].ok || p.site[k].at < uses[k].at) {
				uses[k] = p.site[k]
			}
		}
		c.valueRefs[owner][f] = uses
	}
	if len(left) == 0 {
		delete(pending, owner)
	} else {
		pending[owner] = left
	}
	return added
}

// settleWaiting resolves the calls still pending after the typing from
// empty rows, silently, in the order converge uses (worklist.go; round 8
// review M1).
//
// The worklist holds the waiting bodies and every body they reach in the
// graph known so far, the only bodies whose rows their typing reads. A body
// is checked once every pending callee it has has settled (callees first,
// on demand), and again only when a callee's rows grow. A call typing
// resolves adds its edge at once: a callee new to the worklist joins it with
// the bodies it reaches, and the caller is checked again only if that
// callee's rows grow. A waiting body whose calls stay unresolved is checked
// once its callees settle and wakes nobody. The rows restart empty
// afterwards: the reporting order is decided only once every edge is known.
//
// The bound is converge's: 2·(bodies + edges)·(D + 1) checks, where D
// counts the distinct row elements produced so far. Past it the worklist
// stops and the calls left stay unresolved; no known source reaches it
// (rows that only grow fit it, as in converge, whose bound
// TestConvergeBoundOscillation checks). c.selfChecks counts the checks,
// c.workNodes their syntax nodes and c.worklistSteps the bodies and edges
// the worklist visits, for the linear-work tests.
func (c *checker) settleWaiting(nodes []*bodyNode, byFunc map[*Func]*bodyNode, byVar map[*pkgVar]*bodyNode, pending map[any][]pendingSelf) {
	c.linkBodies(nodes, byFunc, byVar)
	needed := map[*bodyNode]bool{}
	preds := map[*bodyNode][]*bodyNode{}
	w := &worklist{member: func(n *bodyNode) bool { return needed[n] }}
	edges, post := 0, 0
	// need queues root and the bodies it reaches that are not in the
	// worklist yet, each with its postorder index.
	need := func(root *bodyNode) {
		if needed[root] {
			return
		}
		needed[root] = true
		type frame struct {
			n *bodyNode
			i int
		}
		stack := []frame{{n: root}}
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.i < len(top.n.edges) {
				e := top.n.edges[top.i]
				top.i++
				c.worklistSteps++
				edges++
				preds[e] = append(preds[e], top.n)
				if !needed[e] {
					needed[e] = true
					stack = append(stack, frame{n: e})
				}
				continue
			}
			n := top.n
			stack = stack[:len(stack)-1]
			c.worklistSteps++
			n.post = post
			post++
			w.push(n)
		}
	}
	for _, n := range nodes {
		if len(pending[n.owner()]) > 0 {
			need(n)
		}
	}
	seen := map[[2]int32]bool{}
	budget := func() int { return 2 * (len(needed) + edges) * (len(seen) + 1) }
	checks := 0
	c.silent++
	w.run(func(n *bodyNode) bool {
		if checks >= budget() {
			return false
		}
		checks++
		changed := c.checkNode(n)
		c.seenRows(n, seen)
		for _, f := range c.applySelfCalls(n.owner(), pending) {
			to := byFunc[f]
			if to == nil {
				continue
			}
			c.worklistSteps++
			edges++
			n.edges = append(n.edges, to)
			preds[to] = append(preds[to], n)
			need(to)
		}
		if changed {
			for _, p := range preds[n] {
				w.push(p)
			}
		}
		return true
	})
	c.silent--
	c.selfChecks += checks
	c.worklistSteps += w.steps
	for n := range needed {
		if n.fn != nil {
			n.fn.resetRows()
		}
	}
	for _, n := range nodes {
		n.edges = nil
	}
	c.newSolutions()
}

// linkBodies sets each node's edges from the body references (c.refs): to
// a function whose rows are inferred and to a package var. It returns the
// number of edges.
func (c *checker) linkBodies(nodes []*bodyNode, byFunc map[*Func]*bodyNode, byVar map[*pkgVar]*bodyNode) int {
	count := 0
	for _, n := range nodes {
		n.edges = nil
		for _, d := range sortedDeps(c.refs[n.owner()]) {
			var to *bodyNode
			switch d := d.(type) {
			case *Func:
				if d.inferErrors || d.inferUses {
					to = byFunc[d]
				}
			case *pkgVar:
				to = byVar[d]
			}
			if to != nil {
				n.edges = append(n.edges, to)
				count++
			}
		}
	}
	return count
}
