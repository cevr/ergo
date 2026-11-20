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
	// Order is the nodes in an order every edge agrees with, ties by
	// their declaration order.
	Order []int
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
	order, err := orderOf(nodes, g.Edges)
	if err != nil {
		return Graph{}, err
	}
	g.Order = order
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

// orderOf orders the nodes so that every edge runs from earlier to later;
// among the nodes ready at once, the one declared first goes first. A
// cycle is an error naming its nodes.
func orderOf(nodes []Node, edges [][2]int) ([]int, error) {
	indeg := make([]int, len(nodes))
	next := make([][]int, len(nodes))
	for _, e := range edges {
		indeg[e[1]]++
		next[e[0]] = append(next[e[0]], e[1])
	}
	var ready, out []int
	for i := range nodes {
		if indeg[i] == 0 {
			ready = append(ready, i)
		}
	}
	for len(ready) > 0 {
		slices.Sort(ready)
		i := ready[0]
		ready = ready[1:]
		out = append(out, i)
		for _, j := range next[i] {
			if indeg[j]--; indeg[j] == 0 {
				ready = append(ready, j)
			}
		}
	}
	if len(out) < len(nodes) {
		var names []string
		for i := range nodes {
			if indeg[i] > 0 {
				names = append(names, nodes[i].Name)
			}
		}
		return nil, fmt.Errorf("gen %s form a cycle: each writes a file another of them reads", strings.Join(names, ", "))
	}
	return out, nil
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
	for pass := 1; ; pass++ {
		if pass > len(nodes)+1 {
			return rep, fmt.Errorf("gen does not settle after %d passes: a node's run changes what another read before it, every pass", len(nodes)+1)
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
