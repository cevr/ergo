package check

import (
	"fmt"
	"slices"
	"sort"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
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
// function, whose body is Go. A std extern whose Go body is marked pure is
// the exception: its call runs nothing a footprint counts. A for-in over a Seq calls the Seq, a function
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
	// trimmed are the nodes of contexts without some of their entries,
	// by context (without).
	trimmed map[*Context][]trimmedNode
	// foreign marks a footprinter that walks one provider expression of an
	// imported context with its own package's facts (foreignEntry): it
	// makes no edge, since no node of that package is walked here.
	foreign bool
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
	// So is a law body; its footprint is OwnerLaw's, which no build reads.
	for _, l := range c.pkg.Laws {
		if l.Decl.Body == nil {
			continue // a machine's reachability law has no body (MU6)
		}
		bodies = append(bodies, body{fp.node(l.Decl), l.Decl.Body})
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

// context is the node of a with of w: what its builder runs, each provider
// entry that binds a capability of w once replace and embedding are
// resolved (Context.EachProvider). The node is built from w's own entries
// and one edge per embedded context, as the context summary shares its
// embedded sets (rows.md, Context 5; contextsum.go), so a chain of n
// contexts costs O(n) edges, not the O(n²) of an edge per binding of each
// context (#229, C38). A context whose bindings are unknown is opaque.
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
	fp.provides(n, w, nil)
	return n
}

// provides adds to n an edge for each entry w's builder runs, except the
// entries of hidden: w's own provider entries, and for each embedded
// context its node, or, when an entry it runs is hidden here (a replace of
// w, or of a context that embeds w, overrides it), a node of the embedded
// context without those entries (fp.without).
func (fp *footprinter) provides(n *fpNode, w *Context, hidden []*ContextEntry) {
	hidden = append(hidden[:len(hidden):len(hidden)], fp.overridden(w)...)
	for _, e := range w.Entries {
		switch {
		case e == nil:
		case e.Embedded != nil:
			inner := e.Embedded
			switch {
			case inner.unknown || inner.binds == nil:
				n.opaque = true
			case inner.Decl == nil:
				// A context literal embeds only a named context of its
				// package (embeddedContext) or a local one, which the with
				// that builds it walks (build).
				n.opaque = true
			case inner.pkg != fp.pkg:
				// An imported named context (196b U4): what its own
				// package recorded, less the entries hidden here.
				fp.imported(n, inner, runsIn(inner, hidden))
			default:
				n.edges = append(n.edges, fp.without(inner, runsIn(inner, hidden)))
			}
		case e.Provider != nil && !slices.Contains(hidden, e):
			n.edges = append(n.edges, fp.entry(e))
		}
	}
}

// imported adds to n what the builder of w, a named context of another
// package, runs except the entries of hidden, each of which w runs (batch-9
// C4). With none hidden it joins the footprint w's package recorded for
// w's builder, which is the node fp.context makes for a context of this
// package. With some hidden it joins each entry w runs that is not hidden,
// in the same way that provides joins them: an embedded context by this
// rule again, and a provider entry by its provider expression, walked with
// its own package's facts (foreignEntry). A fact that is missing makes n
// opaque, which only adds polls.
func (fp *footprinter) imported(n *fpNode, w *Context, hidden []*ContextEntry) {
	p := w.pkg
	if p == nil || p.Info == nil {
		n.opaque = true
		return
	}
	if len(hidden) == 0 {
		if g, ok := p.Info.Footprints[w.Decl]; ok {
			n.join(g)
		} else {
			n.opaque = true
		}
		return
	}
	hidden = append(hidden[:len(hidden):len(hidden)], fp.overridden(w)...)
	for _, e := range w.Entries {
		switch {
		case e == nil:
		case e.Embedded != nil:
			inner := e.Embedded
			switch {
			case inner.unknown || inner.binds == nil || inner.Decl == nil:
				n.opaque = true
			case inner.pkg == fp.pkg:
				n.edges = append(n.edges, fp.without(inner, runsIn(inner, hidden)))
			default:
				fp.imported(n, inner, runsIn(inner, hidden))
			}
		case e.Provider != nil && !slices.Contains(hidden, e):
			fp.foreignEntry(n, e, p)
		}
	}
}

// foreignEntry adds to n what the provider expression of e, an entry of a
// context of package p, runs, read with p's facts: p's Info keys its
// nodes. Its package recorded no node for one entry, so the expression is
// walked here by a footprinter of p that makes no edge: a call of a
// function, of p or not, and of a literal joins the footprint its package
// recorded, and a with inside the expression is opaque.
func (fp *footprinter) foreignEntry(n *fpNode, e *ContextEntry, p *Package) {
	ffp := &footprinter{pkg: p, info: p.Info, nodes: map[any]*fpNode{}, owners: fp.owners, foreign: true}
	m := &fpNode{}
	ffp.walk(m, e.Provider)
	for _, t := range m.caps {
		n.addCap(t.(*Named))
	}
	n.tasks = n.tasks || m.tasks
	n.opaque = n.opaque || m.opaque || len(m.edges) > 0
}

// overridden are the entries w's replaces override that w's builder does
// not run: an entry runs while it still binds one of its capabilities.
func (fp *footprinter) overridden(w *Context) []*ContextEntry {
	var out []*ContextEntry
	for _, o := range w.Overrides {
		if o.Entry != nil && len(runsIn(w, []*ContextEntry{o.Entry})) == 0 && !slices.Contains(out, o.Entry) {
			out = append(out, o.Entry)
		}
	}
	return out
}

// runsIn is the subset of entries that w's builder runs: each entry that
// binds one of its capabilities in w (Context.Provider).
func runsIn(w *Context, entries []*ContextEntry) []*ContextEntry {
	var out []*ContextEntry
	for _, e := range entries {
		for _, cap := range e.Caps {
			if w.Provider(cap) == e {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// without is the node of what w's builder runs except the entries of
// hidden, each of which w runs: w's own node when hidden is empty, else a
// node made once per w and hidden set.
func (fp *footprinter) without(w *Context, hidden []*ContextEntry) *fpNode {
	if len(hidden) == 0 {
		return fp.context(w)
	}
	for _, m := range fp.trimmed[w] {
		if sameEntries(m.hidden, hidden) {
			return m.n
		}
	}
	n := &fpNode{walked: true}
	fp.list = append(fp.list, n)
	if fp.trimmed == nil {
		fp.trimmed = map[*Context][]trimmedNode{}
	}
	fp.trimmed[w] = append(fp.trimmed[w], trimmedNode{hidden: hidden, n: n})
	fp.provides(n, w, hidden)
	return n
}

// trimmedNode is a node of a context without some of its entries (without).
type trimmedNode struct {
	hidden []*ContextEntry
	n      *fpNode
}

// sameEntries reports whether a and b hold the same entries, each once.
func sameEntries(a, b []*ContextEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for _, e := range a {
		if !slices.Contains(b, e) {
			return false
		}
	}
	return true
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
	if w.Installs != nil {
		// A with of a Context value calls the value, which writes its
		// providers into the environment copy (contextvalues.go).
		n.opaque = true
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
	if root.Local {
		// A local context's literal is at its let, not in this with: each of
		// its own providers runs here, at every with that builds it.
		for _, e := range root.Entries {
			if e != nil && e.Provider != nil {
				n.edges = append(n.edges, fp.entry(e))
			}
		}
	}
	for _, a := range w.Acquired {
		ow := fp.owners.of[a.Entry]
		switch {
		case ow == nil:
			n.opaque = true
		case ow == root:
			// Walked in this frame, or above for a local context.
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
		case *syntax.DeclStmt:
			// The let of a local context runs nothing: each with that
			// builds it runs its providers (build).
			for _, spec := range x.Decl.Specs {
				if localContextLit(spec) != nil {
					return false
				}
			}
		case *syntax.RangeStmt:
			// A for-in over a Seq calls the Seq, a function value, which
			// calls the body back: unknown code runs here.
			if l := fp.info.Loops[x]; l == nil || l.Domain == LoopSeq {
				n.opaque = true
			}
		case *syntax.CallExpr:
			c := fp.info.Calls[x]
			fp.call(n, x, c)
		case syntax.Expr:
			// A conversion to a Context value builds its providers here
			// and reads the environment, which a pure frame has not
			// (contextvalues.go): opaque, as a context read is.
			if w := fp.info.ContextValues[x]; w != nil {
				n.opaque = true
				if !fp.foreign {
					fp.build(n, w)
				}
			}
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
	case CallConversion, CallLen, CallPrelude, CallGoError:
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
		fp.runs(n, c.Func, c.StartsTask)
	case CallFuncValue:
		if lit, ok := unparen(x.Fun).(*syntax.FuncLit); ok {
			// A literal called where it is written runs its body here.
			if fp.foreign {
				if g, ok := fp.info.Footprints[lit]; ok {
					n.join(g)
				} else {
					n.opaque = true
				}
				return
			}
			n.edges = append(n.edges, fp.node(lit))
			return
		}
		n.opaque = true
	case CallOp, CallLawOp, CallEncode, CallDecode:
		// A law's operation has no body until #162 picks an implementation;
		// encode and decode call the operations of a Writer or Reader value.
		n.opaque = true
	case CallWith:
		// The block's calls are walked in this frame; the with also runs
		// the providers it acquires.
		w := fp.info.Withs[x]
		if w == nil || fp.foreign {
			n.opaque = true
			return
		}
		// Each operand builds its own context (rows.md, With 7).
		for ; w != nil; w = w.Next {
			fp.with(n, w)
		}
	default:
		panic(fmt.Sprintf("check: unknown call kind %d in footprint", c.Kind))
	}
}

// runs adds a run of the function f to n: an edge to its node when it is
// a function of the package, the footprint its package recorded when it
// is imported, and opaque when its body is unknown. A host function's
// body is Go; taskResult reports that its result holds a Task. A std
// extern whose Go body is pure (pureStd) adds nothing but that Task.
func (fp *footprinter) runs(n *fpNode, f *Func, taskResult bool) {
	switch {
	case f == nil || f.Decl == nil:
		n.opaque = true
	case pureStd(f):
		n.tasks = n.tasks || taskResult
	case f.Decl.Extern != nil || f.Decl.Body == nil:
		n.opaque = true
		n.tasks = n.tasks || taskResult
	case f.Pkg == fp.pkg && !fp.foreign:
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

// pureStdAlso is a test seam, nil outside tests (export_test.go,
// SetPureStdForTest): it counts more std externs as pure, so the footprint
// property reaches the path of runs that a pure std body whose result holds
// a Task takes (tasks without opaque), before std has such a body
// (fu-pure-task). It only widens pureStd over std externs.
var pureStdAlso func(*Func) bool

// pureStd reports a std extern whose runtime/v1 Go body carries the
// reviewed purity fact (v1.Body.Pure; docs/spec/stdlib.md, Pure bodies). A
// user extern is never pure: its Go body is outside the reviewed set.
func pureStd(f *Func) bool {
	if pureGo(f) {
		return true
	}
	if f == nil || f.Decl == nil || f.Decl.Extern == nil || f.Pkg == nil || !f.Pkg.std {
		return false
	}
	if pureStdAlso != nil && pureStdAlso(f) {
		return true
	}
	if pureCollections == "off" && preludeCollection(f) {
		return false
	}
	path, name := f.Decl.Extern.Split()
	if path != runtimeModule+"/v1" {
		return false
	}
	body, err := v1.BodyOf(name)
	return err == nil && body.Pure
}

// pureGo reports a Go function or method of import go in the reviewed pure
// tier (its binding charges no go.Foreign) that takes no function, so Go
// calls back no Ergo code: its call is transparent in footprints (owner
// q35, reversing stdlib.md Pure bodies 3 for the reviewed list), and its
// caller needs no environment or entry poll for it, as hand Go has none.
func pureGo(f *Func) bool {
	if f == nil || !f.Pkg.Host() || f.Sig == nil || !f.Sig.Uses.Empty() {
		return false
	}
	for _, p := range f.Sig.Params {
		if _, ok := under(p.Type).(*Signature); ok {
			return false
		}
	}
	return true
}

// PureStd reports a std extern whose runtime/v1 Go body is marked pure: the
// one fact the footprint and the lowering's pure functions both read
// (lower, pureStdBody).
func PureStd(f *Func) bool { return pureStd(f) }

// pureCollections, when "off", counts the prelude's Append, Get, Delete and Put
// as opaque calls again, as before lw-pure-prelude: the reference lowering,
// in which a function that calls them takes the environment and polls its
// task. A whole build selects it with
// -ldflags=-X=ergo.local/prototype/internal/sketch/check.pureCollections=off,
// and a test with SetPureCollectionsForTest.
var pureCollections string

// preludeCollection reports the prelude's Append, Get, Delete or Put.
func preludeCollection(f *Func) bool {
	if f.Pkg == nil || f.Pkg.Path != std.PreludePath {
		return false
	}
	switch f.Name {
	case "Append", "Get", "Delete", "Put":
		return true
	}
	return false
}

// SetPureCollectionsForTest sets whether the prelude's Append, Get and
// Delete are pure (pureCollections) until restore runs, for the property
// that compares lw-pure-prelude's lowering with the reference. It is
// process-wide: a caller runs it outside t.Parallel. The compiler never
// calls it.
func SetPureCollectionsForTest(pure bool) (restore func()) {
	saved := pureCollections
	pureCollections = ""
	if !pure {
		pureCollections = "off"
	}
	return func() { pureCollections = saved }
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
		// A context's node and a with's frame read their edges here: one
		// step each (withStep; TestWithSiteWorkLinear counts them).
		withStep(len(v.edges))
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
