package task

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/lawcache"
)

// Graph is the nodes and the edges their last runs give.
type Graph struct {
	Nodes []Node
	// Known says which nodes have a stored last run: a node without one
	// has no edges yet (its outputs are unknown until it runs).
	Known []bool
	// Edges are pairs (from, to) of node indexes: from writes a file to
	// reads, or a file in a folder to lists. Sorted, no repeats, no self
	// edge.
	Edges [][2]int
	// Order is the nodes in an order every edge outside a cycle agrees
	// with, ties by their declaration order.
	Order []int
	// Cycles are the sets of nodes that read each other's outputs, each in
	// declaration order (the order they run in, pass after pass, until
	// they settle).
	Cycles [][]int
}

// Graph reads the nodes' last runs from the store and orders them.
func (r *Runner) Graph(nodes []Node) (Graph, error) {
	recs := make([]*record, len(nodes))
	g := Graph{Nodes: nodes, Known: make([]bool, len(nodes))}
	for i, n := range nodes {
		if text, ok := r.Store.Get(lastKey(n)); ok {
			if rec, ok := decodeRecord(text); ok {
				recs[i] = &rec
				g.Known[i] = true
			}
		}
	}
	g.Edges = edgesOf(recs)
	g.Order, g.Cycles = orderOf(nodes, g.Edges)
	return g, nil
}

// edgesOf are the edges of the nodes' last runs: a before b when an
// output of a is a file b read or lies in a folder b listed.
func edgesOf(recs []*record) [][2]int {
	var edges [][2]int
	for b, rb := range recs {
		if rb == nil {
			continue
		}
		files := map[string]bool{}
		for _, f := range rb.in.Files {
			files[f] = true
		}
		dirs := map[string]bool{}
		for _, d := range rb.in.Dirs {
			dirs[d] = true
		}
		for a, ra := range recs {
			if a == b || ra == nil {
				continue
			}
			for _, o := range ra.outs {
				if files[o.path] || dirs[parentDir(o.path)] {
					edges = append(edges, [2]int{a, b})
					break
				}
			}
		}
	}
	slices.SortFunc(edges, func(x, y [2]int) int {
		if x[0] != y[0] {
			return x[0] - y[0]
		}
		return x[1] - y[1]
	})
	return edges
}

// orderOf orders the nodes so that every edge outside a cycle runs from
// earlier to later; among the parts ready at once, the one declared first
// goes first. A cycle (generators that read each other's outputs, as a
// compiler's generators do when each builds with the others' files) is
// one part, its nodes in declaration order: the passes run it until it
// settles. The cycles are returned, each in declaration order.
func orderOf(nodes []Node, edges [][2]int) (order []int, cycles [][]int) {
	next := make([][]int, len(nodes))
	for _, e := range edges {
		next[e[0]] = append(next[e[0]], e[1])
	}
	// Tarjan's strongly connected components.
	index := make([]int, len(nodes))
	low := make([]int, len(nodes))
	onStack := make([]bool, len(nodes))
	for i := range index {
		index[i] = -1
	}
	comp := make([]int, len(nodes))
	var parts [][]int
	var stack []int
	counter := 0
	var visit func(v int)
	visit = func(v int) {
		index[v], low[v] = counter, counter
		counter++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range next[v] {
			if index[w] < 0 {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] == index[v] {
			var part []int
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp[w] = len(parts)
				part = append(part, w)
				if w == v {
					break
				}
			}
			slices.Sort(part)
			parts = append(parts, part)
		}
	}
	for i := range nodes {
		if index[i] < 0 {
			visit(i)
		}
	}
	// The parts in an order every edge between parts agrees with, ties by
	// each part's first-declared node.
	indeg := make([]int, len(parts))
	seen := map[[2]int]bool{}
	pnext := make([][]int, len(parts))
	for _, e := range edges {
		a, b := comp[e[0]], comp[e[1]]
		if a != b && !seen[[2]int{a, b}] {
			seen[[2]int{a, b}] = true
			indeg[b]++
			pnext[a] = append(pnext[a], b)
		}
	}
	var ready []int
	for p := range parts {
		if indeg[p] == 0 {
			ready = append(ready, p)
		}
	}
	for len(ready) > 0 {
		slices.SortFunc(ready, func(x, y int) int { return parts[x][0] - parts[y][0] })
		p := ready[0]
		ready = ready[1:]
		order = append(order, parts[p]...)
		if len(parts[p]) > 1 {
			cycles = append(cycles, parts[p])
		}
		for _, q := range pnext[p] {
			if indeg[q]--; indeg[q] == 0 {
				ready = append(ready, q)
			}
		}
	}
	return order, cycles
}

