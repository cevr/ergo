package check

import (
	"fmt"
	"sort"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Footprints (rows.md, Footprints; #199 stage 1). A frame's footprint is
// what its body exercises when it runs: each capability operation it calls,
// under its own with too, each task it starts, and each call whose callee's
// footprint is unknown. It is not the capability row: a with discharges a
// row entry and leaves the footprint as it was, and an empty row is no
// evidence of an empty footprint (Footprints 2). The lowering reads the fact
// to place interruption points (runtime.md, I1) and never derives it again.
//
// The frames are the package's functions with a body, its function literals
// and the trailing blocks of calls other than with (a with block runs in
// its enclosing frame), its named contexts (the builder a with of the context runs)
// and its package-level lets (the initializer the root task runs). Making a
// function literal runs nothing, so a literal's body counts for the frames
// that call it, never for the frame that defines it. A call joins the
// callee's footprint when the callee is known: a function of the package
// with a body, a literal called where it is written, or a function of an
// imported package whose Info holds its footprint. Every other call is
// opaque: a function value, an operation of an operation-type value (a
// queue's Push, a Task's Wait, a program's own operation type), and a host
// function, whose body is Go. A for-in over a Seq calls the Seq, a function
// value, so it is opaque too. Recursive frames join to the least fixpoint,
// one strongly connected component at a time, so the work is linear in the
// bodies and the call edges.
// EntryPoll is local to each recursive or opaque impure frame; joining a
// callee's effects does not join its entry placement. Only a walked body
// publishes a Footprints entry, so lowering can reject a missing fact.

// Footprint is one frame's footprint.
type Footprint struct {
	// Caps are the capabilities the frame exercises, by package path and
	// name: each capability operation its body calls, those an enclosing
	// with of its own discharges included, and the capabilities of every
	// known frame it runs.
	Caps []Type
	// Tasks reports that the frame starts a task (task.Go, a host function
	// whose result holds a Task), or runs a known frame that does.
	Tasks bool
	// Opaque reports that the frame runs code whose footprint is unknown: a
	// call of a function value, of an operation of an operation-type value
	// or of a host function, here or in a known frame it runs.
	Opaque bool
	// EntryPoll is frame-local placement metadata, not a callee effect.
	// An impure frame polls at entry if it is recursive or its footprint
	// is opaque (unknown code may re-enter it). Pure recursion never polls.
	EntryPoll bool
}

// Empty reports a pure frame: no capability, no task start and no opaque
// call. Only an empty footprint is purity (rows.md, Footprints 2 and 3).
func (f Footprint) Empty() bool { return len(f.Caps) == 0 && !f.Tasks && !f.Opaque }

// fpNode is one frame, or one context entry's provider, in the footprint graph.
type fpNode struct {
	key    syntax.Node // the frame's Footprints key; nil for a context entry
	caps   map[*TypeName]Type
	tasks  bool
	opaque bool
	edges  []*fpNode
	walked bool // only bodies actually visited may publish a footprint

	index, low int // Tarjan's numbering; index 0 is unvisited
	onStack    bool
	comp       *fpNode // the component's root, once its component is final
	final      Footprint
}

func (n *fpNode) addCap(t *Named) {
	if n.caps == nil {
		n.caps = map[*TypeName]Type{}
	}
	n.caps[t.Obj] = t
}

func (n *fpNode) join(f Footprint) {
	for _, t := range f.Caps {
		if nt, ok := t.(*Named); ok {
			n.addCap(nt)
		}
	}
	n.tasks = n.tasks || f.Tasks
	n.opaque = n.opaque || f.Opaque
}

type footprinter struct {
	pkg   *Package
	info  *Info
	nodes map[any]*fpNode // by frame key, and by *ContextEntry
	list  []*fpNode       // every node, in creation order
	// owners is the checker's one entry-to-declaring-context index
	// (contextOwners), shared with the ownership pass.
	owners *contextOwners
}

// footprints records Info.Footprints once every body is final (finishInfo).
func (c *checker) footprints() {
	info := c.pkg.Info
	fp := &footprinter{pkg: c.pkg, info: info, nodes: map[any]*fpNode{},
		owners: &c.contextOwners}
	type body struct {
		n    *fpNode
		root syntax.Node
	}
	var bodies []body
	for _, f := range c.funcs {
		if f.Decl != nil && f.Decl.Extern == nil && f.Decl.Body != nil {
			bodies = append(bodies, body{fp.node(f.Decl), f.Decl.Body})
		}
	}
	// A test body is a frame keyed by its declaration, as a function's is.
	for _, t := range c.tests.all {
		bodies = append(bodies, body{fp.node(t.Decl), t.Decl.Body})
	}
	for key := range info.Literals {
		switch k := key.(type) {
		case *syntax.FuncLit:
			bodies = append(bodies, body{fp.node(k), k.Body})
		case *syntax.BlockStmt:
			bodies = append(bodies, body{fp.node(k), k})
		}
	}
	for _, fi := range c.files {
		for _, d := range fi.syntax.Decls {
			if vd, ok := d.(*syntax.VarDecl); ok {
				for _, spec := range vd.Specs {
					n := fp.node(spec)
					for _, v := range spec.Values {
						bodies = append(bodies, body{n, v})
					}
				}
			}
		}
	}
	for _, b := range bodies {
		fp.walk(b.n, b.root)
	}
	// A named context's node joins the providers its builder runs: the entry
	// that binds each capability once replace and embedding are resolved,
	// each entry once (Context.EachProvider; lower's contextBuilder).
	names := make([]string, 0, len(c.pkg.Contexts))
	for name := range c.pkg.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w := c.pkg.Contexts[name]
		if w.Decl == nil {
			continue
		}
		fp.context(w)
	}
	fp.solve()
	for _, n := range fp.list {
		if n.key != nil && n.walked {
			info.Footprints[n.key] = n.final
		}
	}
}

// node is the graph node of a frame key or a context entry, made on first use.
func (fp *footprinter) node(key any) *fpNode {
	if n := fp.nodes[key]; n != nil {
		return n
	}
	n := &fpNode{}
	if k, ok := key.(syntax.Node); ok {
		n.key = k
	}
	fp.nodes[key] = n
	fp.list = append(fp.list, n)
	return n
}

// context is the node of a with of w: an edge to each provider entry its
// builder runs. A context whose bindings are unknown is opaque.
func (fp *footprinter) context(w *Context) *fpNode {
	n := fp.node(w.Decl)
	if n.walked {
		return n
	}
	n.walked = true
	if w.unknown || w.binds == nil {
		n.opaque = true
		return n
	}
	seen := map[*ContextEntry]bool{}
	w.EachProvider(func(_ Type, e *ContextEntry) bool {
		if e != nil && !seen[e] {
			seen[e] = true
			n.edges = append(n.edges, fp.entry(e))
		}
		return true
	})
	return n
}

// entry is the node of one provider entry of this package: what its
// provider expression runs.
func (fp *footprinter) entry(e *ContextEntry) *fpNode {
	if en := fp.nodes[e]; en != nil {
		return en
	}
	en := fp.node(e)
	if e.Provider != nil {
		fp.walk(en, e.Provider)
	}
	return en
}

// with adds what the lowering runs for a with in n's frame: the
// construction of its providers and, when its block ends, the release of
// each acquired provider that has a Close (Scope.EndWith runs each
// With.Acquired[i].Close in this frame, whatever the context's spelling).
func (fp *footprinter) with(n *fpNode, w *With) {
	if cb := w.Value; cb != nil {
		// A with of a context value runs its operand here (walked as an
		// argument) and the protocol's two functions on it, which the
		// lowering calls to open the binding (runtime.md, G1). Opening and
		// restoring the binding waits for nothing; a read of a binding is a
		// host call, opaque like every one.
		fp.runs(n, cb.Key, false)
		fp.runs(n, cb.Text, false)
		return
	}
	if len(w.Contexts) == 0 {
		return
	}
	fp.build(n, w)
	for _, a := range w.Acquired {
		if a.Close == nil {
			continue
		}
		if a.Close.Func == nil {
			// An operation of the provider's type: unknown code.
			n.opaque = true
			continue
		}
		fp.runs(n, a.Close.Func, false)
	}
}

// build adds what the lowering runs to build a with's providers, read from
// the facts the lowering reads. `with W` calls W's builder frame, whose
// node joins every provider W binds (fp.context), a superset of the with's
// With.Acquired entries. An inline context builds its With.Acquired entries
// in this frame (buildProviders): its own entries are walked here
// (inspectFrame), and every other acquired entry, an embedded named
// context's at any depth, adds its entry node, so `with context { W }` cannot
// omit a provider that `with W` counts. An imported entry joins its
// context's recorded footprint. A context whose bindings are unknown, or an
// entry with no known context, is opaque.
func (fp *footprinter) build(n *fpNode, w *With) {
	root := w.Contexts[0]
	switch {
	case root.unknown || root.binds == nil:
		n.opaque = true
		return
	case root.Lit != nil:
	case root.pkg == fp.pkg && root.Decl != nil:
		n.edges = append(n.edges, fp.context(root))
		return
	default:
		if p := root.pkg; p != nil && p.Info != nil && root.Decl != nil {
			if g, ok := p.Info.Footprints[root.Decl]; ok {
				n.join(g)
				return
			}
		}
		n.opaque = true
		return
	}
	fp.owners.add(root)
	for _, a := range w.Acquired {
		ow := fp.owners.of[a.Entry]
		switch {
		case ow == nil:
			n.opaque = true
		case ow == root:
			// Walked in this frame.
		case ow.pkg == fp.pkg:
			n.edges = append(n.edges, fp.entry(a.Entry))
		case ow.Decl != nil && providerInfo(ow) != nil:
			if g, ok := ow.pkg.Info.Footprints[ow.Decl]; ok {
				n.join(g)
			} else {
				n.opaque = true
			}
		default:
			n.opaque = true
		}
	}
}

// walk adds what root runs in n's frame: its calls, and the bodies of its
// with blocks. A function literal and the trailing block of a call other
// than with are frames of their own: making one runs nothing.
func (fp *footprinter) walk(n *fpNode, root syntax.Node) {
	n.walked = true
	inspectFrame(fp.info, root, func(x syntax.Node, _ []*syntax.CallExpr) bool {
		switch x := x.(type) {
		case *syntax.RangeStmt:
			// A for-in over a Seq calls the Seq, a function value, which
			// calls the body back: unknown code runs here.
			if l := fp.info.Loops[x]; l == nil || l.Domain == LoopSeq {
				n.opaque = true
			}
		case *syntax.CallExpr:
			c := fp.info.Calls[x]
			fp.call(n, x, c)
		}
		return true
	})
}

// call adds one call's own effect to n.
func (fp *footprinter) call(n *fpNode, x *syntax.CallExpr, c *Call) {
	if c == nil {
		n.opaque = true
		return
	}
	switch c.Kind {
	case CallConversion, CallLen, CallPrelude:
		// These calls add no footprint of their own. CallPrelude is
		// retained compatibility metadata, never produced by the checker.
	case CallProviderOp:
		if sel, ok := unparen(x.Fun).(*syntax.SelectorExpr); ok {
			if t, ok := fp.info.Types[sel.X].(*Named); ok {
				n.addCap(t)
				return
			}
		}
		n.opaque = true
	case CallFunc:
		fp.runs(n, c.Func, c.TaskResult)
	case CallFuncValue:
		if lit, ok := unparen(x.Fun).(*syntax.FuncLit); ok {
			// A literal called where it is written runs its body here.
			n.edges = append(n.edges, fp.node(lit))
			return
		}
		n.opaque = true
	case CallOp:
		n.opaque = true
	case CallWith:
		// The block's calls are walked in this frame; the with also runs
		// the providers it acquires.
		w := fp.info.Withs[x]
		if w == nil {
			n.opaque = true
			return
		}
		fp.with(n, w)
	default:
		panic(fmt.Sprintf("check: unknown call kind %d in footprint", c.Kind))
	}
}

// runs adds a run of the function f to n: an edge to its node when it is
// a function of the package, the footprint its package recorded when it
// is imported, and opaque when its body is unknown. A host function's
// body is Go; taskResult reports that its result holds a Task.
func (fp *footprinter) runs(n *fpNode, f *Func, taskResult bool) {
	switch {
	case f == nil || f.Decl == nil:
		n.opaque = true
	case f.Decl.Extern != nil || f.Decl.Body == nil:
		n.opaque = true
		n.tasks = n.tasks || taskResult
	case f.Pkg == fp.pkg:
		n.edges = append(n.edges, fp.node(f.Decl))
	default:
		if f.Pkg != nil && f.Pkg.Info != nil {
			if g, ok := f.Pkg.Info.Footprints[f.Decl]; ok {
				n.join(g)
				return
			}
		}
		n.opaque = true
	}
}

// solve joins every node's footprint with those of the nodes it reaches, by
// Tarjan's strongly connected components: a component is final once every
// component it reaches is, and all its members share one footprint.
func (fp *footprinter) solve() {
	next := 0
	var stack []*fpNode
	var strong func(v *fpNode)
	strong = func(v *fpNode) {
		next++
		v.index, v.low = next, next
		stack = append(stack, v)
		v.onStack = true
		for _, w := range v.edges {
			switch {
			case w.index == 0:
				strong(w)
				v.low = min(v.low, w.low)
			case w.onStack:
				v.low = min(v.low, w.index)
			}
		}
		if v.low != v.index {
			return
		}
		var comp []*fpNode
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			w.onStack = false
			w.comp = v
			comp = append(comp, w)
			if w == v {
				break
			}
		}
		acc := &fpNode{}
		for _, w := range comp {
			for _, t := range w.caps {
				acc.addCap(t.(*Named))
			}
			acc.tasks = acc.tasks || w.tasks
			acc.opaque = acc.opaque || w.opaque
			for _, s := range w.edges {
				if s.comp != v {
					acc.join(s.final)
				}
			}
		}
		f := Footprint{Tasks: acc.tasks, Opaque: acc.opaque}
		for _, t := range acc.caps {
			f.Caps = append(f.Caps, t)
		}
		sort.Slice(f.Caps, func(i, j int) bool {
			a, b := f.Caps[i].(*Named).Obj, f.Caps[j].(*Named).Obj
			if pa, pb := pkgPath(a.Pkg), pkgPath(b.Pkg); pa != pb {
				return pa < pb
			}
			return a.Name < b.Name
		})
		recursive := len(comp) > 1
		for _, s := range v.edges {
			recursive = recursive || s == v
		}
		f.EntryPoll = !f.Empty() && (recursive || f.Opaque)
		for _, w := range comp {
			w.final = f
		}
	}
	for _, n := range fp.list {
		if n.index == 0 {
			strong(n)
		}
	}
}

func pkgPath(p *Package) string {
	if p == nil {
		return ""
	}
	return p.Path
}
