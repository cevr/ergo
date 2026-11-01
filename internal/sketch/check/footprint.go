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
// its enclosing frame), its named wires (the builder a with of the wire runs)
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

// fpNode is one frame, or one wire entry's provider, in the footprint graph.
type fpNode struct {
	key    syntax.Node // the frame's Footprints key; nil for a wire entry
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
	nodes map[any]*fpNode // by frame key, and by *WireEntry
	list  []*fpNode       // every node, in creation order
}

// footprints records Info.Footprints once every body is final (finishInfo).
func (c *checker) footprints() {
	info := c.pkg.Info
	fp := &footprinter{pkg: c.pkg, info: info, nodes: map[any]*fpNode{}}
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
	// A named wire's node joins the providers its builder runs: the entry
	// that binds each capability once replace and embedding are resolved,
	// each entry once (Wire.EachProvider; lower's wireBuilder).
	names := make([]string, 0, len(c.pkg.Wires))
	for name := range c.pkg.Wires {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w := c.pkg.Wires[name]
		if w.Decl == nil {
			continue
		}
		fp.wire(w)
	}
	fp.solve()
	for _, n := range fp.list {
		if n.key != nil && n.walked {
			info.Footprints[n.key] = n.final
		}
	}
}

// node is the graph node of a frame key or a wire entry, made on first use.
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

// wire is the node of a with of w: an edge to each provider entry its
// builder runs. A wire whose bindings are unknown is opaque.
func (fp *footprinter) wire(w *Wire) *fpNode {
	n := fp.node(w.Decl)
	if n.walked {
		return n
	}
	n.walked = true
	if w.unknown || w.binds == nil {
		n.opaque = true
		return n
	}
	seen := map[*WireEntry]bool{}
	w.EachProvider(func(_ Type, e *WireEntry) bool {
		if e != nil && !seen[e] {
			seen[e] = true
			en := fp.nodes[e]
			if en == nil {
				en = fp.node(e)
				if e.Provider != nil {
					fp.walk(en, e.Provider)
				}
			}
			n.edges = append(n.edges, en)
		}
		return true
	})
	return n
}

// walk adds what root runs in n's frame: its calls, and the bodies of its
// with blocks. A function literal and the trailing block of a call other
// than with are frames of their own: making one runs nothing.
func (fp *footprinter) walk(n *fpNode, root syntax.Node) {
	n.walked = true
	var visit func(x syntax.Node) bool
	visit = func(x syntax.Node) bool {
		switch x := x.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.GoExpr:
			// Not admitted (the checker refuses it); a task start if it were.
			n.tasks, n.opaque = true, true
			return false
		case *syntax.RangeStmt:
			// A for-in over a Seq calls the Seq, a function value, which
			// calls the body back: unknown code runs here.
			if l := fp.info.Loops[x]; l == nil || l.Domain == LoopSeq {
				n.opaque = true
			}
		case *syntax.CallExpr:
			c := fp.info.Calls[x]
			fp.call(n, x, c)
			if x.Trailing != nil && (c == nil || c.Kind != CallWith) {
				syntax.Inspect(x.Fun, visit)
				for _, a := range x.Args {
					syntax.Inspect(a, visit)
				}
				return false
			}
		}
		return true
	}
	syntax.Inspect(root, visit)
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
		f := c.Func
		switch {
		case f == nil || f.Decl == nil:
			n.opaque = true
		case f.Decl.Extern != nil || f.Decl.Body == nil:
			// A host function: its body is Go.
			n.opaque = true
			n.tasks = n.tasks || c.TaskResult
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
		// its wire's builder. An inline wire value's providers are its
		// argument, walked here too.
		w := fp.info.Withs[x]
		if w == nil {
			n.opaque = true
			return
		}
		if len(w.Wires) == 0 {
			return
		}
		if w.Wires[0].pkg == fp.pkg && w.Wires[0].Decl != nil {
			n.edges = append(n.edges, fp.wire(w.Wires[0]))
			return
		}
		if p := w.Wires[0].pkg; p != nil && p.Info != nil && w.Wires[0].Decl != nil {
			if g, ok := p.Info.Footprints[w.Wires[0].Decl]; ok {
				n.join(g)
				return
			}
		}
		n.opaque = true
	default:
		panic(fmt.Sprintf("check: unknown call kind %d in footprint", c.Kind))
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
