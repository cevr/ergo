package check

import (
	"sort"
)

// reachNode records what one body or one named context's build can reach: the
// package functions and methods it names, the capability methods it calls,
// the method names it calls on values of interface types, and the contexts it
// enters or embeds. The context-cycle check walks it (rows.md, Context 5).
type reachNode struct {
	contexts    []*Context
	funcs       []*Func
	capCalls    []capCall
	methodNames []string
}

// capCall is a call of a capability method, Cap.m(…): it reaches method m of
// every provider type bound to Cap in this package.
type capCall struct {
	cap  *Named
	name string
}

// reachOf returns the reach node of a function or a context, emptied: a body is
// checked again during a fixpoint, and its reach is recorded afresh.
func (c *checker) reachOf(key any) *reachNode {
	n := c.reach[key]
	if n == nil {
		n = &reachNode{}
		c.reach[key] = n
		return n
	}
	*n = reachNode{}
	return n
}

// checkContextCycles reports each named context whose build can reach itself
// (S108; rows.md, Context 5): through a provider expression that enters the
// context, a function or method it calls, a capability method whose provider
// enters it, or an embedding. A context's successors are the contexts its build
// reaches without crossing another context; the cycles are the strongly
// connected components of that graph (Tarjan), each reported once at every
// context on it.
func (c *checker) checkContextCycles() {
	if len(c.pkg.Contexts) == 0 {
		return
	}
	byMethod := map[string][]*Func{}
	for _, f := range c.funcs {
		if f.owner != nil {
			byMethod[f.Name] = append(byMethod[f.Name], f)
		}
	}
	names := sortedKeys(c.pkg.Contexts)
	succ := map[*Context][]*Context{}
	for _, name := range names {
		w := c.pkg.Contexts[name]
		succ[w] = c.contextsReached(c.reach[w], byMethod)
	}
	// Tarjan over the context graph (small: one node per named context).
	index := map[*Context]int{}
	low := map[*Context]int{}
	on := map[*Context]bool{}
	var stack []*Context
	next := 0
	cyclic := map[*Context]bool{}
	var visit func(w *Context)
	visit = func(w *Context) {
		index[w], low[w] = next, next
		next++
		stack = append(stack, w)
		on[w] = true
		for _, v := range succ[w] {
			if _, seen := index[v]; !seen {
				visit(v)
				low[w] = min(low[w], low[v])
			} else if on[v] {
				low[w] = min(low[w], index[v])
			}
		}
		if low[w] != index[w] {
			return
		}
		var scc []*Context
		for {
			v := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			on[v] = false
			scc = append(scc, v)
			if v == w {
				break
			}
		}
		self := false
		for _, v := range succ[w] {
			if v == w {
				self = true
			}
		}
		if len(scc) > 1 || self {
			for _, v := range scc {
				cyclic[v] = true
			}
		}
	}
	for _, name := range names {
		w := c.pkg.Contexts[name]
		if _, seen := index[w]; !seen {
			visit(w)
		}
	}
	for _, name := range names {
		w := c.pkg.Contexts[name]
		if !cyclic[w] {
			continue
		}
		c.cur = w.file
		c.errorf(w.Decl.Name, CodeType, "wire %s can reach itself while it is built (a with or an embedding of %s through its providers); a wire's build may not enter itself (rows.md, Wire 5)", w.Name, w.Name)
	}
}

// contextsReached returns the contexts a reach node reaches through functions,
// methods and capability providers, without crossing a context.
func (c *checker) contextsReached(start *reachNode, byMethod map[string][]*Func) []*Context {
	if start == nil {
		return nil
	}
	seenFunc := map[*Func]bool{}
	seenContext := map[*Context]bool{}
	seenName := map[string]bool{}
	var out []*Context
	work := []*reachNode{start}
	visitFunc := func(f *Func) {
		if f == nil || seenFunc[f] {
			return
		}
		seenFunc[f] = true
		if n := c.reach[f]; n != nil {
			work = append(work, n)
		}
	}
	for len(work) > 0 {
		n := work[len(work)-1]
		work = work[:len(work)-1]
		for _, w := range n.contexts {
			if !seenContext[w] && w.pkg == c.pkg {
				seenContext[w] = true
				out = append(out, w)
			}
		}
		for _, f := range n.funcs {
			visitFunc(f)
		}
		for _, cc := range n.capCalls {
			for _, p := range c.providers[cc.cap] {
				visitFunc(p.method(cc.name))
			}
		}
		for _, name := range n.methodNames {
			if seenName[name] {
				continue
			}
			seenName[name] = true
			for _, f := range byMethod[name] {
				visitFunc(f)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