// contentKey is the key of node n's result over the inputs in now: its
// name, identity and environment, each file's digest and each folder's
// listing.
func contentKey(n Node, env string, in Inputs, root string, h *hasher) (string, error) {
	parts := []string{"task entry", n.Name, n.Ident, env}
	for _, f := range in.Files {
		d, err := h.digest(f)
		if err != nil {
			return "", err
		}
		parts = append(parts, "f "+f+" "+d)
	}
	for _, dir := range in.Dirs {
		l, err := listing(root, dir)
		if err != nil {
			return "", err
		}
		parts = append(parts, "d "+dir+" "+l)
	}
	return lawcache.Key(parts...), nil
}

// lookup is node n's stored result for the tree at root as it is now: the
// outputs of a run over the same inputs, when the tree's outputs still
// hash as that run wrote them.
func (r *Runner) lookup(n Node, env, root string, h *hasher) (outs []output, hit bool, err error) {
	text, ok := r.Store.Get(lastKey(n))
	if !ok {
		return nil, false, nil
	}
	last, ok := decodeRecord(text)
	if !ok || last.env != env {
		return nil, false, nil
	}
	key, err := contentKey(n, env, last.in, root, h)
	if err != nil {
		return nil, false, err
	}
	text, ok = r.Store.Get(key)
	if !ok {
		return nil, false, nil
	}
	entry, ok := decodeRecord(text)
	if !ok {
		return nil, false, nil
	}
	for _, o := range entry.outs {
		d, err := h.digest(o.path)
		if err != nil {
			return nil, false, err
		}
		if d != o.digest {
			return entry.outs, false, nil
		}
	}
	return entry.outs, true, nil
}

// run runs node n in the tree at root and stores its result: the outputs
// are the files whose stat the run changed and the outputs of its last
// run that still exist, each with its digest after the run; the key is
// over the inputs the node reads after the run (its own outputs among
// them, when it reads what it writes).
func (r *Runner) run(ctx context.Context, n Node, env, root string, h *hasher) ([]output, error) {
	before, err := snapshot(root)
	if err != nil {
		return nil, err
	}
	if r.Ran != nil {
		r.Ran(n.Name, root)
	}
	text, err := n.Run(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("gen %s: %v\n%s", n.Name, err, strings.TrimRight(string(text), "\n"))
	}
	after, err := snapshot(root)
	if err != nil {
		return nil, err
	}
	written := changed(before, after)
	paths := slices.Clone(written)
	if last, ok := r.Store.Get(lastKey(n)); ok {
		if rec, ok := decodeRecord(last); ok {
			for _, o := range rec.outs {
				if _, ok := after[o.path]; ok {
					paths = append(paths, o.path)
				}
			}
		}
	}
	paths = sortedUnique(paths)
	outs := make([]output, 0, len(paths))
	for _, p := range paths {
		d, err := h.digest(p)
		if err != nil {
			return nil, err
		}
		outs = append(outs, output{path: p, digest: d})
	}
	in, err := n.Inputs(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("gen %s: its inputs: %v", n.Name, err)
	}
	in.Files = sortedUnique(in.Files)
	in.Dirs = sortedUnique(in.Dirs)
	key, err := contentKey(n, env, in, root, h)
	if err != nil {
		return nil, err
	}
	r.Store.Put(key, record{env: env, outs: outs}.encode())
	r.Store.Put(lastKey(n), record{env: env, in: in, outs: outs}.encode())
	return outs, nil
}

