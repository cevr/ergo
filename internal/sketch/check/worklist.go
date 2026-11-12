package check

import "container/heap"

// The order of the silent body checks (round 8 review M1). Settling the
// calls that wait for typing (selfcalls.go settleWaiting) and the fixpoint
// of a cyclic component (rows.go converge) share one worklist and one
// order:
//
//   - Callees first, on demand. Before a body is checked, every callee it
//     has in the worklist's graph that is still queued is checked first,
//     recursively (a depth-first walk over the edges known at that moment,
//     so an edge typing resolves joins the order at once). A callee that is
//     already on that walk is a cycle: the body is checked with the
//     callee's current rows, and checked again if they grow. So in an
//     acyclic part a body is checked only once all its pending callees have
//     settled, never once per settling callee.
//   - Smallest first among the roots. The next root is the queued body with
//     the fewest syntax nodes (then the lowest postorder index): inside a
//     cycle, where no order puts every callee first, the large bodies wait
//     until the small ones they depend on stop growing. A hub that calls
//     every member of a ring (the reviewer's star) is then checked a
//     constant number of times, whatever order declares the ring.
//   - A body whose rows grow wakes its callers in the worklist, except the
//     callers on the current walk, which are about to be checked anyway.
//
// The cost of one check is the body's size (c.workNodes); the walk visits
// each edge of a body once per check of that body (c.worklistSteps), and a
// body has at most one edge per reference in it, so the walk costs no more
// than the checks.
//
// Prior art: Gleam infers a module's functions in the dependency order of
// their strongly connected components (call_graph::into_dependency_order),
// so a caller is never inferred once per callee; Elixir's parallel compiler
// parks a module until the module it waits on is defined and wakes it
// then; the smallest-first choice inside a cycle is the weighted variant of
// the dataflow worklist (a node's recomputation costs its size).

// bodyHeap is the worklist's queue of bodies, least first (bodyLess). A
// queued body knows its slot; a body that is not queued has slot -1.
type bodyHeap []*bodyNode

func bodyLess(a, b *bodyNode) bool {
	if a.size != b.size {
		return a.size < b.size
	}
	return a.post < b.post
}

func (h bodyHeap) Len() int           { return len(h) }
func (h bodyHeap) Less(i, j int) bool { return bodyLess(h[i], h[j]) }
func (h bodyHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].slot, h[j].slot = i, j
}

func (h *bodyHeap) Push(x any) {
	n := x.(*bodyNode)
	n.slot = len(*h)
	*h = append(*h, n)
}

func (h *bodyHeap) Pop() any {
	old := *h
	n := old[len(old)-1]
	*h = old[:len(old)-1]
	n.slot = -1
	return n
}

// A worklist checks the bodies of one solve (a settling or a cyclic
// component) in the shared order. member reports whether a body belongs to
// the solve: an edge to any other body is to a body whose rows are final
// here, and is not followed.
type worklist struct {
	h      bodyHeap
	member func(*bodyNode) bool
	steps  int // edges the walk visited
}

// push queues n unless it is queued or on the current walk.
func (w *worklist) push(n *bodyNode) {
	if n.slot >= 0 || n.active {
		return
	}
	heap.Push(&w.h, n)
}

// run checks the queued bodies in the shared order until none is queued.
// check is called once per body check; it wakes callers with push. A solve
// whose budget is spent stops checking and pushes nothing, so the queue
// drains (stopping early instead changed no count or result: ledger t6-5,
// deleted in EX2).
func (w *worklist) run(check func(*bodyNode)) {
	type frame struct {
		n *bodyNode
		i int
	}
	var stack []frame
	defer func() {
		for _, f := range stack {
			f.n.active = false
		}
		for _, n := range w.h {
			n.slot = -1
		}
		w.h = nil
	}()
	for {
		if len(stack) == 0 {
			if w.h.Len() == 0 {
				return
			}
			n := heap.Pop(&w.h).(*bodyNode)
			n.active = true
			stack = append(stack, frame{n: n})
		}
		top := &stack[len(stack)-1]
		if top.i < len(top.n.edges) {
			e := top.n.edges[top.i]
			top.i++
			w.steps++
			if e.slot >= 0 && w.member(e) {
				heap.Remove(&w.h, e.slot)
				e.active = true
				stack = append(stack, frame{n: e})
			}
			continue
		}
		n := top.n
		stack = stack[:len(stack)-1]
		n.active = false
		check(n)
	}
}