// Gen brings the generated files of the tree up to date, in the order
// the graph gives, or with Check says which are not. A node whose
// stored result holds is a hit and does not run. The order is the one
// the nodes' last runs give; a node with no last run has no edges, so
// after each pass the graph is read again and a node whose inputs a
// later node wrote misses and runs again: passes end when one runs
// nothing (with every node's last run known and the order right, the
// first pass runs each node at most once and the second runs none).
func (r *Runner) Gen(ctx context.Context, nodes []Node, mode Mode) (Report, error) {
	rep := Report{Ran: map[string]int{}, Hits: map[string]int{}}
	envs := make([]string, len(nodes))
	for i, n := range nodes {
		env, err := n.Env(ctx)
		if err != nil {
			return rep, fmt.Errorf("gen %s: %v", n.Name, err)
		}
		envs[i] = env
	}
	tree := newHasher(r.Root)
	tree.load(r.Store)
	defer tree.save(r.Store)
	root, h := r.Root, tree
	var written []string
	inCopy := false
	var removeCopy func()
	toCopy := func() error {
		scratch := r.Scratch
		if scratch == nil {
			scratch = func() (string, func(), error) { return scratchDir(r.Root) }
		}
		dir, remove, err := scratch()
		if err != nil {
			return err
		}
		if err := copyTree(r.Root, dir); err != nil {
			remove()
			return err
		}
		root, h, inCopy = dir, newHasher(dir), true
		removeCopy = remove
		return nil
	}
	defer func() {
		if removeCopy != nil {
			removeCopy()
		}
	}()
	if mode.Check && mode.Fresh {
		if err := toCopy(); err != nil {
			return rep, err
		}
	}
	running := 0 // the passes that ran a node
	for pass := 1; ; pass++ {
		// A backstop past every bound below: the graph of a pass is read
		// from the store, so a run whose stored edges keep moving still ends.
		if running > len(nodes)+1 {
			return rep, fmt.Errorf("gen does not settle after %d passes", running)
		}
		g, err := r.Graph(nodes)
		if err != nil {
			return rep, err
		}
		rep.Order = rep.Order[:0]
		for _, i := range g.Order {
			rep.Order = append(rep.Order, nodes[i].Name)
		}
		rep.Passes = pass
		// A cycle of k generators settles in k+1 passes that run
		// something (every generator alone settles in 2: one to learn the
		// edges, one in order); a generator still running after that keeps
		// changing what another reads, and would loop for ever.
		k := 1
		for _, c := range g.Cycles {
			k = max(k, len(c))
		}
		if running == k+1 {
			var still []string
			for _, i := range g.Order {
				if _, hit, err := r.lookup(nodes[i], envs[i], root, h); err == nil && !hit {
					still = append(still, nodes[i].Name)
				}
			}
			if len(still) > 0 {
				return rep, fmt.Errorf("gen %s keep changing what another reads: they still miss after %d passes, the most a cycle of %d settles in; a generator whose output never settles must not loop", strings.Join(still, ", "), running, k)
			}
		}
		ran := false
		for _, i := range g.Order {
			n := nodes[i]
			stored, hit, err := r.lookup(n, envs[i], root, h)
			if err != nil {
				return rep, fmt.Errorf("gen %s: %v", n.Name, err)
			}
			if hit && !(mode.Fresh && pass == 1) {
				rep.Hits[n.Name]++
				fmt.Fprintf(r.Out, "gen %s: hit (%s)\n", n.Name, n.Class)
				continue
			}
			if mode.Check && !inCopy {
				if err := toCopy(); err != nil {
					return rep, err
				}
				if stored, hit, err = r.lookup(n, envs[i], root, h); err != nil {
					return rep, fmt.Errorf("gen %s: %v", n.Name, err)
				}
			}
			outs, err := r.run(ctx, n, envs[i], root, h)
			if err != nil {
				return rep, err
			}
			rep.Ran[n.Name]++
			ran = true
			for _, o := range outs {
				written = append(written, o.path)
			}
			if hit && !sameOutputs(stored, outs) {
				rep.Contradicted = append(rep.Contradicted, n.Name)
				fmt.Fprintf(r.Out, "gen %s: a fresh run writes what its stored result does not (%s): its declared reads are incomplete\n", n.Name, n.Class)
				continue
			}
			fmt.Fprintf(r.Out, "gen %s: ran\n", n.Name)
		}
		if !ran {
			break
		}
		running++
	}
	if inCopy {
		for _, p := range sortedUnique(written) {
			fresh, err := h.digest(p)
			if err != nil {
				return rep, err
			}
			have, err := tree.digest(p)
			if err != nil {
				return rep, err
			}
			if fresh != have {
				rep.Stale = append(rep.Stale, p)
			}
		}
	}
	return rep, nil
}

// sameOutputs reports whether a fresh run's outputs agree with a stored
// result's: every path the stored result names has the digest it stored.
func sameOutputs(stored, fresh []output) bool {
	now := map[string]string{}
	for _, o := range fresh {
		now[o.path] = o.digest
	}
	for _, o := range stored {
		if d, ok := now[o.path]; ok && d != o.digest {
			return false
		}
	}
	return true
}
